package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

// foxgloveBridgePort is the port foxglove_bridge listens on inside the app.
const foxgloveBridgePort = 8765

// foxgloveBridgeAddress keeps the unauthenticated Foxglove WebSocket reachable
// only through the Wendy tunnel. ROS 2 discovery still uses the device's host
// network independently of this listener address.
const foxgloveBridgeAddress = "127.0.0.1"

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
		app    string
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
ROS topics, in the selected domain. Its device-side WebSocket listens only on
loopback and is accessed through the authenticated Wendy Cloud tunnel.

Domain selection matters: a robot's native ROS 2 is usually on domain 0, but a
Wendy-deployed ROS 2 app with no explicit "domainId" gets a stable domain derived
from its appId — essentially never 0. To bridge one of those, pass --app <appId>
and the matching domain is computed for you. With neither flag the bridge sits on
domain 0 and will show an empty graph if that is not where your nodes are.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			resolvedDomain, err := resolveFoxgloveDomain(domain, app, cmd.Flags().Changed("domain"))
			if err != nil {
				return err
			}
			return foxgloveServe(ctx, foxgloveServeOpts{
				localPort: port,
				domain:    resolvedDomain,
				rmw:       rmw,
				distro:    distro,
				device:    deviceFlag,
				iface:     iface,
			})
		},
	}

	cmd.Flags().IntVar(&port, "port", foxgloveBridgePort, "Local port to forward foxglove_bridge to")
	cmd.Flags().IntVar(&domain, "domain", 0, "ROS_DOMAIN_ID the device's ROS 2 uses")
	cmd.Flags().StringVar(&app, "app", "", "appId of a Wendy-deployed ROS 2 app to bridge; derives its ROS_DOMAIN_ID")
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
}

// resolveFoxgloveDomain decides which ROS_DOMAIN_ID the bridge should join.
//
// `--domain` defaulted to 0, which is right for a robot's native ROS 2 but wrong
// for anything Wendy deployed: an app without an explicit domainId gets
// ROS2AutoDomainID(appID), a stable hash that is essentially never 0. So the
// no-flag invocation bridged an empty graph with nothing explaining why. Passing
// `--app <appId>` now derives the same domain the agent injected — it is a pure
// function of the appId, so no device round-trip is needed — and the bare default
// says out loud what it is doing.
func resolveFoxgloveDomain(domain int, appID string, domainSet bool) (int, error) {
	switch {
	case domainSet && appID != "":
		derived := appconfig.ROS2AutoDomainID(appID)
		if derived != domain {
			return 0, fmt.Errorf("--domain %d conflicts with --app %q, whose ROS_DOMAIN_ID is %d; pass only one",
				domain, appID, derived)
		}
		return domain, nil
	case appID != "":
		derived := appconfig.ROS2AutoDomainID(appID)
		cliLogln("Using ROS_DOMAIN_ID=%d, derived from appId %q.", derived, appID)
		return derived, nil
	case domainSet:
		return domain, nil
	default:
		cliLogln("No --domain or --app given: using ROS_DOMAIN_ID=0 (a robot's native ROS 2). " +
			"Wendy-deployed ROS 2 apps get a domain derived from their appId, so if you " +
			"meant to bridge one of those, re-run with --app <appId>.")
		return 0, nil
	}
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
	if opts.iface != "" && !foxgloveInterfacePattern.MatchString(opts.iface) {
		return fmt.Errorf("invalid network interface %q: use 1-15 letters, digits, '.', '_', ':', or '-'", opts.iface)
	}
	// distro lands in three interpolated positions below: two `apt-get install
	// ros-<distro>-…` lines and the `source /opt/ros/<distro>/setup.bash` inside a
	// `bash -lc` CMD. %q protects the Dockerfile's JSON tokenisation but not the
	// shell semantics *inside* the string, so this is validated for the same reason
	// `iface` is — the comment above foxgloveInterfacePattern already states the
	// invariant, distro just wasn't held to it.
	if !appconfig.ROS2DistroPattern.MatchString(opts.distro) {
		return fmt.Errorf("invalid ROS 2 distro %q: use lowercase letters and digits, "+
			"starting with a letter (e.g. %q)", opts.distro, appconfig.ROS2DefaultDistro)
	}
	if opts.domain < appconfig.ROS2DomainIDMin || opts.domain > appconfig.ROS2DomainIDMax {
		return fmt.Errorf("invalid --domain %d: must be between %d and %d",
			opts.domain, appconfig.ROS2DomainIDMin, appconfig.ROS2DomainIDMax)
	}
	if opts.rmw != "" && !appconfig.IsValidRMWImplementation(opts.rmw) {
		return fmt.Errorf("invalid --rmw %q: expected one of rmw_cyclonedds_cpp, "+
			"rmw_fastrtps_cpp, rmw_connextdds, rmw_gurumdds_cpp", opts.rmw)
	}

	// Keep the explicit ROS_LOCALHOST_ONLY override in the launch command as a
	// compatibility path for devices whose installed agent predates
	// frameworks.ros2.discoveryScope. New agents also honor the manifest field.
	launchCommand := fmt.Sprintf("source /opt/ros/%s/setup.bash && export ROS_LOCALHOST_ONLY=0", opts.distro)
	if opts.iface != "" {
		cycloneURI := fmt.Sprintf(`<CycloneDDS><Domain><General><Interfaces><NetworkInterface name="%s"/></Interfaces></General><SharedMemory><Enable>false</Enable></SharedMemory></Domain></CycloneDDS>`, opts.iface)
		launchCommand += " && export CYCLONEDDS_URI='" + cycloneURI + "'"
	}
	launchCommand += fmt.Sprintf(" && exec ros2 launch foxglove_bridge foxglove_bridge_launch.xml port:=%d address:=%s include_hidden:=true", foxgloveBridgePort, foxgloveBridgeAddress)

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
