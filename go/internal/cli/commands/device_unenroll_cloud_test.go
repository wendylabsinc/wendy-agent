package commands

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/liteclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	cloudpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func preserveCloudUnenrollmentHooks(t *testing.T) {
	t.Helper()
	resolve, auth := resolveUnenrollTargetFn, resolveCloudUnenrollAuthFn
	interactive, confirm, fresh := isInteractiveTerminalFn, confirmDefaultNoFn, ensureFreshCertificateFn
	device, json := deviceFlag, jsonOutput
	t.Cleanup(func() {
		resolveUnenrollTargetFn, resolveCloudUnenrollAuthFn = resolve, auth
		isInteractiveTerminalFn, confirmDefaultNoFn, ensureFreshCertificateFn = interactive, confirm, fresh
		deviceFlag, jsonOutput = device, json
	})
	deviceFlag, jsonOutput = "", false
}

func TestCloudUnenrollTargetRoutesBeforeDeviceConnection(t *testing.T) {
	preserveCloudUnenrollmentHooks(t)
	scoped := cloudDeviceSelector{Endpoint: "cloud.example:443", TenantUUID: liteTestTenant, AssetUUID: liteTestAssetID}.String()
	for _, tc := range []struct {
		name, flag, defaultDevice, contextDevice, want string
		cloudOnly, cloudCommand, cloudMode             bool
	}{
		{name: "local name", flag: "desk", want: "desk"},
		{name: "cloud only name", flag: "desk", cloudOnly: true, want: "desk", cloudMode: true},
		{name: "cloud group name", flag: "desk", cloudCommand: true, want: "desk", cloudMode: true},
		{name: "cloud context selector", flag: "ignored", contextDevice: "offline-board", cloudCommand: true, want: "offline-board", cloudMode: true},
		{name: "explicit cloud selector", flag: scoped, want: scoped, cloudMode: true},
		{name: "saved cloud selector", defaultDevice: scoped, want: scoped, cloudMode: true},
		{name: "saved local selector", defaultDevice: "desk", want: "desk"},
		{name: "cloud group ignores local default", defaultDevice: "desk", cloudCommand: true, cloudMode: true},
		{name: "cloud only local default", defaultDevice: "desk", cloudOnly: true, want: "desk", cloudMode: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setTempConfig(t, &config.Config{DefaultDevice: tc.defaultDevice})
			deviceFlag = tc.flag
			ctx := context.Background()
			if tc.cloudCommand {
				ctx = context.WithValue(ctx, cloudDeviceContextKey{}, cloudDeviceConfig{DeviceName: tc.contextDevice})
			}
			cmd := &cobra.Command{}
			cmd.SetContext(ctx)
			got, cloud, err := cloudUnenrollTarget(cmd, tc.cloudOnly)
			if err != nil || got != tc.want || cloud != tc.cloudMode {
				t.Fatalf("got selector=%q cloud=%v err=%v, want %q %v", got, cloud, err, tc.want, tc.cloudMode)
			}
		})
	}
	deviceFlag = "cloud://broken"
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if _, _, err := cloudUnenrollTarget(cmd, true); err == nil {
		t.Fatal("malformed cloud selector accepted")
	}
}

