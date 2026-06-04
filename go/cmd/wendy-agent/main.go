package main

import (
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
	"github.com/wendylabsinc/wendy/go/internal/agent/mtls"
	agentnet "github.com/wendylabsinc/wendy/go/internal/agent/network"
	"github.com/wendylabsinc/wendy/go/internal/agent/registry"
	"github.com/wendylabsinc/wendy/go/internal/agent/services"
	"github.com/wendylabsinc/wendy/go/internal/shared/browseropen"
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

	// Wrap the logger so agent internal logs are published to the telemetry stream.
	telemetryCore := services.NewTelemetryCore(broadcaster, zapcore.DebugLevel)
	logger = zap.New(zapcore.NewTee(logger.Core(), telemetryCore))

	logger.Info("Starting wendy-agent", zap.String("version", version.Version))

	configPath := "/etc/wendyos"
	if envPath := os.Getenv("WENDY_CONFIG_PATH"); envPath != "" {
		configPath = envPath
	}

	// Migrate provisioning state from the legacy dir before anything reads it,
	// covering paths with no package hook (notably the in-place self-updater).
	services.MigrateLegacyConfigDir(logger, "/etc/wendy-agent", configPath)

	configpartition.Apply(logger, configPath)
	services.CommitMenderUpdate(logger)

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
		logger.Warn("xdg-dbus-proxy not found, Bluetooth containers will have unfiltered D-Bus access")
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

	logManager := services.NewContainerLogManager(logger, broadcaster)

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
	videoSvc := services.NewVideoService(logger)

	provisioningSvc := services.NewProvisioningService(logger, configPath)
	telemetrySvc := services.NewTelemetryService(logger, broadcaster)

	deviceInfoSvc := services.NewDeviceInfoService(logger, hwDiscoverer)
	wifiSvc := services.NewWiFiService(logger, networkMgr)
	bluetoothSvc := services.NewBluetoothService(logger, btManager)
	agentUpdateSvc := services.NewAgentUpdateService(logger, installer)
	osUpdateSvc := services.NewOSUpdateService(logger)
	containerSvcV2 := services.NewContainerServiceV2(containerSvc)
	provisioningSvcV2 := services.NewProvisioningServiceV2(provisioningSvc)
	audioSvcV2 := services.NewAudioServiceV2(audioSvc)
	telemetrySvcV2 := services.NewTelemetryServiceV2(logger, broadcaster)

	otelLogReceiver := services.NewOTELLogsReceiver(broadcaster)
	otelMetricReceiver := services.NewOTELMetricsReceiver(broadcaster)
	otelTraceReceiver := services.NewOTELTraceReceiver(broadcaster)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bleDispatcher := bluetooth.NewDispatcher(networkMgr, containerdClient, hwDiscoverer, btManager)

	// Returns nil if the PEM data is invalid, which causes the registry to stay HTTP.
	registryTLSConfig := func(certPEM, chainPEM, keyPEM string) *tls.Config {
		tlsConfig, err := mtls.NewTLSConfig(certPEM, chainPEM, keyPEM, nil)
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
	}

	if containerdClient != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			services.CollectContainerMetrics(ctx, containerdClient, broadcaster, logManager)
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		services.CollectAgentMetrics(ctx, broadcaster)
	}()

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
		agentpbv2.RegisterWendyWiFiServiceServer(srv, wifiSvc)
		agentpbv2.RegisterWendyBluetoothServiceServer(srv, bluetoothSvc)
		agentpbv2.RegisterWendyAgentUpdateServiceServer(srv, agentUpdateSvc)
		agentpbv2.RegisterWendyOSUpdateServiceServer(srv, osUpdateSvc)
		agentpbv2.RegisterWendyContainerServiceServer(srv, containerSvcV2)
		agentpbv2.RegisterWendyProvisioningServiceServer(srv, provisioningSvcV2)
		agentpbv2.RegisterWendyAudioServiceServer(srv, audioSvcV2)
		agentpbv2.RegisterWendyTelemetryServiceServer(srv, telemetrySvcV2)
	}

	startMTLSServer := func(certPEM, chainPEM, keyPEM string) {
		mtlsMu.Lock()
		defer mtlsMu.Unlock()

		if mtlsServer != nil {
			logger.Warn("mTLS server already running, skipping")
			return
		}

		// Warn if the device clock appears to predate the provisioning cert.
		// This almost always means an unsynchronized RTC, which causes every
		// client cert to appear "not yet valid" from the device's perspective.
		warnClockSkewIfNeeded(logger, certPEM)

		srv, err := mtls.NewServer(certPEM, chainPEM, keyPEM, logger,
			grpc.UnaryInterceptor(interceptor.UnaryErrorInterceptor(logger)),
			grpc.StreamInterceptor(interceptor.StreamErrorInterceptor(logger)),
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
		tlsConfig, err := mtls.NewTLSConfig(certPEM, chainPEM, keyPEM, logger)
		if err != nil {
			logger.Error("Failed to build BLE TLS config", zap.Error(err))
			return
		}
		bluetooth.StartBLEPeripheral(ctx, logger, bleDispatcher, tlsConfig)
	}

	certPEM, chainPEM, keyPEM := provisioningSvc.ProvisioningCerts()
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
	// Once provisioned the mTLS server handles all gRPC traffic and the plaintext
	// port is shut down so unprovisioned clients cannot access device services.
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

	provisioningSvc.OnProvisioned = func(certPEM, chainPEM, keyPEM string) {
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

	otelHTTPReceiver := services.NewOTELHTTPReceiver(logger, broadcaster)
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

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	logger.Info("Received signal, shutting down", zap.String("signal", sig.String()))

	cancel()
	if agentServer != nil {
		agentServer.GracefulStop()
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

// warnClockSkewIfNeeded parses the device's own provisioning cert and logs a
// warning if the system clock predates the cert's NotBefore. A clock that is
// behind the cert's issuance time will reject every legitimate client cert
// ("not yet valid"), making the mTLS port effectively unusable — but silently.
func warnClockSkewIfNeeded(logger *zap.Logger, certPEM string) {
	if certPEM == "" {
		return
	}
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return
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
		return
	}
	now := time.Now()
	if now.Before(cert.NotBefore) {
		logger.Warn("Device clock appears to predate the provisioning certificate — mTLS client cert verification will fail until NTP synchronizes the clock",
			zap.Time("deviceClock", now),
			zap.Time("certNotBefore", cert.NotBefore),
			zap.Duration("clockBehindBy", cert.NotBefore.Sub(now)),
		)
	}
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
