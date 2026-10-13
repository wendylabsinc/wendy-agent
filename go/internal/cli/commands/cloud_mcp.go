package commands

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/cloudrequest"
	wendymcp "github.com/wendylabsinc/wendy/go/internal/cli/mcp"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	cloudpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newCloudMCPCmd() *cobra.Command {
	var cloudGRPC string
	var allowTools []string
	var allowAll bool
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Let a web chat (ChatGPT, Claude) use Wendy through this session",
		Long: "Open a session with the hosted Wendy MCP relay and serve the wendy MCP tools to a web chat you pair with it.\n" +
			"Every call runs here and is signed with your operator key; the chat can do nothing once this command exits.\n" +
			"The chat may call read-only tools; widen that with --allow-tool or --allow-all. Every call that changes state waits for your confirmation here.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isatty.IsTerminal(os.Stdin.Fd()) {
				return fmt.Errorf("wendy cloud mcp asks you before each call that changes state, so it needs a terminal on stdin")
			}
			// OpenRelaySession goes out unmarked, whatever this process inherited.
			if err := cloudrequest.SetChannel(cloudpbv2.RequestChannel_REQUEST_CHANNEL_UNSPECIFIED, "", ""); err != nil {
				return err
			}
			auth, err := pickAuthEntry(cloudGRPC)
			if err != nil {
				return err
			}
			if err := wendymcp.RealmAllowsMCP(auth); err != nil {
				return err
			}
			srv, err := newMCPServer("", []string{"core"})
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			stream, opened, closeConn, err := openRelaySession(ctx, auth)
			if err != nil {
				return err
			}
			defer closeConn()
			term := newPromptTerminal(os.Stdin, cmd.ErrOrStderr())
			return serveCloudMCP(ctx, stream, opened, term, srv, wendymcp.BindingPolicy{AllowAll: allowAll, Tools: allowTools})
		},
	}
	cmd.Flags().StringVar(&cloudGRPC, "cloud-grpc", "", "Cloud gRPC endpoint of the session to use")
	cmd.Flags().StringSliceVar(&allowTools, "allow-tool", nil, "Also allow these tools by name (comma-separated); read-only tools are always allowed")
	cmd.Flags().BoolVar(&allowAll, "allow-all", false, "Allow every tool; calls that change state still need your confirmation")
	return cmd
}

type relayStream = cloudpbv2.RelayMcpService_RelaySessionClient

// openRelaySession opens a Relay MCP session (docs/relay-mcp.md in
// service-protos): the operator-signed OpenRelaySession, then the session's
// one stream, attached with its session_id. The stream lives with ctx.
func openRelaySession(ctx context.Context, auth *config.AuthConfig) (relayStream, *cloudpbv2.OpenRelaySessionResponse, func(), error) {
	if len(auth.Certificates) == 0 || auth.Certificates[0].TenantUUID() == "" {
		return nil, nil, nil, fmt.Errorf("wendy cloud mcp needs an operator session; run 'wendy auth login'")
	}
	tokenCtx, err := cloudContext(ctx, auth)
	if err != nil {
		return nil, nil, nil, err
	}
	conn, err := dialCloudGRPC(auth)
	if err != nil {
		return nil, nil, nil, err
	}
	opened := &cloudpbv2.OpenRelaySessionResponse{}
	err = cloudrequest.Invoke(tokenCtx, conn, auth, cloudpbv2.RelayMcpService_OpenRelaySession_FullMethodName,
		&cloudpbv2.OpenRelaySessionRequest{OrganizationId: auth.Certificates[0].TenantUUID()}, opened)
	if err == nil {
		var stream relayStream
		if stream, err = cloudpbv2.NewRelayMcpServiceClient(conn).RelaySession(tokenCtx); err == nil {
			if err = stream.Send(&cloudpbv2.RelaySessionRequest{Msg: &cloudpbv2.RelaySessionRequest_SessionId{SessionId: opened.GetSessionId()}}); err == nil {
				return stream, opened, func() { _ = conn.Close() }, nil
			}
		}
	}
	_ = conn.Close()
	switch status.Code(err) {
	case codes.PermissionDenied:
		return nil, nil, nil, fmt.Errorf("Wendy Cloud refused the relay session; your organization must allow MCP and Hosted MCP: %w", err)
	case codes.Unimplemented:
		return nil, nil, nil, fmt.Errorf("this Wendy Cloud does not run the MCP relay yet: %w", err)
	}
	return nil, nil, nil, fmt.Errorf("opening the relay session: %w", err)
}

