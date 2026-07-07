package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/wendylabsinc/wendy/go/internal/agent/bluetooth"
	"github.com/wendylabsinc/wendy/go/internal/agent/cdi"
	"github.com/wendylabsinc/wendy/go/internal/agent/configpartition"
	"github.com/wendylabsinc/wendy/go/internal/agent/container"
	agentcontainerd "github.com/wendylabsinc/wendy/go/internal/agent/containerd"
	"github.com/wendylabsinc/wendy/go/internal/agent/dbusproxy"
	"github.com/wendylabsinc/wendy/go/internal/agent/hardware"
	"github.com/wendylabsinc/wendy/go/internal/agent/interceptor"
	"github.com/wendylabsinc/wendy/go/internal/agent/localsocket"
	"github.com/wendylabsinc/wendy/go/internal/agent/mtls"
	agentnet "github.com/wendylabsinc/wendy/go/internal/agent/network"
	"github.com/wendylabsinc/wendy/go/internal/agent/oci"
	"github.com/wendylabsinc/wendy/go/internal/agent/registry"
	"github.com/wendylabsinc/wendy/go/internal/agent/services"
	"github.com/wendylabsinc/wendy/go/internal/agent/timesync"
	"github.com/wendylabsinc/wendy/go/internal/shared/browseropen"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

const (
	defaultAgentPort    = "50051"
	defaultOTELPort     = "4317"
	defaultOTELHTTPPort = "4318"
)

// containerMonitorAdapter satisfies services.ContainerMonitorRegistrar without a
// circular import: container imports services, so we bridge with plain-int policy values
// that mirror container.RestartPolicy.
type containerMonitorAdapter struct {
	m *container.ContainerMonitor
}

func (a *containerMonitorAdapter) Register(appName string, policy int, maxRetries int) {
	var rp container.RestartPolicy
	switch policy {
	case services.RestartPolicyAlways:
		rp = container.RestartAlways
	case services.RestartPolicyUnlessStopped:
		rp = container.RestartUnlessStopped
	case services.RestartPolicyOnFailure:
		rp = container.RestartOnFailure
	default:
		// Unknown or RestartPolicyNo — skip registration.
		return
	}
	a.m.Register(appName, rp, maxRetries)
}

func (a *containerMonitorAdapter) Unregister(appName string) {
	a.m.Unregister(appName)
}

func (a *containerMonitorAdapter) MarkExplicitStop(appName string) {
	a.m.MarkExplicitStop(appName)
}

func (a *containerMonitorAdapter) ClearExplicitStop(appName string) {
	a.m.ClearExplicitStop(appName)
}

// RestartStatuses exposes the monitor's restart bookkeeping so ListContainers
// can report crash-looping apps truthfully (services.RestartStatusProvider).
func (a *containerMonitorAdapter) RestartStatuses() map[string]services.RestartStatus {
	return a.m.RestartStatuses()
}

