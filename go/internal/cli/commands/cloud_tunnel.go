package commands

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/wendylabsinc/wendy/go/internal/cli/clouddefaults"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

const (
	defaultBrokerPort = "50052"
	maxCloudAssets    = 10_000

	// cloudKeepalivePing is how often the client sends an HTTP/2 keepalive
	// ping over the tunnel. It must stay >= the agent's MinTime enforcement
	// policy (10s) and frequent enough to keep the tunnel/NAT warm.
	cloudKeepalivePing = 30 * time.Second
	// cloudKeepaliveACKTimeout is how long to wait for a keepalive ACK before
	// declaring the connection dead. It is generous because long OS-update
	// streams run while the device is saturated (artifact download + mender
	// install), and a busy device can take well over the usual 10s to ACK a
	// ping; a tighter window tears down the stream mid-install.
	cloudKeepaliveACKTimeout = 20 * time.Second
)

type closeFunc func()

func (f closeFunc) Close() error {
	f()
	return nil
}

func certXFCC(cert config.CertificateInfo) string {
	if cert.UserID != "" {
		return fmt.Sprintf("URI=urn:wendy:org:%d:user:%s", cert.OrganizationID, cert.UserID)
	}
	if cert.AssetID != 0 {
		return fmt.Sprintf("URI=urn:wendy:org:%d:asset:%d", cert.OrganizationID, cert.AssetID)
	}
	return ""
}

func cloudContext(ctx context.Context, auth *config.AuthConfig) context.Context {
	if len(auth.Certificates) == 0 {
		return ctx
	}
	cert := auth.Certificates[0]
	md := metadata.MD{}
	if auth.APIKey != "" {
		md.Set("authorization", "Bearer "+auth.APIKey)
	}
	certHeader := certXFCC(cert)
	if certHeader != "" {
		md.Set("x-wendy-client-cert", certHeader)
		md.Set("x-forwarded-client-cert", certHeader)
	}
	return metadata.NewOutgoingContext(ctx, md)
}

func connectToCloudAgent(ctx context.Context, cloudGRPC, deviceName, brokerURL string) (*grpcclient.AgentConnection, error) {
	auth, err := pickAuthEntry(cloudGRPC)
	if err != nil {
		return nil, err
	}

	asset, err := pickCloudDevice(ctx, auth, deviceName, brokerURL)
	if err != nil {
		return nil, err
	}
	cliLogln("Connecting to %s via cloud tunnel...", asset.GetName())

	return connectCloudAsset(ctx, auth, asset, brokerURL)
}

