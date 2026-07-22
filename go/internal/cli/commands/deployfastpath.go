package commands

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// deployFingerprint records what was last successfully deployed for a given app
// to a given device from this machine. It lets `wendy run --detach` skip the
// whole build → OCI export → chunk-diff → reassemble pipeline when nothing that
// could affect the image has changed: instead of rebuilding, we just ensure the
// already-deployed container is running.
//
// This is intentionally a local-only, best-effort optimization (WDY fast path):
// the fingerprint is trusted as-is, and the device is only consulted to confirm
// the app still exists and learn whether it is running. Any mismatch, missing
// fingerprint, or RPC hiccup falls back to the normal deploy, so a stale cache
// can never deploy the wrong code — at worst it triggers an unnecessary build.
type deployFingerprint struct {
	// InputHash is computed by computeBuildInputHash over everything that can
	// affect the built image (Dockerfile, build context, build args, platform).
	InputHash string `json:"inputHash"`
	// AppVersion is the wendy.json version at deploy time, used as a cheap
	// cross-check against the version the device reports.
	AppVersion string `json:"appVersion,omitempty"`
	// LayerDiffIDs are the uncompressed layer diff IDs of the image content that
	// was actually pushed to this device on the last successful deploy. A skip is
	// only permitted when the device still holds every one of these layers (see
	// deviceHasAllLayers): an unchanged input hash alone does not prove the device
	// still has the content — blobs can be GC'd, a partial push can leave the
	// manifest without its blobs, or the local base image can be rebuilt without
	// changing the hash. Verifying the layers are present closes the WDY-1824 hole
	// where the CLI skipped the push and reported success while the device kept
	// running a stale/partial image. Empty (e.g. recorded by a path that doesn't
	// surface diff IDs) means "cannot verify" → never skip.
	LayerDiffIDs []string `json:"layerDiffIds,omitempty"`
}

// deployFingerprintPath returns the on-disk location for an app+device
// fingerprint. deviceKey identifies the target device (see deviceFingerprintKey).
func deployFingerprintPath(appID, deviceKey string) (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cacheDir, "wendy", "deploy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := sanitizeCacheKey(appID) + "_" + sanitizeCacheKey(deviceKey) + ".json"
	return filepath.Join(dir, name), nil
}

// deviceFingerprintKey derives a stable per-device key from the agent version
// response. The agent's public key is the most stable identifier when present;
// otherwise we fall back to a generic key (the worst case is that two devices
// without public keys share a fingerprint slot and occasionally rebuild).
func deviceFingerprintKey(v *agentpb.GetAgentVersionResponse) string {
	if pk := v.GetPublicKey(); pk != "" {
		sum := sha256.Sum256([]byte(pk))
		return hex.EncodeToString(sum[:8])
	}
	return "default"
}

