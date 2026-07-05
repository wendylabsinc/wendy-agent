package services

import (
	"context"
	"os"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// installBlockingGate overrides makeRedundancyGate with a gate that blocks the
// update (a Jetson whose firmware A/B redundancy is not armed). It registers a
// t.Cleanup restoring the original var. Shared by the v1 and v2 preflight tests.
func installBlockingGate(t *testing.T) {
	t.Helper()
	prev := makeRedundancyGate
	makeRedundancyGate = func() redundancyGate {
		return redundancyGate{
			isJetson:   func() bool { return true },
			readEfivar: func(string) ([]byte, error) { return nil, os.ErrNotExist }, // unarmed
		}
	}
	t.Cleanup(func() { makeRedundancyGate = prev })
}

// stubWendyOSUpdateBinary makes wendyOSUpdater.detect() succeed without a real
// device: selectUpdater requires the wendyos-update binary to exist AND
// `status --json` to exit 0 before it hands back a backend, so without a stub on
// PATH the handler fails at backend selection and never reaches the redundancy
// preflight this test exercises. The stub is never asked to install: the gate
// blocks the update before install() is called.
func stubWendyOSUpdateBinary(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := dir + "/wendyos-update"
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("writing stub wendyos-update binary: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

type fakeUpdateOSStreamV2 struct {
	grpc.ServerStreamingServer[agentpbv2.UpdateOSResponse]
	ctx  context.Context
	sent []*agentpbv2.UpdateOSResponse
}

func (f *fakeUpdateOSStreamV2) Context() context.Context { return f.ctx }
func (f *fakeUpdateOSStreamV2) Send(r *agentpbv2.UpdateOSResponse) error {
	f.sent = append(f.sent, r)
	return nil
}

func TestUpdateOSV2BlocksUnarmedJetson(t *testing.T) {
	stubWendyOSUpdateBinary(t)
	installBlockingGate(t)

	s := NewOSUpdateService(zap.NewNop())
	s.isWendyOSHost = func() bool { return true }
	stream := &fakeUpdateOSStreamV2{ctx: context.Background()}

	if err := s.UpdateOS(&agentpbv2.UpdateOSRequest{ArtifactUrl: "http://x/y.wendy"}, stream); err != nil {
		t.Fatalf("UpdateOS returned %v, want nil (blocked with a Failed message)", err)
	}
	if len(stream.sent) != 1 || stream.sent[0].GetFailed() == nil {
		t.Fatalf("expected a single Failed response, got %+v", stream.sent)
	}
	if got := stream.sent[0].GetFailed().GetErrorMessage(); got != redundancyNotEnabledMessage {
		t.Fatalf("Failed message = %q, want redundancyNotEnabledMessage", got)
	}
}

type fakeUpdateOSStreamV1 struct {
	grpc.ServerStreamingServer[agentpb.UpdateOSResponse]
	ctx  context.Context
	sent []*agentpb.UpdateOSResponse
}

func (f *fakeUpdateOSStreamV1) Context() context.Context { return f.ctx }
func (f *fakeUpdateOSStreamV1) Send(r *agentpb.UpdateOSResponse) error {
	f.sent = append(f.sent, r)
	return nil
}

func TestUpdateOSV1BlocksUnarmedJetson(t *testing.T) {
	stubWendyOSUpdateBinary(t)
	installBlockingGate(t)

	s := NewAgentService(zap.NewNop(), nil, nil, nil, nil)
	s.isWendyOSHost = func() bool { return true }
	stream := &fakeUpdateOSStreamV1{ctx: context.Background()}

	if err := s.UpdateOS(&agentpb.UpdateOSRequest{ArtifactUrl: "http://x/y.wendy"}, stream); err != nil {
		t.Fatalf("UpdateOS returned %v, want nil (blocked with a Failed message)", err)
	}
	if len(stream.sent) != 1 || stream.sent[0].GetFailed() == nil {
		t.Fatalf("expected a single Failed response, got %+v", stream.sent)
	}
	if got := stream.sent[0].GetFailed().GetErrorMessage(); got != redundancyNotEnabledMessage {
		t.Fatalf("Failed message = %q, want redundancyNotEnabledMessage", got)
	}
}
