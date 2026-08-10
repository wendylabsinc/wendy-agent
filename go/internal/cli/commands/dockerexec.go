package commands

import (
	"context"
	"os/exec"
	"time"
)

// A `docker buildx ...` invocation is not one process: the docker CLI execs the
// docker-buildx plugin as a child of its own. Killing the `docker` pid — which
// is all exec.CommandContext does by default — leaves that plugin running, and
// a stranded buildx plugin keeps the exclusive flock it holds on
// <docker-config>/buildx/.lock. Every later buildx command on the machine, in
// any wendy process, then blocks on that lock forever with no output: the
// symptom that reads as "two parallel builds deadlocked".
//
// groupCommandContext puts the invocation in its own process group and kills
// the whole group on cancellation, so the plugin is reaped along with the CLI.
func groupCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	// The killed group can still hold the write end of a pipe this process is
	// reading (buildkit's progress stream is inherited by the plugin), and Wait
	// blocks on those copies until they close. WaitDelay bounds that so a
	// cancelled build cannot wedge its caller.
	cmd.WaitDelay = dockerKillGrace
	return cmd
}

// dockerKillGrace is how long Wait may spend draining inherited pipes after the
// process group has been killed.
const dockerKillGrace = 5 * time.Second

// dockerControlTimeout bounds a docker/buildx *control-plane* call — inspect,
// create, rm, cp, restart, version. Against a healthy daemon these return in
// well under a second; against an unhealthy one they can block indefinitely,
// because neither the docker CLI nor buildx applies a timeout of its own. The
// case that motivated this: a builder whose stored endpoint names a docker
// context that no longer runs (a `desktop-linux` builder on a machine since
// moved to orbstack). `docker buildx rm` on it dials a dead daemon forever
// while holding the builder-store lock, taking every future build with it.
//
// Calls with a legitimately unbounded runtime — the build itself — use
// groupCommandContext directly and are bounded by the caller's own context.
const dockerControlTimeout = 90 * time.Second

// dockerBootstrapTimeout bounds `buildx inspect --bootstrap`, which is a
// control-plane call that legitimately takes minutes the first time: it starts
// the builder container, pulling the buildkit image if the machine has never
// had one. It is still bounded, because a bootstrap against a dead endpoint
// hangs exactly like an rm does.
const dockerBootstrapTimeout = 10 * time.Minute

// dockerCommand is a docker invocation that cannot outlive this process. Use it
// for long-running calls (the build); the caller's context governs the deadline.
func dockerCommand(ctx context.Context, args ...string) *exec.Cmd {
	return groupCommandContext(ctx, "docker", args...)
}

// dockerControlCommand is dockerCommand with dockerControlTimeout applied. The
// returned stop func releases the timer and must be called, conventionally via
// defer, exactly as with context.WithTimeout's cancel.
func dockerControlCommand(ctx context.Context, args ...string) (*exec.Cmd, func()) {
	cctx, cancel := context.WithTimeout(ctx, dockerControlTimeout)
	return dockerCommand(cctx, args...), cancel
}

// runDockerControl, outputDockerControl and combinedDockerControl are the
// deadline-and-cleanup-managed forms of the three ways this package runs a
// docker control call. Call sites use these rather than building a command by
// hand so no control call can be added without a deadline.
func runDockerControl(ctx context.Context, args ...string) error {
	cmd, stop := dockerControlCommand(ctx, args...)
	defer stop()
	return cmd.Run()
}

func outputDockerControl(ctx context.Context, args ...string) ([]byte, error) {
	cmd, stop := dockerControlCommand(ctx, args...)
	defer stop()
	return cmd.Output()
}

func combinedDockerControl(ctx context.Context, args ...string) ([]byte, error) {
	cmd, stop := dockerControlCommand(ctx, args...)
	defer stop()
	return cmd.CombinedOutput()
}

// combinedDockerBootstrap is combinedDockerControl on the bootstrap budget.
func combinedDockerBootstrap(ctx context.Context, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, dockerBootstrapTimeout)
	defer cancel()
	return dockerCommand(cctx, args...).CombinedOutput()
}
