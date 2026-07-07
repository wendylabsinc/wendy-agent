package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

const (
	maxConcurrentBuilds = 4

	// Builds and pushes are fused in one buildx invocation, so build concurrency
	// also bounds how many multi-GB images push through the single device-registry
	// mTLS tunnel at once. Large groups (e.g. the 14-service go2 template, with a
	// ~10 GB GPU image) collapse that tunnel under full fan-out, so groups at or
	// above largeGroupThreshold are throttled to largeGroupConcurrency concurrent
	// builds (WDY-1690). This is the heuristic default; users can override it with
	// --max-concurrency (WDY-1693).
	largeGroupThreshold   = 8
	largeGroupConcurrency = 2
)

// multiBuildConcurrency returns the auto (heuristic) number of service images to
// build+push at once for a group of numServices, throttling large groups to
// protect the shared device registry tunnel (WDY-1690).
func multiBuildConcurrency(numServices int) int {
	n := maxConcurrentBuilds
	if numServices >= largeGroupThreshold {
		n = largeGroupConcurrency
	}
	if n > numServices {
		n = numServices
	}
	if n < 1 {
		n = 1
	}
	return n
}

// resolveBuildConcurrency returns the effective build+push concurrency for
// buildCount services. A positive override (--max-concurrency, WDY-1693) takes
// precedence over the auto heuristic; either way the result is clamped to
// [1, buildCount].
func resolveBuildConcurrency(buildCount, override int) int {
	if buildCount < 1 {
		return 1
	}
	n := multiBuildConcurrency(buildCount)
	if override > 0 {
		n = override
	}
	if n > buildCount {
		n = buildCount
	}
	if n < 1 {
		n = 1
	}
	return n
}

