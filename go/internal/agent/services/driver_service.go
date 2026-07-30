package services

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/shared/sigverify"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// Driver add-on store on the /data partition, consumed by the OS-side
// /usr/sbin/wendyos-sysext-apply.sh (merge -> overlay -> depmod -> modprobe).
const (
	driverEnabledDir  = "/data/extensions/enabled"
	driverModulesDir  = "/data/extensions/modules-load.d"
	sysextApplyScript = "/usr/sbin/wendyos-sysext-apply.sh"
	driverSystemPath  = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

	// A self-describing add-on bakes its module list into the .raw; systemd-sysext
	// exposes it here once merged. ListDrivers falls back to this when the /data
	// override is absent, mirroring wendyos-sysext-apply's precedence.
	driverBakedModulesDir = "/usr/lib/modules-load.d"
)

// maxDriverImageSize caps a driver .raw to guard against a runaway upload/download.
const maxDriverImageSize = 512 << 20 // 512 MiB

// DriverInstallSpec describes a driver add-on to install from a URL. Both the
// first-boot seed (configpartition) and the InstallDriver RPC's URL path build
// the same steps: fetch -> verify sha256 + signature -> place on /data -> apply.
type DriverInstallSpec struct {
	Name          string
	KernelVersion string
	SHA256        string
	Signature     []byte
	ArtifactURL   string
	ModulesLoad   []string
}

// DriverService implements WendyDriverService: verify a signed driver add-on
// (.raw), store it on /data under its stable name, and apply it via the OS
// sysext-apply path. Drivers are built ahead of time in CI; the device never
// compiles them.
type DriverService struct {
	agentpbv2.UnimplementedWendyDriverServiceServer

	logger   *zap.Logger
	verifier *sigverify.Verifier

	// apply touches the shared /usr sysext overlay; serialize concurrent applies.
	mu sync.Mutex

	// requireSignature makes an install fail closed when no signing key is
	// embedded. Set for callers that are not already root-equivalent.
	requireSignature bool

	// Seams for tests.
	enabledDir      string
	modulesDir      string
	bakedModulesDir string
	applyScript     string
	unameR          func() string
	httpGet         func(ctx context.Context, url string) (io.ReadCloser, error)
}

// NewDriverService builds the driver service with production defaults. Signature
// verification uses the shared pinned-key verifier (a fail-safe no-op until a
// driver-signing key is embedded, matching the agent-update/container paths).
func NewDriverService(logger *zap.Logger) *DriverService {
	return &DriverService{
		logger:          logger,
		verifier:        sigverify.DefaultVerifier,
		enabledDir:      driverEnabledDir,
		modulesDir:      driverModulesDir,
		bakedModulesDir: driverBakedModulesDir,
		applyScript:     sysextApplyScript,
		unameR:          unameRelease,
		httpGet:         httpGetBody,
	}
}

// NewSeedDriverService builds the service for the first-boot config-partition
// seed. That partition is unauthenticated storage — anyone who can write the
// FAT32 filesystem gets a module loaded, with no operator in the loop — so this
// path requires a real signature and fails closed while no key is embedded,
// unlike the mTLS RPC whose caller is already root-equivalent.
func NewSeedDriverService(logger *zap.Logger) *DriverService {
	s := NewDriverService(logger)
	s.requireSignature = true
	return s
}

// driverApplyStream is satisfied by both the bidi (install) and server-streaming
// (remove) gRPC stream servers, so the progress helpers work for either.
type driverApplyStream interface {
	Send(*agentpbv2.DriverApplyResponse) error
}

func sendDriverProgress(s driverApplyStream, phase string, percent int32) {
	_ = s.Send(&agentpbv2.DriverApplyResponse{
		ResponseType: &agentpbv2.DriverApplyResponse_Progress_{
			Progress: &agentpbv2.DriverApplyResponse_Progress{Phase: phase, Percent: percent},
		},
	})
}