func TestCloudUnenrollCommandDoesNotResolveDevice(t *testing.T) {
	preserveCloudUnenrollmentHooks(t)
	scoped := cloudDeviceSelector{Endpoint: "cloud.example:443", TenantUUID: liteTestTenant, AssetUUID: liteTestAssetID}.String()
	for _, tc := range []struct {
		name, flag, defaultDevice, endpoint string
		cloudOnly, cloudCommand             bool
	}{
		{name: "cloud only offline name", flag: "offline-board", cloudOnly: true},
		{name: "cloud group offline name", flag: "offline-board", cloudCommand: true},
		{name: "cloud group endpoint", flag: "offline-board", cloudCommand: true, endpoint: "other.example:443"},
		{name: "cloud default", defaultDevice: scoped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setTempConfig(t, &config.Config{DefaultDevice: tc.defaultDevice})
			deviceFlag = tc.flag
			resolveUnenrollTargetFn = func(context.Context, ...resolveOption) (*SelectedDevice, error) {
				t.Fatal("cloud removal attempted to resolve or connect to the device")
				return nil, nil
			}
			wantErr := errors.New("stop after cloud auth selection")
			called := false
			resolveCloudUnenrollAuthFn = func(endpoint, selector string) (*config.AuthConfig, string, error) {
				called = true
				wantSelector := tc.flag
				if wantSelector == "" {
					wantSelector = tc.defaultDevice
				}
				if endpoint != tc.endpoint || selector != wantSelector {
					t.Fatalf("cloud auth got endpoint=%q selector=%q, want %q %q", endpoint, selector, tc.endpoint, wantSelector)
				}
				return nil, "", wantErr
			}
			cmd := newDeviceUnenrollCmd()
			cmd.SetContext(context.Background())
			if tc.cloudOnly {
				if err := cmd.Flags().Set("cloud-only", "true"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.cloudCommand {
				wrapCloudDeviceCommands(cmd, func(*cobra.Command) cloudDeviceConfig {
					return cloudDeviceConfig{DeviceName: tc.flag, CloudGRPC: tc.endpoint}
				})
			}
			if err := cmd.RunE(cmd, nil); !called || !errors.Is(err, wantErr) {
				t.Fatalf("cloud route lost: called=%v err=%v", called, err)
			}
		})
	}
}

func TestUnenrollCommandDispatchesSelectedLite(t *testing.T) {
	preserveCloudUnenrollmentHooks(t)
	preserveLiteUnenrollmentHooks(t)
	setTempConfig(t, &config.Config{})
	resolveUnenrollTargetFn = func(context.Context, ...resolveOption) (*SelectedDevice, error) {
		return &SelectedDevice{External: &models.ExternalDevice{ProviderKey: "wendy-lite", ConnectionInfo: map[string]string{"type": "USB"}}}, nil
	}
	connectLiteUnenrollmentFn = func(models.ExternalDevice) (liteUnenrollmentDevice, error) {
		return &fakeLiteUnenrollmentDevice{identity: &liteclient.DeviceIdentity{ID: "441bf6804824"}}, nil
	}
	called := false
	unenrollLiteCloudFn = func(_ *cobra.Command, _, _, deviceID string, assumeYes bool) error {
		called = true
		if deviceID != "lite-441bf6804824" || !assumeYes {
			t.Fatalf("Lite dispatch lost identity or confirmation: %q %v", deviceID, assumeYes)
		}
		return nil
	}
	cmd := newDeviceUnenrollCmd()
	cmd.SetContext(context.Background())
	if err := cmd.Flags().Set("yes", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, nil); err != nil || !called {
		t.Fatalf("Lite device rejected or misrouted: called=%v err=%v", called, err)
	}
}

func TestUnenrollCommandUsesSelectedCloudAssetWithoutProvisioning(t *testing.T) {
	preserveCloudUnenrollmentHooks(t)
	preserveLiteUnenrollmentHooks(t)
	setTempConfig(t, &config.Config{})
	scoped := cloudDeviceSelector{Endpoint: "cloud.example:443", TenantUUID: liteTestTenant, AssetUUID: liteTestAssetID}.String()
	for _, tc := range []struct {
		name   string
		target *SelectedDevice
	}{
		{name: "agent", target: &SelectedDevice{Agent: &grpcclient.AgentConnection{}, DefaultSelector: scoped}},
		{name: "Lite selector", target: &SelectedDevice{External: &models.ExternalDevice{ProviderKey: "wendy-lite", ConnectionInfo: map[string]string{"type": "Cloud"}}, DefaultSelector: scoped}},
		{name: "Lite metadata", target: &SelectedDevice{External: &models.ExternalDevice{ProviderKey: "wendy-lite", ConnectionInfo: map[string]string{"type": "Cloud", "cloud": "cloud.example:443", "tenantId": liteTestTenant, "assetId": liteTestAssetID}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolveUnenrollTargetFn = func(context.Context, ...resolveOption) (*SelectedDevice, error) { return tc.target, nil }
			connectLiteUnenrollmentFn = func(models.ExternalDevice) (liteUnenrollmentDevice, error) {
				t.Fatal("selected cloud asset attempted a Lite device connection")
				return nil, nil
			}
			wantErr := errors.New("stop after cloud selection")
			called := false
			resolveCloudUnenrollAuthFn = func(_, selector string) (*config.AuthConfig, string, error) {
				called = true
				if selector != scoped {
					t.Fatalf("cloud picker identity lost: %q", selector)
				}
				return nil, "", wantErr
			}
			cmd := newDeviceUnenrollCmd()
			cmd.SetContext(context.Background())
			if err := cmd.RunE(cmd, nil); !called || !errors.Is(err, wantErr) {
				t.Fatalf("selected cloud target was not routed to cloud removal: called=%v err=%v", called, err)
			}
		})
	}
}

func unenrollTestCloudDevice(id, name, identity string) cloudDiscoveryDevice {
	asset := &cloudpbv2.Asset{Id: id, Name: name, PkiDeviceName: proto.String(identity), OrganizationId: testOperatorTenant}
	return cloudDiscoveryDevice{cloudAssetMetadata: asset, v2: asset, key: id}
}

func TestUnenrollResolutionUsesExactIdentity(t *testing.T) {
	first := unenrollTestCloudDevice(liteTestAssetID, "renamed-board", "lite-441bf6804824")
	second := unenrollTestCloudDevice("fc741e8b-4098-4830-bfa5-3f35c1aa0e60", "lite-441bf6804824", "lite-other")
	devices := []cloudDiscoveryDevice{first, second}
	for _, tc := range []struct{ name, selector, deviceID, want string }{
		{name: "local hardware wins over name", selector: second.GetName(), deviceID: "lite-441bf6804824", want: first.key},
		{name: "exact UUID", selector: first.key, want: first.key},
		{name: "cloud name", selector: "RENAMED-BOARD", want: first.key},
		{name: "PKI ID fallback", selector: "lite-other", want: second.key},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveUnenrollDevice(devices, tc.selector, tc.deviceID)
			if err != nil || got.key != tc.want {
				t.Fatalf("resolved %q err=%v, want %q", got.key, err, tc.want)
			}
		})
	}
	if _, err := resolveUnenrollDevice(devices, second.GetName(), ""); err == nil {
		t.Fatal("cloud name matching another device's identity was accepted")
	}
	if _, err := resolveUnenrollDevice(devices, first.GetName(), "lite-missing"); err == nil {
		t.Fatal("missing local hardware identity fell back to a cloud name")
	}
	if _, err := resolveUnenrollDevice(devices, "", "LITE-441BF6804824"); err == nil {
		t.Fatal("hardware identity match ignored case")
	}
	duplicateIdentity := unenrollTestCloudDevice("other-id", "other-name", "lite-441bf6804824")
	if _, err := resolveUnenrollDevice(append(devices, duplicateIdentity), "", "lite-441bf6804824"); err == nil || !strings.Contains(err.Error(), "multiple cloud enrollments") {
		t.Fatalf("ambiguous identity accepted: %v", err)
	}
	duplicateName := unenrollTestCloudDevice("other-id", "renamed-board", "lite-third")
	if _, err := resolveUnenrollDevice(append(devices, duplicateName), "renamed-board", ""); err == nil {
		t.Fatal("ambiguous cloud name accepted")
	}
}

