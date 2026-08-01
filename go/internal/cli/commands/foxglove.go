package commands

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// foxgloveBridgePort is the port foxglove_bridge listens on inside the app.
const foxgloveBridgePort = 8765

// Keep the unauthenticated Foxglove WebSocket on device loopback. Direct mode
// reaches it through WendyTunnelService; Cloud mode uses the broker tunnel.
const foxgloveBridgeAddress = "127.0.0.1"

// foxgloveAppID is the appId of the generated foxglove_bridge app; used both in
// the generated wendy.json and to remove a prior instance before redeploy.
const foxgloveAppID = "sh.wendy.foxglovebridge"

// Keep the queue small enough to bound visualization latency, but large enough
// for the bridge's initial control-plane burst (server info, channels,
// parameters, and services). A backlog of 1 disconnects the client as soon as
// two control messages are pending; data-plane messages are already dropped
// oldest-first when this bounded queue fills.
const foxgloveDefaultMessageBacklog = 32

// The default view is intentionally useful but bandwidth-bounded. Point clouds,
// voxel/range/height maps, and raw images require explicit --topic opt-in (or
// --all-topics) because a single active Foxglove panel can saturate a 100 Mbit
// robot link with those streams.
var foxgloveDefaultTopicWhitelist = []string{
	`^/tf$`,
	`^/tf_static$`,
	`^/front_camera/image/compressed$`,
	`^/odom$`,
	`^/joint_states$`,
	`^/diagnostics$`,
	`^/diagnostics/.*$`,
	`^/uslam/frontend/odom$`,
	`^/sportmodestate$`,
	`^/lf/sportmodestate$`,
}

// unitreeROS2Commit is the peeled commit for unitree_ros2 v0.3.0. Pinning the
// source keeps generated bridge images reproducible while providing the Go2's
// public unitree_go, unitree_api, and unitree_hg message definitions to Foxglove.
const unitreeROS2Commit = "66ae09858245ac3d2231c0cc209e36a88f8d7d03"

// unitreeSDK2Commit pins the official DDS definitions used to fill public ROS
// package gaps. The converter rejects unknown field declarations, preventing a
// future SDK layout from being silently mistranslated.
const unitreeSDK2Commit = "21d0a3b2c46ee48c8fdf2783becb6be3beb0a59b"

// unitreeSDK2PythonCommit pins the official Python camera client used to
// request JPEG frames from a Go2's VideoHub service.
const unitreeSDK2PythonCommit = "65691c8a8bc53b98d3976dba4dbf9d5d20b2e7f5"

//go:embed unitree_sdk2_to_ros.py
var unitreeSDK2ToROSScript string

