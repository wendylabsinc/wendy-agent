package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// newWatchCmd builds the `wendy watch` command: it watches the project
// directory and re-runs the normal deploy pipeline (build + chunk-diff
// redeploy, detached) on every saved change, so editing a file redeploys the
// app automatically.
func newWatchCmd() *cobra.Command {
	var opts runOptions
	var debounceMS int
	var verbose bool

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Watch the project directory and redeploy on every change",
		Long: "Watches the current directory (or --prefix) and re-runs the build + " +
			"deploy pipeline whenever a file changes, replacing the running container. " +
			"Runs detached (does not stream logs); use 'wendy device logs' to tail output. " +
			"Build output is hidden unless a build fails; pass --verbose to always show it.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateEnvFlag(opts.env); err != nil {
				return err
			}
			// Keep the loop quiet: hide build output unless a build fails.
			// Detached + non-interactive are enforced by watchCommand itself.
			opts.quietBuild = !verbose
			return watchCommand(cmd.Context(), opts, time.Duration(debounceMS)*time.Millisecond)
		},
	}

	cmd.Flags().StringVar(&opts.buildType, "build-type", "", "Build type when a Dockerfile sits alongside Package.swift or Python markers: docker, swift, or python")
	cmd.Flags().StringVar(&opts.dockerfile, "dockerfile", "", "Build file to build from: a Dockerfile, Containerfile, or Stagefile (e.g. Dockerfile.prod, prod.stagefile.yaml)")
	cmd.Flags().StringVar(&opts.builder, "builder", "", "Image builder to force for Dockerfile/Containerfile builds: docker or apple-container")
	cmd.Flags().StringVar(&opts.stagefileBackend, "stagefile-backend", "", "Stagefile compiler backend: dockerfile (default) or llb")
	cmd.Flags().BoolVar(&opts.debug, "debug", false, "Enable debug logging")
	cmd.Flags().StringVar(&opts.prefix, "prefix", "", "Project directory to watch instead of the current working directory")
	cmd.Flags().StringVar(&opts.product, "product", "", "Swift Package Manager product to build and run")
	cmd.Flags().StringVar(&opts.service, "service", "", "Build and run only the named service and its dependencies")
	cmd.Flags().StringSliceVar(&opts.userArgs, "user-args", nil, "Extra arguments to pass to the container")
	cmd.Flags().StringArrayVar(&opts.env, "env", nil, "Set an environment variable in the container as KEY=VALUE; repeatable, and overrides wendy.json env of the same key")
	cmd.Flags().BoolVar(&opts.restartUnlessStopped, "restart-unless-stopped", false, "Restart the container unless manually stopped")
	cmd.Flags().BoolVar(&opts.restartOnFailure, "restart-on-failure", false, "Restart the container on failure")
	cmd.Flags().IntVar(&debounceMS, "debounce", 400, "Quiet period in milliseconds after the last change before redeploying")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "Always show build output (default: hidden unless the build fails)")

	return cmd
}

// withWatchInvariants returns opts with the settings the watch loop requires:
// it must run detached and never prompt, so a rapid series of saves can't block
// on log streaming or an interactive confirmation between redeploy cycles. Both
// entry points (`wendy watch` and `wendy run --watch`) route through
// watchCommand, so enforcing the invariants here keeps them from drifting.
func withWatchInvariants(opts runOptions) runOptions {
	opts.detach = true
	opts.yes = true
	if opts.watchState == nil {
		opts.watchState = &watchDeployState{hashes: map[string]string{}}
	}
	return opts
}

// watchDeployState is intentionally scoped to one watch process. The first
// deploy establishes a known-good baseline; later deploys may preserve a
// service only when its effective desired-state hash still matches and the
// device independently reports its container as running.
type watchDeployState struct {
	mu     sync.Mutex
	hashes map[string]string
}

func (s *watchDeployState) matches(key, hash string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hashes[key] == hash
}

func (s *watchDeployState) record(key, hash string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hashes[key] = hash
}

func watchServiceKey(deviceKey, appID, service string) string {
	return deviceKey + "/" + appID + "/" + service
}

// watchDesiredHash hashes the complete effective desired state of one service.
// Callers include both its image/build identity and its runtime configuration,
// so a watch cycle never preserves a container whose settings have changed.
func watchDesiredHash(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("wendy-watch-service-v1\x00"), b...))
	return hex.EncodeToString(sum[:]), nil
}

type watchServiceCandidate struct {
	appID         string
	containerName string
	desiredHash   string
}

// selectPreservedWatchServices returns services that are both unchanged since
// this watch session last deployed them and currently running on the device.
// Either condition failing makes the service part of the next repair/deploy.
func selectPreservedWatchServices(state *watchDeployState, deviceKey string, candidates map[string]watchServiceCandidate, deviceStates map[string]agentpb.AppRunningState) map[string]bool {
	preserve := map[string]bool{}
	for name, candidate := range candidates {
		key := watchServiceKey(deviceKey, candidate.appID, name)
		if state.matches(key, candidate.desiredHash) && deviceStates[strings.ToLower(candidate.containerName)] == agentpb.AppRunningState_RUNNING {
			preserve[name] = true
		}
	}
	return preserve
}

func filterPreservedServices(ordered []string, preserve map[string]bool) []string {
	changed := make([]string, 0, len(ordered))
	for _, name := range ordered {
		if !preserve[name] {
			changed = append(changed, name)
		}
	}
	return changed
}