func TestCloudUnenrollAuthKeepsScopedOrganization(t *testing.T) {
	auth := liteTestAuth("cloud.example:443")
	other := liteTestAuth("cloud.example:443")
	other.Certificates[0].PrincipalURI = "spiffe://wendy.sh/tenant/7aa28a2c-3747-4dd5-aa38-c863128c1f9d/operator/test"
	setTempConfig(t, &config.Config{Auth: []config.AuthConfig{*other, *auth}})
	scoped := cloudDeviceSelector{Endpoint: auth.CloudGRPC, TenantUUID: liteTestTenant, AssetUUID: liteTestAssetID}.String()
	got, assetID, err := resolveCloudUnenrollAuth("", scoped)
	if err != nil || got == nil || got.Certificates[0].TenantUUID() != liteTestTenant || assetID != liteTestAssetID {
		t.Fatalf("scoped cloud organization lost: asset=%q err=%v", assetID, err)
	}
	if _, _, err := resolveCloudUnenrollAuth("other.example:443", scoped); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting cloud endpoint accepted: %v", err)
	}
	if _, _, err := resolveCloudUnenrollAuth("", "cloud://invalid"); err == nil {
		t.Fatal("malformed scoped selector accepted")
	}
}

type unenrollConfirmationServer struct {
	cloudpbv2.UnimplementedAssetServiceServer
	asset   *cloudpbv2.Asset
	lists   atomic.Int32
	gets    atomic.Int32
	deletes atomic.Int32
	online  atomic.Bool
}