//go:embed go2_camera_bridge.py
var go2CameraBridgeScript string

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
		port    int
		domain  int
		rmw     string
		distro  string
		iface   string
		topics  []string
		all     bool
		backlog int
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Deploy foxglove_bridge to the device and open a tunnel for Foxglove Studio",
		Long: `Generates a foxglove_bridge app, deploys it to the target device with
'wendy run' (host networking, so it joins the device's ROS 2 graph — including a
robot's native host ROS 2), then forwards its WebSocket port to your machine.

Connect Foxglove Studio to the printed ws:// URL. For a robot whose ROS 2 uses a
non-default domain or RMW (e.g. a Unitree Go2 on CycloneDDS), pass --domain and
--rmw so the bridge matches it. By default the bridge exposes a bandwidth-safe
set of transforms, odometry, diagnostics, and compressed camera topics. Use
repeatable --topic expressions to choose another set, or --all-topics to expose
everything. When no --interface is supplied, Wendy uses
the unique device interface on Unitree's 192.168.123.0/24 robot network when one
is present. The device-side WebSocket binds to 127.0.0.1 and is reached through
either 'wendy device tunnel' or the authenticated Cloud tunnel.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("message-backlog") && backlog < 1 {
				return fmt.Errorf("--message-backlog must be at least 1")
			}
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
				topics:    topics,
				allTopics: all,
				backlog:   backlog,
			})
		},
	}

	cmd.Flags().IntVar(&port, "port", foxgloveBridgePort, "Local port to forward foxglove_bridge to")
	cmd.Flags().IntVar(&domain, "domain", 0, "ROS_DOMAIN_ID the device's ROS 2 uses")
	cmd.Flags().StringVar(&rmw, "rmw", "rmw_cyclonedds_cpp", "RMW implementation the device's ROS 2 uses")
	cmd.Flags().StringVar(&distro, "distro", "humble", "ROS 2 distro to build foxglove_bridge from")
	cmd.Flags().StringVar(&iface, "interface", "", "Override the CycloneDDS network interface (auto-detects a Unitree robot network)")
	cmd.Flags().StringArrayVar(&topics, "topic", nil, "Expose only topics matching this regular expression (repeatable; replaces the bandwidth-safe defaults)")
	cmd.Flags().BoolVar(&all, "all-topics", false, "Expose every ROS topic (may use substantial bandwidth)")
	cmd.Flags().IntVar(&backlog, "message-backlog", foxgloveDefaultMessageBacklog, "Maximum outgoing data messages queued per client")

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
	unitree   bool // include public Unitree ROS 2 message definitions
	topics    []string
	allTopics bool
	backlog   int
}

// foxgloveServe generates a foxglove_bridge app in a temp dir, deploys it to the
// device via `wendy run --detach`, then forwards the bridge's WebSocket port.
// Direct device commands forward over the selected LAN connection; cloud device
// commands deploy and forward through the same cloud asset.
func foxgloveServe(ctx context.Context, opts foxgloveServeOpts) error {
	if err := validateFoxgloveBandwidthOpts(opts); err != nil {
		return err
	}
	if opts.localPort < 1 || opts.localPort > 65535 {
		return fmt.Errorf("invalid local port %d", opts.localPort)
	}
	targetName, target, err := resolveFoxgloveTarget(ctx, opts)
	if err != nil {
		return err
	}
	opts.device = targetName

	if strings.Contains(strings.ToLower(opts.rmw), "cyclone") {
		iface, err := detectProbableUnitreeInterface(ctx, target)
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
	// Direct mode keeps this exact selected agent connection open for the local
	// Foxglove relay below. Cloud mode uses a separately pinned broker tunnel.
	if opts.cloud {
		target.Close()
	} else {
		defer target.Close()
	}

	// Reserve the direct-mode local endpoint before doing any expensive image
	// work. Besides failing fast on a genuine conflict, this prevents another
	// serve process from stealing the requested port while deployment runs.
	var localListener net.Listener
	if !opts.cloud {
		localListener, err = listenDeviceTunnel(uint32(opts.localPort))
		if err != nil {
			return err
		}
		defer localListener.Close()
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
		return serveDeviceTunnel(ctx, target.Agent, localListener, foxgloveBridgePort)
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
func resolveFoxgloveTarget(ctx context.Context, opts foxgloveServeOpts) (string, *SelectedDevice, error) {
	if opts.cloud {
		auth, err := pickAuthEntry(opts.cloudCfg.CloudGRPC)
		if err != nil {
			return "", nil, err
		}
		asset, err := pickCloudDevice(ctx, auth, opts.cloudCfg.DeviceName, opts.cloudCfg.BrokerURL)
		if err != nil {
			return "", nil, err
		}
		conn, err := connectCloudAsset(ctx, auth, asset, opts.cloudCfg.BrokerURL)
		if err != nil {
			return "", nil, err
		}
		return strconv.FormatInt(int64(asset.GetId()), 10), &SelectedDevice{Agent: conn}, nil
	}

	target, err := resolveTarget(ctx, ExcludeBluetooth())
	if err != nil {
		return "", nil, err
	}
	if target.Agent == nil || target.Agent.Host == "" {
		target.Close()
		return "", nil, fmt.Errorf("foxglove serve requires a WendyOS device reachable on the LAN")
	}
	return target.Agent.Host, target, nil
}

// detectProbableUnitreeInterface asks the already-selected Wendy agent for the
// same filtered interface inventory shown by `wendy device info`. Reusing this
// connection ensures cloud detection cannot drift away from the pinned asset.
func detectProbableUnitreeInterface(ctx context.Context, target *SelectedDevice) (string, error) {
	if target == nil || target.Agent == nil {
		return "", nil
	}
	resp, err := target.Agent.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
	if err != nil {
		return "", fmt.Errorf("getting device network interfaces: %w", err)
	}
	return probableUnitreeInterface(resp.GetNetworkInterfaces()), nil
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
	if err := validateFoxgloveBandwidthOpts(opts); err != nil {
		return err
	}
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
	if opts.unitree && opts.iface != "" {
		launchCommand += fmt.Sprintf(" && (python3 /opt/wendy/go2_camera_bridge.py --interface %s &)", opts.iface)
	}
	topicWhitelist, err := foxgloveTopicWhitelistArg(opts)
	if err != nil {
		return err
	}
	backlog := opts.backlog
	if backlog == 0 {
		backlog = foxgloveDefaultMessageBacklog
	}
	launchCommand += fmt.Sprintf(" && exec ros2 launch foxglove_bridge foxglove_bridge_launch.xml port:=%d address:=%s include_hidden:=true topic_whitelist:=%s message_backlog_size:=%d", foxgloveBridgePort, foxgloveBridgeAddress, shellQuote(topicWhitelist), backlog)

	aptPackages := fmt.Sprintf(`      ros-%s-foxglove-bridge \
      ros-%s-rmw-cyclonedds-cpp`, opts.distro, opts.distro)
	unitreeBuild := ""
	if opts.unitree {
		aptPackages += fmt.Sprintf(` \
      git \
      python3-pip \
      python3-colcon-common-extensions \
      ros-%s-grid-map-msgs \
      ros-%s-rosidl-default-generators \
      ros-%s-rosidl-generator-dds-idl`, opts.distro, opts.distro, opts.distro)
		if err := os.WriteFile(filepath.Join(dir, "unitree_sdk2_to_ros.py"), []byte(unitreeSDK2ToROSScript), 0o644); err != nil {
			return fmt.Errorf("writing Unitree SDK2 schema converter: %w", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "go2_camera_bridge.py"), []byte(go2CameraBridgeScript), 0o644); err != nil {
			return fmt.Errorf("writing Go2 camera bridge: %w", err)
		}
		unitreeBuild = fmt.Sprintf(`