func sendDriverFailure(s driverApplyStream, msg string) error {
	return s.Send(&agentpbv2.DriverApplyResponse{
		ResponseType: &agentpbv2.DriverApplyResponse_Failed_{
			Failed: &agentpbv2.DriverApplyResponse_Failed{ErrorMessage: msg},
		},
	})
}

func sendDriverCompleted(s driverApplyStream, name string, rebootRequired bool) error {
	return s.Send(&agentpbv2.DriverApplyResponse{
		ResponseType: &agentpbv2.DriverApplyResponse_Completed_{
			Completed: &agentpbv2.DriverApplyResponse_Completed{Name: name, RebootRequired: rebootRequired},
		},
	})
}

// InstallDriver stages a driver .raw (streamed chunks or fetched from a URL),
// verifies its sha256 + signature, places it under its stable name on /data,
// writes the modules-load config, and applies it. Progress is streamed back.
func (s *DriverService) InstallDriver(stream grpc.BidiStreamingServer[agentpbv2.InstallDriverRequest, agentpbv2.DriverApplyResponse]) error {
	// Locking happens in finalize: staging is client-paced with no server-side
	// deadline, so holding the lock here would let one stalled client wedge
	// every driver RPC.
	first, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "no install request received: %v", err)
	}
	spec := first.GetSpec()
	if spec == nil {
		return status.Error(codes.InvalidArgument, "first InstallDriver message must carry a DriverSpec")
	}
	if err := validateDriverName(spec.GetName()); err != nil {
		return sendDriverFailure(stream, err.Error())
	}
	if err := s.checkKernel(spec.GetName(), spec.GetKernelVersion(), spec.GetArtifactUrl() != ""); err != nil {
		return sendDriverFailure(stream, err.Error())
	}

	// Stage bytes: either the client's chunks or a URL the agent fetches itself.
	sendDriverProgress(stream, "receiving", 10)
	var tmpPath string
	var digest []byte
	if url := spec.GetArtifactUrl(); url != "" {
		tmpPath, digest, err = s.stageFromURL(stream.Context(), url)
	} else {
		tmpPath, digest, err = s.stageFromStream(stream)
	}
	if err != nil {
		return sendDriverFailure(stream, err.Error())
	}
	defer os.Remove(tmpPath)

	sendDriverProgress(stream, "applying", 70)
	install := DriverInstallSpec{
		Name:        spec.GetName(),
		SHA256:      spec.GetSha256(),
		Signature:   spec.GetSignature(),
		ModulesLoad: spec.GetModulesLoad(),
	}
	if err := s.finalize(stream.Context(), install, tmpPath, digest); err != nil {
		return sendDriverFailure(stream, err.Error())
	}

	s.logger.Info("driver add-on installed", zap.String("name", spec.GetName()), zap.String("sha256", hex.EncodeToString(digest)))
	return sendDriverCompleted(stream, spec.GetName(), false)
}

// InstallFromURL installs a driver add-on described by spec, fetching the .raw
// from spec.ArtifactURL. Used by the first-boot seed; it shares the exact
// verify/place/apply path as the InstallDriver RPC.
func (s *DriverService) InstallFromURL(ctx context.Context, spec DriverInstallSpec) error {
	// Serialized in finalize, not here — see InstallDriver.
	if err := validateDriverName(spec.Name); err != nil {
		return err
	}
	if err := s.checkKernel(spec.Name, spec.KernelVersion, true); err != nil {
		return err
	}
	if spec.ArtifactURL == "" {
		return fmt.Errorf("driver %q has no artifact URL", spec.Name)
	}
	tmpPath, digest, err := s.stageFromURL(ctx, spec.ArtifactURL)
	if err != nil {
		return err
	}
	defer os.Remove(tmpPath)
	if err := s.finalize(ctx, spec, tmpPath, digest); err != nil {
		return err
	}
	s.logger.Info("driver add-on installed", zap.String("name", spec.Name), zap.String("sha256", hex.EncodeToString(digest)))
	return nil
}

