//go:build !linux

package services

import (
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// DumpKernelLog is unsupported off Linux: the kernel ring buffer (/dev/kmsg) is
// a Linux-only interface.
func (s *AgentService) DumpKernelLog(_ *agentpb.DumpKernelLogRequest, _ grpc.ServerStreamingServer[agentpb.DumpKernelLogResponse]) error {
	return status.Error(codes.Unimplemented, "kernel log dump is only available on Linux devices")
}
