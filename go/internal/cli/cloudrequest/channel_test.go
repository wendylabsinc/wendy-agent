package cloudrequest

import (
	"testing"

	cloudpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
)

func TestMarkChannel(t *testing.T) {
	for _, env := range []string{channelEnv, mcpClientIDEnv, relaySessionIDEnv} {
		t.Setenv(env, "")
	}
	plain := &cloudpbv2.SignedRequest{}
	markChannel(plain)
	if plain.GetChannel() != cloudpbv2.RequestChannel_REQUEST_CHANNEL_UNSPECIFIED {
		t.Fatalf("plain CLI call marked %v", plain.GetChannel())
	}

	if err := SetChannel(cloudpbv2.RequestChannel_REQUEST_CHANNEL_MCP_LOCAL, "", ""); err != nil {
		t.Fatal(err)
	}
	local := &cloudpbv2.SignedRequest{}
	markChannel(local)
	if local.GetChannel() != cloudpbv2.RequestChannel_REQUEST_CHANNEL_MCP_LOCAL || local.GetMcpClientId() != "" {
		t.Fatalf("local MCP call marked %v", local)
	}

	if err := SetChannel(cloudpbv2.RequestChannel_REQUEST_CHANNEL_HOSTED_MCP, "https://chatgpt.com/oauth", "sess-1"); err != nil {
		t.Fatal(err)
	}
	hosted := &cloudpbv2.SignedRequest{}
	markChannel(hosted)
	if hosted.GetChannel() != cloudpbv2.RequestChannel_REQUEST_CHANNEL_HOSTED_MCP || hosted.GetMcpClientId() != "https://chatgpt.com/oauth" || hosted.GetRelaySessionId() != "sess-1" {
		t.Fatalf("hosted MCP call marked %v", hosted)
	}
}
