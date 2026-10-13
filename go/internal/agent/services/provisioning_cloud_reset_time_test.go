package services

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/unenrollproof"
	cloudpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Every refusal must leave both the enrollment and credential bytes intact.
func checkResetStateUnchanged(t *testing.T, svc *ProvisioningService) func() {
	t.Helper()
	original := make(map[string][]byte)
	for _, name := range []string{"provisioning.json", "device-key.pem", "acme-account-key.pem"} {
		data, err := os.ReadFile(filepath.Join(svc.configPath, name))
		if err != nil {
			t.Fatal(err)
		}
		original[name] = data
	}
	return func() {
		t.Helper()
		for name, want := range original {
			got, err := os.ReadFile(filepath.Join(svc.configPath, name))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("reset refusal changed %s: %v", name, err)
			}
		}
	}
}

func TestCloudResetChecksOperatorValidityAtSystemTime(t *testing.T) {
	for _, name := range []string{"expired", "not-yet-valid"} {
		t.Run(name, func(t *testing.T) {
			svc, req, ctx := cloudResetFixture(t)
			unchanged := checkResetStateUnchanged(t, svc)
			p, _ := peer.FromContext(ctx)
			actor := p.AuthInfo.(credentials.TLSInfo).State.PeerCertificates[0]
			if name == "expired" {
				actor.NotAfter = time.Now().Add(-time.Hour)
			} else {
				actor.NotBefore = time.Now().Add(time.Hour)
			}
			if _, err := NewProvisioningServiceV2(svc).Unprovision(ctx, req); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("reset = %v, want permission denied", err)
			}
			unchanged()
		})
	}
}

func TestCloudResetChecksRevocationFreshnessAtSystemTime(t *testing.T) {
	for _, offset := range []time.Duration{-2 * time.Hour, 2 * time.Hour} {
		t.Run(offset.String(), func(t *testing.T) {
			// The signed OCSP evidence is expired or not yet valid. Keep the
			// operator valid so the refusal specifically exercises that proof.
			svc, req, ctx := cloudResetFixtureAt(t, time.Now().Add(offset))
			unchanged := checkResetStateUnchanged(t, svc)
			p, _ := peer.FromContext(ctx)
			actor := p.AuthInfo.(credentials.TLSInfo).State.PeerCertificates[0]
			actor.NotBefore = time.Now().Add(-4 * time.Hour)
			actor.NotAfter = time.Now().Add(4 * time.Hour)
			if _, err := NewProvisioningServiceV2(svc).Unprovision(ctx, req); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("reset = %v, want failed precondition", err)
			}
			unchanged()
		})
	}
}

func TestCloudResetRejectsFutureDeletion(t *testing.T) {
	svc, req, ctx := cloudResetFixture(t)
	unchanged := checkResetStateUnchanged(t, svc)
	var deleted cloudpbv2.DeletedAsset
	if err := proto.Unmarshal(req.CloudDeletion, &deleted); err != nil {
		t.Fatal(err)
	}
	deleted.DeletedAt = timestamppb.New(time.Now().Add(time.Hour))
	var err error
	req.CloudDeletion, err = proto.Marshal(&deleted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewProvisioningServiceV2(svc).Unprovision(ctx, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("reset = %v, want failed precondition", err)
	}
	unchanged()
}

func TestCloudResetCanceledRequestRetainsKeys(t *testing.T) {
	svc, req, ctx := cloudResetFixture(t)
	unchanged := checkResetStateUnchanged(t, svc)
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := NewProvisioningServiceV2(svc).Unprovision(ctx, req); status.Code(err) != codes.Canceled {
		t.Fatalf("reset = %v, want canceled", err)
	}
	unchanged()
}

func TestCloudResetRecordsSystemAuthorizationTime(t *testing.T) {
	svc, req, ctx := cloudResetFixture(t)
	before := time.Now().Unix()
	response, err := NewProvisioningServiceV2(svc).Unprovision(ctx, req)
	after := time.Now().Unix()
	if err != nil {
		t.Fatal(err)
	}
	// ReadCompletion also verifies the signed OCSP evidence at the recorded
	// authorization time, as restart recovery does after erasing the keys.
	completion, _, err := unenrollproof.ReadCompletion(response.GetUnenrollmentCompletion())
	if err != nil {
		t.Fatal(err)
	}
	if completion.AuthorizedAt < before || completion.AuthorizedAt > after {
		t.Fatalf("authorization time = %d, want system time in [%d, %d]", completion.AuthorizedAt, before, after)
	}
}

func TestCloudResetDoesNotUseOrWriteClockFloor(t *testing.T) {
	for _, name := range []string{"absent", "malformed", "future", "directory"} {
		t.Run(name, func(t *testing.T) {
			svc, req, ctx := cloudResetFixture(t)
			path := filepath.Join(svc.configPath, "clock_floor")
			var original []byte
			switch name {
			case "malformed":
				original = []byte("invalid clock floor")
			case "future":
				original = make([]byte, 8)
				binary.BigEndian.PutUint64(original, uint64(time.Now().Add(24*time.Hour).Unix()))
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if original != nil {
				if err := os.WriteFile(path, original, 0400); err != nil {
					t.Fatal(err)
				}
			}
			response, err := NewProvisioningServiceV2(svc).Unprovision(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := unenrollproof.ReadCompletion(response.GetUnenrollmentCompletion()); err != nil {
				t.Fatal(err)
			}
			if original != nil {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, original) {
					t.Fatalf("clock floor changed: %v", err)
				}
			} else if name == "directory" {
				info, err := os.Stat(path)
				if err != nil || !info.IsDir() {
					t.Fatalf("clock directory replaced: %v", err)
				}
				entries, err := os.ReadDir(path)
				if err != nil || len(entries) != 0 {
					t.Fatalf("clock directory changed: %v", err)
				}
			} else if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("reset created clock floor: %v", err)
			}
			entries, err := os.ReadDir(svc.configPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Name() != "provisioning.json" && entry.Name() != "clock_floor" {
					t.Fatalf("unexpected persistent file after reset: %s", entry.Name())
				}
			}
		})
	}
}
