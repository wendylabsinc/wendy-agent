package commands

import (
	"context"
	"errors"

	wendy "github.com/wendylabsinc/wendy-go"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	pb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
	"google.golang.org/protobuf/proto"
)

// Authentication and display caching remain host-owned. Core owns the list RPC
// and paging loop. Restore grpc-go errors at the CLI boundary to retain its
// established diagnostics and cache each observed name, even on a later error.
type organizationTransport struct {
	wendy.Transport
	auth *config.AuthConfig
}

func (t organizationTransport) ServerStream(ctx context.Context, request wendy.RPCRequest) (wendy.ServerStream, error) {
	cloudCtx, err := cloudContext(ctx, t.auth)
	if err != nil {
		return nil, err
	}
	stream, err := t.Transport.ServerStream(cloudCtx, request)
	if err != nil {
		return nil, organizationRPCError(err)
	}
	return organizationStream{ServerStream: stream, endpoint: t.auth.CloudGRPC}, nil
}

type organizationStream struct {
	wendy.ServerStream
	endpoint string
}

func (s organizationStream) Recv() ([]byte, error) {
	message, err := s.ServerStream.Recv()
	if err != nil {
		return nil, organizationRPCError(err)
	}
	var response pb.ListOrganizationsResponse
	if proto.Unmarshal(message, &response) == nil && response.Organization != nil {
		cacheCloudOrganizationName(s.endpoint, response.Organization.Id, response.Organization.Name)
	}
	return message, nil
}
func organizationRPCError(err error) error {
	var status *wendy.RPCStatusError
	if errors.As(err, &status) && status.Cause != nil {
		return status.Cause
	}
	return err
}
