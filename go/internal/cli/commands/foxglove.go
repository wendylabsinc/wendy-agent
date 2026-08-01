package commands

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"
)

// foxgloveBridgePort is the port foxglove_bridge listens on inside the app.
const foxgloveBridgePort = 8765

// foxgloveAppID is the appId of the generated foxglove_bridge app; used both in
// the generated wendy.json and to remove a prior instance before redeploy.
const foxgloveAppID = "sh.wendy.foxglovebridge"

// Linux interface names are limited to IFNAMSIZ-1 (15) bytes. Keeping the
// accepted character set narrow also makes it safe to embed the name in the
// generated CycloneDDS XML and shell command.
var foxgloveInterfacePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`)

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
		iface  string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Deploy foxglove_bridge to the device and open a tunnel for Foxglove Studio",
		Long: `Generates a foxglove_bridge app, deploys it to the target device with
'wendy run' (host networking, so it joins the device's ROS 2 graph — including a
robot's native host ROS 2), then forwards its WebSocket port to your machine.

Connect Foxglove Studio to the printed ws:// URL. For a robot whose ROS 2 uses a
non-default domain or RMW (e.g. a Unitree Go2 on CycloneDDS), pass --domain and
--rmw so the bridge matches it. The bridge exposes every topic, including hidden
ROS topics, in the selected domain.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			cloudCfg, cloud := cloudDeviceConfigFromContext(ctx)
			return foxgloveServe(ctx, foxgloveServeOpts{
				localPort: port,
				domain:    domain,
				rmw:       rmw,
				distro:    distro,
				device:    deviceFlag,
				iface:     iface,
				cloud:     cloud,
				cloudCfg:  cloudCfg,
			})
		},
	}

	cmd.Flags().IntVar(&port, "port", foxgloveBridgePort, "Local port to forward foxglove_bridge to")
	cmd.Flags().IntVar(&domain, "domain", 0, "ROS_DOMAIN_ID the device's ROS 2 uses")
	cmd.Flags().StringVar(&rmw, "rmw", "rmw_cyclonedds_cpp", "RMW implementation the device's ROS 2 uses")
	cmd.Flags().StringVar(&distro, "distro", "humble", "ROS 2 distro to build foxglove_bridge from")
	cmd.Flags().StringVar(&iface, "interface", "", "CycloneDDS network interface to use (for multi-interface devices)")

	return cmd
}

type foxgloveServeOpts struct {
	localPort int
	domain    int
	rmw       string
	distro    string
	device    string // global --device; "" = default device
	iface     string // optional CycloneDDS network interface
	cloud     bool
	cloudCfg  cloudDeviceConfig
}