func main() {
	if handled, code := handleUtilityCommand(os.Args[1:]); handled {
		os.Exit(code)
	}

	logCfg := zap.NewProductionConfig()
	if os.Getenv("WENDY_DEBUG") != "" {
		logCfg = zap.NewDevelopmentConfig()
	}
	logger, err := logCfg.Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	// Create the telemetry broadcaster early so we can tee agent logs into it.
	broadcaster := services.NewTelemetryBroadcaster()

	telemetryBuf := services.NewTelemetryBuffer(services.TelemetryBufferConfig{}, broadcaster, logger)
	if !telemetryBuf.DiskEnabled() {
		logger.Warn("telemetry disk buffer unavailable, falling back to in-memory only")
	}

	// Wrap the logger so agent internal logs are published to the telemetry stream.
	telemetryCore := services.NewTelemetryCore(telemetryBuf, zapcore.DebugLevel)
	logger = zap.New(zapcore.NewTee(logger.Core(), telemetryCore))

	logger.Info("Starting wendy-agent", zap.String("version", version.Version))

	configPath := "/etc/wendy-agent"
	if envPath := os.Getenv("WENDY_CONFIG_PATH"); envPath != "" {
		configPath = envPath
	}

	configpartition.Apply(logger, configPath)

	// Time sync: apply config-partition floor immediately, then start
	// background Roughtime + multicast sync.
	timesyncMgr := timesync.NewManager(logger, configPath)
	timesyncMgr.ApplyFloor()

	// Run the OS-update gate after the time floor is applied so the marker
	// staleness check and the persisted result timestamps use a sane clock.
	services.RunOSUpdateGate(logger)

	services.CleanupOldBackups(logger)
	cdi.EnsureNVIDIACDISpec(logger)

	var networkMgr services.NetworkManager
	if nm := agentnet.NewNMCLINetworkManager(logger); nm != nil {
		networkMgr = nm
	}
	hwDiscoverer := hardware.NewSystemHardwareDiscoverer(logger)
	btManager := bluetooth.NewManager(logger)

	var proxyMgr *dbusproxy.Manager
	if dbusproxy.IsAvailable() {
		proxyMgr = dbusproxy.NewManager(logger)
	} else {
		// WDY-1093: without xdg-dbus-proxy there is no way to scope D-Bus to
		// org.bluez, so containers declaring the bluetooth entitlement are
		// refused rather than started with unfiltered access.
		logger.Warn("xdg-dbus-proxy not found; containers with the bluetooth entitlement will be refused")
	}

	// Initialize containerd client (best-effort; may fail on non-Linux or without containerd).
	var containerdClient services.ContainerdClient
	containerdAddr := os.Getenv("WENDY_CONTAINERD_ADDR")
	if containerdAddr == "" {
		containerdAddr = agentcontainerd.DefaultAddress
	}
	ctrdClient, ctrdErr := agentcontainerd.NewClient(logger, containerdAddr, proxyMgr)
	if ctrdErr != nil {
		logger.Warn("Failed to connect to containerd (container features will be unavailable)", zap.Error(ctrdErr))
	} else {
		containerdClient = ctrdClient
		defer ctrdClient.Close()
	}

	logManager := services.NewContainerLogManager(logger, telemetryBuf)

	installer := &services.AgentInstaller{}
	agentSvc := services.NewAgentService(logger, networkMgr, hwDiscoverer, btManager, installer)

	var monitor *container.ContainerMonitor
	if containerdClient != nil {
		monitor = container.NewContainerMonitor(logger, containerdClient, logManager, 15*time.Second)
	}

	containerSvcOpts := []services.ContainerServiceOption{
		services.WithLogManager(logManager),
	}
	if monitor != nil {
		containerSvcOpts = append(containerSvcOpts, services.WithMonitor(&containerMonitorAdapter{m: monitor}))
	}
	containerSvc := services.NewContainerService(logger, containerdClient,
		containerSvcOpts...,
	)
	audioSvc := services.NewAudioService(logger)

	provisioningSvc := services.NewProvisioningService(logger, configPath)
	telemetrySvc := services.NewTelemetryService(logger, broadcaster, telemetryBuf)

	deviceInfoSvc := services.NewDeviceInfoService(logger, hwDiscoverer)
	timeSyncSvc := services.NewTimeSyncService(logger, timesyncMgr)
	wifiSvc := services.NewWiFiService(logger, networkMgr)
	bluetoothSvc := services.NewBluetoothService(logger, btManager)
	agentUpdateSvc := services.NewAgentUpdateService(logger, installer)
	osUpdateSvc := services.NewOSUpdateService(logger)
	containerSvcV2 := services.NewContainerServiceV2(containerSvc)
	provisioningSvcV2 := services.NewProvisioningServiceV2(provisioningSvc)
	audioSvcV2 := services.NewAudioServiceV2(audioSvc)
	telemetrySvcV2 := services.NewTelemetryServiceV2(logger, broadcaster, telemetryBuf)
	// ROS 2 inspection requires the containerd-backed sidecar runtime; the
	// service is only registered when containerd connected (WDY-1332).
	var ros2Svc *services.ROS2Service
	if ctrdClient != nil {
		ros2Svc = services.NewROS2Service(logger, ctrdClient, agentcontainerd.ROS2BagDir)
	}

	// OTEL receivers.
	otelLogReceiver := services.NewOTELLogsReceiver(telemetryBuf)
	otelMetricReceiver := services.NewOTELMetricsReceiver(telemetryBuf)
	otelTraceReceiver := services.NewOTELTraceReceiver(telemetryBuf)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go timesyncMgr.RunDirect(ctx)
	go timesyncMgr.RunMulticast(ctx)

	videoSvc := services.NewVideoService(ctx, logger)
	defer videoSvc.Shutdown()

	bleDispatcher := bluetooth.NewDispatcher(networkMgr, containerdClient, hwDiscoverer, btManager)

	// Returns nil if the PEM data is invalid, which causes the registry to stay HTTP.
	registryTLSConfig := func(certPEM, chainPEM, keyPEM string) *tls.Config {
		tlsConfig, err := mtls.NewTLSConfig(certPEM, chainPEM, keyPEM, nil, certNotBeforeFloor(certPEM))
		if err != nil {
			logger.Error("Failed to build registry TLS config", zap.Error(err))
			return nil
		}
		return tlsConfig
	}

	var (
		registrySrv   *registry.Server
		registrySrvMu sync.Mutex
	)

	// When tlsConfig is non-nil serves HTTPS; nil means plain HTTP (pre-provisioning only).
	startRegistry := func(tlsConfig *tls.Config) {
		registrySrvMu.Lock()
		defer registrySrvMu.Unlock()

		if registrySrv != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := registrySrv.Shutdown(shutdownCtx); err != nil {
				logger.Warn("Registry shutdown error during restart", zap.Error(err))
			}
			registrySrv = nil
		}

		registryAddr := "0.0.0.0:5000"
		if addr := os.Getenv("WENDY_REGISTRY_ADDR"); addr != "" {
			registryAddr = addr
		}

		srv, err := registry.Start(ctx, containerdAddr, registryAddr, logger, tlsConfig)
		if err != nil {
			logger.Warn("Failed to start embedded dev registry (image push will be unavailable)", zap.Error(err))
			return
		}
		registrySrv = srv
	}

	var wg sync.WaitGroup

	if monitor != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			monitor.Run(ctx)
		}()
		// Re-launch apps that should run after a reboot (per their restart
		// policy, minus user-stopped ones) now that the monitor is running.
		// Done in its own goroutine so agent startup isn't blocked on container I/O.
		go monitor.ReconcileBootContainers(ctx)
	}

	if containerdClient != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			services.CollectContainerMetrics(ctx, containerdClient, telemetryBuf, logManager)
		}()
	}

	if ctrdClient != nil {
		if err := ctrdClient.ReapOrphanedROS2Sidecars(ctx); err != nil {
			logger.Warn("ROS 2 sidecar reap on boot failed", zap.Error(err))
		}
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		services.CollectAgentMetrics(ctx, telemetryBuf)
	}()

	// Collect kernel messages from /dev/kmsg as OTel debug/trace logs.
	// Opt-in: set WENDY_COLLECT_DMESG=true to enable. Disabled by default
	// because kernel messages may contain PII (MAC addresses, serial numbers,
	// process names, filesystem paths) that operators must consciously opt into
	// forwarding to their telemetry backend.
	collectDmesgEnv := os.Getenv("WENDY_COLLECT_DMESG")
	collectDmesg, collectDmesgErr := strconv.ParseBool(collectDmesgEnv)
	if collectDmesgEnv != "" && collectDmesgErr != nil {
		logger.Warn("WENDY_COLLECT_DMESG has unrecognised value; dmesg collection disabled",
			zap.String("value", collectDmesgEnv),
		)
	}
	if collectDmesg {
		// Dual-control gate: env-var (WENDY_COLLECT_DMESG) is the first domain;
		// the DPIA confirmation file is the second, filesystem domain. A process
		// with only env-var write access cannot enable collection on its own.
		// CollectDmesgLogs enforces this independently, but the pre-check here
		// makes both controls visible at the callsite and avoids starting a
		// goroutine that would immediately return on DPIA failure.
		// Check both existence and non-empty content to mirror CollectDmesgLogs.
		dpiaContent, dpiaErr := os.ReadFile(services.DmesgDPIAConfirmFile)
		dpiaValid := dpiaErr == nil && len(bytes.TrimSpace(dpiaContent)) > 0
		for i := range dpiaContent {
			dpiaContent[i] = 0
		}
		dpiaContent = nil
		if !dpiaValid {
			logger.Info("kernel dmesg collection skipped: DPIA confirmation file absent or empty",
				zap.String("file", services.DmesgDPIAConfirmFile),
				zap.String("reason", "filesystem-domain gate not satisfied; WENDY_COLLECT_DMESG alone is insufficient"),
			)
		} else {
			logger.Info("kernel dmesg collection enabled", zap.String("source", "/dev/kmsg"))
			wg.Add(1)
			go func() {
				defer wg.Done()
				services.CollectDmesgLogs(ctx, logger, broadcaster)
			}()
		}
	} else {
		logger.Info("kernel dmesg collection disabled (set WENDY_COLLECT_DMESG=true to enable)")
	}

	// mTLS organization-equality enforcement mode. Read once here so the
	// startMTLSServer closure can capture it. The default (empty value) is grace,
	// which enforces org-equality for certs that carry an org identity but allows
	// legacy certs without one — easing migration before cert rotation completes.
	orgMode, ok := interceptor.ParseOrgMode(os.Getenv("WENDY_MTLS_ORG_ENFORCEMENT"))
	if !ok {
		logger.Warn("WENDY_MTLS_ORG_ENFORCEMENT has unrecognised value; defaulting to grace",
			zap.String("value", os.Getenv("WENDY_MTLS_ORG_ENFORCEMENT")))
	}
	logger.Info("mTLS org enforcement mode", zap.String("mode", orgMode.String()))

	// Main agent gRPC server port.
	agentPort := defaultAgentPort
	if p := os.Getenv("WENDY_AGENT_PORT"); p != "" {
		agentPort = p
	}

	// mtlsPortNum is agentPort+1; computed here so startTunnelBroker can capture it.
	agentPortNum, err := strconv.Atoi(agentPort)
	if err != nil {
		logger.Fatal("Invalid agent port", zap.String("port", agentPort), zap.Error(err))
	}
	mtlsPortNum := agentPortNum + 1

	// startTunnelBroker launches the tunnel broker presence loop in the background.
	// ProvisioningInfo() is called inside the goroutine to avoid re-entering the
	// provisioning mutex when called from the OnProvisioned callback.
	startTunnelBroker := func() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cloudHost, orgID, assetID, enrolled := provisioningSvc.ProvisioningInfo()
			if !enrolled {
				return
			}
			brokerURL := os.Getenv("WENDY_BROKER_URL")
			if brokerURL == "" {
				brokerURL = brokerURLForCloudHost(cloudHost)
			}
			_, chainPEM, _ := provisioningSvc.ProvisioningCerts()
			if chainPEM == "" {
				logger.Warn("CA chain PEM unavailable; cannot start tunnel broker (re-provision if this persists)")
				return
			}
			client := services.NewTunnelBrokerClient(logger, brokerURL, orgID, assetID, chainPEM, mtlsPortNum)
			client.Run(ctx)
		}()
	}

	var mtlsServer *grpc.Server
	var mtlsMu sync.Mutex

	registerAllServices := func(srv *grpc.Server) {
		agentpb.RegisterWendyAgentServiceServer(srv, agentSvc)
		agentpb.RegisterWendyContainerServiceServer(srv, containerSvc)
		agentpb.RegisterWendyAudioServiceServer(srv, audioSvc)
		agentpb.RegisterWendyVideoServiceServer(srv, videoSvc)
		agentpb.RegisterWendyProvisioningServiceServer(srv, provisioningSvc)
		agentpb.RegisterWendyTelemetryServiceServer(srv, telemetrySvc)
		agentpbv2.RegisterWendyDeviceInfoServiceServer(srv, deviceInfoSvc)
		agentpbv2.RegisterWendyTimeSyncServiceServer(srv, timeSyncSvc)
		agentpbv2.RegisterWendyWiFiServiceServer(srv, wifiSvc)
		agentpbv2.RegisterWendyBluetoothServiceServer(srv, bluetoothSvc)
		agentpbv2.RegisterWendyAgentUpdateServiceServer(srv, agentUpdateSvc)
		agentpbv2.RegisterWendyOSUpdateServiceServer(srv, osUpdateSvc)
		agentpbv2.RegisterWendyContainerServiceServer(srv, containerSvcV2)
		agentpbv2.RegisterWendyProvisioningServiceServer(srv, provisioningSvcV2)
		agentpbv2.RegisterWendyAudioServiceServer(srv, audioSvcV2)
		agentpbv2.RegisterWendyTelemetryServiceServer(srv, telemetrySvcV2)
		if ros2Svc != nil {
			agentpbv2.RegisterROS2ServiceServer(srv, ros2Svc)
		}
	}

	startMTLSServer := func(certPEM, chainPEM, keyPEM string) {
		mtlsMu.Lock()
		defer mtlsMu.Unlock()

		if mtlsServer != nil {
			logger.Warn("mTLS server already running, skipping")
			return
		}

		floor := certNotBeforeFloor(certPEM)
		if floor.IsZero() && certPEM != "" {
			logger.Warn("Could not extract NotBefore from provisioning cert — NTP clock skew protection is disabled")
		} else if now := time.Now(); !floor.IsZero() && now.Before(floor) {
			logger.Warn("Device clock predates provisioning cert — using cert NotBefore as mTLS time floor; clock will sync when network is available",
				zap.Time("deviceClock", now),
				zap.Time("floor", floor),
				zap.Duration("clockBehindBy", floor.Sub(now)),
			)
		}

		// Derive this device's own organization from its leaf certificate so the
		// mTLS interceptor can enforce org-equality. We deliberately derive from
		// certPEM (the device's own leaf) rather than provisioningSvc.ProvisioningInfo():
		// startMTLSServer is also invoked from inside the OnProvisioned callback,
		// where taking the provisioning mutex would risk re-entrancy (see the comment
		// at the startTunnelBroker closure). Both call sites already pass certPEM.
		expectedOrg, haveOrg := deviceOrgFromCertPEM(certPEM)
		effectiveMode := orgMode
		if orgMode != interceptor.OrgModeOff && !haveOrg {
			// Fail safe: the device cannot determine its own org, so it cannot
			// meaningfully compare a client's org against it. Rather than brick the
			// device (rejecting all clients) or silently enforce against an unknown
			// self-org, disable enforcement for this server and log loudly.
			logger.Error("cannot determine device organization from own certificate; mTLS org enforcement DISABLED for this server",
				zap.String("configuredMode", orgMode.String()))
			effectiveMode = interceptor.OrgModeOff
		}
		if effectiveMode != interceptor.OrgModeOff {
			logger.Info("mTLS server enforcing org",
				zap.Int32("org", expectedOrg),
				zap.String("mode", effectiveMode.String()))
		}

		srv, err := mtls.NewServer(certPEM, chainPEM, keyPEM, logger, floor, expectedOrg, effectiveMode,
			// UnaryMTLSInterceptor and StreamMTLSInterceptor are embedded inside
			// mtls.NewServer and run before these caller-provided interceptors.
			grpc.ChainUnaryInterceptor(interceptor.UnaryErrorInterceptor(logger)),
			grpc.ChainStreamInterceptor(interceptor.StreamErrorInterceptor(logger)),
		)
		if err != nil {
			logger.Error("Failed to create mTLS server", zap.Error(err))
			return
		}

		// Register all services on the mTLS server.
		registerAllServices(srv)

		// Compute mTLS port = agentPort + 1.
		portNum, err := strconv.Atoi(agentPort)
		if err != nil {
			logger.Error("Failed to parse agent port for mTLS", zap.String("port", agentPort), zap.Error(err))
			return
		}
		mtlsPort := strconv.Itoa(portNum + 1)

		lis, err := net.Listen("tcp", "[::]:"+mtlsPort)
		if err != nil {
			logger.Error("Failed to listen on mTLS port", zap.String("port", mtlsPort), zap.Error(err))
			return
		}

		mtlsServer = srv

		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Info("mTLS gRPC server listening", zap.String("port", mtlsPort))
			if err := srv.Serve(lis); err != nil {
				logger.Error("mTLS gRPC server error", zap.Error(err))
			}
		}()
	}

	// Only called after provisioning so the cert is available.
	startBLEPeripheral := func(certPEM, chainPEM, keyPEM string) {
		tlsConfig, err := mtls.NewTLSConfig(certPEM, chainPEM, keyPEM, logger, certNotBeforeFloor(certPEM))
		if err != nil {
			logger.Error("Failed to build BLE TLS config", zap.Error(err))
			return
		}
		bluetooth.StartBLEPeripheral(ctx, logger, bleDispatcher, tlsConfig)
	}

	// Check if already provisioned and start mTLS server and tunnel broker if certificates exist.
	certPEM, chainPEM, keyData := provisioningSvc.ProvisioningCerts()
	keyPEM := string(keyData)
	for i := range keyData {
		keyData[i] = 0
	}
	alreadyProvisioned := certPEM != "" && keyPEM != ""

	if alreadyProvisioned {
		startMTLSServer(certPEM, chainPEM, keyPEM)
		startTunnelBroker()
		configpartition.UpdateAvahiForProvisioning(logger, mtlsPortNum)
		startBLEPeripheral(certPEM, chainPEM, keyPEM)
	}

	// Start the embedded dev container registry (Linux only, best-effort).
	// If already provisioned, start immediately with HTTPS; otherwise HTTP until provisioned.
	if runtime.GOOS == "linux" && ctrdErr == nil {
		if alreadyProvisioned {
			startRegistry(registryTLSConfig(certPEM, chainPEM, keyPEM))
		} else {
			startRegistry(nil)
		}
	}

	// Plaintext gRPC server — only needed until the device is provisioned.
	// Once provisioned, the mTLS server handles remote gRPC traffic and this
	// plaintext port is shut down. The local unix socket (/run/wendy/agent.sock)
	// remains active for on-device containers with the admin entitlement.
	var agentServer *grpc.Server
	if !alreadyProvisioned {
		agentServer = grpc.NewServer(
			grpc.UnaryInterceptor(interceptor.UnaryErrorInterceptor(logger)),
			grpc.StreamInterceptor(interceptor.StreamErrorInterceptor(logger)),
			grpc.InitialWindowSize(8*1024*1024),
			grpc.InitialConnWindowSize(16*1024*1024),
			grpc.KeepaliveParams(keepalive.ServerParameters{
				MaxConnectionIdle: 5 * time.Minute,
				Time:              30 * time.Second,
				Timeout:           10 * time.Second,
			}),
			grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
				MinTime:             10 * time.Second,
				PermitWithoutStream: true,
			}),
		)
		registerAllServices(agentServer)

		agentLis, err := net.Listen("tcp", "[::]:"+agentPort)
		if err != nil {
			logger.Fatal("Failed to listen on agent port", zap.String("port", agentPort), zap.Error(err))
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Info("Agent gRPC server listening", zap.String("port", agentPort))
			if err := agentServer.Serve(agentLis); err != nil {
				logger.Error("Agent gRPC server error", zap.Error(err))
			}
		}()
	}

	// Local control socket: the agent's full gRPC over a unix socket with NO
	// mTLS. Access is gated solely by the admin entitlement (oci.applyAdmin
	// bind-mounts this socket into entitled containers). Disabled with
	// WENDY_LOCAL_SOCKET=off.
	var localSocketServer *grpc.Server
	if os.Getenv("WENDY_LOCAL_SOCKET") != "off" {
		localSocketServer = grpc.NewServer(
			grpc.UnaryInterceptor(interceptor.UnaryErrorInterceptor(logger)),
			grpc.StreamInterceptor(interceptor.StreamErrorInterceptor(logger)),
		)
		registerAllServices(localSocketServer)

		// oci.AdminAgentSocketHostPath is the single source of truth for this
		// path: the admin entitlement bind-mounts its parent directory into
		// containers (see oci.applyAdmin), so the path we serve and the path we
		// mount must never drift. It lives on the disk-backed /var/lib/wendy
		// tree, not tmpfs /run, so its directory inode survives reboots — a /run
		// path was wiped on every boot and stranded long-lived admin containers
		// on the orphaned pre-reboot inode (in-container socket read as ENOENT).
		localSocketPath := oci.AdminAgentSocketHostPath
		localLis, err := localsocket.Listen(localSocketPath)
		if err != nil {
			logger.Error("Failed to listen on local control socket", zap.Error(err))
		} else {
			wg.Add(1)
			go func() {
				defer wg.Done()
				logger.Info("Agent local control socket listening", zap.String("path", localSocketPath))
				if err := localSocketServer.Serve(localLis); err != nil {
					logger.Error("Local control socket server error", zap.Error(err))
				}
			}()
		}
	}

	// Set up the provisioning callback to start the mTLS server, shut down
	// the plaintext server, and switch the registry to HTTPS.
	provisioningSvc.OnProvisioned = func(certPEM, chainPEM string, keyData []byte) {
		defer func() {
			for i := range keyData {
				keyData[i] = 0
			}
		}()
		keyPEM := string(keyData)
		startMTLSServer(certPEM, chainPEM, keyPEM)
		startTunnelBroker()
		configpartition.UpdateAvahiForProvisioning(logger, mtlsPortNum)
		startBLEPeripheral(certPEM, chainPEM, keyPEM)
		if agentServer != nil {
			logger.Info("Device provisioned — shutting down plaintext gRPC port", zap.String("port", agentPort))
			go agentServer.GracefulStop()
		}
		if runtime.GOOS == "linux" && ctrdErr == nil {
			go startRegistry(registryTLSConfig(certPEM, chainPEM, keyPEM))
		}
	}

	// Set up the unprovisioning callback: revert the mDNS advertisement to the
	// plaintext port and exit so systemd restarts the agent into unprovisioned
	// mode. A clean restart is simpler and more reliable than tearing down the
	// mTLS server, tunnel broker, BLE peripheral, and HTTPS registry in place.
	provisioningSvc.OnUnprovisioned = func() {
		configpartition.UpdateAvahiForUnprovisioning(logger, agentPortNum)
		logger.Info("Device unprovisioned — restarting agent into unprovisioned mode")
		os.Exit(0)
	}

	// Self-enroll from a token staged by agent.sh (Linux Desktop install).
	// Best-effort and non-blocking: a cloud outage must never delay the agent
	// coming up locally (mDNS discovery still works unenrolled).
	go provisioningSvc.ApplyEnrollmentFile(context.Background())

	otelPort := defaultOTELPort
	if p := os.Getenv("WENDY_OTEL_PORT"); p != "" {
		otelPort = p
	}

	otelServer := grpc.NewServer(
		grpc.UnaryInterceptor(interceptor.UnaryErrorInterceptor(logger)),
		grpc.StreamInterceptor(interceptor.StreamErrorInterceptor(logger)),
		grpc.InitialWindowSize(8*1024*1024),
		grpc.InitialConnWindowSize(16*1024*1024),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle: 5 * time.Minute,
			Time:              30 * time.Second,
			Timeout:           10 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	otelpb.RegisterLogsServiceServer(otelServer, otelLogReceiver)
	otelpb.RegisterMetricsServiceServer(otelServer, otelMetricReceiver)
	otelpb.RegisterTraceServiceServer(otelServer, otelTraceReceiver)

	otelLis, err := listenDualStackLoopback(otelPort)
	if err != nil {
		logger.Fatal("Failed to listen on OTEL port", zap.String("port", otelPort), zap.Error(err))
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Info("OTEL gRPC receiver listening", zap.String("port", otelPort))
		if err := otelServer.Serve(otelLis); err != nil {
			logger.Error("OTEL gRPC server error", zap.Error(err))
		}
	}()

	otelHTTPPort := defaultOTELHTTPPort
	if p := os.Getenv("WENDY_OTEL_HTTP_PORT"); p != "" {
		otelHTTPPort = p
	}

	otelHTTPReceiver := services.NewOTELHTTPReceiver(logger, telemetryBuf)
	otelHTTPLis, err := listenDualStackLoopback(otelHTTPPort)
	if err != nil {
		logger.Fatal("Failed to listen on OTEL HTTP port", zap.String("port", otelHTTPPort), zap.Error(err))
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Info("OTEL HTTP receiver listening", zap.String("port", otelHTTPPort))
		if err := otelHTTPReceiver.Serve(otelHTTPLis); err != nil && err != http.ErrServerClosed {
			logger.Error("OTEL HTTP server error", zap.Error(err))
		}
	}()

	cloudFlusher := services.NewCloudFlusherWithProvisioning(logger, telemetryBuf, provisioningSvc)
	if telemetryBuf.DiskEnabled() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cloudFlusher.Run(ctx)
		}()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	logger.Info("Received signal, shutting down", zap.String("signal", sig.String()))

	cancel()
	if agentServer != nil {
		agentServer.GracefulStop()
	}
	if localSocketServer != nil {
		localSocketServer.GracefulStop()
	}
	otelServer.GracefulStop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := otelHTTPReceiver.Shutdown(shutdownCtx); err != nil {
		logger.Error("OTEL HTTP server shutdown error", zap.Error(err))
	}

	mtlsMu.Lock()
	if mtlsServer != nil {
		mtlsServer.GracefulStop()
	}
	mtlsMu.Unlock()

	wg.Wait()

	logger.Info("wendy-agent stopped")
}

