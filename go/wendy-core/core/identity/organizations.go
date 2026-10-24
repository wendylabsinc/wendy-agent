package identity

import (
	"context"
	"fmt"
	"io"

	"github.com/wendylabsinc/WendyOS/go/wendy-core/core/identity/internal/cloudpb"
	"github.com/wendylabsinc/WendyOS/go/wendy-core/platform"
	"google.golang.org/protobuf/proto"
)

// Generated with protoc 36.2 and protoc-gen-go v1.36.10. The generator pins
// the Go plugin and isolates application descriptor/type registration so hosts
// can also link their own generated copies of the same service contracts.
//go:generate python3 ../../scripts/generate_identity.py

// Organization preserves the complete serialized Organization message alongside
// its ID and display name. Hosts can decode Message using their generated API
// without losing fields that this sign-in workflow does not inspect.
type Organization struct {
	ID, Name string
	Message  []byte
}

const orgPageSize = 200
const orgPageCap = 50

// ListOrganizations moves the existing bounded organization stream/paging loop
// into Core. The host transport supplies session authentication on every call.
// This consumes the existing offset RPC unchanged; it adds no offset API or
// query options. Stable-anchor paging awaits an upstream service contract.
func ListOrganizations(ctx context.Context, transport platform.Transport) ([]Organization, error) {
	var organizations []Organization
	for page := 0; page < orgPageCap; page++ {
		request := &cloudpb.ListOrganizationsRequest{
			Offset: proto.Int32(int32(page * orgPageSize)), Limit: proto.Int32(orgPageSize),
		}
		data, err := proto.Marshal(request)
		if err != nil {
			return nil, err
		}
		stream, err := transport.ServerStream(ctx, platform.RPCRequest{Method: "/wendycloud.v2.OrganizationService/ListOrganizations", Message: data})
		if err != nil {
			return nil, err
		}
		received, err := receiveOrganizations(stream, &organizations)
		if err != nil {
			return nil, err
		}
		if received < orgPageSize {
			return organizations, nil
		}
	}
	return nil, fmt.Errorf("Cloud returned too many organization pages")
}

func receiveOrganizations(stream platform.ServerStream, organizations *[]Organization) (int, error) {
	defer stream.Close()
	received := 0
	for {
		data, err := stream.Recv()
		if err == io.EOF {
			return received, nil
		}
		if err != nil {
			return 0, fmt.Errorf("listing organizations: %w", err)
		}
		var response cloudpb.ListOrganizationsResponse
		if err := proto.Unmarshal(data, &response); err != nil {
			return 0, fmt.Errorf("listing organizations: %w", err)
		}
		org := response.GetOrganization()
		if org == nil {
			continue
		}
		wire, err := proto.Marshal(org)
		if err != nil {
			return 0, err
		}
		*organizations = append(*organizations, Organization{ID: org.GetId(), Name: org.GetName(), Message: wire})
		received++
	}
}

// OrganizationCollection distinguishes an empty membership from failed probes.
// PickerSession is the first credentialed session that answered successfully.
type OrganizationCollection struct {
	Organizations      []Organization
	PickerSession      *Session
	SuccessfulSessions int
	Failures           []error
}

// CollectOrganizations probes credentialed sessions and deduplicates by org ID.
// label and report preserve host-specific labels and diagnostics without moving
// terminal output into Core. list receives the actual session, so host refresh
// and secret-store adapters can retain their existing behavior.
func CollectOrganizations(ctx context.Context, sessions []Session, list func(context.Context, *Session) ([]Organization, error), label func(*Session) string, report func(error)) OrganizationCollection {
	var result OrganizationCollection
	seen := make(map[string]bool)
	for i := range sessions {
		session := &sessions[i]
		if len(session.Certificates) == 0 {
			continue
		}
		fetched, err := list(ctx, session)
		if err != nil {
			e := fmt.Errorf("listing organizations for %s: %w", label(session), err)
			if report != nil {
				report(e)
			}
			result.Failures = append(result.Failures, e)
			continue
		}
		result.SuccessfulSessions++
		if result.PickerSession == nil {
			result.PickerSession = session
		}
		for _, org := range fetched {
			if seen[org.ID] {
				continue
			}
			seen[org.ID] = true
			result.Organizations = append(result.Organizations, org)
		}
	}
	return result
}
