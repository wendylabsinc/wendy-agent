package commands

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	_ "embed"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

const (
	observeGatewayPort          = 8780
	observeGatewayAddress       = "127.0.0.1"
	observeAppID                = "sh.wendy.observe"
	observeDefaultMaxHz         = 10.0
	observeDefaultBandwidthMbps = 8.0
	observeDefaultPointStride   = 4
	observeDefaultJPEGQuality   = 65
	observeDefaultMaxWidth      = 960
)

//go:embed observe_gateway.py
var observeGatewayScript string

type observeOpts struct {
	localPort       int
	domain          int
	rmw             string
	distro          string
	device          string
	iface           string
	maxHz           float64
	bandwidthMbps   float64
	pointStride     int
	jpegQuality     int
	maxWidth        int
	cloud           bool
	cloudCfg        cloudDeviceConfig
	unitree         bool
	snapshotTimeout float64
}

// newObserveCmd exposes the same deep Observe interface at both `wendy
// observe` and `wendy device observe`. The generated app owns ROS and stream
// complexity; the command only chooses the device and session-wide limits.
func newObserveCmd() *cobra.Command {
	opts := &observeOpts{}
	cmd := &cobra.Command{
		Use:   "observe",
		Short: "Preprocess and stream robot data on demand",
		Long: `Deploy Wendy Observe to the device and open an authenticated tunnel.

Observe subscribes to ROS topics only while a client requests them. Continuous
latest-value data is available over WSS, while catalog, snapshot, and
single-stream responses are available over HTTPS. Known image and point-cloud
types are compressed or downsampled on the device before crossing the network.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runObserveFromCobra(cmd, *opts)
		},
	}
	bindObserveFlags(cmd, opts)
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Deploy and serve the Wendy Observe gateway",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runObserveFromCobra(cmd, *opts)
		},
	}
	cmd.AddCommand(serve)
	return cmd
}

func bindObserveFlags(cmd *cobra.Command, opts *observeOpts) {
	f := cmd.PersistentFlags()
	f.IntVar(&opts.localPort, "port", observeGatewayPort, "Local HTTPS/WSS port")
	f.IntVar(&opts.domain, "domain", 0, "ROS_DOMAIN_ID the device's ROS 2 uses")
	f.StringVar(&opts.rmw, "rmw", "rmw_cyclonedds_cpp", "RMW implementation the device's ROS 2 uses")
	f.StringVar(&opts.distro, "distro", "humble", "ROS 2 distribution for the Observe runtime")
	f.StringVar(&opts.iface, "interface", "", "Override the CycloneDDS network interface")
	f.Float64Var(&opts.maxHz, "max-hz", observeDefaultMaxHz, "Maximum output rate allowed per stream")
	f.Float64Var(&opts.bandwidthMbps, "max-bandwidth", observeDefaultBandwidthMbps, "Serialized Observe-frame budget in megabits/second")
	f.IntVar(&opts.pointStride, "point-stride", observeDefaultPointStride, "Minimum point-cloud sampling stride")
	f.IntVar(&opts.jpegQuality, "jpeg-quality", observeDefaultJPEGQuality, "Maximum JPEG quality (1-100)")
	f.IntVar(&opts.maxWidth, "max-image-width", observeDefaultMaxWidth, "Maximum encoded image width")
	f.Float64Var(&opts.snapshotTimeout, "snapshot-timeout", 5.0, "Seconds to wait for an HTTPS snapshot")
}

func runObserveFromCobra(cmd *cobra.Command, opts observeOpts) error {
	if err := validateObserveOpts(opts); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	opts.device = deviceFlag
	opts.cloudCfg, opts.cloud = cloudDeviceConfigFromContext(ctx)
	return runObserve(ctx, opts)
}

func validateObserveOpts(opts observeOpts) error {
	if opts.localPort < 1 || opts.localPort > 65535 {
		return fmt.Errorf("invalid local port %d", opts.localPort)
	}
	if opts.maxHz <= 0 || opts.maxHz > 120 {
		return fmt.Errorf("--max-hz must be greater than 0 and at most 120")
	}
	if opts.bandwidthMbps <= 0 || opts.bandwidthMbps > 10000 {
		return fmt.Errorf("--max-bandwidth must be greater than 0 and at most 10000 Mbps")
	}
	if opts.pointStride < 1 {
		return fmt.Errorf("--point-stride must be at least 1")
	}
	if opts.jpegQuality < 1 || opts.jpegQuality > 100 {
		return fmt.Errorf("--jpeg-quality must be between 1 and 100")
	}
	if opts.maxWidth < 160 {
		return fmt.Errorf("--max-image-width must be at least 160")
	}
	if opts.snapshotTimeout <= 0 {
		return fmt.Errorf("--snapshot-timeout must be greater than 0")
	}
	if opts.iface != "" && !foxgloveInterfacePattern.MatchString(opts.iface) {
		return fmt.Errorf("invalid network interface %q: use 1-15 letters, digits, '.', '_', ':', or '-'", opts.iface)
	}
	return nil
}

func runObserve(ctx context.Context, opts observeOpts) error {
	targetName, target, err := resolveObserveTarget(ctx, opts)
	if err != nil {
		return err
	}
	opts.device = targetName
	defer target.Close()

	if strings.Contains(strings.ToLower(opts.rmw), "cyclone") {
		iface, detectErr := detectProbableUnitreeInterface(ctx, target)
		if detectErr != nil && opts.iface == "" {
			cliLogln("Warning: could not inspect device interfaces; using CycloneDDS automatic selection: %v", detectErr)
		} else if iface != "" {
			opts.unitree = true
			if opts.iface == "" {
				opts.iface = iface
				cliLogln("Detected Unitree ROS network on %s; Observe will bind CycloneDDS to it", iface)
			}
		}
	}

	listener, err := listenDeviceTunnel(uint32(opts.localPort))
	if err != nil {
		return err
	}
	listenerOpen := true
	defer func() {
		if listenerOpen {
			_ = listener.Close()
		}
	}()

	if err := deployObserve(ctx, opts); err != nil {
		return err
	}

	errCh := make(chan error, 1)
	if !opts.cloud {
		listenerOpen = false
		go func() {
			defer listener.Close()
			errCh <- serveDeviceTunnel(ctx, target.Agent, listener, observeGatewayPort)
		}()
	} else {
		_ = listener.Close()
		listenerOpen = false
		self, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locating wendy binary: %w", err)
		}
		tunnel := exec.CommandContext(ctx, self, observeTunnelArgs(opts)...)
		tunnel.Stdout = os.Stdout
		tunnel.Stderr = os.Stderr
		if err := tunnel.Start(); err != nil {
			return fmt.Errorf("starting Observe cloud tunnel: %w", err)
		}
		go func() { errCh <- tunnel.Wait() }()
	}

	if err := waitForObserveEndpoint(ctx, opts.localPort, errCh); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	caPath, fingerprint, err := saveObserveCertificate(opts.localPort, opts.device)
	if err != nil {
		return fmt.Errorf("saving Observe session certificate: %w", err)
	}
	cliSuccess("Wendy Observe is ready")
	cliLogln("Live multiplexed streams: wss://localhost:%d/v1/live", opts.localPort)
	cliLogln("HTTPS catalog:            https://localhost:%d/v1/catalog", opts.localPort)
	cliLogln("HTTPS snapshot:           https://localhost:%d/v1/snapshot?topic=/topic", opts.localPort)
	cliLogln("HTTPS single stream:      https://localhost:%d/v1/stream?topic=/topic", opts.localPort)
	cliLogln("Session CA certificate:   %s", caPath)
	cliLogln("Certificate SHA-256:      %s", fingerprint)

	err = <-errCh
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("forwarding Observe gateway: %w", err)
	}
	return nil
}

func resolveObserveTarget(ctx context.Context, opts observeOpts) (string, *SelectedDevice, error) {
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
		return "", nil, fmt.Errorf("observe requires a WendyOS device reachable on the LAN")
	}
	return target.Agent.Host, target, nil
}

func deployObserve(ctx context.Context, opts observeOpts) error {
	dir, err := os.MkdirTemp("", "wendy-observe-*")
	if err != nil {
		return fmt.Errorf("creating temporary Observe app: %w", err)
	}
	defer os.RemoveAll(dir)
	if err := writeObserveApp(dir, opts); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating wendy binary: %w", err)
	}
	_ = exec.CommandContext(ctx, self, observeRemoveArgs(opts)...).Run()
	cliLogln("Deploying Wendy Observe to the device...")
	run := exec.CommandContext(ctx, self, observeRunArgs(opts)...)
	run.Dir = dir
	run.Stdin = os.Stdin
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	if err := run.Run(); err != nil {
		return fmt.Errorf("deploying Wendy Observe: %w", err)
	}
	return nil
}

func observeRemoveArgs(opts observeOpts) []string {
	args := []string{"device", "apps", "remove", observeAppID, "--force", "--cleanup", "--device", opts.device}
	if opts.cloud {
		args = append([]string{"cloud"}, args...)
		args = appendCloudFoxgloveFlags(args, opts.cloudCfg)
	}
	return args
}

func observeRunArgs(opts observeOpts) []string {
	args := []string{"run", "--detach", "--device", opts.device}
	if opts.cloud {
		args = append([]string{"cloud"}, args...)
		args = appendCloudFoxgloveFlags(args, opts.cloudCfg)
	} else {
		args = append(args, "--lan")
	}
	return args
}

func observeTunnelArgs(opts observeOpts) []string {
	args := []string{"cloud", "tunnel", fmt.Sprintf("%d:%d", opts.localPort, observeGatewayPort), "--device", opts.device}
	return appendCloudFoxgloveFlags(args, opts.cloudCfg)
}

func waitForObserveEndpoint(ctx context.Context, port int, tunnelErr <-chan error) error {
	readyCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if probeObserveEndpoint(port) == nil {
			return nil
		}
		select {
		case err := <-tunnelErr:
			if err == nil {
				return fmt.Errorf("Observe tunnel stopped before the gateway became ready")
			}
			return fmt.Errorf("Observe tunnel stopped before the gateway became ready: %w", err)
		case <-readyCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("timed out waiting for Wendy Observe on port %d", port)
		case <-ticker.C:
		}
	}
}

func observeHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 750 * time.Millisecond,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			TLSClientConfig: &tls.Config{ // #nosec G402 -- certificate is pinned immediately after the authenticated tunnel is established.
				InsecureSkipVerify: true,
				MinVersion:         tls.VersionTLS12,
			},
		},
	}
}

func probeObserveEndpoint(port int) error {
	response, err := observeHTTPClient().Get(fmt.Sprintf("https://localhost:%d/v1/health", port))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return fmt.Errorf("Observe health returned %s", response.Status)
	}
	return nil
}

func saveObserveCertificate(port int, device string) (string, string, error) {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), &tls.Config{ // #nosec G402 -- the cert is retrieved over an authenticated Wendy tunnel and then pinned.
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		return "", "", err
	}
	defer conn.Close()
	certificates := conn.ConnectionState().PeerCertificates
	if len(certificates) == 0 {
		return "", "", fmt.Errorf("Observe gateway did not present a certificate")
	}
	certificate := certificates[0]
	if err := certificate.VerifyHostname("localhost"); err != nil {
		return "", "", fmt.Errorf("Observe certificate is not valid for localhost: %w", err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(cache, "wendy", "observe")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	nameHash := sha256.Sum256([]byte(device))
	path := filepath.Join(dir, "session-"+hex.EncodeToString(nameHash[:8])+".pem")
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return "", "", err
	}
	fingerprint := sha256.Sum256(certificate.Raw)
	return path, hex.EncodeToString(fingerprint[:]), nil
}

func writeObserveApp(dir string, opts observeOpts) error {
	if err := validateObserveOpts(opts); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "observe_gateway.py"), []byte(observeGatewayScript), 0o644); err != nil {
		return fmt.Errorf("writing Observe gateway: %w", err)
	}

	launch := fmt.Sprintf("source /opt/ros/%s/setup.bash", opts.distro)
	if opts.unitree {
		launch += " && source /opt/unitree_msgs/setup.bash"
	}
	launch += " && export ROS_LOCALHOST_ONLY=0"
	if opts.iface != "" {
		cycloneURI := fmt.Sprintf(`<CycloneDDS><Domain><General><Interfaces><NetworkInterface name="%s"/></Interfaces></General><SharedMemory><Enable>false</Enable></SharedMemory></Domain></CycloneDDS>`, opts.iface)
		launch += " && export CYCLONEDDS_URI=" + shellQuote(cycloneURI)
	}
	if opts.unitree && opts.iface != "" {
		if err := os.WriteFile(filepath.Join(dir, "unitree_sdk2_to_ros.py"), []byte(unitreeSDK2ToROSScript), 0o644); err != nil {
			return fmt.Errorf("writing Unitree schema converter: %w", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "go2_camera_bridge.py"), []byte(go2CameraBridgeScript), 0o644); err != nil {
			return fmt.Errorf("writing Go2 camera bridge: %w", err)
		}
		launch += fmt.Sprintf(" && (python3 /opt/wendy/go2_camera_bridge.py --interface %s --fps 8 --jpeg-quality %d --max-width %d &)", opts.iface, opts.jpegQuality, opts.maxWidth)
	}
	bytesPerSecond := int64(opts.bandwidthMbps * 1_000_000 / 8)
	launch += " && install -d -m 0700 /run/wendy-observe"
	launch += " && openssl req -x509 -newkey rsa:2048 -sha256 -nodes -days 1 -keyout /run/wendy-observe/key.pem -out /run/wendy-observe/cert.pem -subj /CN=localhost -addext subjectAltName=DNS:localhost,IP:127.0.0.1 >/dev/null 2>&1"
	launch += fmt.Sprintf(" && exec python3 /opt/wendy/observe_gateway.py --address %s --port %d --tls-cert /run/wendy-observe/cert.pem --tls-key /run/wendy-observe/key.pem --max-hz %g --max-bytes-per-second %d --point-stride %d --jpeg-quality %d --max-width %d --snapshot-timeout %g", observeGatewayAddress, observeGatewayPort, opts.maxHz, bytesPerSecond, opts.pointStride, opts.jpegQuality, opts.maxWidth, opts.snapshotTimeout)

	aptPackages := fmt.Sprintf(`      openssl \
      python3-aiohttp \
      python3-numpy \
      python3-pil \
      ros-%s-rmw-cyclonedds-cpp \
      ros-%s-rosidl-runtime-py \
      ros-%s-sensor-msgs`, opts.distro, opts.distro, opts.distro)
	unitreeBuild := ""
	if opts.unitree {
		aptPackages += fmt.Sprintf(` \
      git \
      python3-pip \
      python3-colcon-common-extensions \
      ros-%s-grid-map-msgs \
      ros-%s-rosidl-default-generators \
      ros-%s-rosidl-generator-dds-idl`, opts.distro, opts.distro, opts.distro)
		unitreeBuild = fmt.Sprintf(`
ARG UNITREE_ROS2_COMMIT=%s
ARG UNITREE_SDK2_COMMIT=%s
ARG UNITREE_SDK2_PYTHON_COMMIT=%s
COPY unitree_sdk2_to_ros.py /tmp/unitree_sdk2_to_ros.py
RUN git clone https://github.com/unitreerobotics/unitree_ros2.git /tmp/unitree_ros2 \
    && cd /tmp/unitree_ros2 && git checkout --detach "${UNITREE_ROS2_COMMIT}" \
    && git clone https://github.com/unitreerobotics/unitree_sdk2.git /tmp/unitree_sdk2 \
    && cd /tmp/unitree_sdk2 && git checkout --detach "${UNITREE_SDK2_COMMIT}" \
    && python3 /tmp/unitree_sdk2_to_ros.py --sdk-root /tmp/unitree_sdk2 --ros-root /tmp/unitree_ros2/cyclonedds_ws/src/unitree --source-commit "${UNITREE_SDK2_COMMIT}" \
    && . /opt/ros/%s/setup.sh \
    && colcon --log-base /tmp/unitree-log build --base-paths /tmp/unitree_ros2/cyclonedds_ws/src/unitree --build-base /tmp/unitree-build --install-base /opt/unitree_msgs --merge-install --packages-select unitree_api unitree_go unitree_hg --cmake-args -DBUILD_TESTING=OFF \
    && rm -rf /tmp/unitree_ros2 /tmp/unitree_sdk2 /tmp/unitree-build /tmp/unitree-log /tmp/unitree_sdk2_to_ros.py
RUN mkdir -p /opt/cyclonedds \
    && ln -s /opt/ros/%s/include /opt/cyclonedds/include \
    && ln -s /opt/ros/%s/bin /opt/cyclonedds/bin \
    && ln -s /opt/ros/%s/lib/$(dpkg-architecture -qDEB_HOST_MULTIARCH) /opt/cyclonedds/lib \
    && CYCLONEDDS_HOME=/opt/cyclonedds pip3 install --no-cache-dir cyclonedds==0.10.2 \
    && git clone https://github.com/unitreerobotics/unitree_sdk2_python.git /opt/unitree_sdk2_python \
    && cd /opt/unitree_sdk2_python && git checkout --detach "${UNITREE_SDK2_PYTHON_COMMIT}" \
    && pip3 install --no-cache-dir --no-deps .
COPY go2_camera_bridge.py /opt/wendy/go2_camera_bridge.py
`, unitreeROS2Commit, unitreeSDK2Commit, unitreeSDK2PythonCommit, opts.distro, opts.distro, opts.distro, opts.distro)
	}

	dockerfile := fmt.Sprintf(`# Auto-generated by 'wendy observe'.
FROM ros:%s
RUN apt-get update && apt-get install -y --no-install-recommends \
%s \
    && rm -rf /var/lib/apt/lists/*
%s
COPY observe_gateway.py /opt/wendy/observe_gateway.py
CMD ["bash","-lc",%q]
`, opts.distro, aptPackages, unitreeBuild, launch)
	wJSON := fmt.Sprintf(`{
  "appId": %q,
  "platform": "linux",
  "version": "1.0.0",
  "frameworks": {
    "ros2": { "domainId": %d, "rmw": %q, "distro": %q, "discoveryScope": "host" }
  },
  "entitlements": [
    { "type": "network", "mode": "host" }
  ],
  "services": {
    "observe": { "context": "." }
  }
}
`, observeAppID, opts.domain, opts.rmw, opts.distro)
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		return fmt.Errorf("writing Observe Dockerfile: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wendy.json"), []byte(wJSON), 0o644); err != nil {
		return fmt.Errorf("writing Observe manifest: %w", err)
	}
	return nil
}
