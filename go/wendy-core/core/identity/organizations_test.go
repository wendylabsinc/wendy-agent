package identity

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/wendylabsinc/WendyOS/go/wendy-core/core/identity/internal/cloudpb"
	"github.com/wendylabsinc/WendyOS/go/wendy-core/platform"
	"github.com/wendylabsinc/WendyOS/go/wendy-core/platform/platformtest"
	"google.golang.org/protobuf/proto"
)

func organizationWire(t *testing.T, id string) []byte {
	t.Helper()
	response := &cloudpb.ListOrganizationsResponse{Organization: &cloudpb.Organization{Id: id, Name: "Robotics"}}
	b, err := proto.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestOrganizationPagingClosesEveryStreamAndPreservesStatus(t *testing.T) {
	closed, calls := 0, 0
	transport := platformtest.Transport{ServerStreamFunc: func(ctx context.Context, r platform.RPCRequest) (platform.ServerStream, error) {
		request := new(cloudpb.ListOrganizationsRequest)
		if err := proto.Unmarshal(r.Message, request); err != nil {
			t.Fatal(err)
		}
		if r.Method != "/wendycloud.v2.OrganizationService/ListOrganizations" || request.GetOffset() != int32(calls*200) || request.GetLimit() != 200 {
			t.Fatal("paging contract changed")
		}
		calls++
		n := 0
		return platformtest.ServerStream{RecvFunc: func() ([]byte, error) {
			n++
			if n <= 200 && calls == 1 {
				return organizationWire(t, "org"), nil
			}
			return nil, io.EOF
		}, CloseFunc: func() error { closed++; return nil }}, nil
	}}
	orgs, err := ListOrganizations(context.Background(), transport)
	if err != nil || len(orgs) != 200 || calls != 2 || closed != 2 || orgs[0].Name != "Robotics" {
		t.Fatalf("paging: count=%d calls=%d closed=%d err=%v", len(orgs), calls, closed, err)
	}
	refused := &platform.RPCStatusError{Code: platform.RPCPermissionDenied, Message: "membership refused"}
	transport.ServerStreamFunc = func(context.Context, platform.RPCRequest) (platform.ServerStream, error) {
		return platformtest.ServerStream{RecvFunc: func() ([]byte, error) { return nil, refused }, CloseFunc: func() error { closed++; return nil }}, nil
	}
	_, err = ListOrganizations(context.Background(), transport)
	var status *platform.RPCStatusError
	if !errors.As(err, &status) || status != refused || closed != 3 {
		t.Fatalf("stream status lost: %v", err)
	}
}
func TestOrganizationCollectionDistinguishesFailuresFromEmptyMembership(t *testing.T) {
	sessions := []Session{{Certificates: []Certificate{{OrganizationID: 1}}}, {Certificates: []Certificate{{OrganizationID: 2}}}, {APIKey: "token-only"}}
	reported := 0
	failure := errors.New("offline")
	collection := CollectOrganizations(context.Background(), sessions, func(_ context.Context, s *Session) ([]Organization, error) {
		if s.Certificates[0].OrganizationID == 1 {
			return nil, failure
		}
		return nil, nil
	}, func(*Session) string { return "session" }, func(error) { reported++ })
	if collection.SuccessfulSessions != 1 || len(collection.Failures) != 1 || !errors.Is(collection.Failures[0], failure) || reported != 1 || collection.PickerSession != &sessions[1] {
		t.Fatal("failed probe looked like empty membership")
	}
}