// sanitizeCacheKey makes a string safe to use as a filename component.
func sanitizeCacheKey(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

func loadDeployFingerprint(appID, deviceKey string) (*deployFingerprint, bool) {
	p, err := deployFingerprintPath(appID, deviceKey)
	if err != nil {
		return nil, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	var fp deployFingerprint
	if err := json.Unmarshal(data, &fp); err != nil {
		return nil, false
	}
	if fp.InputHash == "" {
		return nil, false
	}
	return &fp, true
}

func saveDeployFingerprint(appID, deviceKey string, fp deployFingerprint) {
	p, err := deployFingerprintPath(appID, deviceKey)
	if err != nil {
		return
	}
	data, err := json.Marshal(fp)
	if err != nil {
		return
	}
	// Best-effort: a failed write just means the next run rebuilds.
	_ = os.WriteFile(p, data, 0o644)
}

// computeBuildInputHash hashes everything that can change the built image:
// the resolved Dockerfile contents, the build context files (honoring
// .dockerignore conservatively), and the sorted build args + platform.
//
// Correctness rule: the hash MUST change whenever anything buildkit could use
// changes. We therefore over-approximate — we hash the whole context minus
// .dockerignore'd paths (a superset of what COPY/ADD can pull in), and any
// .dockerignore pattern we cannot confidently parse is simply not applied (so a
// file is hashed rather than skipped). This can only cause an unnecessary
// rebuild, never a missed change.
func computeBuildInputHash(cwd, dockerfile, platform string, buildArgs map[string]string) (string, error) {
	h := sha256.New()
	io.WriteString(h, "wendy-deploy-fingerprint-v1\n")
	io.WriteString(h, "platform="+platform+"\n")

	keys := make([]string, 0, len(buildArgs))
	for k := range buildArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		io.WriteString(h, "arg "+k+"="+buildArgs[k]+"\n")
	}

	// Resolve and hash the Dockerfile.
	dfPath := filepath.Join(cwd, "Dockerfile")
	if dockerfile != "" {
		resolved, err := confinedDockerfilePath(cwd, dockerfile)
		if err != nil {
			return "", err
		}
		dfPath = resolved
	}
	dfData, err := os.ReadFile(dfPath)
	if err != nil {
		return "", fmt.Errorf("reading Dockerfile for fingerprint: %w", err)
	}
	io.WriteString(h, fmt.Sprintf("dockerfile %d\n", len(dfData)))
	h.Write(dfData)

	// Hash the build context, honoring .dockerignore.
	ignore := loadDockerIgnore(cwd)
	var files []string
	err = filepath.WalkDir(cwd, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(cwd, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if ignore.matches(rel + "/") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if ignore.matches(rel) {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walking build context for fingerprint: %w", err)
	}
	sort.Strings(files)
	for _, rel := range files {
		f, err := os.Open(filepath.Join(cwd, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		io.WriteString(h, "file "+rel+"\n")
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return "", err
		}
		f.Close()
	}

	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// dockerIgnore is a conservative .dockerignore matcher. It only excludes a path
// when a pattern confidently matches; negation (!) and patterns it cannot parse
// are ignored, which keeps the matcher safe (it can only under-exclude, leading
// to extra files in the hash and at worst an unnecessary rebuild).
type dockerIgnore struct {
	patterns  []string
	negations []string // re-include patterns (lines starting with '!')
}

func loadDockerIgnore(cwd string) *dockerIgnore {
	di := &dockerIgnore{}
	f, err := os.Open(filepath.Join(cwd, ".dockerignore"))
	if err != nil {
		return di
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negate := strings.HasPrefix(line, "!")
		line = strings.TrimPrefix(line, "!")
		line = strings.TrimPrefix(line, "/")
		line = strings.TrimSuffix(line, "/")
		if line == "" {
			continue
		}
		if negate {
			di.negations = append(di.negations, line)
		} else {
			di.patterns = append(di.patterns, line)
		}
	}
	// A scan error just yields a partial pattern set; the matcher stays safe
	// (fewer exclusions → more files hashed → at worst an unnecessary rebuild).
	_ = sc.Err()
	return di
}

// matches reports whether rel (a forward-slash relative path; directories carry
// a trailing slash) is excluded. A path matching any negation pattern is always
// kept (hashed): negations re-include files, so honoring them as "force include"
// keeps the matcher safe — it can only over-hash, never under-detect a change.
func (di *dockerIgnore) matches(rel string) bool {
	clean := strings.TrimSuffix(rel, "/")
	for _, neg := range di.negations {
		if matchIgnorePattern(neg, clean) {
			return false
		}
	}
	for _, pat := range di.patterns {
		if matchIgnorePattern(pat, clean) {
			return true
		}
	}
	return false
}

// matchIgnorePattern reports whether a single (already sign/slash-trimmed)
// pattern matches the cleaned relative path.
func matchIgnorePattern(pat, clean string) bool {
	// Exact path or directory-prefix match.
	if clean == pat || strings.HasPrefix(clean, pat+"/") {
		return true
	}
	// Glob against the full relative path.
	if ok, err := path.Match(pat, clean); err == nil && ok {
		return true
	}
	// Glob against the basename for patterns without a separator (e.g. *.pyc).
	if !strings.Contains(pat, "/") {
		if ok, err := path.Match(pat, path.Base(clean)); err == nil && ok {
			return true
		}
	}
	return false
}

// tryDeployFastPath attempts to satisfy a detached run without building. It
// returns (true, nil) when the app was confirmed up to date and is now running
// (either it already was, or we started it). It returns (false, nil) whenever
// the normal build/deploy path should run instead — no fingerprint, a mismatch,
// the app missing from the device, or any RPC error (all safe fallbacks).
func tryDeployFastPath(ctx context.Context, conn *grpcclient.AgentConnection, appCfg *appconfig.AppConfig, deviceKey, inputHash string, opts runOptions) (bool, error) {
	fp, ok := loadDeployFingerprint(appCfg.AppID, deviceKey)
	if !ok || fp.InputHash != inputHash {
		return false, nil
	}

	// Unchanged inputs are necessary but not sufficient: the device must still
	// hold the image content we pushed last time. Without this check a skip can
	// leave the device running a stale/partial image while the CLI reports
	// success (WDY-1824) — e.g. blobs GC'd, a half-completed push, or a rebuilt
	// local base image that never changed the input hash.
	if !deviceHasAllLayers(ctx, conn, fp.LayerDiffIDs) {
		return false, nil
	}

	state, found, err := lookupAppState(ctx, conn, appCfg.AppID)
	if err != nil || !found {
		// Device unreachable for the query or app no longer present — rebuild.
		return false, nil
	}

	if state == agentpb.AppRunningState_RUNNING {
		cliLogln("No changes detected; %s is already up to date and running.", containerDisplayName(appCfg))
		// The container is untouched, so the agent-side (in-container) hook can't
		// be re-run, but fire the host-side postStart hook so `wendy run` behaves
		// the same whether or not it took the fast path (e.g. re-opening the URL).
		runPostStartHostHook(ctx, conn, appCfg)
		return true, nil
	}

	// Present but stopped — start it without rebuilding. Mirror the normal
	// detached deploy path so the fast path stays a transparent optimization:
	// attach the agent-side postStart hook to the start RPC (via context
	// metadata), then fire the host-side postStart hook below.
	if _, err := conn.ContainerService.StartContainer(contextWithPostStartAgentHook(ctx, appCfg), &agentpb.StartContainerRequest{
		AppName:       appCfg.AppID,
		RestartPolicy: resolveRestartPolicy(opts),
	}); err != nil {
		// Could not start the existing container; fall back to a full deploy.
		return false, nil
	}
	cliLogln("No changes detected; started existing %s.", containerDisplayName(appCfg))
	runPostStartHostHook(ctx, conn, appCfg)
	return true, nil
}

// runPostStartHostHook mirrors the normal detached deploy path's host-side
// postStart handling: wait for readiness, then announce the reachable URL and
// fire the host hook fire-and-forget on a background context so it outlives
// the CLI process. A failed readiness probe skips both (see
// runPostStartIfReady). The agent-side (in-container) hook is attached
// separately to the StartContainer RPC's context, so it only runs when the
// fast path actually (re)starts the container.
func runPostStartHostHook(ctx context.Context, conn *grpcclient.AgentConnection, appCfg *appconfig.AppConfig) {
	runPostStartIfReady(ctx, context.Background(), conn, appCfg)
}

// containerExitDetail returns a short human summary of why appID's container
// stopped (e.g. "container crashed (exit 1)"), or "" if it's running, the cause
// wasn't recorded, or the lookup fails. Best-effort — it exists only to enrich
// a readiness failure, so it must never itself error out the deploy.
func containerExitDetail(ctx context.Context, conn *grpcclient.AgentConnection, appID string) string {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	stream, err := conn.ContainerService.ListContainers(cctx, &agentpb.ListContainersRequest{})
	if err != nil {
		return ""
	}
	for {
		resp, err := stream.Recv()
		if err != nil {
			break
		}
		c := resp.GetContainer()
		if c == nil || !strings.EqualFold(c.GetAppName(), appID) {
			continue
		}
		// Anything that isn't running counts — a crash-looping app (WDY-1826)
		// reports CRASH_LOOPING, not STOPPED, and its exit detail is exactly
		// what a readiness failure needs to explain itself.
		if c.GetRunningState() != agentpb.AppRunningState_RUNNING {
			if s := terminationSummary(c.GetTerminationReason(), c.GetExitCode()); s != "" {
				return "container " + s
			}
		}
		break
	}
	return ""
}

// warnReadiness logs a readiness failure, appending why the container stopped
// when the agent recorded a reason — so a bare "timed out" isn't the only thing
// the user sees when a startup crash is the real cause (WDY-1819).
func warnReadiness(ctx context.Context, conn *grpcclient.AgentConnection, appID string, err error) {
	msg := err.Error()
	if d := containerExitDetail(ctx, conn, appID); d != "" {
		msg += " — " + d
	}
	cliLogln("Warning: %s", msg)
}

// deviceHasAllLayers reports whether the device's content store still holds
// every one of diffIDs — i.e. the actual image blobs, not just a container
// record or a registry tag. It is the content check that gates every push-skip
// (WDY-1824): the local fingerprint only proves the inputs are unchanged since
// we last pushed, never that the device still has what we pushed.
//
// It is deliberately fail-closed: an empty diffID list (we never recorded what
// was deployed, so we cannot verify it), an agent too old for QueryLayers
// (Unimplemented), any RPC error, or a single missing layer all return false so
// the caller falls back to a real build+push. Only a device that confirms every
// layer is present authorizes a skip.
func deviceHasAllLayers(ctx context.Context, conn *grpcclient.AgentConnection, diffIDs []string) bool {
	if len(diffIDs) == 0 {
		return false
	}
	resp, err := conn.ContainerService.QueryLayers(ctx, &agentpb.QueryLayersRequest{DiffIds: diffIDs})
	if err != nil {
		return false
	}
	present := make(map[string]bool, len(resp.GetPresent()))
	for _, p := range resp.GetPresent() {
		present[p.GetDiffId()] = true
	}
	for _, id := range diffIDs {
		if !present[id] {
			return false
		}
	}
	return true
}

// lookupAppState queries the device for the running state of a single app.
func lookupAppState(ctx context.Context, conn *grpcclient.AgentConnection, appID string) (agentpb.AppRunningState, bool, error) {
	stream, err := conn.ContainerService.ListContainers(ctx, &agentpb.ListContainersRequest{})
	if err != nil {
		return agentpb.AppRunningState_STOPPED, false, err
	}
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return agentpb.AppRunningState_STOPPED, false, err
		}
		c := resp.GetContainer()
		if c == nil {
			continue
		}
		if strings.EqualFold(c.GetAppName(), appID) {
			return c.GetRunningState(), true, nil
		}
	}
	return agentpb.AppRunningState_STOPPED, false, nil
}