// foxgloveServe generates a foxglove_bridge app in a temp dir, deploys it to the
// device via `wendy run --detach`, then forwards the bridge's WebSocket port.
// Direct device commands forward over the selected LAN connection; cloud device
// commands deploy and forward through the same cloud asset.
func foxgloveServe(ctx context.Context, opts foxgloveServeOpts) error {
	target, err := resolveFoxgloveTarget(ctx, opts)
	if err != nil {
		return err
	}
	opts.device = target

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
	rmArgs := foxgloveRemoveArgs(opts)
	rm := exec.CommandContext(ctx, self, rmArgs...)
	_ = rm.Run() // best-effort; ignore "not found" etc.

	cliLogln("Deploying foxglove_bridge to the device...")
	runArgs := foxgloveRunArgs(opts)
	run := exec.CommandContext(ctx, self, runArgs...)
	run.Dir = dir
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	run.Stdin = os.Stdin
	if err := run.Run(); err != nil {
		return fmt.Errorf("deploying foxglove_bridge (wendy run): %w", err)
	}

	cliSuccess("foxglove_bridge deployed. Connect Foxglove Studio to ws://localhost:%d", opts.localPort)
	if !opts.cloud {
		return forwardFoxgloveLAN(ctx, opts.device, opts.localPort)
	}

	// The cloud asset was resolved before deployment and is passed by numeric ID
	// here, so the tunnel cannot drift to another online device.
	tunArgs := foxgloveTunnelArgs(opts)
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

// resolveFoxgloveTarget selects the device once and returns an address that can
// safely pin all subsequent operations. Cloud devices use their stable numeric
// asset ID; LAN devices use the host from the established agent connection.
func resolveFoxgloveTarget(ctx context.Context, opts foxgloveServeOpts) (string, error) {
	if opts.cloud {
		auth, err := pickAuthEntry(opts.cloudCfg.CloudGRPC)
		if err != nil {
			return "", err
		}
		asset, err := pickCloudDevice(ctx, auth, opts.cloudCfg.DeviceName, opts.cloudCfg.BrokerURL)
		if err != nil {
			return "", err
		}
		return strconv.FormatInt(int64(asset.GetId()), 10), nil
	}

	target, err := resolveTarget(ctx, ExcludeBluetooth())
	if err != nil {
		return "", err
	}
	defer target.Close()
	if target.Agent == nil || target.Agent.Host == "" {
		return "", fmt.Errorf("foxglove serve requires a WendyOS device reachable on the LAN")
	}
	return target.Agent.Host, nil
}

func foxgloveRemoveArgs(opts foxgloveServeOpts) []string {
	args := []string{"device", "apps", "remove", foxgloveAppID, "--force", "--cleanup", "--device", opts.device}
	if opts.cloud {
		args = append([]string{"cloud"}, args...)
		args = appendCloudFoxgloveFlags(args, opts.cloudCfg)
	}
	return args
}

func foxgloveRunArgs(opts foxgloveServeOpts) []string {
	args := []string{"run", "--detach", "--device", opts.device}
	if opts.cloud {
		args = append([]string{"cloud"}, args...)
		args = appendCloudFoxgloveFlags(args, opts.cloudCfg)
	} else {
		args = append(args, "--lan")
	}
	return args
}

func foxgloveTunnelArgs(opts foxgloveServeOpts) []string {
	args := []string{"cloud", "tunnel", fmt.Sprintf("%d:%d", opts.localPort, foxgloveBridgePort), "--device", opts.device}
	return appendCloudFoxgloveFlags(args, opts.cloudCfg)
}

func appendCloudFoxgloveFlags(args []string, cfg cloudDeviceConfig) []string {
	if cfg.CloudGRPC != "" {
		args = append(args, "--cloud-grpc", cfg.CloudGRPC)
	}
	if cfg.BrokerURL != "" {
		args = append(args, "--broker-url", cfg.BrokerURL)
	}
	return args
}

func forwardFoxgloveLAN(ctx context.Context, host string, localPort int) error {
	target := net.JoinHostPort(host, strconv.Itoa(foxgloveBridgePort))
	proxy, err := startRegistryProxy(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort)), target)
	if err != nil {
		return fmt.Errorf("forwarding foxglove_bridge port over LAN: %w", err)
	}
	defer proxy.Close()

	cliSuccess("Forwarding 127.0.0.1:%d → %s (via LAN)", localPort, target)
	cliLogln("Press Ctrl+C to stop.")
	<-ctx.Done()
	return nil
}

// writeFoxgloveApp writes the Dockerfile + wendy.json for a foxglove_bridge app
// into dir, templated for the requested distro/domain/rmw.
func writeFoxgloveApp(dir string, opts foxgloveServeOpts) error {
	if opts.iface != "" && !foxgloveInterfacePattern.MatchString(opts.iface) {
		return fmt.Errorf("invalid network interface %q: use 1-15 letters, digits, '.', '_', ':', or '-'", opts.iface)
	}

	// Keep the explicit ROS_LOCALHOST_ONLY override in the launch command as a
	// compatibility path for devices whose installed agent predates
	// frameworks.ros2.discoveryScope. New agents also honor the manifest field.
	launchCommand := fmt.Sprintf("source /opt/ros/%s/setup.bash && export ROS_LOCALHOST_ONLY=0", opts.distro)
	if opts.iface != "" {
		cycloneURI := fmt.Sprintf(`<CycloneDDS><Domain><General><Interfaces><NetworkInterface name="%s"/></Interfaces></General><SharedMemory><Enable>false</Enable></SharedMemory></Domain></CycloneDDS>`, opts.iface)
		launchCommand += " && export CYCLONEDDS_URI='" + cycloneURI + "'"
	}
	launchCommand += fmt.Sprintf(" && exec ros2 launch foxglove_bridge foxglove_bridge_launch.xml port:=%d address:=0.0.0.0 include_hidden:=true", foxgloveBridgePort)

	dockerfile := fmt.Sprintf(`# Auto-generated by 'wendy device foxglove serve'.
FROM ros:%s
RUN apt-get update && apt-get install -y --no-install-recommends \
      ros-%s-foxglove-bridge \
      ros-%s-rmw-cyclonedds-cpp \
    && rm -rf /var/lib/apt/lists/*
CMD ["bash","-lc",%q]
`, opts.distro, opts.distro, opts.distro, launchCommand)

	wendyJSON := fmt.Sprintf(`{
  "appId": %[4]q,
  "platform": "linux",
  "version": "1.0.0",
  "frameworks": {
    "ros2": { "domainId": %[1]d, "rmw": %[2]q, "distro": %[3]q, "discoveryScope": "host" }
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
