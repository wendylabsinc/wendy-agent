package commands

import (
	"context"
	_ "embed"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// foxgloveBridgePort is the port foxglove_bridge listens on inside the app.
const foxgloveBridgePort = 8765

// foxgloveAppID is the appId of the generated foxglove_bridge app; used both in
// the generated wendy.json and to remove a prior instance before redeploy.
const foxgloveAppID = "sh.wendy.foxglovebridge"

// unitreeROS2Commit is the peeled commit for unitree_ros2 v0.3.0. Pinning the
// source keeps generated bridge images reproducible while providing the Go2's
// public unitree_go, unitree_api, and unitree_hg message definitions to Foxglove.
const unitreeROS2Commit = "66ae09858245ac3d2231c0cc209e36a88f8d7d03"

// unitreeSDK2Commit pins the official DDS definitions used to fill public ROS
// package gaps. The converter rejects unknown field declarations, preventing a
// future SDK layout from being silently mistranslated.
const unitreeSDK2Commit = "21d0a3b2c46ee48c8fdf2783becb6be3beb0a59b"

//go:embed unitree_sdk2_to_ros.py
var unitreeSDK2ToROSScript string

// Linux interface names are limited to IFNAMSIZ-1 (15) bytes. Keeping the
// accepted character set narrow also makes it safe to embed the name in the
// generated CycloneDDS XML and shell command.
var foxgloveInterfacePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`)

// Unitree documents 192.168.123.0/24 as the wired robot network used by its
// ROS 2 setup. A Wendy device with exactly one interface on this subnet is
// therefore probably Unitree-connected even when it has no stored robot type.
var unitreeROSNetwork = netip.MustParsePrefix("192.168.123.0/24")

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
ROS topics, in the selected domain. When no --interface is supplied, Wendy uses
the unique device interface on Unitree's 192.168.123.0/24 robot network when one
is present.`,
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
				iface:     iface,
			})
		},
	}

	cmd.Flags().IntVar(&port, "port", foxgloveBridgePort, "Local port to forward foxglove_bridge to")
	cmd.Flags().IntVar(&domain, "domain", 0, "ROS_DOMAIN_ID the device's ROS 2 uses")
	cmd.Flags().StringVar(&rmw, "rmw", "rmw_cyclonedds_cpp", "RMW implementation the device's ROS 2 uses")
	cmd.Flags().StringVar(&distro, "distro", "humble", "ROS 2 distro to build foxglove_bridge from")
	cmd.Flags().StringVar(&iface, "interface", "", "Override the CycloneDDS network interface (auto-detects a Unitree robot network)")

	return cmd
}

type foxgloveServeOpts struct {
	localPort int
	domain    int
	rmw       string
	distro    string
	device    string // global --device; "" = default device
	iface     string // optional CycloneDDS network interface
	unitree   bool   // include public Unitree ROS 2 message definitions
}

