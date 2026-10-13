package services

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
	"github.com/wendylabsinc/wendy/go/internal/agent/meshsharing"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
	pb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	adminTenantID = "0b7c1d9e-2a3f-4c5d-8e90-1234567890ab"
	adminDeviceID = "11111111-2222-4333-8444-555555555555"
	adminPeerID   = "22222222-3333-4444-8555-666666666666"
	otherTenantID = "f4a1b2c3-d4e5-4f60-9a7b-8c9d0e1f2a3b"
)

func adminTenant(t *testing.T) meshid.Tenant {
	t.Helper()
	tenant, err := meshid.ParseTenant(adminTenantID)
	if err != nil {
		t.Fatalf("tenant UUID: %v", err)
	}
	return tenant
}

func adminDevice(t *testing.T) meshid.Device {
	t.Helper()
	device, err := meshid.ParseDevice(adminTenant(t), adminDeviceID)
	if err != nil {
		t.Fatalf("device UUID: %v", err)
	}
	return device
}

// userPrincipal names a tenant-scoped user caller, the only principal the
// admin service accepts for local mesh administration.
func userPrincipal(tenantID, name string) string {
	return certs.UserSPIFFEURI(tenantID, name)
}

func TestLocalMeshAdminAuthorizationAndPartialUpdates(t *testing.T) {
	dir := t.TempDir()
	tenant, device := adminTenant(t), adminDevice(t)
	svc := NewLocalMeshAdminService(nil, dir, func() (meshid.Tenant, meshid.Device) { return tenant, device })
	request := &pb.ConfigureLocalMeshRequest{Participate: proto.Bool(true), Nan: proto.Bool(true)}
	for _, ctx := range []context.Context{
		context.Background(),
		ctxWithIdentity(t, device.Principal()),
		ctxWithIdentity(t, userPrincipal(otherTenantID, "bob")),
	} {
		if _, err := svc.ConfigureLocalMesh(ctx, request); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("configure authorization: %v", err)
		}
		if _, err := svc.GetLocalMeshStatus(ctx, &pb.GetLocalMeshStatusRequest{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("status authorization: %v", err)
		}
	}
	ctx := ctxWithIdentity(t, userPrincipal(adminTenantID, "alice"))
	if _, err := svc.ConfigureLocalMesh(ctx, &pb.ConfigureLocalMeshRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty update accepted: %v", err)
	}
	if _, err := svc.ConfigureLocalMesh(ctx, &pb.ConfigureLocalMeshRequest{Roam: proto.Bool(true)}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("roam without participate accepted: %v", err)
	}
	statusBefore, err := svc.GetLocalMeshStatus(ctx, &pb.GetLocalMeshStatusRequest{})
	if err != nil || statusBefore.GetRuntimeAvailable() || statusBefore.GetConfigured().GetNan() {
		t.Fatalf("incorrect disabled default: %+v %v", statusBefore, err)
	}
	if _, err := svc.ConfigureLocalMesh(ctx, request); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, update := range []*pb.ConfigureLocalMeshRequest{{Roam: proto.Bool(true)}, {ShareUplink: proto.Bool(true)}, {Ble: proto.Bool(true)}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.ConfigureLocalMesh(ctx, update); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, err := svc.GetLocalMeshStatus(ctx, &pb.GetLocalMeshStatusRequest{})
	if err != nil || !got.GetConfigured().GetParticipate() || !got.GetConfigured().GetRoam() ||
		!got.GetConfigured().GetShareUplink() || !got.GetConfigured().GetNan() || !got.GetConfigured().GetBle() {
		t.Fatalf("partial settings lost: %+v %v", got, err)
	}
	if _, err = svc.ConfigureLocalMesh(ctx, &pb.ConfigureLocalMeshRequest{Participate: proto.Bool(false)}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("non-atomic disable accepted: %v", err)
	}
	if _, err = svc.ConfigureLocalMesh(ctx, &pb.ConfigureLocalMeshRequest{Nan: proto.Bool(false)}); err != nil {
		t.Fatal(err)
	}
	got, err = svc.GetLocalMeshStatus(ctx, &pb.GetLocalMeshStatusRequest{})
	if err != nil || got.GetConfigured().GetNan() || !got.GetConfigured().GetBle() {
		t.Fatalf("BLE inferred from NAN toggle: %+v %v", got, err)
	}
	svc.SetRuntimeStatusSource(func() LocalMeshRuntimeStatus {
		return LocalMeshRuntimeStatus{Available: true, State: "roaming", Detail: "using verified donor", AuthenticatedPeers: 3, GatewayPrincipal: device.Principal()}
	})
	got, err = svc.GetLocalMeshStatus(ctx, &pb.GetLocalMeshStatusRequest{})
	if err != nil || !got.GetRuntimeAvailable() || got.GetGatewayPrincipal() != device.Principal() || got.GetAuthenticatedPeers() != 3 {
		t.Fatalf("runtime status not attached: %+v %v", got, err)
	}
	tenant, err = meshid.ParseTenant(otherTenantID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.GetLocalMeshStatus(ctx, &pb.GetLocalMeshStatusRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("stale user tenant accepted: %v", err)
	}
}

func TestLocalMeshAdminCarrierUpdatePreservesConfiguredTCP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local-mesh.json")
	tenant, device := adminTenant(t), adminDevice(t)
	before := localmesh.TCPConfig{Listen: "127.0.0.1:17700", Peers: []localmesh.TCPPeer{{Device: adminPeerID, Address: "127.0.0.1:17701"}}}
	if err := meshsharing.SaveCarrierConfig(path, device, before); err != nil {
		t.Fatal(err)
	}
	svc := NewLocalMeshAdminService(nil, dir, func() (meshid.Tenant, meshid.Device) { return tenant, device })
	ctx := ctxWithIdentity(t, userPrincipal(adminTenantID, "alice"))
	if _, err := svc.ConfigureLocalMesh(ctx, &pb.ConfigureLocalMeshRequest{Nan: proto.Bool(true), Ble: proto.Bool(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfigureLocalMesh(ctx, &pb.ConfigureLocalMeshRequest{Nan: proto.Bool(false), Ble: proto.Bool(false)}); err != nil {
		t.Fatal(err)
	}
	after, err := localmesh.LoadTCPConfig(path, device)
	if err != nil || after == nil || after.Listen != before.Listen || len(after.Peers) != 1 || after.Peers[0] != before.Peers[0] || after.NAN || after.BLE {
		t.Fatalf("TCP configuration lost: %+v %v", after, err)
	}
}

func TestLocalMeshAdminCanDisableLastRadioWithoutTCPConfig(t *testing.T) {
	dir := t.TempDir()
	tenant, device := adminTenant(t), adminDevice(t)
	svc := NewLocalMeshAdminService(nil, dir, func() (meshid.Tenant, meshid.Device) { return tenant, device })
	ctx := ctxWithIdentity(t, userPrincipal(adminTenantID, "alice"))
	if _, err := svc.ConfigureLocalMesh(ctx, &pb.ConfigureLocalMeshRequest{Nan: proto.Bool(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfigureLocalMesh(ctx, &pb.ConfigureLocalMeshRequest{Nan: proto.Bool(false)}); err != nil {
		t.Fatal(err)
	}
	carriers, err := localmesh.LoadTCPConfig(filepath.Join(dir, "local-mesh.json"), device)
	if err != nil || carriers != nil {
		t.Fatalf("radio-only disabled state should remove opt-in file: %+v %v", carriers, err)
	}
}