// checkKernel rejects a .ko built for another kernel. Remote installs must
// declare the version: nobody inspected the artifact, so silence is not consent.
// A local file may omit it - the operator picked the exact bytes.
func (s *DriverService) checkKernel(name, kernelVersion string, remote bool) error {
	if kernelVersion == "" {
		if remote {
			return fmt.Errorf("driver %q does not declare a kernel version; refusing to install it unverified", name)
		}
		return nil
	}
	if running := s.unameR(); kernelVersion != running {
		return fmt.Errorf("driver %q was built for kernel %s but this device runs %s", name, kernelVersion, running)
	}
	return nil
}

// finalize verifies a staged .raw (sha256 + signature), places it under its
// stable name, writes the modules-load config, and applies it. It holds s.mu:
// place+apply mutate shared state (/data store and the merged /usr), so they
// must not interleave with another install or a remove.
func (s *DriverService) finalize(ctx context.Context, spec DriverInstallSpec, tmpPath string, digest []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	got := hex.EncodeToString(digest)
	if spec.SHA256 != "" && !strings.EqualFold(spec.SHA256, got) {
		return fmt.Errorf("sha256 mismatch: expected %s, got %s", spec.SHA256, got)
	}
	// SECURITY: with no signing key embedded the verifier is a no-op, so sha256
	// proves integrity but not origin. Accepted only for the mTLS RPC pre-GA (that
	// caller already has a root shell on the same server); the unauthenticated
	// seed sets requireSignature and is refused below.
	if !s.verifier.Enabled() {
		if s.requireSignature {
			return fmt.Errorf("driver %q cannot be authenticated (no signing key embedded); refusing install from an unauthenticated source", spec.Name)
		}
		s.logger.Warn("driver signature verification is DISABLED (no signing key embedded); installing unverified kernel modules",
			zap.String("name", spec.Name), zap.String("sha256", got))
	}
	if err := s.verifier.Verify(digest, spec.Signature); err != nil {
		switch {
		case errors.Is(err, sigverify.ErrUnsigned):
			return fmt.Errorf("driver add-on is unsigned; refusing install")
		case errors.Is(err, sigverify.ErrBadSignature):
			return fmt.Errorf("driver add-on signature verification failed; refusing install")
		default:
			return fmt.Errorf("driver add-on signature verification error: %v", err)
		}
	}
	// Roll back to the pre-install state on failure: a reinstall/upgrade that
	// fails must leave the working version in place, not delete it, and must not
	// leave the rest of the add-ons unmerged (a failed refresh unmerges all).
	snap, err := s.snapshotDriver(spec.Name)
	if err != nil {
		return err
	}
	if err := s.place(spec.Name, tmpPath, spec.ModulesLoad); err != nil {
		s.removePlaced(spec.Name) // place() may have renamed the .raw before failing
		snap.restore()
		return err
	}
	if err := s.apply(ctx); err != nil {
		s.removePlaced(spec.Name)
		snap.restore()
		s.reapply(ctx)
		return fmt.Errorf("apply failed: %v", err)
	}
	snap.commit()
	return nil
}

// stagedFile accumulates a .raw to a temp file on the /data store while hashing
// it and enforcing the size cap.
type stagedFile struct {
	f       *os.File
	path    string
	hasher  hash.Hash
	written int64
}