func connectCloudAsset(ctx context.Context, auth *config.AuthConfig, asset *cloudpb.Asset, brokerURL string) (*grpcclient.AgentConnection, error) {
	brokerConn, err := dialCloudBroker(auth, brokerURL)
	if err != nil {
		return nil, err
	}

	cleanupBroker := true
	defer func() {
		if cleanupBroker {
			_ = brokerConn.Close()
		}
	}()

	// Provisioned agents serve mTLS on agentPort+1 (50052) for remote clients; the
	// plaintext port (50051) is shut down after provisioning. (On-device containers
	// with the admin entitlement can reach the agent via the local unix socket.)
	tunnelConn, err := openBrokerTunnel(ctx, brokerConn, auth, asset.GetId(), defaultAgentPort+1)
	if err != nil {
		return nil, fmt.Errorf("opening cloud tunnel to %s: %w", asset.GetName(), err)
	}

	dialOpt, closeTunnel := tunnelDialer(tunnelConn)

	cert := auth.Certificates[0]
	x509Cert, err := tls.X509KeyPair([]byte(cert.PemCertificate), []byte(cert.PemPrivateKey))
	if err != nil {
		closeTunnel()
		return nil, fmt.Errorf("loading agent mTLS cert: %w", err)
	}
	verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
		ChainPEM:      cert.PemCertificateChain,
		ExpectedOrgID: int32(cert.OrganizationID),
	})
	if err != nil {
		closeTunnel()
		return nil, fmt.Errorf("building TLS verifier: %w", err)
	}
	tlsCfg := &tls.Config{
		Certificates:       []tls.Certificate{x509Cert},
		InsecureSkipVerify: true, //nolint:gosec — hostname bypass only; VerifyConnection validates server cert against Wendy PKI
		VerifyConnection:   verifyConn,
		MinVersion:         tls.VersionTLS12,
	}

	grpcConn, err := grpc.NewClient(
		"passthrough:///cloud-tunnel",
		dialOpt,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithInitialWindowSize(8*1024*1024),
		grpc.WithInitialConnWindowSize(16*1024*1024),
		grpc.WithReadBufferSize(256*1024),
		grpc.WithWriteBufferSize(256*1024),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                cloudKeepalivePing,
			Timeout:             cloudKeepaliveACKTimeout,
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		closeTunnel()
		return nil, fmt.Errorf("creating tunnelled gRPC connection: %w", err)
	}

	agentConn := grpcclient.NewFromConn(grpcConn)
	agentConn.Host = asset.GetName()
	agentConn.IsMTLS = true
	agentConn.CertInfo = &cert
	agentConn.RegistryDialer = func(ctx context.Context, port int) (net.Conn, error) {
		return openBrokerTunnel(ctx, brokerConn, auth, asset.GetId(), uint32(port))
	}
	// Pin reconnect to this exact asset (by id) so a post-restart reconnect
	// can't drift to a different cloud device — the asset name may be empty or
	// ambiguous, and re-running device discovery while the agent is mid-restart
	// can match whichever other device happens to be reachable.
	agentConn.Reconnect = func(rctx context.Context) (*grpcclient.AgentConnection, error) {
		return waitForCloudAgentRestart(rctx, auth, asset, brokerURL)
	}
	agentConn.ExtraClosers = append(agentConn.ExtraClosers, closeFunc(closeTunnel), brokerConn)
	cleanupBroker = false
	return agentConn, nil
}

func waitForCloudAgentRestart(ctx context.Context, auth *config.AuthConfig, asset *cloudpb.Asset, brokerURL string) (*grpcclient.AgentConnection, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	restartErr := func() error {
		return fmt.Errorf("timed out waiting for %s (id=%d) to restart", asset.GetName(), asset.GetId())
	}
	// Give the agent a moment to begin shutdown.
	select {
	case <-time.After(time.Second):
	case <-ctx.Done():
		return nil, restartErr()
	}
	for {
		select {
		case <-ctx.Done():
			return nil, restartErr()
		default:
		}
		attemptCtx, attemptCancel := context.WithTimeout(ctx, 10*time.Second)
		conn, err := connectCloudAsset(attemptCtx, auth, asset, brokerURL)
		if err != nil {
			attemptCancel()
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				return nil, restartErr()
			}
			continue
		}
		probeCtx, probeCancel := context.WithTimeout(ctx, 3*time.Second)
		_, probeErr := conn.AgentService.GetAgentVersion(probeCtx, &agentpb.GetAgentVersionRequest{})
		probeCancel()
		attemptCancel()
		if probeErr == nil {
			return conn, nil
		}
		conn.Close()
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return nil, restartErr()
		}
	}
}

func dialCloudBroker(auth *config.AuthConfig, brokerURL string) (*grpc.ClientConn, error) {
	brokerURL = clouddefaults.BrokerURL(auth.CloudGRPC, brokerURL, defaultBrokerPort)

	if len(auth.Certificates) == 0 {
		return nil, fmt.Errorf("auth entry has no certificates; re-run 'wendy auth login'")
	}
	cert := auth.Certificates[0]
	tlsCfg, err := certs.LoadTLSConfig(
		cert.PemCertificate,
		cert.PemCertificateChain,
		cert.PemPrivateKey,
		"",
	)
	if err != nil {
		return nil, fmt.Errorf("loading broker TLS config: %w", err)
	}

	if !strings.HasSuffix(brokerURL, ":443") {
		// For non-standard ports (local/on-prem broker) the server presents a cert
		// signed by the Wendy CA, not a public CA. Skip hostname verification but
		// validate the chain against the stored Wendy CA bundle.
		caPool := x509.NewCertPool()
		if !caPool.AppendCertsFromPEM([]byte(cert.PemCertificateChain)) {
			return nil, fmt.Errorf("no valid CA certificates in PemCertificateChain")
		}
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // Hostname verification is intentionally skipped for non-standard broker endpoints; VerifyConnection validates the chain against the Wendy CA.
		tlsCfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("broker presented no TLS certificate")
			}
			intermediates := x509.NewCertPool()
			for _, c := range cs.PeerCertificates[1:] {
				intermediates.AddCert(c)
			}
			_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{
				Roots:         caPool,
				Intermediates: intermediates,
				KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			return err
		}
	}

	conn, err := grpc.NewClient(brokerURL,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithInitialWindowSize(8*1024*1024),
		grpc.WithInitialConnWindowSize(16*1024*1024),
		grpc.WithReadBufferSize(256*1024),
		grpc.WithWriteBufferSize(256*1024),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                cloudKeepalivePing,
			Timeout:             cloudKeepaliveACKTimeout,
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("connecting to broker at %s: %w", brokerURL, err)
	}
	return conn, nil
}