// serveCloudMCP runs one relay session: it shows the pairing code, asks the
// user about each web client that asks to bind, and serves the first accepted
// one until the relay ends the session or ctx ends (Ctrl-C). Every cloud call
// made meanwhile carries channel HOSTED_MCP with the client and session ids.
// Strings from the relay are untrusted: they reach the terminal only through
// printable, so none can rewrite a question the user answers.
// ponytail: one binding per session, later pairings are denied; serve several
// when the desktop app needs it.
func serveCloudMCP(ctx context.Context, stream relayStream, opened *cloudpbv2.OpenRelaySessionResponse, term *promptTerminal, srv *wendymcp.Server, policy wendymcp.BindingPolicy) (result error) {
	ctx, cancel := context.WithCancel(ctx)
	relay := &relayConn{stream: stream}
	pairings := make(chan *cloudpbv2.PairingRequested)
	received := make(chan error, 1)
	go func() { received <- relay.receive(ctx, pairings) }()
	var served chan error
	defer func() {
		// Every exit stops the server and waits for it to close what it
		// opened (tunnels) before the stream and the process go.
		cancel()
		relay.closeInput()
		if served != nil {
			<-served
		}
		_ = stream.CloseSend()
		term.printf("Session closed.\n")
	}()

	term.printf("Pairing code: %s\nGive this code to the Wendy connector in your web chat. Press Ctrl-C to end the session.\n", opened.GetPairingCode())
	var client string
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-received:
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("relay session: %w", err)
		case err := <-served:
			served = nil
			return err
		case p := <-pairings:
			name, id, code := printable(p.GetClientName()), printable(p.GetClientId()), printable(p.GetCode())
			switch {
			case served != nil:
				term.printf("Denied %q: this session already serves %q.\n", name, client)
			case p.GetCode() != opened.GetPairingCode():
				term.printf("Denied %q: it presented code %q, not this session's.\n", name, code)
			case term.ask(ctx, fmt.Sprintf("%q (%s) asks to use this session with code %s. Allow? [y/N] ", name, id, code)):
				if err := cloudrequest.SetChannel(cloudpbv2.RequestChannel_REQUEST_CHANNEL_HOSTED_MCP, p.GetClientId(), opened.GetSessionId()); err != nil {
					return err
				}
				in, err := relay.bind(p.GetBindingId())
				if err != nil {
					return err
				}
				client = name
				policy.Confirm = func(ctx context.Context, tool string, args map[string]any) bool {
					shown, _ := json.Marshal(args)
					return term.ask(ctx, fmt.Sprintf("%q wants to run %s %s. Allow? [y/N] ", client, printable(tool), printable(string(shown))))
				}
				policy.Log = func(tool, outcome string) {
					term.printf("%s  %q  %s  %s\n", time.Now().Format("15:04:05"), client, printable(tool), printable(outcome))
				}
				term.printf("Serving %q. Read-only tools run without asking.\n", client)
				served = make(chan error, 1)
				go func() {
					served <- srv.Serve(ctx, &policy, in, &frameWriter{relay: relay, binding: p.GetBindingId()})
				}()
				continue
			default:
				term.printf("Denied %q.\n", name)
			}
			if err := relay.send(&cloudpbv2.RelaySessionRequest{Msg: &cloudpbv2.RelaySessionRequest_PairingDecision{
				PairingDecision: &cloudpbv2.PairingDecision{BindingId: p.GetBindingId()},
			}}); err != nil {
				return fmt.Errorf("relay session: %w", err)
			}
		}
	}
}

// printable replaces every rune a terminal could act on (controls, escape
// sequences, bidi overrides) so relay-supplied text displays as itself.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return '\uFFFD'
	}, s)
}

// relayConn is the CLI end of the session stream. Sends are serialized (a
// gRPC stream allows one sender); frames carry per-sender sequence numbers
// from 1, and a received gap or repeat ends the session.
type relayConn struct {
	stream relayStream

	sendMu sync.Mutex
	sent   uint64

	mu      sync.Mutex
	binding string // the accepted binding; frames for any other are dropped
	in      *io.PipeWriter
	out     *io.PipeReader
}