// newStagedFile creates a temp file on the same filesystem as the enabled dir so
// the eventual rename into place is atomic.
func (s *DriverService) newStagedFile() (*stagedFile, error) {
	if err := os.MkdirAll(s.enabledDir, 0o755); err != nil {
		return nil, fmt.Errorf("preparing driver store: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(s.enabledDir), ".driver-*.raw.tmp")
	if err != nil {
		return nil, fmt.Errorf("creating temp file: %w", err)
	}
	return &stagedFile{f: f, path: f.Name(), hasher: sha256.New()}, nil
}

func (sf *stagedFile) Write(p []byte) (int, error) {
	sf.written += int64(len(p))
	if sf.written > maxDriverImageSize {
		return 0, fmt.Errorf("driver image exceeds the %d-byte limit", maxDriverImageSize)
	}
	if _, err := sf.f.Write(p); err != nil {
		return 0, err
	}
	sf.hasher.Write(p)
	return len(p), nil
}

// commit flushes and closes the temp file, returning its sha256 digest.
func (sf *stagedFile) commit() ([]byte, error) {
	if sf.written == 0 {
		sf.abort()
		return nil, fmt.Errorf("driver image is empty")
	}
	if err := sf.f.Sync(); err != nil {
		sf.abort()
		return nil, fmt.Errorf("syncing driver image: %w", err)
	}
	if err := sf.f.Close(); err != nil {
		os.Remove(sf.path)
		return nil, fmt.Errorf("closing driver image: %w", err)
	}
	return sf.hasher.Sum(nil), nil
}

func (sf *stagedFile) abort() {
	sf.f.Close()
	os.Remove(sf.path)
}

// stageFromURL downloads the .raw to a temp file, returning its path + sha256.
func (s *DriverService) stageFromURL(ctx context.Context, url string) (string, []byte, error) {
	sf, err := s.newStagedFile()
	if err != nil {
		return "", nil, err
	}
	body, err := s.httpGet(ctx, url)
	if err != nil {
		sf.abort()
		return "", nil, fmt.Errorf("fetching driver image: %w", err)
	}
	defer body.Close()
	if _, err := io.Copy(sf, body); err != nil {
		sf.abort()
		return "", nil, fmt.Errorf("downloading driver image: %w", err)
	}
	digest, err := sf.commit()
	if err != nil {
		return "", nil, err
	}
	return sf.path, digest, nil
}

// stageFromStream reads the .raw from the client's InstallDriver chunks.
func (s *DriverService) stageFromStream(stream grpc.BidiStreamingServer[agentpbv2.InstallDriverRequest, agentpbv2.DriverApplyResponse]) (string, []byte, error) {
	sf, err := s.newStagedFile()
	if err != nil {
		return "", nil, err
	}
	for {
		msg, rerr := stream.Recv()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			sf.abort()
			return "", nil, fmt.Errorf("receiving driver image: %w", rerr)
		}
		if chunk := msg.GetChunk(); chunk != nil {
			if _, werr := sf.Write(chunk.GetData()); werr != nil {
				sf.abort()
				return "", nil, werr
			}
		}
	}
	digest, err := sf.commit()
	if err != nil {
		return "", nil, err
	}
	return sf.path, digest, nil
}

// place atomically moves the staged .raw to enabled/<name>.raw and writes the
// modules-load config. The on-device filename derives from the verified name, so
// it matches the image's extension-release (systemd-sysext merges by name).
func (s *DriverService) place(name, tmpPath string, modules []string) error {
	dst := filepath.Join(s.enabledDir, name+".raw")
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("setting driver image permissions: %w", err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		return fmt.Errorf("placing driver image: %w", err)
	}
	if err := os.MkdirAll(s.modulesDir, 0o755); err != nil {
		return fmt.Errorf("preparing modules-load dir: %w", err)
	}
	conf := filepath.Join(s.modulesDir, name+".conf")
	if len(modules) == 0 {
		os.Remove(conf) //nolint:errcheck // no modules to autoload for this add-on
		return nil
	}
	if err := os.WriteFile(conf, []byte(strings.Join(modules, "\n")+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing modules-load config: %w", err)
	}
	return nil
}

// removePlaced deletes a driver add-on's on-disk state (its .raw and modules-load
// conf), used to roll back a partially-applied install so a failure leaves nothing
// installed or declared.
func (s *DriverService) removePlaced(name string) {
	os.Remove(filepath.Join(s.enabledDir, name+".raw"))  //nolint:errcheck
	os.Remove(filepath.Join(s.modulesDir, name+".conf")) //nolint:errcheck
}

// driverSnapshot holds a driver's pre-operation state so a failed install or
// remove restores the working version instead of destroying it. Backups are
// renames to dotted .bak names, which the apply script's globs ignore.
type driverSnapshot struct {
	rawBak, confBak string // "" when the file did not exist
	raw, conf       string
}

// snapshotDriver moves a driver's current state aside. It fails rather than
// returning a partial snapshot: callers rely on restore() to undo everything,
// so a half-stashed driver must not reach the mutating step.
func (s *DriverService) snapshotDriver(name string) (*driverSnapshot, error) {
	snap := &driverSnapshot{
		raw:  filepath.Join(s.enabledDir, name+".raw"),
		conf: filepath.Join(s.modulesDir, name+".conf"),
	}
	stash := func(path, bak string) (string, error) {
		if _, err := os.Stat(path); err != nil {
			return "", nil // nothing installed yet
		}
		if err := os.Rename(path, bak); err != nil {
			return "", fmt.Errorf("setting %s aside: %w", filepath.Base(path), err)
		}
		return bak, nil
	}
	var err error
	if snap.rawBak, err = stash(snap.raw, filepath.Join(s.enabledDir, "."+name+".raw.bak")); err != nil {
		return nil, err
	}
	if snap.confBak, err = stash(snap.conf, filepath.Join(s.modulesDir, "."+name+".conf.bak")); err != nil {
		snap.restore() // put the .raw back rather than leave it stashed
		return nil, err
	}
	return snap, nil
}

// installed reports whether the driver existed before the operation.
func (snap *driverSnapshot) installed() bool { return snap.rawBak != "" }

// restore puts the snapshotted state back, replacing whatever the failed
// operation left behind.
func (snap *driverSnapshot) restore() {
	for _, p := range []struct{ bak, dst string }{{snap.rawBak, snap.raw}, {snap.confBak, snap.conf}} {
		if p.bak == "" {
			continue
		}
		os.Remove(p.dst)        //nolint:errcheck // replaced by the backup below
		os.Rename(p.bak, p.dst) //nolint:errcheck // best effort; nothing better on failure
	}
}

// commit drops the backups once the operation has succeeded.
func (snap *driverSnapshot) commit() {
	for _, bak := range []string{snap.rawBak, snap.confBak} {
		if bak != "" {
			os.Remove(bak) //nolint:errcheck
		}
	}
}

// reapply re-runs the apply script after a rollback. A failed apply tears down
// the module overlay and aborts `systemd-sysext refresh` wholesale, which
// unmerges every healthy add-on too, so the restored set must be merged again.
func (s *DriverService) reapply(ctx context.Context) {
	if err := s.apply(ctx); err != nil {
		s.logger.Error("could not restore the previous driver set after a failed operation; a reboot will re-apply it",
			zap.Error(err))
	}
}

// apply runs the sysext-apply script detached from the caller's cancellation: a
// CLI Ctrl-C/disconnect must not SIGKILL it mid-merge and leave /usr half-applied.
// A 2-minute timeout still bounds a hung script.
func (s *DriverService) apply(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.applyScript)
	cmd.Env = driverEnvWithPath()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v (%s)", s.applyScript, err, strings.TrimSpace(string(out)))
	}
	s.logger.Info("sysext-apply completed", zap.String("output", strings.TrimSpace(string(out))))
	return nil
}

