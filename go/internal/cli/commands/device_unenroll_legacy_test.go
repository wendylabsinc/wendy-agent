package commands

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type legacyUnenrollServer struct {
	cloudpb.UnimplementedCertificateServiceServer
	cloudpb.UnimplementedAssetServiceServer
	mu            sync.Mutex
	events        []string
	revokeError   bool
	revokeSuccess bool
	deleteSuccess bool
}

func (s *legacyUnenrollServer) ListCertificates(req *cloudpb.ListCertificatesRequest, stream grpc.ServerStreamingServer[cloudpb.ListCertificatesResponse]) error {
	if req.GetAssetId() != 42 {
		return status.Error(codes.InvalidArgument, "wrong asset")
	}
	for _, cert := range []*cloudpb.Certificate{
		{Id: 11, Status: cloudpb.CertificateStatus_CERTIFICATE_STATUS_ACTIVE},
		{Id: 12, Status: cloudpb.CertificateStatus_CERTIFICATE_STATUS_REVOKED},
		nil,
		{Id: 13, Status: cloudpb.CertificateStatus_CERTIFICATE_STATUS_ACTIVE},
		{Id: 14, Status: cloudpb.CertificateStatus_CERTIFICATE_STATUS_EXPIRED},
	} {
		if err := stream.Send(&cloudpb.ListCertificatesResponse{Certificate: cert}); err != nil {
			return err
		}
	}
	return nil
}

func (s *legacyUnenrollServer) RevokeCertificate(_ context.Context, req *cloudpb.RevokeCertificateRequest) (*cloudpb.RevokeCertificateResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, fmt.Sprintf("revoke %d", req.GetCertificateId()))
	if s.revokeError && req.GetCertificateId() == 13 {
		return nil, status.Error(codes.PermissionDenied, "revocation refused")
	}
	return &cloudpb.RevokeCertificateResponse{Success: s.revokeSuccess}, nil
}

func (s *legacyUnenrollServer) DeleteAsset(_ context.Context, req *cloudpb.DeleteAssetRequest) (*cloudpb.DeleteAssetResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, fmt.Sprintf("delete %d", req.GetId()))
	return &cloudpb.DeleteAssetResponse{Success: s.deleteSuccess}, nil
}

func TestLegacyCloudUnenrollRequiresRevocationBeforeDeletion(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		revokeError, revokeSuccess bool
		deleteSuccess              bool
		wantRevoked                int
		wantDeleted                bool
		wantError                  string
		wantEvents                 []string
	}{
		{name: "success", revokeSuccess: true, deleteSuccess: true, wantRevoked: 2, wantDeleted: true, wantEvents: []string{"revoke 11", "revoke 13", "delete 42"}},
		{name: "revocation error", revokeError: true, revokeSuccess: true, deleteSuccess: true, wantRevoked: 1, wantError: "revoking certificate 13", wantEvents: []string{"revoke 11", "revoke 13"}},
		{name: "revocation not confirmed", deleteSuccess: true, wantError: "confirm revocation", wantEvents: []string{"revoke 11"}},
		{name: "deletion not confirmed", revokeSuccess: true, wantRevoked: 2, wantError: "confirm deletion", wantEvents: []string{"revoke 11", "revoke 13", "delete 42"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &legacyUnenrollServer{revokeError: tc.revokeError, revokeSuccess: tc.revokeSuccess, deleteSuccess: tc.deleteSuccess}
			listener := bufconn.Listen(1 << 20)
			server := grpc.NewServer()
			cloudpb.RegisterCertificateServiceServer(server, service)
			cloudpb.RegisterAssetServiceServer(server, service)
			go server.Serve(listener)
			t.Cleanup(server.Stop)
			t.Cleanup(func() { listener.Close() })
			conn, err := grpc.NewClient("passthrough:///legacy-unenroll", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { conn.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			revoked, deleted, err := cloudUnenrollCleanupClient(ctx, conn, 42)
			if revoked != tc.wantRevoked || deleted != tc.wantDeleted || (err == nil) != (tc.wantError == "") || err != nil && !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("got revoked=%d deleted=%v err=%v, want %d %v %q", revoked, deleted, err, tc.wantRevoked, tc.wantDeleted, tc.wantError)
			}
			service.mu.Lock()
			defer service.mu.Unlock()
			if !reflect.DeepEqual(service.events, tc.wantEvents) {
				t.Fatalf("wrong operation order: %v, want %v", service.events, tc.wantEvents)
			}
		})
	}
}

func TestUnenrollPreservesNumericCloudV1Routing(t *testing.T) {
	for _, tc := range []struct {
		name       string
		org, asset int32
		legacy     bool
	}{
		{"legacy", 55, 277, true},
		{"minimum valid numeric IDs", 1, 1, true},
		{"UUID enrollment", 0, 0, false},
		{"missing organization", 0, 277, false},
		{"missing asset", 55, 0, false},
		{"invalid organization", -1, 277, false},
		{"invalid asset", 55, -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLegacyCloudEnrollment(tc.org, tc.asset); got != tc.legacy {
				t.Fatalf("legacy routing = %v, want %v", got, tc.legacy)
			}
		})
	}
}