ARG UNITREE_ROS2_COMMIT=%s
ARG UNITREE_SDK2_COMMIT=%s
ARG UNITREE_SDK2_PYTHON_COMMIT=%s
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
RUN mkdir -p /opt/cyclonedds \
    && ln -s /opt/ros/%s/include /opt/cyclonedds/include \
    && ln -s /opt/ros/%s/bin /opt/cyclonedds/bin \
    && ln -s /opt/ros/%s/lib/$(dpkg-architecture -qDEB_HOST_MULTIARCH) /opt/cyclonedds/lib \
    && CYCLONEDDS_HOME=/opt/cyclonedds \
      pip3 install --no-cache-dir cyclonedds==0.10.2 \
    && git clone https://github.com/unitreerobotics/unitree_sdk2_python.git /opt/unitree_sdk2_python \
    && cd /opt/unitree_sdk2_python \
    && git checkout --detach "${UNITREE_SDK2_PYTHON_COMMIT}" \
    && pip3 install --no-cache-dir --no-deps .
COPY go2_camera_bridge.py /opt/wendy/go2_camera_bridge.py
`, unitreeROS2Commit, unitreeSDK2Commit, unitreeSDK2PythonCommit, opts.distro, opts.distro, opts.distro, opts.distro)
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

func validateFoxgloveBandwidthOpts(opts foxgloveServeOpts) error {
	if opts.allTopics && len(opts.topics) > 0 {
		return fmt.Errorf("--all-topics cannot be combined with --topic")
	}
	if opts.backlog < 0 {
		return fmt.Errorf("--message-backlog must be at least 1")
	}
	return nil
}

func foxgloveTopicWhitelistArg(opts foxgloveServeOpts) (string, error) {
	topics := opts.topics
	if opts.allTopics {
		topics = []string{`.*`}
	} else if len(topics) == 0 {
		topics = foxgloveDefaultTopicWhitelist
	}
	encoded, err := json.Marshal(topics)
	if err != nil {
		return "", fmt.Errorf("encoding Foxglove topic whitelist: %w", err)
	}
	return string(encoded), nil
}
