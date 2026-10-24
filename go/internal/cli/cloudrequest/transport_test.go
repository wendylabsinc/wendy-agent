package cloudrequest

import (
	"context"
	"strings"
	"testing"

	pb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type collisionCloud struct{ *fakeCloud }

func (f collisionCloud) Invoke(ctx context.Context, method string, args, reply any, options ...grpc.CallOption) error {
	if method == pb.OperatorSessionService_RegisterOperatorLeaf_FullMethodName {
		return f.fakeCloud.Invoke(ctx, method, args, reply, options...)
	}
	for _, option := range options {
		if trailer, ok := option.(grpc.TrailerCallOption); ok {
			*trailer.TrailerAddr = metadata.Pairs(CollidingFieldMetadataKey, "name", "binary-bin", string([]byte{0, 255}))
		}
	}
	return status.Error(codes.AlreadyExists, "device name already used")
}

func TestSDKAdapterPreservesCollisionTrailerAndGRPCDiagnostic(t *testing.T) {
	auth, _, _ := testAuth(t)
	conn := collisionCloud{&fakeCloud{t: t}}
	var trailer metadata.MD
	err := InvokeWithTrailer(context.Background(), conn, auth, pb.DeviceEnrollmentService_EnrollDevice_FullMethodName, &pb.EnrollDeviceRequest{DeviceId: "dev-1"}, &pb.EnrollDeviceResponse{}, &trailer)
	if status.Code(err) != codes.AlreadyExists || err.Error() != "rpc error: code = AlreadyExists desc = device name already used" {
		t.Fatal("diagnostic changed", err)
	}
	if trailer.Get(CollidingFieldMetadataKey)[0] != "name" || trailer.Get("binary-bin")[0] != string([]byte{0, 255}) {
		t.Fatal("trailers changed", trailer)
	}
}

func TestSDKAdapterPreservesFinalRetryDiagnostic(t *testing.T) {
	auth, _, _ := testAuth(t)
	denied := status.Error(codes.PermissionDenied, "permission refused")
	conn := &fakeCloud{t: t, refuse: []error{denied, denied}}
	err := Invoke(context.Background(), conn, auth, pb.DeviceEnrollmentService_EnrollDevice_FullMethodName, &pb.EnrollDeviceRequest{DeviceId: "dev-1"}, &pb.EnrollDeviceResponse{})
	if status.Code(err) != codes.PermissionDenied || !strings.HasSuffix(err.Error(), "with the operator certificate attached: rpc error: code = PermissionDenied desc = permission refused") {
		t.Fatal("retry diagnostic changed", err)
	}
}