func openBrokerTunnel(ctx context.Context, brokerConn *grpc.ClientConn, auth *config.AuthConfig, assetID int32, remotePort uint32) (net.Conn, error) {
	client := cloudpb.NewTunnelBrokerServiceClient(brokerConn)

	stream, err := client.ClientTunnel(cloudContext(ctx, auth))
	if err != nil {
		return nil, fmt.Errorf("opening tunnel stream: %w", err)
	}

	if err := stream.Send(&cloudpb.ClientTunnelMessage{
		Content: &cloudpb.ClientTunnelMessage_Open{
			Open: &cloudpb.ClientTunnelOpen{
				AssetId: assetID,
				Host:    "localhost",
				Port:    remotePort,
			},
		},
	}); err != nil {
		return nil, fmt.Errorf("sending tunnel open: %w", err)
	}

	local, remote := net.Pipe()

	go func() {
		defer remote.Close()
		for {
			msg, err := stream.Recv()
			if err != nil {
				if tlsDebug := os.Getenv("WENDY_TLS_DEBUG") != ""; tlsDebug {
					fmt.Fprintf(os.Stderr, "[tunnel-debug] broker stream closed: %v\n", err)
				}
				break
			}
			if len(msg.Payload) > 0 {
				if _, err := remote.Write(msg.Payload); err != nil {
					break
				}
			}
			if msg.HalfClose {
				break
			}
		}
	}()

	go func() {
		buf := make([]byte, 256*1024)
		for {
			n, readErr := remote.Read(buf)
			if n > 0 {
				payload := make([]byte, n)
				copy(payload, buf[:n])
				if err := stream.Send(&cloudpb.ClientTunnelMessage{
					Content: &cloudpb.ClientTunnelMessage_Data{
						Data: &cloudpb.TunnelData{Payload: payload},
					},
				}); err != nil {
					break
				}
			}
			if readErr != nil {
				if readErr == io.EOF {
					_ = stream.Send(&cloudpb.ClientTunnelMessage{
						Content: &cloudpb.ClientTunnelMessage_Data{
							Data: &cloudpb.TunnelData{HalfClose: true},
						},
					})
				}
				break
			}
		}
		_ = stream.CloseSend()
	}()

	return local, nil
}

func fetchCloudAssets(ctx context.Context, auth *config.AuthConfig) ([]*cloudpb.Asset, error) {
	return fetchCloudAssetsFiltered(ctx, auth, true)
}

func resolveCloudAsset(assets []*cloudpb.Asset, deviceName string) (*cloudpb.Asset, error) {
	if len(assets) == 0 {
		return nil, fmt.Errorf("no enrolled devices found for this org; enroll a device with 'wendy device enroll' first")
	}
	if deviceName != "" {
		lower := strings.ToLower(deviceName)
		var matched *cloudpb.Asset
		for _, a := range assets {
			if strings.ToLower(a.GetName()) == lower {
				if matched != nil {
					return nil, fmt.Errorf("multiple devices match %q; use a more specific name", deviceName)
				}
				matched = a
			}
		}
		if matched != nil {
			return matched, nil
		}
		// Numeric asset-id fallback: allows targeting unnamed devices.
		if id, err := strconv.Atoi(strings.TrimSpace(deviceName)); err == nil {
			for _, a := range assets {
				if a.GetId() == int32(id) {
					return a, nil
				}
			}
		}
		return nil, fmt.Errorf("no device named or with id %q found; run 'wendy cloud discover --json' to list ids", deviceName)
	}
	if len(assets) == 1 {
		return assets[0], nil
	}
	var b strings.Builder
	for i, a := range assets {
		if i > 0 {
			b.WriteString(", ")
		}
		name := a.GetName()
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(&b, "%d=%s", a.GetId(), name)
	}
	return nil, fmt.Errorf("multiple cloud devices found; rerun with --device <id|name> (%s)", b.String())
}