// RemoveDriver drops a driver add-on from the enabled set and re-applies.
func (s *DriverService) RemoveDriver(req *agentpbv2.RemoveDriverRequest, stream grpc.ServerStreamingServer[agentpbv2.DriverApplyResponse]) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	name := req.GetName()
	if err := validateDriverName(name); err != nil {
		return sendDriverFailure(stream, err.Error())
	}
	raw := filepath.Join(s.enabledDir, name+".raw")
	if _, err := os.Stat(raw); err != nil {
		return sendDriverFailure(stream, fmt.Sprintf("driver %q is not installed", name))
	}

	sendDriverProgress(stream, "removing", 30)
	// Snapshot rather than delete outright: if the unmerge fails the driver is
	// still merged into /usr, so putting it back keeps /data agreeing with the
	// running system and lets the operator retry instead of stranding a
	// merged-but-unlisted driver until reboot.
	snap, err := s.snapshotDriver(name)
	if err != nil {
		return sendDriverFailure(stream, err.Error())
	}
	if !snap.installed() {
		return sendDriverFailure(stream, fmt.Sprintf("driver %q is not installed", name))
	}

	sendDriverProgress(stream, "applying", 70)
	if err := s.apply(stream.Context()); err != nil {
		snap.restore()
		s.reapply(stream.Context())
		return sendDriverFailure(stream, fmt.Sprintf("apply failed: %v", err))
	}
	snap.commit()

	s.logger.Info("driver add-on removed", zap.String("name", name))
	// An already-loaded module stays resident until a reboot (it is not force-unloaded).
	return sendDriverCompleted(stream, name, false)
}

