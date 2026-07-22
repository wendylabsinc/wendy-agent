package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
)

// foxgloveBridgePort is the port foxglove_bridge listens on inside the app.
const foxgloveBridgePort = 8765

// foxgloveAppID is the appId of the generated foxglove_bridge app; used both in
// the generated wendy.json and to remove a prior instance before redeploy.
const foxgloveAppID = "sh.wendy.foxglovebridge"

// newFoxgloveCmd builds the `wendy device foxglove` command group.
func newFoxgloveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "foxglove",
		Short: "Bridge the device's ROS 2 graph to Foxglove Studio",
	}
	cmd.AddCommand(newFoxgloveServeCmd())
	return cmd
}

func newFoxgloveServeCmd() *cobra.Command {
	var (
		port   int
		domain int
		rmw    string
		distro string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Deploy foxglove_bridge to the device and open a tunnel for Foxglove Studio",
		Long: `Generates a foxglove_bridge app, deploys it to the target device with
'wendy run' (host networking, so it joins the device's ROS 2 graph — including a
robot's native host ROS 2), then forwards its WebSocket port to your machine.

Connect Foxglove Studio to the printed ws:// URL. For a robot whose ROS 2 uses a
non-default domain or RMW (e.g. a Unitree Go2 on CycloneDDS), pass --domain and
--rmw so the bridge matches it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return foxgloveServe(ctx, foxgloveServeOpts{
				localPort: port,
				domain:    domain,
				rmw:       rmw,
				distro:    distro,
				device:    deviceFlag,
			})
		},
	}

	cmd.Flags().IntVar(&port, "port", foxgloveBridgePort, "Local port to forward foxglove_bridge to")
	cmd.Flags().IntVar(&domain, "domain", 0, "ROS_DOMAIN_ID the device's ROS 2 uses")
	cmd.Flags().StringVar(&rmw, "rmw", "rmw_cyclonedds_cpp", "RMW implementation the device's ROS 2 uses")
	cmd.Flags().StringVar(&distro, "distro", "humble", "ROS 2 distro to build foxglove_bridge from")

	return cmd
}

type foxgloveServeOpts struct {
	localPort int
	domain    int
	rmw       string
	distro    string
	device    string // global --device; "" = default device
}

// foxgloveServe generates a foxglove_bridge app in a temp dir, deploys it to the
// device via `wendy run --detach`, then forwards the bridge's WebSocket port to
// localhost via `wendy cloud tunnel`. The tunnel runs until ctx is cancelled.
func foxgloveServe(ctx context.Context, opts foxgloveServeOpts) error {
	dir, err := os.MkdirTemp("", "wendy-foxglove-*")
	if err != nil {
		return fmt.Errorf("creating temp app dir: %w", err)
	}
	defer os.RemoveAll(dir)

	if err := writeFoxgloveApp(dir, opts); err != nil {
		return err
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating wendy binary: %w", err)
	}

	// Deploy the app to the device (build locally, push to the device, start
	// detached). Reuses the full `wendy run` pipeline.
	//
	// First best-effort remove any prior foxglove_bridge instance so re-running
	// is idempotent — a previously-deployed app (it stays running detached after
	// the tunnel is Ctrl-C'd) otherwise collides on redeploy ("snapshot already
	// exists"). Errors (e.g. nothing deployed yet) are ignored.
	rmArgs := []string{"device", "apps", "remove", foxgloveAppID, "--force", "--cleanup"}
	if opts.device != "" {
		rmArgs = append(rmArgs, "--device", opts.device)
	}
	rm := exec.CommandContext(ctx, self, rmArgs...)
	_ = rm.Run() // best-effort; ignore "not found" etc.

	cliLogln("Deploying foxglove_bridge to the device...")
	runArgs := []string{"run", "--detach"}
	if opts.device != "" {
		runArgs = append(runArgs, "--device", opts.device)
	}
	run := exec.CommandContext(ctx, self, runArgs...)
	run.Dir = dir
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	run.Stdin = os.Stdin
	if err := run.Run(); err != nil {
		return fmt.Errorf("deploying foxglove_bridge (wendy run): %w", err)
	}

	// Forward the bridge's WebSocket port. `cloud tunnel` listens on the local
	// port and blocks until ctx is cancelled (Ctrl-C).
	cliSuccess("foxglove_bridge deployed. Connect Foxglove Studio to ws://localhost:%d", opts.localPort)
	tunArgs := []string{"cloud", "tunnel", fmt.Sprintf("%d:%d", opts.localPort, foxgloveBridgePort)}
	if opts.device != "" {
		tunArgs = append(tunArgs, "--device", opts.device)
	}
	tun := exec.CommandContext(ctx, self, tunArgs...)
	tun.Stdout = os.Stdout
	tun.Stderr = os.Stderr
	if err := tun.Run(); err != nil {
		if ctx.Err() != nil {
			return nil // clean Ctrl-C
		}
		return fmt.Errorf("forwarding foxglove_bridge port (wendy cloud tunnel): %w", err)
	}
	return nil
}

// writeFoxgloveApp writes the Dockerfile + wendy.json for a foxglove_bridge app
// into dir, templated for the requested distro/domain/rmw.
func writeFoxgloveApp(dir string, opts foxgloveServeOpts) error {
	dockerfile := fmt.Sprintf(`# Auto-generated by 'wendy device foxglove serve'.
FROM ros:%[1]s
RUN apt-get update && apt-get install -y --no-install-recommends \
      ros-%[1]s-foxglove-bridge \
      ros-%[1]s-rmw-cyclonedds-cpp \
    && rm -rf /var/lib/apt/lists/*
CMD ["bash","-lc","source /opt/ros/%[1]s/setup.bash && exec ros2 launch foxglove_bridge foxglove_bridge_launch.xml port:=%[2]d address:=0.0.0.0"]
`, opts.distro, foxgloveBridgePort)

	wendyJSON := fmt.Sprintf(`{
  "appId": %[4]q,
  "platform": "linux",
  "version": "1.0.0",
  "frameworks": {
    "ros2": { "domainId": %[1]d, "rmw": %[2]q, "distro": %[3]q }
  },
  "entitlements": [
    { "type": "network", "mode": "host" }
  ],
  "services": {
    "foxglove": { "context": "." }
  }
}
`, opts.domain, opts.rmw, opts.distro, foxgloveAppID)

	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		return fmt.Errorf("writing Dockerfile: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wendy.json"), []byte(wendyJSON), 0o644); err != nil {
		return fmt.Errorf("writing wendy.json: %w", err)
	}
	return nil
}