func pickCloudDevice(ctx context.Context, auth *config.AuthConfig, deviceName, brokerURL string) (*cloudpb.Asset, error) {
	if len(auth.Certificates) == 0 {
		return nil, fmt.Errorf("auth entry has no certificates; re-run 'wendy auth login'")
	}

	var assets []*cloudpb.Asset
	if isInteractiveTerminal() {
		prog := tui.NewProgressProgram(tui.NewSpinner("Fetching devices from cloud..."))
		var fetchErr error
		go func() {
			assets, fetchErr = fetchCloudAssets(ctx, auth)
			prog.Send(tui.SpinnerDoneMsg{})
		}()
		finalModel, err := prog.Run()
		if err != nil {
			return nil, fmt.Errorf("spinner: %w", err)
		}
		if sm, ok := finalModel.(tui.SpinnerModel); ok && !sm.Done() {
			return nil, ErrUserCancelled
		}
		if fetchErr != nil {
			return nil, fetchErr
		}
	} else {
		var err error
		assets, err = fetchCloudAssets(ctx, auth)
		if err != nil {
			return nil, err
		}
	}

	// When running interactively with no --device and multiple assets, skip
	// resolveCloudAsset (which now returns an enumerated error) and fall
	// straight through to the interactive picker.
	if isInteractiveTerminal() && deviceName == "" && len(assets) > 1 {
		// fall through to picker below
	} else {
		asset, err := resolveCloudAsset(assets, deviceName)
		if err != nil || asset != nil {
			return asset, err
		}
	}

	m := newCloudDiscoverModel(ctx, auth, brokerURL, false, true, assets)
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("device picker: %w", err)
	}
	cm := finalModel.(cloudDiscoverModel)
	if cm.quitting && cm.selected == nil {
		return nil, ErrUserCancelled
	}
	if cm.selected == nil {
		return nil, fmt.Errorf("no device selected")
	}
	return cm.selected, nil
}

func boolPtr(b bool) *bool { return &b }

func dialCloudGRPC(auth *config.AuthConfig) (*grpc.ClientConn, error) {
	if len(auth.Certificates) == 0 {
		return nil, fmt.Errorf("auth entry has no certificates; re-run 'wendy auth login'")
	}
	cert := auth.Certificates[0]
	var transport grpc.DialOption
	if strings.HasSuffix(auth.CloudGRPC, ":443") {
		tlsCfg, err := certs.LoadTLSConfig(
			cert.PemCertificate,
			cert.PemCertificateChain,
			cert.PemPrivateKey,
			"",
		)
		if err != nil {
			return nil, fmt.Errorf("loading TLS config: %w", err)
		}
		transport = grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg))
	} else {
		transport = grpc.WithTransportCredentials(insecure.NewCredentials())
	}
	conn, err := grpc.NewClient(auth.CloudGRPC,
		transport,
		grpc.WithInitialWindowSize(8*1024*1024),
		grpc.WithInitialConnWindowSize(16*1024*1024),
		grpc.WithReadBufferSize(256*1024),
		grpc.WithWriteBufferSize(256*1024),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                cloudKeepalivePing,
			Timeout:             cloudKeepaliveACKTimeout,
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("connecting to cloud: %w", err)
	}
	return conn, nil
}

func tunnelDialer(tunnelConn net.Conn) (grpc.DialOption, func()) {
	var once sync.Once
	return grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
		return tunnelConn, nil
	}), func() { once.Do(func() { tunnelConn.Close() }) }
}