func resolveServiceSubset(services map[string]*appconfig.ServiceConfig, only string) (map[string]*appconfig.ServiceConfig, error) {
	if only == "" {
		return services, nil
	}

	svc, ok := services[only]
	if !ok {
		return nil, fmt.Errorf("--service %q not found in services map", only)
	}

	subset := map[string]*appconfig.ServiceConfig{only: svc}
	var walk func(name string) error
	walk = func(name string) error {
		svc, ok := services[name]
		if !ok || svc == nil {
			return nil
		}
		for _, dep := range svc.DependsOn {
			if _, seen := subset[dep]; !seen {
				depSvc, ok := services[dep]
				if !ok {
					return fmt.Errorf("service %q depends on unknown service %q", name, dep)
				}
				subset[dep] = depSvc
				if err := walk(dep); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(only); err != nil {
		return nil, err
	}
	return subset, nil
}

// serviceTopoOrder delegates to the shared appconfig package.
func serviceTopoOrder(services map[string]*appconfig.ServiceConfig) ([]string, error) {
	return appconfig.ServiceTopoOrder(services)
}

// buildServiceImage is the per-service build+push step. It is a package var so
// stress/concurrency tests can substitute a fake builder and exercise the
// parallel scheduling, skip handling, and failure-map collection without Docker.
var buildServiceImage = buildAndPushImageForAgent

// serviceFingerprintKey namespaces a deploy fingerprint per service within an
// app group, so each service's build inputs are tracked independently.
func serviceFingerprintKey(appID, service string) string {
	return appID + "/svc/" + service
}

// deviceContainerNames returns the lowercased set of container identities the
// device currently knows about (any running state). ListContainers reports one
// entry per app group whose AppName is the bare app id; per-service identities
// live in its Services list. We record both the bare app id (single-container
// apps) and each "<appId>_<service>" name (multi-service apps), matching
// AppConfig.ContainerName / multiServiceContainerName so callers can look a
// service up directly. Best-effort: on any RPC error it returns an empty set, so
// callers simply don't skip anything.
func deviceContainerNames(ctx context.Context, conn *grpcclient.AgentConnection) map[string]bool {
	present := map[string]bool{}
	stream, err := conn.ContainerService.ListContainers(ctx, &agentpb.ListContainersRequest{})
	if err != nil {
		return present
	}
	for {
		resp, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return present
		}
		c := resp.GetContainer()
		if c == nil {
			continue
		}
		app := c.GetAppName()
		present[strings.ToLower(app)] = true
		for _, s := range c.GetServices() {
			if s.GetName() != "" {
				present[strings.ToLower(app+"_"+s.GetName())] = true
			}
		}
	}
	return present
}

// planServicePushSkips decides, per service, whether its build+push can be
// skipped. A skip is only permitted when ALL of the following hold: the build
// inputs are unchanged since the last successful push to this device, the
// device still has that service's container, AND we can confirm the device
// still holds the exact image content we pushed (contentPresent). It returns
// the skip set and the freshly computed per-service input hashes, so the caller
// can persist fingerprints for the services it actually builds.
//
// The content check is the fix for WDY-1824: an unchanged input hash and a
// present container do not prove the device still has the pushed image content
// (blobs can be GC'd, a push can half-complete, or a rebuilt local base image
// never changes the input hash), so skipping on those alone could leave the
// device running a stale/partial image while the CLI reports success.
//
// Content verification for the multi-service builder path is a known gap: these
// services are built and pushed to the *device registry* (buildAndPushImageForAgent),
// whose compressed layer blobs are keyed by compressed digest, so the diff-ID
// QueryLayers check that guards the single-service chunk-diff fast path
// (deviceHasAllLayers) can never confirm them. The WDY-1692 commit already
// called this out ("a device-registry digest pre-check is a planned follow-up
// for full robustness against a wiped registry") and it is tracked as the
// WDY-1824 follow-up. Until that registry-digest RPC lands, contentPresent
// fails closed for every registry-push service, so this path never skips — the
// safe behavior, at the cost of re-pushing unchanged images (the WDY-1692
// optimization stays dormant for multi-service, deliberately and visibly, not
// via a silently-unsatisfiable diff-ID check).
//
// Best-effort throughout: any error for a service (or WENDY_PUSH_SKIP=0) just
// means "don't skip it".
func planServicePushSkips(ctx context.Context, conn *grpcclient.AgentConnection, cwd, appID, deviceKey, platform string, services map[string]*appconfig.ServiceConfig, buildArgs map[string]string) (skip map[string]bool, hashes map[string]string) {
	skip = map[string]bool{}
	hashes = map[string]string{}
	if os.Getenv("WENDY_PUSH_SKIP") == "0" {
		return skip, hashes
	}

	present := deviceContainerNames(ctx, conn)
	for name, svc := range services {
		contextDir := filepath.Join(cwd, svc.Context)
		dockerfile, err := resolveDockerfile(contextDir, "", false)
		if err != nil {
			continue
		}
		hash, err := computeBuildInputHash(contextDir, dockerfile, platform, buildArgs)
		if err != nil {
			continue
		}
		hashes[name] = hash

		fp, ok := loadDeployFingerprint(serviceFingerprintKey(appID, name), deviceKey)
		if !ok || fp.InputHash != hash {
			continue
		}
		if !present[strings.ToLower(multiServiceContainerName(appID, name))] {
			continue
		}
		// Container presence is not enough: confirm the device still holds the
		// image content we pushed. Without this a skip can leave a stale/partial
		// image running while we report success (WDY-1824).
		if !contentPresentForService(ctx, conn, fp) {
			continue
		}
		skip[name] = true
	}
	return skip, hashes
}

// contentPresentForService reports whether the device is confirmed to still hold
// the image content recorded for a registry-push service, gating a push-skip
// (WDY-1824).
//
// It is fail-closed. The multi-service builder pushes to the device registry,
// whose layers the diff-ID QueryLayers check cannot see (compressed blobs are
// keyed by compressed digest, not by uncompressed diff ID). So recorded layer
// diff IDs are still verified via QueryLayers when present — a future
// chunk-diff-based multi-service push would record verifiable IDs — but a
// fingerprint without them (every registry push today) can never be confirmed
// and returns false. Confirming registry-pushed content needs the device
// registry-digest pre-check deferred from WDY-1692; see WDY-1824 follow-up.
func contentPresentForService(ctx context.Context, conn *grpcclient.AgentConnection, fp *deployFingerprint) bool {
	if fp == nil {
		return false
	}
	return deviceHasAllLayers(ctx, conn, fp.LayerDiffIDs)
}

// runMultiServiceWithAgent orchestrates the full build → push → create →
// stream pipeline for a multi-service wendy.json on a single agent.
func runMultiServiceWithAgent(ctx context.Context, conn *grpcclient.AgentConnection, cwd string, appCfg *appconfig.AppConfig, opts runOptions) error {
	services, err := resolveServiceSubset(appCfg.Services, opts.service)
	if err != nil {
		return err
	}

	versionResp, err := conn.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
	if err != nil {
		return fmt.Errorf("querying device version: %w", err)
	}
	agentOS := versionResp.GetOs()
	architecture := versionResp.GetCpuArchitecture()
	if architecture == "" {
		cliLogln("Warning: agent did not report CPU architecture; assuming arm64.")
		architecture = "arm64"
	}
	platform := resolveAgentPlatform(appCfg.Platform, agentOS, architecture)
	if strings.EqualFold(agentOS, appconfig.PlatformDarwin) {
		return rejectUnsupportedMacRunProject("multi-service", platform)
	}

	if err := requireRegistryAuth(ctx, conn); err != nil {
		return err
	}

	regPort := registryPort(agentOS)

	buildArgs := map[string]string{
		"WENDY_PLATFORM": wendyPlatform(versionResp.GetDeviceType()),
	}
	if opts.debug {
		cliLogln("Warning: building with WENDY_DEBUG=true — do not deploy to production.")
		buildArgs["WENDY_DEBUG"] = "true"
	}
	applyDeviceBuildArgHints(buildArgs, versionResp)

	// Ensure the Apple Container system is up once, before the parallel builds,
	// so an explicit --builder apple-container prompts/starts a single time
	// rather than racing across service goroutines.
	if err := ensureAppleContainerSystemForBuilder(ctx, opts.builder, opts.yes); err != nil {
		return err
	}

	// Decide which services can skip build+push entirely: those whose build
	// inputs are unchanged since the last successful push to this device, whose
	// container is still present, AND whose image content the device is confirmed
	// to still hold. This is the WDY-1692 optimization (avoid re-pushing unchanged
	// images — notably the multi-GB GPU base — and the HEAD-check storm re-push
	// triggers), tightened for WDY-1824 so it never skips onto stale/partial
	// content. NOTE: the registry-push builder path can't yet prove content
	// presence, so this currently skips nothing for multi-service; see
	// planServicePushSkips / contentPresentForService.
	deviceKey := deviceFingerprintKey(versionResp)
	skip, hashes := planServicePushSkips(ctx, conn, cwd, appCfg.AppID, deviceKey, platform, services, buildArgs)
	if n := len(skip); n > 0 {
		cliLogln("%d of %d services unchanged and already on device; skipping their build/push.", n, len(services))
	}

	// Build all service images in parallel, then create and start containers.
	failed, buildErr := buildServicesParallel(ctx, conn, regPort, cwd, appCfg.AppID, services, platform, buildArgs, opts.builder, skip, opts.maxConcurrency)
	if buildErr != nil {
		return buildErr
	}

	// Record fingerprints for the services we actually built+pushed, so the next
	// run can skip them. Skipped services already have a matching fingerprint;
	// failed services must not be recorded (their image isn't on the device).
	for name := range services {
		if skip[name] || failed[name] != nil {
			continue
		}
		if h, ok := hashes[name]; ok {
			saveDeployFingerprint(serviceFingerprintKey(appCfg.AppID, name), deviceKey, deployFingerprint{InputHash: h, AppVersion: appCfg.Version})
		}
	}

	// Default (all-or-nothing): any build/push failure aborts the whole group so
	// no half-deployed group is left behind. --keep-going deploys what built and
	// reports the rest (WDY-1691).
	if len(failed) > 0 && !opts.keepGoing {
		return joinServiceErrors(failed)
	}

	// Determine which services can actually be deployed: those that built and
	// whose dependencies all built too. partialErr is surfaced at the end so the
	// command still exits non-zero after deploying the healthy subset.
	deployServices := services
	var partialErr error
	if len(failed) > 0 {
		deployable, dropped := resolveDeployableServices(services, failed)
		deployServices = deployable
		cliNotice("Partial deploy: %d deploying, %d failed (%s)%s.",
			len(deployable), len(failed), strings.Join(sortedServiceErrorKeys(failed), ", "),
			droppedSummary(dropped))
		partialErr = joinServiceErrors(failed)
		if len(deployable) == 0 {
			cliNotice("No services left to deploy.")
			return partialErr
		}
	}

	// Create (and start) containers in dependency order.
	ordered, err := serviceTopoOrder(deployServices)
	if err != nil {
		return err
	}
	createService := func(name string) error {
		svc := services[name]
		deviceImage := fmt.Sprintf("localhost:%d/%s-%s:latest", regPort,
			strings.ToLower(appCfg.AppID), strings.ToLower(name))

		serviceCfg := multiServiceCreateConfig(appCfg, name, svc)
		appConfigData, err := json.Marshal(serviceCfg)
		if err != nil {
			return fmt.Errorf("marshaling config for service %s: %w", name, err)
		}

		restartPolicy := resolveRestartPolicy(opts)
		createReq := &agentpb.CreateContainerRequest{
			ImageName:     deviceImage,
			AppName:       serviceCfg.ContainerName(),
			AppConfig:     appConfigData,
			RestartPolicy: restartPolicy,
		}

		cliLogln("Creating container for service %s...", name)
		if err := createContainerWithProgress(ctx, conn.ContainerService, createReq); err != nil {
			return fmt.Errorf("creating container for service %s: %w", name, err)
		}
		cliLogln("Service %s container created.", name)
		return nil
	}

	if opts.deploy {
		// Create-only: no service ever starts, so shared-namespace groups
		// cannot join here — the join happens at create time against the
		// primary's running task. Such groups should be deployed without
		// --deploy (or started service-by-service in dependency order).
		for _, name := range ordered {
			if err := createService(name); err != nil {
				return err
			}
		}
		cliLogln("App group %s created (not started, --deploy).", appCfg.AppID)
		return partialErr
	}

	// Create and start each service in dependency order, multiplexing log
	// output with per-service prefixes. Interleaving create and start is
	// load-bearing for shared-ipc/shared-network groups: a secondary's
	// namespace join is resolved at container create time against the
	// primary's running task, so the primary must be started before the
	// next service is created.
	if err := startAndStreamServices(ctx, conn, appCfg.AppID, ordered, opts, createService); err != nil {
		return err
	}
	// In --keep-going mode, exit non-zero after deploying the healthy subset so
	// callers/CI still see that some services failed.
	return partialErr
}

// droppedSummary formats the services skipped because a dependency failed, for
// the partial-deploy notice. Returns "" when nothing was dropped.
func droppedSummary(dropped map[string]string) string {
	if len(dropped) == 0 {
		return ""
	}
	names := make([]string, 0, len(dropped))
	for n := range dropped {
		names = append(names, n)
	}
	sort.Strings(names)
	return fmt.Sprintf(", %d skipped (failed dependency: %s)", len(names), strings.Join(names, ", "))
}

// buildServicesParallel builds all service images concurrently (up to
// maxConcurrentBuilds at a time). Services in skip are already on the device with
// unchanged inputs, so their build+push is skipped (WDY-1692). Progress is shown
// via a Bubbletea multi-spinner in interactive terminals and via plain log lines
// otherwise.
func buildServicesParallel(
	ctx context.Context,
	conn *grpcclient.AgentConnection,
	regPort int,
	cwd, appID string,
	services map[string]*appconfig.ServiceConfig,
	platform string,
	buildArgs map[string]string,
	builder string,
	skip map[string]bool,
	maxConcurrency int,
) (map[string]error, error) {
	names := make([]string, 0, len(services))
	for n := range services {
		names = append(names, n)
	}
	sort.Strings(names)

	type result struct {
		name string
		err  error
		dur  time.Duration
		log  string
	}

	results := make(chan result, len(names))

	// Concurrency (and the tunnel pressure it controls) is driven by the services
	// actually built — skipped ones push nothing.
	buildCount := 0
	for _, n := range names {
		if !skip[n] {
			buildCount++
		}
	}
	concurrency := resolveBuildConcurrency(buildCount, maxConcurrency)
	switch {
	case maxConcurrency > 0 && concurrency < buildCount:
		cliLogln("Building up to %d service(s) at a time (--max-concurrency).", concurrency)
	case maxConcurrency <= 0 && concurrency < maxConcurrentBuilds && concurrency < buildCount:
		cliLogln("Throttling to %d concurrent builds for %d services to protect the device registry tunnel (WDY-1690); override with --max-concurrency.", concurrency, buildCount)
	}
	sem := make(chan struct{}, concurrency)

	var prog *tea.Program
	if isInteractiveTerminal() {
		title := fmt.Sprintf("Building %d service(s)...", len(names))
		m := tui.NewMultiSpinner(title, names)
		prog = tui.NewProgressProgram(m)
	}

	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func(name string, svc *appconfig.ServiceConfig) {
			defer wg.Done()

			// Unchanged service already on the device: skip build+push entirely.
			if skip[name] {
				if prog != nil {
					prog.Send(tui.MultiSpinnerStartMsg{Name: name})
					prog.Send(tui.MultiSpinnerDoneMsg{Name: name, Err: nil, Dur: 0})
				} else {
					cliLogln("Service %s unchanged; skipping build/push (already on device).", name)
				}
				results <- result{name: name}
				return
			}

			sem <- struct{}{}
			defer func() { <-sem }()

			if prog != nil {
				prog.Send(tui.MultiSpinnerStartMsg{Name: name})
			} else {
				cliLogln("Building service %s...", name)
			}

			start := time.Now()
			contextDir := filepath.Join(cwd, svc.Context)
			repo := fmt.Sprintf("%s-%s", strings.ToLower(appID), strings.ToLower(name))
			dockerfile, dockerfileErr := resolveDockerfile(contextDir, "", false)

			var buildOut io.Writer
			var logBuf bytes.Buffer
			var tally func() tui.BuildTally = func() tui.BuildTally { return tui.BuildTally{} }
			if prog != nil {
				// Parse this service's stream into per-row detail updates and
				// cache/rebuild tallies. Raw output is still buffered for the
				// on-failure dump.
				emit, getTally := newServiceProgressEmitter(prog, name)
				tally = getTally
				parser := tui.NewBuildParser(emit)
				buildOut = io.MultiWriter(parser, &logBuf)
			} else {
				buildOut = os.Stdout
			}
			var logOutW io.Writer = &logBuf
			if prog == nil {
				logOutW = os.Stderr
			}
			err := dockerfileErr
			if err == nil {
				// Pass the per-service repo as the build's cache key so each concurrent
				// build gets its own isolated local buildx cache dir (WDY-1689); sharing
				// one dir corrupts BuildKit's cache-export ingest store under concurrency.
				err = buildServiceImage(ctx, conn, regPort, builder, contextDir, repo, platform, dockerfile, buildArgs, repo, buildOut, logOutW)
			}
			dur := time.Since(start)

			if prog != nil {
				t := tally()
				prog.Send(tui.MultiSpinnerDoneMsg{Name: name, Err: err, Dur: dur, Cached: t.Cached, Rebuilt: t.Rebuilt})
			} else if err != nil {
				cliLogln("Service %s build failed: %v", name, err)
			} else {
				cliLogln("Service %s built (%s).", name, dur.Round(time.Millisecond))
			}

			results <- result{name: name, err: err, dur: dur, log: logBuf.String()}
		}(name, services[name])
	}

	// Wait for all goroutines, close the results channel, then signal TUI done.
	go func() {
		wg.Wait()
		close(results)
		if prog != nil {
			prog.Send(tui.MultiSpinnerAllDoneMsg{})
		}
	}()

	if prog != nil {
		if _, runErr := prog.Run(); runErr != nil {
			return nil, fmt.Errorf("build progress TUI: %w", runErr)
		}
	}

	// Collect per-service failures. For failed services, print their buffered
	// output now that the spinner has exited and the terminal is clean. The caller
	// decides whether any failure aborts the group (default) or only its own
	// service is dropped (--keep-going, WDY-1691).
	failed := map[string]error{}
	for r := range results {
		if r.err != nil {
			failed[r.name] = r.err
			if r.log != "" {
				fmt.Fprintf(os.Stderr, "\n[%s] build log:\n%s", r.name, r.log)
			}
		}
	}
	return failed, nil
}

// sortedServiceErrorKeys returns the service names in failed, sorted, for stable
// error/report output.
func sortedServiceErrorKeys(failed map[string]error) []string {
	names := make([]string, 0, len(failed))
	for n := range failed {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// joinServiceErrors builds a single error from a per-service failure map, in
// stable order.
func joinServiceErrors(failed map[string]error) error {
	var errs []error
	for _, n := range sortedServiceErrorKeys(failed) {
		errs = append(errs, fmt.Errorf("service %s: %w", n, failed[n]))
	}
	return errors.Join(errs...)
}

// resolveDeployableServices returns the subset of services that can be deployed:
// those that built successfully and whose (transitive) dependencies all built
// successfully too. dropped maps each service that is skipped *because of a failed
// dependency* (not its own failure) to a human-readable reason. Failed services
// are reported separately via the failed map. A dependency cycle is treated as
// not deployable (serviceTopoOrder reports the cycle itself).
func resolveDeployableServices(services map[string]*appconfig.ServiceConfig, failed map[string]error) (deployable map[string]*appconfig.ServiceConfig, dropped map[string]string) {
	deployable = map[string]*appconfig.ServiceConfig{}
	dropped = map[string]string{}

	const (
		unknown  = 0
		yes      = 1
		no       = 2
		visiting = 3
	)
	state := map[string]int{}

	var canDeploy func(name string) bool
	canDeploy = func(name string) bool {
		switch state[name] {
		case yes:
			return true
		case no, visiting:
			return false
		}
		state[name] = visiting
		svc, ok := services[name]
		if !ok || svc == nil || failed[name] != nil {
			state[name] = no
			return false
		}
		for _, dep := range svc.DependsOn {
			if !canDeploy(dep) {
				state[name] = no
				dropped[name] = fmt.Sprintf("dependency %q was not deployed", dep)
				return false
			}
		}
		state[name] = yes
		return true
	}

	for name := range services {
		if canDeploy(name) {
			deployable[name] = services[name]
		}
	}
	return deployable, dropped
}

var serviceLogStyle = lipgloss.NewStyle().Foreground(tui.ColorInfo)

// newServiceProgressEmitter returns an emit callback for tui.NewBuildParser that
// forwards the active step as a MultiSpinner detail line and accumulates the
// cached/rebuilt tally for the service's done row.
func newServiceProgressEmitter(prog *tea.Program, name string) (func(tui.BuildStepEvent), func() tui.BuildTally) {
	var t tui.BuildTally
	emit := func(e tui.BuildStepEvent) {
		switch e.Status {
		case tui.BuildStepRunning:
			prog.Send(tui.MultiSpinnerDetailMsg{Name: name, Detail: e.Display})
		case tui.BuildStepCached:
			if e.Kind == tui.BuildVertexStep {
				t.Cached++
			}
		case tui.BuildStepDone:
			if e.Kind == tui.BuildVertexStep {
				t.Rebuilt++
			}
		}
	}
	return emit, func() tui.BuildTally { return t }
}

// multiServiceCreateConfig builds the per-service AppConfig transmitted to
// the agent for a standalone multi-service app. The group identity and
// runtime context (isolation, frameworks, shared entitlements) must travel
// with every service: the agent keys namespace sharing, ROS 2 env injection,
// and container naming on these fields (WDY-878, WDY-884).
func multiServiceCreateConfig(appCfg *appconfig.AppConfig, name string, svc *appconfig.ServiceConfig) *appconfig.AppConfig {
	cfg := &appconfig.AppConfig{
		AppID:       appCfg.AppID,
		ServiceName: name,
		Version:     appCfg.Version,
		Platform:    appCfg.Platform,
		Isolation:   appCfg.Isolation,
		Frameworks:  appCfg.Frameworks,
	}
	cfg.Entitlements = append(append([]appconfig.Entitlement{}, appCfg.Entitlements...), svc.Entitlements...)
	cfg.Entitlements = deduplicateEntitlements(cfg.Entitlements)
	if svc.Frameworks != nil {
		cfg.Frameworks = svc.Frameworks
	}
	return cfg
}

// multiServiceContainerName returns the container name the agent derives for
// a service: "{appId}_{serviceName}" (WDY-878). Start/stop calls must address
// the same name the create path produced.
func multiServiceContainerName(appID, serviceName string) string {
	return appID + "_" + serviceName
}

// startAndStreamServices starts all service containers and streams their
// combined output to stdout/stderr with a "[serviceName] " prefix per line.
// This is a best-effort multiplexer; proper per-service log routing is handled
// by WDY-893 (multiplexed AttachContainer).
// createService is invoked for each service, in dependency order, immediately
// before that service is started — after every earlier service is already
// running. This ordering is required for shared-ipc/shared-network groups:
// the agent resolves a secondary's namespace join at container create time
// against the primary's running task.
func startAndStreamServices(ctx context.Context, conn *grpcclient.AgentConnection, appID string, ordered []string, opts runOptions, createService func(name string) error) error {
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	// Ctrl+C stops all services.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		cliLogln("\nStopping services...")
		runCancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		var stopWg sync.WaitGroup
		for _, name := range ordered {
			stopWg.Add(1)
			go func(name string) {
				defer stopWg.Done()
				_, _ = conn.ContainerService.StopContainer(stopCtx, &agentpb.StopContainerRequest{
					AppName: multiServiceContainerName(appID, name),
				})
			}(name)
		}
		stopWg.Wait()
	}()

	if opts.detach {
		for _, name := range ordered {
			if err := createService(name); err != nil {
				return err
			}
			containerName := multiServiceContainerName(appID, name)
			stream, err := conn.ContainerService.StartContainer(runCtx, &agentpb.StartContainerRequest{
				AppName: containerName,
			})
			if err != nil {
				return fmt.Errorf("starting service %s: %w", name, err)
			}
			if _, err := stream.Recv(); err != nil && err != io.EOF {
				return fmt.Errorf("waiting for service %s to start: %w", name, err)
			}
		}
		cliLogln("App group %s running in detached mode.", appID)
		return nil
	}

	type logLine struct {
		service string
		stdout  bool
		data    []byte
	}
	lines := make(chan logLine, 256)

	// Create and start sequentially in dependency order; the first Recv
	// blocks until the agent's Started ack, guaranteeing each service's task
	// is running before the next service's container is created.
	var wg sync.WaitGroup
	for _, name := range ordered {
		if err := createService(name); err != nil {
			runCancel()
			wg.Wait()
			return err
		}
		containerName := multiServiceContainerName(appID, name)
		stream, err := conn.ContainerService.StartContainer(runCtx, &agentpb.StartContainerRequest{
			AppName: containerName,
		})
		if err != nil {
			runCancel()
			wg.Wait()
			return fmt.Errorf("starting service %s: %w", name, err)
		}
		if _, err := stream.Recv(); err != nil && err != io.EOF {
			runCancel()
			wg.Wait()
			return fmt.Errorf("waiting for service %s to start: %w", name, err)
		}
		wg.Add(1)
		go func(name string, stream agentpb.WendyContainerService_StartContainerClient) {
			defer wg.Done()
			for {
				resp, recvErr := stream.Recv()
				if recvErr == io.EOF {
					return
				}
				if recvErr != nil {
					if runCtx.Err() == nil {
						cliLogln("Warning: service %s stream: %v", name, recvErr)
					}
					return
				}
				if out := resp.GetStdoutOutput(); out != nil {
					select {
					case lines <- logLine{service: name, stdout: true, data: out.GetData()}:
					case <-runCtx.Done():
						return
					}
				}
				if out := resp.GetStderrOutput(); out != nil {
					select {
					case lines <- logLine{service: name, stdout: false, data: out.GetData()}:
					case <-runCtx.Done():
						return
					}
				}
			}
		}(name, stream)
	}

	go func() {
		wg.Wait()
		close(lines)
	}()

	cliLogln("App group %s started (%d services).", appID, len(ordered))

	for line := range lines {
		prefix := serviceLogStyle.Render(fmt.Sprintf("[%s] ", line.service))
		if line.stdout {
			fmt.Fprintf(os.Stdout, "%s%s", prefix, line.data)
		} else {
			fmt.Fprintf(os.Stderr, "%s%s", prefix, line.data)
		}
	}

	if runCtx.Err() != nil {
		cliLogln("\nApp group %s stopped.", appID)
		return nil
	}
	cliLogln("\nApp group %s stopped.", appID)
	return nil
}
