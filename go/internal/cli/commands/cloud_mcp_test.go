package commands

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	wendymcp "github.com/wendylabsinc/wendy/go/internal/cli/mcp"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	cloudpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// fakeRelay plays the hosted relay's side of RelaySession with a per-test
// script.
type fakeRelay struct {
	cloudpbv2.UnimplementedRelayMcpServiceServer
	script func(stream cloudpbv2.RelayMcpService_RelaySessionServer) error
}

func (f *fakeRelay) RelaySession(stream cloudpbv2.RelayMcpService_RelaySessionServer) error {
	first, err := stream.Recv()
	if err != nil || first.GetSessionId() != "sess-1" {
		return err
	}
	return f.script(stream)
}

// testOpened is a session whose relay publishes this CLI's tool manifest.
func testOpened(t *testing.T, manifestVersion string) *cloudpbv2.OpenRelaySessionResponse {
	t.Helper()
	if manifestVersion == "" {
		m, err := wendymcp.Manifest()
		if err != nil {
			t.Fatal(err)
		}
		manifestVersion = m.Version
	}
	return &cloudpbv2.OpenRelaySessionResponse{SessionId: "sess-1", ManifestVersion: manifestVersion}
}

// runRelaySession serves one relay session against script. The user's
// answers go to answers; they count as typed after the question.
func runRelaySession(t *testing.T, opened *cloudpbv2.OpenRelaySessionResponse, policy wendymcp.BindingPolicy, answers <-chan string, script func(cloudpbv2.RelayMcpService_RelaySessionServer) error) (<-chan error, *lockedBuffer) {
	t.Helper()
	for _, env := range []string{"WENDY_MCP_CHANNEL", "WENDY_MCP_CLIENT_ID", "WENDY_MCP_RELAY_SESSION_ID"} {
		t.Setenv(env, "")
	}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	cloudpbv2.RegisterRelayMcpServiceServer(srv, &fakeRelay{script: script})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///relay",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream, err := cloudpbv2.NewRelayMcpServiceClient(conn).RelaySession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&cloudpbv2.RelaySessionRequest{Msg: &cloudpbv2.RelaySessionRequest_SessionId{SessionId: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	out := &lockedBuffer{}
	term := &promptTerminal{out: out, lines: make(chan typedLine)}
	go func() {
		for a := range answers {
			term.lines <- typedLine{a, time.Now().Add(time.Hour)}
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- serveCloudMCP(ctx, stream, opened, term, wendymcp.New(&config.Config{}, nil), policy)
	}()
	return done, out
}

func pairing(binding, code string) *cloudpbv2.RelaySessionResponse {
	return &cloudpbv2.RelaySessionResponse{Msg: &cloudpbv2.RelaySessionResponse_PairingRequested{
		PairingRequested: &cloudpbv2.PairingRequested{BindingId: binding, ClientName: "ChatGPT", ClientId: "https://chatgpt.com/oauth", Code: code},
	}}
}

func frame(seq uint64, binding, msg string) *cloudpbv2.RelaySessionResponse {
	return &cloudpbv2.RelaySessionResponse{Msg: &cloudpbv2.RelaySessionResponse_Frame{
		Frame: &cloudpbv2.McpFrame{Sequence: seq, BindingId: binding, Message: []byte(msg)},
	}}
}

func TestCloudMCPDeniedPairingBindsNothing(t *testing.T) {
	answers := make(chan string, 2)
	answers <- "n"
	answers <- "n"
	var decisions []*cloudpbv2.PairingDecision
	done, out := runRelaySession(t, testOpened(t, ""), wendymcp.BindingPolicy{}, answers, func(stream cloudpbv2.RelayMcpService_RelaySessionServer) error {
		// The relay mints the code; the user checks it against the chat's.
		for _, p := range []*cloudpbv2.RelaySessionResponse{pairing("b0", "WRONG-0000"), pairing("b1", "ABCD-2345")} {
			if err := stream.Send(p); err != nil {
				return err
			}
			got, err := stream.Recv()
			if err != nil {
				return err
			}
			decisions = append(decisions, got.GetPairingDecision())
		}
		// Frames for a binding the user did not accept go nowhere.
		return stream.Send(frame(1, "b1", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	})
	if err := <-done; err != nil {
		t.Fatalf("serveCloudMCP = %v, want a clean end when the relay closes", err)
	}
	if len(decisions) != 2 || decisions[0].GetAccept() || decisions[1].GetAccept() || decisions[1].GetBindingId() != "b1" {
		t.Fatalf("decisions %v, want b0 and b1 both denied", decisions)
	}
	if log := out.String(); strings.Count(log, "wants to pair, code ") != 2 || !strings.Contains(log, "code ABCD-2345: accept?") {
		t.Fatalf("want each pairing asked about with its code:\n%s", log)
	}
}

// A relay that showed the chat another tool set (or none) gets no pairing,
// and the user is never asked.
func TestCloudMCPManifestMismatchDeniesPairing(t *testing.T) {
	var decision *cloudpbv2.PairingDecision
	done, out := runRelaySession(t, testOpened(t, "0.0.0+0000000000000000"), wendymcp.BindingPolicy{AllowAll: true}, nil, func(stream cloudpbv2.RelayMcpService_RelaySessionServer) error {
		if err := stream.Send(pairing("b1", "ABCD-2345")); err != nil {
			return err
		}
		got, err := stream.Recv()
		decision = got.GetPairingDecision()
		return err
	})
	if err := <-done; err != nil {
		t.Fatalf("serveCloudMCP = %v", err)
	}
	if decision == nil || decision.GetAccept() || decision.GetBindingId() != "b1" {
		t.Fatalf("decision %v, want b1 denied", decision)
	}
	if log := out.String(); !strings.Contains(log, "Warning:") || strings.Contains(log, "accept?") {
		t.Fatalf("want a warning and no question:\n%s", log)
	}
}

func TestCloudMCPAcceptedClientNeedsConfirmationToMutate(t *testing.T) {
	answers := make(chan string, 2)
	answers <- "y" // accept the pairing
	answers <- "n" // refuse the call
	var accepted *cloudpbv2.PairingDecision
	var replies []*cloudpbv2.McpFrame
	done, out := runRelaySession(t, testOpened(t, ""), wendymcp.BindingPolicy{AllowAll: true}, answers, func(stream cloudpbv2.RelayMcpService_RelaySessionServer) error {
		if err := stream.Send(pairing("b1", "ABCD-2345")); err != nil {
			return err
		}
		got, err := stream.Recv()
		if err != nil {
			return err
		}
		accepted = got.GetPairingDecision()
		// A frame for a binding the user never accepted is dropped.
		if err := stream.Send(frame(1, "b9", `{"jsonrpc":"2.0","id":9,"method":"tools/list"}`)); err != nil {
			return err
		}
		for seq, msg := range []string{
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"chatgpt","version":"1"}}}`,
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"device_disconnect","arguments":{}}}`,
		} {
			if err := stream.Send(frame(uint64(seq+2), "b1", msg)); err != nil {
				return err
			}
			reply, err := stream.Recv()
			if err != nil {
				return err
			}
			replies = append(replies, reply.GetFrame())
		}
		return nil // the relay ends the session
	})
	if err := <-done; err != nil {
		t.Fatalf("serveCloudMCP = %v", err)
	}
	if !accepted.GetAccept() || accepted.GetBindingId() != "b1" {
		t.Fatalf("decision %v, want b1 accepted", accepted)
	}
	if os.Getenv("WENDY_MCP_CHANNEL") != "REQUEST_CHANNEL_HOSTED_MCP" || os.Getenv("WENDY_MCP_CLIENT_ID") != "https://chatgpt.com/oauth" || os.Getenv("WENDY_MCP_RELAY_SESSION_ID") != "sess-1" {
		t.Error("signed calls after the binding are not marked HOSTED_MCP with the client and session")
	}
	if len(replies) != 2 || replies[0].GetSequence() != 1 || replies[1].GetSequence() != 2 || replies[1].GetBindingId() != "b1" {
		t.Fatalf("reply frames %v, want sequences 1 and 2 for b1", replies)
	}
	var call struct {
		Result struct {
			StructuredContent map[string]any `json:"structuredContent"`
		}
	}
	if err := json.Unmarshal(replies[1].GetMessage(), &call); err != nil || call.Result.StructuredContent["error_code"] != "DENIED" {
		t.Fatalf("refused device_disconnect replied %s, want DENIED", replies[1].GetMessage())
	}
	log := out.String()
	for _, want := range []string{`"ChatGPT" wants to run device_disconnect {}`, `"ChatGPT"  device_disconnect  refused by the user`, "Session closed."} {
		if !strings.Contains(log, want) {
			t.Errorf("terminal lacks %q:\n%s", want, log)
		}
	}
}