// ListDrivers reports the installed (declared) drivers plus realized state.
func (s *DriverService) ListDrivers(ctx context.Context, _ *agentpbv2.ListDriversRequest) (*agentpbv2.ListDriversResponse, error) {
	resp := &agentpbv2.ListDriversResponse{
		BaseVersion:      osReleaseVersionID(),
		KernelVersion:    s.unameR(),
		LoadedModules:    loadedKernelModules(),
		MergedExtensions: mergedSysextNames(ctx),
	}

	loaded := make(map[string]bool, len(resp.LoadedModules))
	for _, m := range resp.LoadedModules {
		loaded[m] = true
	}

	entries, _ := os.ReadDir(s.enabledDir) //nolint:errcheck // absent dir => no drivers
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".raw") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".raw")
		mods := readModulesConf(filepath.Join(s.modulesDir, name+".conf"))
		if len(mods) == 0 {
			// Self-describing add-on: the module list is baked into the .raw and
			// surfaces at the merged path once systemd-sysext merges it. Mirror
			// wendyos-sysext-apply's precedence (/data override, then baked-in) so
			// the list reflects what actually loads.
			mods = readModulesConf(filepath.Join(s.bakedModulesDir, name+".conf"))
		}
		resp.Installed = append(resp.Installed, &agentpbv2.InstalledDriver{
			Name:        name,
			ModulesLoad: mods,
			Loaded:      allModulesLoaded(mods, loaded),
		})
	}
	return resp, nil
}

// --- helpers ---

func validateDriverName(name string) error {
	if name == "" {
		return fmt.Errorf("driver name is empty")
	}
	// A leading dot would install fine but never merge: the apply script's
	// "$ENABLED"/*.raw globs skip dotfiles, so it would look applied and do nothing.
	if strings.HasPrefix(name, ".") || strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("invalid driver name %q", name)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return fmt.Errorf("invalid driver name %q (allowed: letters, digits, '.', '_', '-')", name)
		}
	}
	return nil
}

// driverEnvWithPath is the whole environment for the privileged apply script:
// built from nothing, so an inherited IFS/BASH_ENV/LD_* can never steer it.
func driverEnvWithPath() []string {
	return []string{"PATH=" + driverSystemPath}
}

func ptr[T any](v T) *T { return &v }

func httpGetBody(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	if err := validateArtifactURL(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	// Redirects run on the caller's goroutine while dials run on their own, so
	// the current host is shared through an atomic rather than a closed-over var.
	var host atomic.Pointer[string]
	host.Store(ptr(req.URL.Hostname()))
	client := &http.Client{
		Timeout: 5 * time.Minute,
		// Check the address actually dialled: Control sees the resolved IP, so a
		// validated name cannot be rebound to an internal one in between.
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 30 * time.Second,
				Control: func(_, address string, _ syscall.RawConn) error {
					return checkDialAddress(*host.Load(), address)
				},
			}).DialContext,
		},
		// Every hop must pass the same checks, or a permitted URL could bounce
		// the fetch somewhere it would never have been allowed to go directly.
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects fetching driver image")
			}
			if err := validateArtifactURL(r.URL.String()); err != nil {
				return err
			}
			host.Store(ptr(r.URL.Hostname()))
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected status %d fetching %s", resp.StatusCode, rawURL)
	}
	return resp.Body, nil
}

