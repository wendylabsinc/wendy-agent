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

	"github.com/distribution/reference"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// deployFingerprint records what was last successfully deployed for a given app
// or service to a given device from this machine. It lets run paths skip the
// whole build → OCI export → chunk-diff → reassemble pipeline when nothing that
// could affect the image or effective service configuration has changed. The
// single-container detached path also uses it to ensure the existing container
// is running without recreating it.
//
// This is intentionally a local-only, best-effort optimization (WDY fast path).
// The fingerprint selects a candidate, but the device must verify both its
// image layers and the container's persisted immutable identity before a skip.
// Any mismatch, missing state, or RPC hiccup falls back to the normal deploy.
type deployFingerprint struct {
	// InputHash is the legacy/shared desired-state slot. Single-container
	// fingerprints store ContainerIdentityHash here as well for compatibility;
	// service fingerprints store their service desired-state hash.
	InputHash string `json:"inputHash"`
	// ContainerIdentityHash covers image content and every create-time runtime
	// setting. It excludes only metadata the agent can safely reconcile on a
	// running task. An empty value denotes a legacy fingerprint and fails closed.
	ContainerIdentityHash string `json:"containerIdentityHash,omitempty"`
	// LiveMetadataHash covers the subset that UpdateRunningContainerMetadata can
	// change without replacing the container: currently version and restart
	// policy. It is compared only after container identity and content checks.
	LiveMetadataHash string `json:"liveMetadataHash,omitempty"`
	// AppVersion mirrors the live-metadata version at deploy time. The fast path
	// cross-checks it against the version the device reports before a no-op.
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

// deployRuntimeIdentity is the subset of a single-container deployment that
// affects the container the agent creates. Build-only and CLI-only settings
// are deliberately absent: changing a readiness probe or a host-side hook must
// not invalidate an otherwise reusable container, while changing resources,
// entitlements, isolation, framework wiring, arguments, or environment must.
// Restart policy and app version are tracked separately as live metadata.
//
// Keep AppConfig as a value so newly-added device runtime fields fail safe: they
// are included unless runtimeAppConfig explicitly clears them below.
type deployRuntimeIdentity struct {
	AppConfig appconfig.AppConfig `json:"appConfig"`
	UserArgs  []string            `json:"userArgs,omitempty"`
	Env       []string            `json:"env,omitempty"`
}

type deployLiveMetadataIdentity struct {
	AppVersion    string                 `json:"appVersion,omitempty"`
	RestartPolicy *agentpb.RestartPolicy `json:"restartPolicy,omitempty"`
}

type deployIdentityHashes struct {
	Container string
	Metadata  string
}

// computeDeployIdentityHashes separates immutable create-time identity from
// metadata the agent can reconcile on a running container. New device-runtime
// fields remain in AppConfig by default and therefore fail safely into a full
// deploy unless they are deliberately cleared here.
func computeDeployIdentityHashes(imageInputHash string, appCfg *appconfig.AppConfig, userArgs, env []string, restartPolicy *agentpb.RestartPolicy) (deployIdentityHashes, error) {
	if imageInputHash == "" || appCfg == nil {
		return deployIdentityHashes{}, fmt.Errorf("image input hash and app config are required")
	}

	runtimeCfg := *appCfg
	// These fields influence local build selection, native file deployment, or
	// CLI lifecycle behavior, but are not consumed when the Linux container is
	// created. Excluding them lets readiness/hook-only edits retain the running
	// container without weakening device-runtime correctness.
	runtimeCfg.Platform = ""
	runtimeCfg.Xcode = nil
	runtimeCfg.Run = nil // effective arguments are carried separately below
	runtimeCfg.Readiness = nil
	runtimeCfg.Hooks = nil
	runtimeCfg.Python = nil
	runtimeCfg.Files = nil
	runtimeCfg.Brewfile = ""
	runtimeCfg.Env = nil // resolved values are carried separately below
	runtimeCfg.Services = nil
	runtimeCfg.Version = "" // live label metadata, carried separately below

	payload, err := json.Marshal(deployRuntimeIdentity{
		AppConfig: runtimeCfg,
		UserArgs:  userArgs,
		Env:       env,
	})
	if err != nil {
		return deployIdentityHashes{}, fmt.Errorf("marshaling deploy runtime identity: %w", err)
	}

	h := sha256.New()
	io.WriteString(h, "wendy-container-identity-v1\n")
	io.WriteString(h, imageInputHash)
	h.Write([]byte{'\n'})
	h.Write(payload)
	containerHash := "sha256:" + hex.EncodeToString(h.Sum(nil))

	metadata, err := json.Marshal(deployLiveMetadataIdentity{
		AppVersion:    effectiveAppVersion(appCfg.Version),
		RestartPolicy: restartPolicy,
	})
	if err != nil {
		return deployIdentityHashes{}, fmt.Errorf("marshaling deploy live metadata identity: %w", err)
	}
	h = sha256.New()
	io.WriteString(h, "wendy-live-metadata-identity-v1\n")
	h.Write(metadata)
	return deployIdentityHashes{
		Container: containerHash,
		Metadata:  "sha256:" + hex.EncodeToString(h.Sum(nil)),
	}, nil
}

func effectiveAppVersion(version string) string {
	if version == "" {
		return "latest"
	}
	return version
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
	if fp.InputHash == "" && fp.ContainerIdentityHash == "" {
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
// .dockerignore conservatively), and the sorted build args + platform. deployEnv
// is hashed too — it is applied at container create rather than at build, so a
// changed env must still force a redeploy even when the image is identical.
//
// Correctness rule: the hash MUST change whenever anything buildkit could use
// changes. We therefore over-approximate — we hash the whole context minus
// .dockerignore'd paths (a superset of what COPY/ADD can pull in), and any
// .dockerignore pattern we cannot confidently parse is simply not applied (so a
// file is hashed rather than skipped). This can only cause an unnecessary
// rebuild, never a missed change.
func computeBuildInputHash(cwd, dockerfile, platform string, buildArgs map[string]string, deployEnv []string) (string, error) {
	h := sha256.New()
	// v2: invalidates fingerprints recorded while the stale-manifest bug
	// (fixed 2026-08-08 in this PR) could pair a fresh input hash with a
	// stale deploy — forces one honest rebuild per app after upgrade.
	io.WriteString(h, "wendy-deploy-fingerprint-v2\n")
	io.WriteString(h, "platform="+platform+"\n")

	// deployEnv arrives sorted from resolveServiceEnv; --env order is the
	// user's and is hashed as given.
	for _, kv := range deployEnv {
		io.WriteString(h, "env "+kv+"\n")
	}

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

	// Hash the build context, honoring the ignore file the build itself uses:
	// BuildKit gives <dockerfile>.dockerignore precedence over .dockerignore
	// (the Stagefile flow derives a deny-all allowlist there), so the walk must
	// follow the same file or it hashes paths the build can never see.
	ignore := loadDockerIgnoreForBuild(cwd, dfPath)
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

// dockerfileBasesContentPinned reports whether rebuilding this Dockerfile is
// independent of mutable registry tags. Persistent no-build skipping is only
// safe when every external base is scratch or digest-pinned; merely proving
// that the previously-built layers still exist on the device does not prove a
// mutable FROM tag still resolves to those layers.
//
// This intentionally accepts only a small, unambiguous subset of Dockerfile
// syntax. ARG-expanded bases and malformed/uncertain instructions fail closed
// to a normal build. Multi-stage references to an earlier named stage are safe.
func dockerfileBasesContentPinned(cwd, dockerfile string) (bool, error) {
	dfPath := filepath.Join(cwd, "Dockerfile")
	if dockerfile != "" {
		resolved, err := confinedDockerfilePath(cwd, dockerfile)
		if err != nil {
			return false, err
		}
		dfPath = resolved
	}
	f, err := os.Open(dfPath)
	if err != nil {
		return false, fmt.Errorf("reading Dockerfile base images: %w", err)
	}
	defer f.Close()

	var logical strings.Builder
	stages := make(map[string]struct{})
	foundFrom := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		continued := strings.HasSuffix(line, "\\")
		line = strings.TrimSpace(strings.TrimSuffix(line, "\\"))
		if logical.Len() > 0 {
			logical.WriteByte(' ')
		}
		logical.WriteString(line)
		if continued {
			continue
		}

		fields := strings.Fields(logical.String())
		logical.Reset()
		if len(fields) == 0 || !strings.EqualFold(fields[0], "FROM") {
			continue
		}
		foundFrom = true
		i := 1
		for i < len(fields) && strings.HasPrefix(fields[i], "--") {
			// FROM flags use --key=value. Reject split/unknown forms instead of
			// guessing which following token is the image reference.
			if !strings.Contains(fields[i], "=") {
				return false, nil
			}
			i++
		}
		if i >= len(fields) {
			return false, nil
		}
		base := fields[i]
		_, priorStage := stages[strings.ToLower(base)]
		if !strings.EqualFold(base, "scratch") && !priorStage {
			parsed, parseErr := reference.ParseAnyReference(base)
			if parseErr != nil {
				return false, nil
			}
			if _, pinned := parsed.(reference.Digested); !pinned {
				return false, nil
			}
		}

		if i+2 < len(fields) && strings.EqualFold(fields[i+1], "AS") {
			alias := strings.ToLower(fields[i+2])
			if alias == "" || strings.HasPrefix(alias, "$") {
				return false, nil
			}
			stages[alias] = struct{}{}
		} else if i+1 != len(fields) {
			return false, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("scanning Dockerfile base images: %w", err)
	}
	if logical.Len() != 0 {
		return false, nil
	}
	return foundFrom, nil
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
	return loadDockerIgnoreFile(filepath.Join(cwd, ".dockerignore"))
}

// loadDockerIgnoreForBuild picks the ignore file BuildKit would use for this
// dockerfile: <dockerfile>.dockerignore when present, else the context's
// .dockerignore.
func loadDockerIgnoreForBuild(cwd, dockerfilePath string) *dockerIgnore {
	if dockerfilePath != "" {
		perDockerfile := dockerfilePath + ".dockerignore"
		if fi, err := os.Stat(perDockerfile); err == nil && fi.Mode().IsRegular() {
			return loadDockerIgnoreFile(perDockerfile)
		}
	}
	return loadDockerIgnore(cwd)
}

func loadDockerIgnoreFile(path string) *dockerIgnore {
	di := &dockerIgnore{}
	f, err := os.Open(path)
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
	isDir := strings.HasSuffix(rel, "/")
	for _, neg := range di.negations {
		if matchIgnorePattern(neg, clean) {
			return false
		}
		// A directory with a re-included descendant must stay walkable: with
		// "*" + "!src/app.py", skipping src/ wholesale would hide the allowlisted
		// file's changes (the unsafe direction — a missed change, not an extra
		// rebuild).
		if isDir && strings.HasPrefix(neg, clean+"/") {
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
// returns (true, nil) when the app was confirmed up to date and still running.
// It returns (false, nil) whenever
// the normal build/deploy path should run instead — no fingerprint, a mismatch,
// the app missing from the device, or any RPC error (all safe fallbacks).
func tryDeployFastPath(ctx context.Context, conn *grpcclient.AgentConnection, appCfg *appconfig.AppConfig, deviceKey string, identity deployIdentityHashes, opts runOptions) (bool, error) {
	fp, ok := loadDeployFingerprint(appCfg.AppID, deviceKey)
	if !ok || fp.ContainerIdentityHash == "" || fp.ContainerIdentityHash != identity.Container {
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

	state, deviceVersion, found, err := lookupAppState(ctx, conn, appCfg.AppID)
	if err != nil || !found {
		// Device unreachable for the query or app no longer present — rebuild.
		return false, nil
	}

	if state == agentpb.AppRunningState_RUNNING {
		// Always perform the agent-side compare-and-swap, even when metadata
		// appears unchanged locally. Another deployer may have replaced the app
		// since this machine wrote its fingerprint; the persisted identity is the
		// only authoritative proof that this is still our exact container.
		if _, err := conn.ContainerService.UpdateRunningContainerMetadata(ctx, &agentpb.UpdateRunningContainerMetadataRequest{
			AppName:                   appCfg.AppID,
			AppVersion:                effectiveAppVersion(appCfg.Version),
			RestartPolicy:             resolveRestartPolicy(opts),
			ExpectedContainerIdentity: identity.Container,
		}); err != nil {
			return false, nil
		}
		if fp.LiveMetadataHash != identity.Metadata || deviceVersion != effectiveAppVersion(appCfg.Version) {
			fp.LiveMetadataHash = identity.Metadata
			fp.AppVersion = effectiveAppVersion(appCfg.Version)
			saveDeployFingerprint(appCfg.AppID, deviceKey, *fp)
			cliLogln("Updated runtime metadata for running %s without recreating it.", containerDisplayName(appCfg))
			return true, nil
		}
		cliLogln("No changes detected; %s is already up to date and running.", containerDisplayName(appCfg))
		// No host-side postStart hook: the fast path only ever runs detached
		// (see the opts.detach gate on tryDeployFastPath's caller), and
		// detached deploys don't block on readiness — see
		// runPostStartIfReady's doc comment. The container is untouched, so the
		// agent-side hook cannot re-run either.
		return true, nil
	}
	// The metadata RPC intentionally accepts only running tasks. Without an
	// equivalent identity-bearing CAS for stopped containers, starting one could
	// resurrect a deployment replaced by another machine. Rebuild fail-closed.
	return false, nil
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

// lookupAppState queries the device for the running state and reported version
// of a single app.
func lookupAppState(ctx context.Context, conn *grpcclient.AgentConnection, appID string) (agentpb.AppRunningState, string, bool, error) {
	stream, err := conn.ContainerService.ListContainers(ctx, &agentpb.ListContainersRequest{})
	if err != nil {
		return agentpb.AppRunningState_STOPPED, "", false, err
	}
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return agentpb.AppRunningState_STOPPED, "", false, err
		}
		c := resp.GetContainer()
		if c == nil {
			continue
		}
		if strings.EqualFold(c.GetAppName(), appID) {
			return c.GetRunningState(), c.GetAppVersion(), true, nil
		}
	}
	return agentpb.AppRunningState_STOPPED, "", false, nil
}