// adjustSharedNamespacePreserve expands a primary-service redeploy to the
// whole shared-namespace group. Secondaries bake the primary PID's namespaces
// into their OCI specs, so they are genuinely affected when that primary is
// replaced even if their own desired-state hashes did not change.
func adjustSharedNamespacePreserve(ordered []string, preserve map[string]bool, isolation string) map[string]bool {
	if len(ordered) == 0 || !appconfig.IsSharedNamespaceIsolation(isolation) || preserve[ordered[0]] {
		return preserve
	}
	return map[string]bool{}
}

func watchCommand(ctx context.Context, opts runOptions, debounce time.Duration) error {
	if debounce <= 0 {
		debounce = 400 * time.Millisecond
	}
	opts = withWatchInvariants(opts)
	root, err := resolveRunWorkingDir(opts)
	if err != nil {
		return err
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	if err := addDirsRecursive(w, root); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stopOnce sync.Once
	stopWatch := func() {
		stopOnce.Do(func() {
			cliLogln("\nStopping watch.")
			cancel()
		})
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case <-sigCh:
			stopWatch()
		case <-ctx.Done():
		}
	}()

	deployer := &debouncedDeployer{opts: opts, stopWatch: stopWatch}

	cliLogln("Watching %s for changes (Ctrl-C to stop)...", root)
	deployer.trigger(ctx) // initial deploy

	timer := time.NewTimer(debounce)
	if !timer.Stop() {
		<-timer.C
	}
	pending := false

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if watchShouldIgnore(ev.Name, root) {
				continue
			}
			// Watch newly-created directories so files added under them are seen.
			if ev.Op&fsnotify.Create != 0 {
				if fi, statErr := os.Stat(ev.Name); statErr == nil && fi.IsDir() && !watchIsIgnoredDir(filepath.Base(ev.Name)) {
					_ = addDirsRecursive(w, ev.Name)
				}
			}
			pending = true
			timer.Reset(debounce)
		case watchErr, ok := <-w.Errors:
			if !ok {
				return nil
			}
			cliLogln("watch error: %v", watchErr)
		case <-timer.C:
			if pending {
				pending = false
				deployer.trigger(ctx)
			}
		}
	}
}

// debouncedDeployer runs the deploy pipeline for the latest change, cancelling
// any deploy still in flight so a rapid series of saves collapses to the most
// recent one.
type debouncedDeployer struct {
	opts      runOptions
	stopWatch context.CancelFunc
	mu        sync.Mutex
	cancelCur context.CancelFunc
}

var watchRunCommand = runCommand

func (d *debouncedDeployer) trigger(parent context.Context) {
	d.mu.Lock()
	if d.cancelCur != nil {
		d.cancelCur()
	}
	runCtx, cancel := context.WithCancel(parent)
	d.cancelCur = cancel
	d.mu.Unlock()

	go func() {
		start := time.Now()
		cliLogln("↻ change detected — redeploying...")
		if err := watchRunCommand(runCtx, d.opts); err != nil {
			if runCtx.Err() != nil {
				return // superseded by a newer change or shutting down
			}
			if errors.Is(err, ErrUserCancelled) {
				// Ctrl-C read by an interactive build UI arrives as a key event,
				// not an OS signal. Treat it exactly like the signal path instead
				// of reporting a failed deploy and continuing to watch.
				if d.stopWatch != nil {
					d.stopWatch()
				}
				return
			}
			cliLogln("✗ deploy failed: %v (still watching)", err)
			return
		}
		cliLogln("✓ redeployed in %s", time.Since(start).Round(time.Millisecond))
	}()
}

// addDirsRecursive adds root and all of its non-ignored subdirectories to the
// watcher. fsnotify watches a directory non-recursively, so every directory
// must be added explicitly.
func addDirsRecursive(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if !entry.IsDir() {
			return nil
		}
		if p != root && watchIsIgnoredDir(entry.Name()) {
			return filepath.SkipDir
		}
		return w.Add(p)
	})
}

// watchShouldIgnore reports whether a changed path should not trigger a rebuild
// (it lives under an ignored directory, or is an editor temp/swap file).
func watchShouldIgnore(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if watchIsIgnoredDir(part) {
			return true
		}
	}
	base := filepath.Base(path)
	// Build artifacts the deploy pipeline itself writes into the project dir
	// (prepareDockerBuildFile / the Stagefile compiler). Reacting to them
	// would make every watched deploy cancel and restart itself: the deploy
	// writes the file, the watcher sees the write, the debouncer kills the
	// in-flight deploy and triggers the next one, forever.
	// Covers every Stagefile variant's artifacts too: the generated namespace is
	// "Dockerfile.generated*" (which subsumes each one's paired .dockerignore),
	// and each variant maintains its own "<variant>.stagefile.lock.yaml".
	if isGeneratedBuildFileName(base) || isStagefileLockName(base) {
		return true
	}
	return strings.HasSuffix(base, "~") ||
		strings.HasSuffix(base, ".swp") ||
		strings.HasSuffix(base, ".swx") ||
		strings.HasSuffix(base, ".tmp")
}

// watchIsIgnoredDir reports whether a directory name should be excluded from
// watching: hidden directories and common dependency/build output directories.
func watchIsIgnoredDir(name string) bool {
	switch name {
	case "node_modules", ".build", ".swiftpm", "__pycache__", ".venv", "venv", "dist", "target", "build":
		return true
	}
	// Hidden directories (.git, .idea, .vscode, …).
	return len(name) > 1 && strings.HasPrefix(name, ".")
}