// certNotBeforeFloor parses the device's own provisioning cert and returns its
// NotBefore time to use as a lower bound on time.Now() during mTLS cert
// verification. This lets the device accept legitimate client certs even when
// the system clock has not yet been synchronised via NTP (e.g. after a power
// cycle without WiFi). Returns a zero time.Time if the cert cannot be parsed.
func certNotBeforeFloor(certPEM string) time.Time {
	if certPEM == "" {
		return time.Time{}
	}
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return time.Time{}
	}
	// ML-DSA certs from pki-core have trailing ASN.1 bytes that cause
	// x509.ParseCertificate to fail. Strip them with the same fallback
	// used elsewhere in this repo (e.g. internal/agent/mtls/mldsa_verify.go).
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		var raw asn1.RawValue
		if _, asn1Err := asn1.Unmarshal(block.Bytes, &raw); asn1Err == nil {
			cert, err = x509.ParseCertificate(raw.FullBytes)
		}
	}
	if err != nil {
		return time.Time{}
	}
	return cert.NotBefore
}

// deviceOrgFromCertPEM parses the device's own leaf certificate (ML-DSA aware,
// mirroring certNotBeforeFloor) and extracts its organization ID via
// certs.OrgFromClientCert. It returns (org, true) when an org identity is present
// and valid, and (0, false) on any parse/extract error or when the cert carries no
// org identity. The caller treats (0, false) as "device org unknown".
func deviceOrgFromCertPEM(certPEM string) (int32, bool) {
	if certPEM == "" {
		return 0, false
	}
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return 0, false
	}
	// ML-DSA certs from pki-core have trailing ASN.1 bytes that cause
	// x509.ParseCertificate to fail. Strip them with the same fallback used by
	// certNotBeforeFloor and internal/agent/mtls/mldsa_verify.go.
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		var raw asn1.RawValue
		if _, asn1Err := asn1.Unmarshal(block.Bytes, &raw); asn1Err == nil {
			cert, err = x509.ParseCertificate(raw.FullBytes)
		}
	}
	if err != nil {
		return 0, false
	}
	org, hasOrg, err := certs.OrgFromClientCert(cert)
	if err != nil || !hasOrg {
		return 0, false
	}
	return org, true
}

