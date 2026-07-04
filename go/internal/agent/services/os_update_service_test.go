package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// installArmPossibleArmer overrides makeRedundancyArmer with an armer whose
// decide() yields armPossible (Jetson, unarmed, APP_b present, no prior
// attempt) and whose reboot seam flips *rebooted. It registers a t.Cleanup that
// restores the original var. Shared by the v1 and v2 preflight tests so both
// exercise the identical arming decision.
func installArmPossibleArmer(t *testing.T, rebooted *bool) {
	t.Helper()
	prev := makeRedundancyArmer
	makeRedundancyArmer = func(*zap.Logger) *redundancyArmer {
		return &redundancyArmer{
			logger:   zap.NewNop(),
			isJetson: func() bool { return true },
			// unarmed
			readEfivar: func(string) ([]byte, error) { return nil, context.Canceled },
			statPath: func(p string) error {
				if p == armAttemptMarker {
					return context.Canceled // no marker yet
				}
				return nil // APP_b present
			},
			lookPath:    func(string) (string, bool) { return "", false },
			writeMarker: func(string) error { return nil },
			writeEfivar: func(string, []byte) error { return nil },
			reboot:      func() error { *rebooted = true; return nil },
		}
	}
	t.Cleanup(func() { makeRedundancyArmer = prev })
}

// stubWendyOSUpdateBinary makes wendyOSUpdater.detect() succeed without a real
// device: selectUpdater requires the wendyos-update binary to exist AND
// `status --json` to exit 0 before it hands back a backend, so without a stub
// on PATH the handler fails at backend selection and never reaches the
// redundancy preflight this test exercises. The stub is never asked to
// perform a real install: decide()==armPossible makes the preflight return
// before install() is called.
func stubWendyOSUpdateBinary(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "wendyos-update")
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

func TestUpdateOSV2ArmsRedundancyAndReboots(t *testing.T) {
	stubWendyOSUpdateBinary(t)

	rebooted := false
	installArmPossibleArmer(t, &rebooted)

	s := NewOSUpdateService(zap.NewNop())
	s.isWendyOSHost = func() bool { return true }
	stream := &fakeUpdateOSStreamV2{ctx: context.Background()}

	err := s.UpdateOS(&agentpbv2.UpdateOSRequest{ArtifactUrl: "http://x/y.wendy"}, stream)
	if err != nil {
		t.Fatalf("UpdateOS returned %v, want nil (rebooting)", err)
	}
	if !rebooted {
		t.Fatal("expected arm() to reboot")
	}
	if len(stream.sent) != 1 || stream.sent[0].GetArmingRedundancy() == nil {
		t.Fatalf("expected a single ArmingRedundancy response, got %+v", stream.sent)
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

func TestUpdateOSV1ArmsRedundancyAndReboots(t *testing.T) {
	stubWendyOSUpdateBinary(t)

	rebooted := false
	installArmPossibleArmer(t, &rebooted)

	s := NewAgentService(zap.NewNop(), nil, nil, nil, nil)
	s.isWendyOSHost = func() bool { return true }
	stream := &fakeUpdateOSStreamV1{ctx: context.Background()}

	err := s.UpdateOS(&agentpb.UpdateOSRequest{ArtifactUrl: "http://x/y.wendy"}, stream)
	if err != nil {
		t.Fatalf("UpdateOS returned %v, want nil (rebooting)", err)
	}
	if !rebooted {
		t.Fatal("expected arm() to reboot")
	}
	if len(stream.sent) != 1 || stream.sent[0].GetArmingRedundancy() == nil {
		t.Fatalf("expected a single ArmingRedundancy response, got %+v", stream.sent)
	}
}