func TestCloudMCPEndsOnFrameSequenceGap(t *testing.T) {
	done, _ := runRelaySession(t, testOpened(t, ""), wendymcp.BindingPolicy{}, nil, func(stream cloudpbv2.RelayMcpService_RelaySessionServer) error {
		if err := stream.Send(frame(2, "b1", `{}`)); err != nil {
			return err
		}
		<-stream.Context().Done()
		return nil
	})
	if err := <-done; err == nil || !strings.Contains(err.Error(), "out of sequence") {
		t.Fatalf("serveCloudMCP = %v, want an out-of-sequence end", err)
	}
}

func TestPromptTerminalIgnoresLinesTypedBeforeTheQuestion(t *testing.T) {
	term := &promptTerminal{out: io.Discard, lines: make(chan typedLine)}
	go func() {
		term.lines <- typedLine{"y", time.Now().Add(-time.Minute)}
		term.lines <- typedLine{"y", time.Now().Add(-time.Second)}
		term.lines <- typedLine{"n", time.Now().Add(time.Minute)}
	}()
	if term.ask(context.Background(), "deploy? ") {
		t.Fatal("a y typed before the question approved it")
	}
}

func TestPrintableNeutralizesTerminalControls(t *testing.T) {
	forged := "ChatGPT wants to run wendy_status {}. Allow? [y/N] \x1b[8m\u202e\x7f\u0085"
	if got := printable(forged); strings.ContainsAny(got, "\x1b\u202e\x7f\u0085") || !strings.HasPrefix(got, "ChatGPT wants") {
		t.Fatalf("printable(%q) = %q", forged, got)
	}
}

