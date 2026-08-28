package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeDeviceCachePruneClient struct {
	response *agentpbv2.PruneCacheResponse
	dryRun   bool
	err      error
}

func (f *fakeDeviceCachePruneClient) PruneCache(_ context.Context, req *agentpbv2.PruneCacheRequest, _ ...grpc.CallOption) (*agentpbv2.PruneCacheResponse, error) {
	f.dryRun = req.GetDryRun()
	return f.response, f.err
}

func TestRunDeviceCachePruneRPCExplainsOldAgent(t *testing.T) {
	fake := &fakeDeviceCachePruneClient{err: status.Error(codes.Unimplemented, "unknown method")}
	err := runDeviceCachePruneRPC(context.Background(), fake, &bytes.Buffer{}, false, false)
	if err == nil || !strings.Contains(err.Error(), "wendy device update") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunDeviceCachePruneRPCDryRun(t *testing.T) {
	fake := &fakeDeviceCachePruneClient{response: &agentpbv2.PruneCacheResponse{
		ContentBlobs: 2, ContentBytes: 1_000, Snapshots: 3, SnapshotBytes: 2_000, MinimumAgeSeconds: 3600,
	}}
	var out bytes.Buffer
	if err := runDeviceCachePruneRPC(context.Background(), fake, &out, true, false); err != nil {
		t.Fatalf("runDeviceCachePruneRPC: %v", err)
	}
	if !fake.dryRun || !strings.Contains(out.String(), "Eligible: 3.0 kB") {
		t.Fatalf("output = %q, dryRun=%v", out.String(), fake.dryRun)
	}
}

func TestRunDeviceCachePruneRPCJSON(t *testing.T) {
	fake := &fakeDeviceCachePruneClient{response: &agentpbv2.PruneCacheResponse{ContentBlobs: 1, MinimumAgeSeconds: 3600}}
	var out bytes.Buffer
	if err := runDeviceCachePruneRPC(context.Background(), fake, &out, false, true); err != nil {
		t.Fatalf("runDeviceCachePruneRPC: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if got["contentBlobs"] != float64(1) || got["minimumAgeSeconds"] != float64(3600) {
		t.Fatalf("JSON = %v", got)
	}
}