// validateArtifactURL restricts driver fetches to http/https with a host. Pinning
// to an allowlisted registry host and blocking internal address ranges is a
// follow-up for when the registry host is defined (it must not break on-prem or
// usb0 link-local registries used for local installs).
// driverRegistryHost is the only host the agent fetches driver images from by
// default: the public bucket the CLI resolves add-ons out of. Anything else has
// to be named explicitly, because this fetch is a request primitive aimed at
// whatever the caller asks for.
const driverRegistryHost = "storage.googleapis.com"

// driverExtraHostsEnv widens the allowlist (comma-separated) for bench and
// on-prem registries. Setting it on the agent unit takes root, so hosts named
// here are also exempt from the internal-address check.
const driverExtraHostsEnv = "WENDYOS_DRIVER_ARTIFACT_HOSTS"

func extraArtifactHosts() map[string]bool {
	hosts := map[string]bool{}
	for _, h := range strings.Split(os.Getenv(driverExtraHostsEnv), ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			hosts[h] = true
		}
	}
	return hosts
}

// validateArtifactURL vets a URL the agent will fetch on the caller's behalf.
// Default-deny by host: unrestricted, this is an SSRF probe into whatever the
// device can reach. httpGetBody re-runs it on every redirect hop.
func validateArtifactURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid artifact URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("artifact URL scheme %q not allowed (use http or https)", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("artifact URL has no host")
	}
	// Credentials in the URL would be sent to whatever the host resolves to, and
	// land in logs; the registry never needs them.
	if u.User != nil {
		return fmt.Errorf("artifact URL must not embed credentials")
	}
	host := strings.ToLower(u.Hostname())
	if host != driverRegistryHost && !extraArtifactHosts()[host] {
		return fmt.Errorf("artifact URL host %q is not an allowed driver registry (set %s to allow it)", host, driverExtraHostsEnv)
	}
	return nil
}

// checkDialAddress rejects a connection into the device's own networks. It sees
// the resolved address at dial time, so a name that passed validation cannot be
// rebound to an internal IP afterwards. Explicitly opted-in hosts are exempt.
func checkDialAddress(host, address string) error {
	if extraArtifactHosts()[strings.ToLower(host)] {
		return nil
	}
	ipStr, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("unexpected dial address %q: %w", address, err)
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return fmt.Errorf("unresolvable dial address %q", address)
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return fmt.Errorf("artifact host resolved to the internal address %s; refusing to fetch", ip)
	}
	return nil
}

func unameRelease() string {
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

func osReleaseVersionID() string {
	_, version, _ := parseOSRelease("/etc/os-release")
	return version
}

func loadedKernelModules() []string {
	f, err := os.Open("/proc/modules")
	if err != nil {
		return nil
	}
	defer f.Close()
	var mods []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) > 0 {
			mods = append(mods, fields[0])
		}
	}
	return mods
}

func readModulesConf(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var mods []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		mods = append(mods, line)
	}
	return mods
}

func allModulesLoaded(mods []string, loaded map[string]bool) bool {
	if len(mods) == 0 {
		return false
	}
	for _, m := range mods {
		// modprobe treats '-' and '_' interchangeably; /proc/modules uses '_'.
		if !loaded[strings.ReplaceAll(m, "-", "_")] && !loaded[m] {
			return false
		}
	}
	return true
}

func mergedSysextNames(ctx context.Context) []string {
	cmd := exec.CommandContext(ctx, "systemd-sysext", "status", "--no-legend")
	cmd.Env = driverEnvWithPath()
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "/usr" && fields[1] != "none" {
			names = append(names, fields[1])
		}
	}
	return names
}