// foxgloveServe generates a foxglove_bridge app in a temp dir, deploys it to the
// device via `wendy run --detach`, then forwards the bridge's WebSocket port to
// localhost via `wendy cloud tunnel`. The tunnel runs until ctx is cancelled.
func foxgloveServe(ctx context.Context, opts foxgloveServeOpts) error {
	if strings.Contains(strings.ToLower(opts.rmw), "cyclone") {
		iface, err := detectProbableUnitreeInterface(ctx)
		if err != nil && opts.iface == "" {
			// Interface inference is a convenience, not a prerequisite. Older
			// agents may not report network metadata, and normal CycloneDDS
			// automatic selection remains a useful fallback for other robots.
			cliLogln("Warning: could not inspect device interfaces; using CycloneDDS automatic selection: %v", err)
		} else if iface != "" {
			opts.unitree = true
			if opts.iface == "" {
				opts.iface = iface
				cliLogln("Detected probable Unitree ROS network on %s; using it for CycloneDDS and loading Unitree message definitions", iface)
			} else {
				cliLogln("Detected probable Unitree ROS network on %s; loading Unitree message definitions", iface)
			}
		}
	}

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

// detectProbableUnitreeInterface asks the selected Wendy agent for the same
// filtered interface inventory shown by `wendy device info`. Detection is
// deliberately best-effort: callers fall back to CycloneDDS selection when an
// older agent omits the inventory or the network is not unambiguous.
func detectProbableUnitreeInterface(ctx context.Context) (string, error) {
	target, err := resolveTarget(ctx)
	if err != nil {
		return "", err
	}
	defer target.Close()

	if target.Agent == nil {
		return "", nil
	}
	resp, err := target.Agent.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
	if err != nil {
		return "", fmt.Errorf("getting device network interfaces: %w", err)
	}
	return probableUnitreeInterface(resp.GetNetworkInterfaces()), nil
}

// probableUnitreeInterface returns the sole valid interface with an address on
// Unitree's documented robot subnet. Multiple matching interfaces are treated
// as ambiguous rather than choosing one based on enumeration order.
func probableUnitreeInterface(ifaces []*agentpb.NetworkInterface) string {
	var match string
	for _, iface := range ifaces {
		name := iface.GetName()
		if !foxgloveInterfacePattern.MatchString(name) {
			continue
		}
		for _, rawAddr := range iface.GetIpAddresses() {
			addr, err := netip.ParseAddr(rawAddr)
			if err != nil || !unitreeROSNetwork.Contains(addr) {
				continue
			}
			if match != "" && match != name {
				return ""
			}
			match = name
		}
	}
	return match
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
	launchCommand := fmt.Sprintf("source /opt/ros/%s/setup.bash", opts.distro)
	if opts.unitree {
		launchCommand += " && source /opt/unitree_msgs/setup.bash"
	}
	launchCommand += " && export ROS_LOCALHOST_ONLY=0"
	if opts.iface != "" {
		cycloneURI := fmt.Sprintf(`<CycloneDDS><Domain><General><Interfaces><NetworkInterface name="%s"/></Interfaces></General><SharedMemory><Enable>false</Enable></SharedMemory></Domain></CycloneDDS>`, opts.iface)
		launchCommand += " && export CYCLONEDDS_URI='" + cycloneURI + "'"
	}
	launchCommand += fmt.Sprintf(" && exec ros2 launch foxglove_bridge foxglove_bridge_launch.xml port:=%d address:=0.0.0.0 include_hidden:=true", foxgloveBridgePort)

	aptPackages := fmt.Sprintf(`      ros-%s-foxglove-bridge \
      ros-%s-rmw-cyclonedds-cpp`, opts.distro, opts.distro)
	unitreeBuild := ""
	if opts.unitree {
		aptPackages += fmt.Sprintf(` \
      git \
      python3-colcon-common-extensions \
      ros-%s-grid-map-msgs \
      ros-%s-rosidl-default-generators \
      ros-%s-rosidl-generator-dds-idl`, opts.distro, opts.distro, opts.distro)
		if err := os.WriteFile(filepath.Join(dir, "unitree_sdk2_to_ros.py"), []byte(unitreeSDK2ToROSScript), 0o644); err != nil {
			return fmt.Errorf("writing Unitree SDK2 schema converter: %w", err)
		}
		unitreeBuild = fmt.Sprintf(`
ARG UNITREE_ROS2_COMMIT=%s
ARG UNITREE_SDK2_COMMIT=%s
COPY unitree_sdk2_to_ros.py /tmp/unitree_sdk2_to_ros.py
RUN git clone https://github.com/unitreerobotics/unitree_ros2.git /tmp/unitree_ros2 \
    && cd /tmp/unitree_ros2 \
    && git checkout --detach "${UNITREE_ROS2_COMMIT}" \
    && git clone https://github.com/unitreerobotics/unitree_sdk2.git /tmp/unitree_sdk2 \
    && cd /tmp/unitree_sdk2 \
    && git checkout --detach "${UNITREE_SDK2_COMMIT}" \
    && python3 /tmp/unitree_sdk2_to_ros.py \
         --sdk-root /tmp/unitree_sdk2 \
         --ros-root /tmp/unitree_ros2/cyclonedds_ws/src/unitree \
         --source-commit "${UNITREE_SDK2_COMMIT}" \
    && . /opt/ros/%s/setup.sh \
    && colcon --log-base /tmp/unitree-log build \
         --base-paths /tmp/unitree_ros2/cyclonedds_ws/src/unitree \
         --build-base /tmp/unitree-build \
         --install-base /opt/unitree_msgs \
         --merge-install \
         --packages-select unitree_api unitree_go unitree_hg \
         --cmake-args -DBUILD_TESTING=OFF \
    && install -D -m 0644 /tmp/unitree_sdk2/LICENSE /opt/unitree_msgs/share/unitree_sdk2/LICENSE \
    && printf '%%s\n' "${UNITREE_SDK2_COMMIT}" > /opt/unitree_msgs/share/unitree_sdk2/SOURCE_COMMIT \
    && rm -rf /tmp/unitree_ros2 /tmp/unitree_sdk2 /tmp/unitree-build /tmp/unitree-log /tmp/unitree_sdk2_to_ros.py
`, unitreeROS2Commit, unitreeSDK2Commit, opts.distro)
	}

	dockerfile := fmt.Sprintf(`# Auto-generated by 'wendy device foxglove serve'.
FROM ros:%s
RUN apt-get update && apt-get install -y --no-install-recommends \
%s \
    && rm -rf /var/lib/apt/lists/*
%s
CMD ["bash","-lc",%q]
`, opts.distro, aptPackages, unitreeBuild, launchCommand)

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