func (r *relayConn) send(req *cloudpbv2.RelaySessionRequest) error {
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	if f := req.GetFrame(); f != nil {
		r.sent++
		f.Sequence = r.sent
	}
	return r.stream.Send(req)
}

// bind accepts binding and returns the reader its MCP messages arrive on.
func (r *relayConn) bind(binding string) (io.Reader, error) {
	out, in := io.Pipe()
	r.mu.Lock()
	r.binding, r.in, r.out = binding, in, out
	r.mu.Unlock()
	err := r.send(&cloudpbv2.RelaySessionRequest{Msg: &cloudpbv2.RelaySessionRequest_PairingDecision{
		PairingDecision: &cloudpbv2.PairingDecision{BindingId: binding, Accept: true},
	}})
	if err != nil {
		return nil, fmt.Errorf("relay session: %w", err)
	}
	return out, nil
}

// closeInput ends the bound client's input, unblocking both the server
// reading it and receive writing to it.
func (r *relayConn) closeInput() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.in != nil {
		_ = r.in.Close()
		_ = r.out.Close()
	}
}

// receive routes the relay's messages until the stream ends: pairing requests
// to pairings, the accepted binding's frames to its reader.
func (r *relayConn) receive(ctx context.Context, pairings chan<- *cloudpbv2.PairingRequested) error {
	var last uint64
	for {
		msg, err := r.stream.Recv()
		if err != nil {
			return err
		}
		if p := msg.GetPairingRequested(); p != nil {
			select {
			case pairings <- p:
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		f := msg.GetFrame()
		if f == nil {
			continue
		}
		if f.GetSequence() != last+1 {
			return fmt.Errorf("relay frame %d after %d: out of sequence", f.GetSequence(), last)
		}
		last = f.GetSequence()
		r.mu.Lock()
		binding, in := r.binding, r.in
		r.mu.Unlock()
		if in == nil || f.GetBindingId() != binding {
			continue
		}
		if _, err := in.Write(append(f.GetMessage(), '\n')); err != nil {
			return err
		}
	}
}

// frameWriter sends each newline-terminated MCP message the server writes as
// one frame to binding.
type frameWriter struct {
	relay   *relayConn
	binding string
	buf     []byte
}

func (w *frameWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		msg := bytes.Clone(w.buf[:i])
		w.buf = w.buf[i+1:]
		if err := w.relay.send(&cloudpbv2.RelaySessionRequest{Msg: &cloudpbv2.RelaySessionRequest_Frame{
			Frame: &cloudpbv2.McpFrame{BindingId: w.binding, Message: msg},
		}}); err != nil {
			return 0, err
		}
	}
}

// promptTerminal serializes the session's yes/no questions and log lines on
// the user's terminal; MCP frames never touch it.
type promptTerminal struct {
	mu    sync.Mutex
	out   io.Writer
	lines chan typedLine
}

type typedLine struct {
	text string
	at   time.Time
}

func newPromptTerminal(in io.Reader, out io.Writer) *promptTerminal {
	t := &promptTerminal{out: out, lines: make(chan typedLine, 64)}
	go func() {
		// Never blocks, so each line is stamped when it arrives, not when a
		// question takes it; a full buffer drops lines nobody asked for.
		scanner := bufio.NewScanner(in)
		for scanner.Scan() {
			select {
			case t.lines <- typedLine{scanner.Text(), time.Now()}:
			default:
			}
		}
		close(t.lines)
	}()
	return t
}

func (t *promptTerminal) printf(format string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintf(t.out, format, args...)
}

// ask reports whether the user answered y or yes. A line typed before the
// question was shown cannot answer it, so a stray "y" never approves a call
// the user has not seen; ctx ending or closed input answers no.
func (t *promptTerminal) ask(ctx context.Context, question string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprint(t.out, question)
	asked := time.Now()
	for {
		select {
		case line, ok := <-t.lines:
			if !ok {
				return false
			}
			if line.at.Before(asked) {
				continue
			}
			answer := strings.ToLower(strings.TrimSpace(line.text))
			return answer == "y" || answer == "yes"
		case <-ctx.Done():
			fmt.Fprintln(t.out)
			return false
		}
	}
}