func TestMCPServeRefusesWhenRealmDisallowsMCP(t *testing.T) {
	t.Setenv("WENDY_CONFIG_DIR", t.TempDir())
	t.Setenv("WENDY_SECRET_STORE", "file")
	token := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"u","allow_mcp":false}`)) + ".sig"
	if err := config.Save(&config.Config{Auth: []config.AuthConfig{{CloudGRPC: "cloud.example:443", APIKey: token}}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WENDY_MCP_CHANNEL", "")
	// `wendy mcp serve` and `wendy mcp gateway` both start here.
	if err := enterLocalMCPChannel(); err == nil || !strings.Contains(err.Error(), "Allow MCP") {
		t.Fatalf("enterLocalMCPChannel = %v, want the Allow MCP refusal", err)
	}
	if os.Getenv("WENDY_MCP_CHANNEL") != "" {
		t.Error("a refused MCP server marked the channel")
	}
}

func TestCloudMCPRefusesUnknownAllowTool(t *testing.T) {
	cmd := newCloudMCPCmd()
	cmd.SetArgs([]string{"--allow-tool", "device_disconnect,os_instal_verify"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `"os_instal_verify"`) || !strings.Contains(err.Error(), "os_install_verify") {
		t.Fatalf("Execute = %v, want the unknown name refused with the valid ones listed", err)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