func brokerURLForCloudHost(cloudHost string) string {
	host, port, err := net.SplitHostPort(cloudHost)
	if err == nil {
		if port == "443" {
			return cloudHost
		}
		return net.JoinHostPort(host, "50052")
	}
	return net.JoinHostPort(cloudHost, "50052")
}

func handleUtilityCommand(args []string) (bool, int) {
	if len(args) == 0 {
		return false, 0
	}

	if args[0] == "--version" || args[0] == "-v" {
		fmt.Println(version.Version)
		return true, 0
	}

	if args[0] != "utils" {
		return false, 0
	}

	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: wendy-agent utils open-browser <url>")
		return true, 2
	}
	if args[1] != "open-browser" {
		return false, 0
	}

	if len(args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: wendy-agent utils open-browser <url>")
		return true, 2
	}

	rawURL := args[2]
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid URL %q: %v\n", rawURL, err)
		return true, 2
	}
	if parsed.Scheme == "" {
		fmt.Fprintf(os.Stderr, "invalid URL %q: missing scheme (e.g. http:// or https://)\n", rawURL)
		return true, 2
	}
	if (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host == "" {
		fmt.Fprintf(os.Stderr, "invalid URL %q: must include a host (e.g. http://localhost:3000)\n", rawURL)
		return true, 2
	}

	if err := browseropen.Open(rawURL); err != nil {
		fmt.Fprintf(os.Stderr, "Could not open browser: %v\n", err)
		fmt.Println(rawURL)
		return true, 0
	}

	fmt.Printf("Opening %s in default browser...\n", rawURL)
	return true, 0
}
