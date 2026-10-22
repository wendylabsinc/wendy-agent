package commands

import (
	"context"
	"crypto/tls"
	"fmt"
	"github.com/wendylabsinc/wendy/go/internal/cli/liteclient"
	"github.com/wendylabsinc/wendy/go/proto/gen/wcomrelaypb"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/providers"
	"github.com/wendylabsinc/wendy/go/internal/shared/cloudrelay"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	cloudpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
	"google.golang.org/grpc"
)

// liteCloudOSType identifies an asset enrolled as a Lite board. Such a board has no WendyOS agent to tunnel to: it is
// reached through Cloud's WendyCom relay instead.
const liteCloudOSType = "wendy-lite"

func init() {
	providers.CloudRelayDialer = dialLiteCloudRelay
	providers.LiteIdentityResolver = localLiteIdentity
	providers.LiteDeviceCAChainResolver = liteDeviceCAChain
}

// localLiteIdentity uses the session's primary credential for LAN and BLE. The
// P-256 Lite operator leaf belongs to the Cloud relay only.
func localLiteIdentity(ctx context.Context, auth *config.AuthConfig) (config.CertificateInfo, string, error) {
	if err := ctx.Err(); err != nil {
		return config.CertificateInfo{}, "", err
	}
	if auth == nil || len(auth.Certificates) == 0 {
		return config.CertificateInfo{}, "", fmt.Errorf("no operator certificate; run 'wendy auth login'")
	}
	if err := ensureFreshCertificateFn(ctx, auth); err != nil {
		return config.CertificateInfo{}, "", fmt.Errorf("%w; run 'wendy auth login' for this context", err)
	}
	cert, err := cloudrelay.LiteOperatorIdentity(auth.Certificates[0])
	if err != nil {
		return config.CertificateInfo{}, "", err
	}
	// An explicit client-identity override must not replace device trust.
	trust := auth.Certificates[0].PemCertificateChain
	if trust == "" {
		return config.CertificateInfo{}, "", fmt.Errorf("auth context has no device CA chain; run 'wendy auth login'")
	}
	return cert, trust, nil
}

// isLiteCloudAsset reports whether Cloud lists asset as a Wendy Lite board.
func isLiteCloudAsset(asset *cloudpbv2.Asset) bool {
	return strings.EqualFold(asset.GetOsType(), liteCloudOSType)
}

// cloudLiteSelectedDevice targets a Wendy Lite asset through the Cloud relay.
// Nothing is dialled here: like its other transports, the Lite provider
// connects when a command uses the device. selector is the asset's cloud://
// default.
func cloudLiteSelectedDevice(auth *config.AuthConfig, asset *cloudpbv2.Asset, selector string) (*SelectedDevice, error) {
	if auth == nil || auth.CloudGRPC == "" || len(auth.Certificates) == 0 || auth.Certificates[0].TenantUUID() == "" || asset.GetId() == "" {
		return nil, fmt.Errorf("a Wendy Lite cloud device requires a cloud endpoint, tenant and asset UUID")
	}
	provider := &providers.MicroWendyProvider{}
	return &SelectedDevice{
		External: &models.ExternalDevice{
			ID:          "wendy-lite:cloud:" + asset.GetId(),
			DisplayName: asset.GetName(),
			ProviderKey: provider.Key(),
			ConnectionInfo: map[string]string{
				"type":     "Cloud",
				"assetId":  asset.GetId(),
				"cloud":    auth.CloudGRPC,
				"tenantId": auth.Certificates[0].TenantUUID(),
				"deviceId": asset.GetPkiDeviceName(),
			},
			IsWendyDevice:   true,
			OS:              asset.GetOsType(),
			OSVersion:       asset.GetOsVersion(),
			CPUArchitecture: asset.GetArchitecture(),
		},
		Provider:        provider,
		DefaultSelector: selector,
	}, nil
}

// selectedAgent is the agent of a named target, for callers that need one. A
// Wendy Lite board picked through Cloud has none, so they refuse it rather
// than pass on a nil connection.
func selectedAgent(picked *SelectedDevice) (*grpcclient.AgentConnection, error) {
	if picked.Agent != nil {
		return picked.Agent, nil
	}
	if picked.External != nil {
		return nil, fmt.Errorf("%s is a Wendy Lite device; this command needs a WendyOS agent", picked.External.DisplayName)
	}
	return nil, fmt.Errorf("selected device has no WendyOS agent")
}

// dialLiteCloudRelay is the Lite provider's providers.CloudRelayDialer. It
// uses the login for the device's cloud and tenant, as a cloud:// selector
// does, and the same connection and call metadata as every other Cloud RPC,
// so the relay sees the user's session (DPoP when the token is key-bound).
func dialLiteCloudRelay(ctx context.Context, device models.ExternalDevice, renew bool) (*grpc.ClientConn, context.Context, liteclient.RelayOptions, error) {
	options := liteclient.RelayOptions{}
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, options, err
	}
	selector := cloudDeviceSelector{Endpoint: device.ConnectionInfo["cloud"], TenantUUID: device.ConnectionInfo["tenantId"]}
	if selector.Endpoint == "" || selector.TenantUUID == "" {
		return nil, nil, options, fmt.Errorf("Wendy Lite cloud device has no cloud endpoint or tenant")
	}
	auth, err := selector.auth(cfg)
	if err != nil {
		return nil, nil, options, err
	}
	options, err = liteRelayOptions(ctx, auth, renew)
	if err != nil {
		return nil, nil, options, err
	}
	options.Redial = func(ctx context.Context, endpoint string) (*grpc.ClientConn, context.Context, error) {
		// Let gRPC derive TLS's server name from the target. An explicit
		// hostname-only ServerName also replaces :authority, dropping the port
		// that the session's DPoP proof includes in its request URI.
		return dialLiteCloudBroker(ctx, auth, endpoint, &tls.Config{MinVersion: tls.VersionTLS12})
	}
	discovery, err := dialCloudGRPC(auth)
	if err != nil {
		return nil, nil, options, err
	}
	defer discovery.Close()
	cloudCtx, err := cloudContext(ctx, auth)
	if err != nil {
		return nil, nil, options, err
	}
	discoveryCtx, cancel := context.WithTimeout(cloudCtx, 30*time.Second)
	defer cancel()
	route, err := wcomrelaypb.NewWendyComRelayDiscoveryServiceClient(discovery).Resolve(discoveryCtx, &wcomrelaypb.ResolveRequest{AssetId: device.ConnectionInfo["assetId"]})
	if err != nil {
		return nil, nil, options, err
	}
	conn, callCtx, err := options.Redial(ctx, route.Endpoint)
	return conn, callCtx, options, err
}

func dialLiteCloudBroker(ctx context.Context, auth *config.AuthConfig, endpoint string, tlsConfig *tls.Config) (*grpc.ClientConn, context.Context, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, nil, fmt.Errorf("invalid Lite broker endpoint")
	}
	transport := grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig))
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !(u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())) {
			return nil, nil, fmt.Errorf("Lite broker endpoint must use HTTPS")
		}
		transport = grpc.WithTransportCredentials(insecure.NewCredentials())
	}
	target := u.Host
	if u.Port() == "" {
		target = net.JoinHostPort(u.Hostname(), "443")
	}
	local := *auth
	local.CloudGRPC = target
	callCtx, err := cloudContext(ctx, &local)
	if err != nil {
		return nil, nil, err
	}
	conn, err := grpc.NewClient(target, append([]grpc.DialOption{transport}, dpopDialOptions(&local)...)...)
	return conn, callCtx, err
}
