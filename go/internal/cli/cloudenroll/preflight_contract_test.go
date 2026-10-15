package cloudenroll

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// statusConn answers every unary call with one recorded IsProvisioned status.
type statusConn struct {
	resp *agentpbv2.IsProvisionedResponse
}

func (c statusConn) Invoke(_ context.Context, _ string, _, reply any, _ ...grpc.CallOption) error {
	proto.Merge(reply.(proto.Message), c.resp)
	return nil
}

func (statusConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("unused")
}

// The agents' answers are pinned by the same contract file (Go and Swift agent
// tests); this proves what the CLI concludes from each of them.
func TestCheckAgentEnrollmentContract(t *testing.T) {
	data, err := os.ReadFile("../../../../Proto/conformance/agent-provisioning-v2/contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Steps []struct {
			Name         string          `json:"name"`
			Response     json.RawMessage `json:"response"`
			CLIPreflight string          `json:"cliPreflight"`
		}
	}
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, step := range contract.Steps {
		if step.CLIPreflight == "" {
			continue
		}
		resp := &agentpbv2.IsProvisionedResponse{}
		if err := protojson.Unmarshal(step.Response, resp); err != nil {
			t.Fatalf("%s: %v", step.Name, err)
		}
		got := "ok"
		if err := CheckAgentEnrollment(context.Background(), statusConn{resp}); err != nil {
			got = err.Error()
		}
		if got != step.CLIPreflight {
			t.Fatalf("%s: preflight %q, want %q", step.Name, got, step.CLIPreflight)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("contract has no preflight steps")
	}
}