func (s *unenrollConfirmationServer) GetAsset(_ context.Context, req *cloudpbv2.GetAssetRequest) (*cloudpbv2.Asset, error) {
	s.gets.Add(1)
	return s.asset, nil
}

func (s *unenrollConfirmationServer) ListAssets(req *cloudpbv2.ListAssetsRequest, stream grpc.ServerStreamingServer[cloudpbv2.ListAssetsResponse]) error {
	s.lists.Add(1)
	s.online.Store(req.GetOnlineOnly())
	return stream.Send(&cloudpbv2.ListAssetsResponse{Asset: s.asset, Total: 1})
}

func (s *unenrollConfirmationServer) DeleteAsset(context.Context, *cloudpbv2.SignedRequest) (*cloudpbv2.DeleteAssetResponse, error) {
	s.deletes.Add(1)
	return &cloudpbv2.DeleteAssetResponse{}, nil
}

func TestCloudUnenrollConfirmsBeforeMutation(t *testing.T) {
	preserveCloudUnenrollmentHooks(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &unenrollConfirmationServer{asset: unenrollTestCloudDevice(liteTestAssetID, "offline-board", "lite-441bf6804824").v2}
	grpcServer := grpc.NewServer()
	cloudpbv2.RegisterAssetServiceServer(grpcServer, server)
	go grpcServer.Serve(listener)
	t.Cleanup(grpcServer.Stop)
	auth := oidcEnrollmentAuth(t)
	auth.CloudGRPC = listener.Addr().String()
	ensureFreshCertificateFn = func(context.Context, *config.AuthConfig) error { return nil }
	resolveCloudUnenrollAuthFn = func(_ string, selector string) (*config.AuthConfig, string, error) { return auth, selector, nil }
	confirmDefaultNoFn = func(string) bool { return false }
	for _, tc := range []struct {
		name, selector string
		interactive    bool
	}{
		{name: "noninteractive offline name", selector: "offline-board"},
		{name: "noninteractive exact UUID", selector: liteTestAssetID},
		{name: "declined", selector: "offline-board", interactive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isInteractiveTerminalFn = func() bool { return tc.interactive }
			cmd := &cobra.Command{}
			cmd.SetContext(context.Background())
			var output bytes.Buffer
			cmd.SetOut(&output)
			err := runCloudDeviceUnenroll(cmd, "", tc.selector, "", false)
			if tc.interactive {
				if err != nil || !strings.Contains(output.String(), "Aborted.") {
					t.Fatalf("declined removal result: %v %q", err, output.String())
				}
			} else if err == nil || !strings.Contains(err.Error(), "--yes") {
				t.Fatalf("missing confirmation error: %v", err)
			}
			if server.deletes.Load() != 0 {
				t.Fatal("asset deleted without confirmation")
			}
		})
	}
	if server.lists.Load() != 2 || server.gets.Load() != 1 || server.online.Load() {
		t.Fatalf("wrong offline asset lookup: lists=%d gets=%d online=%v", server.lists.Load(), server.gets.Load(), server.online.Load())
	}
}
