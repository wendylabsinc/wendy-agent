package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/timesync"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	cloudpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestCloudResetDisabledBeforeAuthorizationOrMutation(t *testing.T) {
	for _, name := range []string{"valid revoked leaf and caller deletion", "fabricated asset", "missing operator", "nil request"} {
		t.Run(name, func(t *testing.T) {
			svc, req, ctx := cloudResetFixture(t)
			files := map[string][]byte{}
			for _, name := range []string{"provisioning.json", "device-key.pem", "acme-account-key.pem"} {
				data, err := os.ReadFile(filepath.Join(svc.configPath, name))
				if err != nil {
					t.Fatal(err)
				}
				files[name] = data
			}
			svc.trustedTime = func(context.Context) (timesync.TimeWindow, error) {
				t.Fatal("disabled reset acquired time for authorization")
				return timesync.TimeWindow{}, nil
			}
			switch name {
			case "fabricated asset":
				var deleted cloudpbv2.DeletedAsset
				if err := proto.Unmarshal(req.CloudDeletion, &deleted); err != nil {
					t.Fatal(err)
				}
				deleted.Id = "44444444-4444-4444-8444-444444444444"
				var err error
				req.CloudDeletion, err = proto.Marshal(&deleted)
				if err != nil {
					t.Fatal(err)
				}
			case "missing operator":
				ctx = context.Background()
			case "nil request":
				req = nil
			}
			v2 := NewProvisioningServiceV2(svc)
			state, err := v2.IsProvisioned(ctx, &agentpbv2.IsProvisionedRequest{})
			if err != nil || state.GetProvisioned() == nil || state.GetProvisioned().GetCloudUnenrollmentSupported() {
				t.Fatalf("unsafe reset capability advertised: %v, %v", state, err)
			}
			if _, err := v2.Unprovision(ctx, req); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("reset = %v, want failed precondition", err)
			}
			for name, original := range files {
				got, err := os.ReadFile(filepath.Join(svc.configPath, name))
				if err != nil || string(got) != string(original) {
					t.Fatalf("disabled reset changed %s: %v", name, err)
				}
			}
			if !svc.enrolled {
				t.Fatal("disabled reset changed enrollment")
			}
		})
	}
}

func TestCloudResetDisabledPreservesNumericV2Unprovision(t *testing.T) {
	svc, dir := newTestProvisioningService(t)
	defer os.RemoveAll(dir)
	ctx := context.Background()
	if _, err := svc.StartProvisioning(ctx, &agentpb.StartProvisioningRequest{
		OrganizationId: 7, CloudHost: "unprov.wendy.io", AssetId: 70,
	}); err != nil {
		t.Fatal(err)
	}
	v2 := NewProvisioningServiceV2(svc)
	if _, err := v2.Unprovision(ctx, &agentpbv2.UnprovisionRequest{}); err != nil {
		t.Fatalf("numeric v2 reset failed: %v", err)
	}
	state, err := v2.IsProvisioned(ctx, &agentpbv2.IsProvisionedRequest{})
	if err != nil || state.GetNotProvisioned() == nil {
		t.Fatalf("numeric device remains enrolled: %v, %v", state, err)
	}
}
