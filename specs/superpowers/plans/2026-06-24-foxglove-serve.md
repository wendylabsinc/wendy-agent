# `wendy device foxglove serve` (P1, read-only) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `wendy device foxglove serve`, a CLI-hosted Foxglove WebSocket Protocol server that bridges a WendyOS device's live ROS 2 topics (raw CDR + `ros2msg` schemas) to Foxglove Studio.

**Architecture:** The CLI hosts a `foxglove.websocket.v1` server on localhost. It reaches the device over the existing mTLS gRPC `ROS2Service`, which gains two RPCs — `GetMessageDefinition` (assembles the concatenated `ros2msg` schema via recursive `ros2 interface show`) and `SubscribeRaw` (streams CDR bytes via `ros2 topic echo --raw`). Both new agent handlers reuse the existing sidecar `ExecROS2` mechanism; no sidecar image change.

**Tech Stack:** Go 1.26, gRPC, Cobra, `protoc`/`protoc-gen-go`, `github.com/coder/websocket`, ROS 2 `ros2` CLI (in the sidecar).

## Global Constraints

- Module: `github.com/wendylabsinc/wendy`; all Go code under `go/`; tests run from repo root: `go test ./go/...`.
- Go version floor: `go 1.26.4` (see `go.mod`).
- Proto regeneration: `bash go/scripts/generate-proto.sh` (requires `protoc` v7.x, `protoc-gen-go` v1.36.x, `protoc-gen-go-grpc` on `PATH`). Generated stubs land in `go/proto/gen/agentpb/v2/`; commit them.
- New agent RPCs go in package `wendy.agent.services.v2` (`Proto/wendy/agent/services/v2/ros2_service.proto`), Go import alias `agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"`.
- Reuse existing ROS 2 patterns: `s.resolveSidecars`, `s.pickSidecarForTopic`, `s.runIn`, `s.runtime.ExecROS2`, `validateROS2GraphName`. Do **not** add a new sidecar image or run non-`ros2` commands in the sidecar (`ExecROS2` only ever runs `ros2 <args>`).
- WebSocket server binds `127.0.0.1` by default. Message encoding is `cdr`; schema encoding is `ros2msg`. No data is sent to third parties.
- Follow existing error-handling idioms: agent handlers return `status.Errorf(codes.…, …)`; CLI translates transport errors via `ros2RPCError`.

---

## Task 1: Proto — add `SubscribeRaw` + `GetMessageDefinition`, regenerate stubs

**Files:**
- Modify: `Proto/wendy/agent/services/v2/ros2_service.proto`
- Regenerate: `go/proto/gen/agentpb/v2/ros2_service.pb.go`, `go/proto/gen/agentpb/v2/ros2_service_grpc.pb.go`

**Interfaces:**
- Produces (Go types, after regen): `agentpbv2.GetROS2MessageDefinitionRequest{DomainId *int32, Topic string}`, `agentpbv2.GetROS2MessageDefinitionResponse{MessageType string, Schema string}`, `agentpbv2.SubscribeRawROS2Request{DomainId *int32, Topic string}`, `agentpbv2.RawROS2Message{Cdr []byte, TimestampNs int64}`. New client methods `ROS2ServiceClient.GetMessageDefinition(...)` and `ROS2ServiceClient.SubscribeRaw(...)` (returns `grpc.ServerStreamingClient[agentpbv2.RawROS2Message]`); new server methods on `ROS2ServiceServer`.

- [ ] **Step 1: Add the RPCs and messages to the proto**

In `Proto/wendy/agent/services/v2/ros2_service.proto`, inside `service ROS2Service { … }`, after the existing `EchoTopic`/`MonitorHz` server-streaming RPCs (around line 25), add:

```proto
    // Foxglove bridge. Returns the full concatenated ros2msg schema for a
    // topic's message type so a Foxglove channel can be advertised before
    // subscribing.
    rpc GetMessageDefinition(GetROS2MessageDefinitionRequest) returns (GetROS2MessageDefinitionResponse);

    // Foxglove bridge. Streams raw CDR-serialized messages for a topic until
    // the client cancels (mirrors EchoTopic but emits bytes, not YAML).
    rpc SubscribeRaw(SubscribeRawROS2Request) returns (stream RawROS2Message);
```

At the end of the file add the message definitions:

```proto
message GetROS2MessageDefinitionRequest {
    optional int32 domain_id = 1;
    string topic = 2;
}

message GetROS2MessageDefinitionResponse {
    string message_type = 1; // e.g. "sensor_msgs/msg/Image"
    // Full concatenated ros2msg definition: the top-level message body, then
    // each non-primitive dependency, joined with the rosbag2/Foxglove separator
    // line of 80 '=' characters followed by "MSG: <pkg>/<Type>". Consumed by
    // Foxglove with schemaEncoding="ros2msg".
    string schema = 2;
}

message SubscribeRawROS2Request {
    optional int32 domain_id = 1;
    string topic = 2;
}

message RawROS2Message {
    bytes cdr = 1;          // serialized message including the 4-byte CDR encapsulation header
    int64 timestamp_ns = 2; // device receipt time (unix nanoseconds)
}
```

- [ ] **Step 2: Regenerate stubs**

Run: `bash go/scripts/generate-proto.sh`
Expected: prints "Proto generation complete!" and `git status` shows modified `go/proto/gen/agentpb/v2/ros2_service.pb.go` and `ros2_service_grpc.pb.go`.

If `protoc`/plugins are missing, install: `go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11` and `go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest`, and ensure `protoc` is installed (`brew install protobuf`).

- [ ] **Step 3: Verify it builds**

Run: `go build ./go/...`
Expected: success (the `UnimplementedROS2ServiceServer` embedding means the new methods are not yet required to compile).

- [ ] **Step 4: Commit**

```bash
git add Proto/wendy/agent/services/v2/ros2_service.proto go/proto/gen/agentpb/v2/
git commit -m "proto: add ROS2 SubscribeRaw + GetMessageDefinition for Foxglove bridge"
```

---

## Task 2: Agent — concatenated `ros2msg` schema assembler + `GetMessageDefinition`

**Files:**
- Create: `go/internal/agent/services/ros2_schema.go`
- Create: `go/internal/agent/services/ros2_schema_test.go`
- Modify: `go/internal/agent/services/ros2_service.go` (add `GetMessageDefinition` handler)

**Interfaces:**
- Consumes: `ros2SC`, `s.resolveSidecars`, `s.pickSidecarForTopic`, `s.runIn`, `validateROS2GraphName` (all in `ros2_service.go`).
- Produces: `func ros2OwnFields(show string) []string`, `func ros2ComplexTypesIn(fields []string) []string`, `func normalizeMsgType(ref string) string`, `func assembleROS2MsgSchema(rootBody string, depBodies map[string]string, order []string) string`. Handler `GetMessageDefinition`.

**Background — why this is non-trivial:** `ros2 interface show sensor_msgs/msg/Image` expands nested types **inline with tab indentation**, e.g.:

```
std_msgs/Header header
	builtin_interfaces/Time stamp
		int32 sec
		uint32 nanosec
	string frame_id
uint32 height
uint8[] data
```

Foxglove's `ros2msg` parser instead wants the **concatenated** form (each type's *own* fields, dependencies appended after an 80-`=` separator + `MSG:` header):

```
std_msgs/Header header
uint32 height
uint8[] data
================================================================================
MSG: std_msgs/Header
builtin_interfaces/Time stamp
string frame_id
================================================================================
MSG: builtin_interfaces/Time
int32 sec
uint32 nanosec
```

We reconstruct it by: for each type, run `ros2 interface show` and keep only the **indent-0** lines (a type's own fields); recurse into complex field types (those containing `/`).

- [ ] **Step 1: Write failing tests for the pure helpers**

Create `go/internal/agent/services/ros2_schema_test.go`:

```go
package services

import (
	"strings"
	"testing"
)

func TestROS2OwnFields(t *testing.T) {
	show := "std_msgs/Header header\n" +
		"\tbuiltin_interfaces/Time stamp\n" +
		"\t\tint32 sec\n" +
		"\t\tuint32 nanosec\n" +
		"\tstring frame_id\n" +
		"uint32 height\n" +
		"uint8[] data\n"
	got := ros2OwnFields(show)
	want := []string{"std_msgs/Header header", "uint32 height", "uint8[] data"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ros2OwnFields = %q, want %q", got, want)
	}
}

func TestROS2OwnFields_DropsCommentsAndBlanks(t *testing.T) {
	show := "# a comment\n\nuint8 STATUS_OK=0\nint32 value\n\t# nested comment\n\tint32 nested\n"
	got := ros2OwnFields(show)
	want := []string{"uint8 STATUS_OK=0", "int32 value"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ros2OwnFields = %q, want %q", got, want)
	}
}

func TestROS2ComplexTypesIn(t *testing.T) {
	fields := []string{"std_msgs/Header header", "uint32 height", "geometry_msgs/Point[] pts", "string name", "uint8[36] cov"}
	got := ros2ComplexTypesIn(fields)
	want := []string{"std_msgs/Header", "geometry_msgs/Point"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ros2ComplexTypesIn = %v, want %v", got, want)
	}
}

func TestNormalizeMsgType(t *testing.T) {
	cases := map[string]string{
		"std_msgs/Header":        "std_msgs/msg/Header",
		"std_msgs/msg/Header":    "std_msgs/msg/Header",
		"geometry_msgs/Point":    "geometry_msgs/msg/Point",
	}
	for in, want := range cases {
		if got := normalizeMsgType(in); got != want {
			t.Errorf("normalizeMsgType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAssembleROS2MsgSchema(t *testing.T) {
	root := "std_msgs/Header header\nuint32 height"
	deps := map[string]string{
		"std_msgs/Header":          "builtin_interfaces/Time stamp\nstring frame_id",
		"builtin_interfaces/Time":  "int32 sec\nuint32 nanosec",
	}
	order := []string{"std_msgs/Header", "builtin_interfaces/Time"}
	got := assembleROS2MsgSchema(root, deps, order)
	sep := strings.Repeat("=", 80)
	want := root + "\n" + sep + "\nMSG: std_msgs/Header\nbuiltin_interfaces/Time stamp\nstring frame_id\n" +
		sep + "\nMSG: builtin_interfaces/Time\nint32 sec\nuint32 nanosec"
	if got != want {
		t.Fatalf("assembleROS2MsgSchema =\n%q\nwant\n%q", got, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./go/internal/agent/services/ -run 'TestROS2OwnFields|TestROS2ComplexTypesIn|TestNormalizeMsgType|TestAssembleROS2MsgSchema' -v`
Expected: FAIL — undefined `ros2OwnFields`, etc.

- [ ] **Step 3: Implement the helpers**

Create `go/internal/agent/services/ros2_schema.go`:

```go
package services

import "strings"

// ros2OwnFields returns a message type's own field lines from `ros2 interface
// show` output: the lines with no leading whitespace (nested types are emitted
// indented). Blank lines and pure comments are dropped; constants and field
// lines are kept verbatim.
func ros2OwnFields(show string) []string {
	var out []string
	for _, line := range strings.Split(show, "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue // blank or nested-expansion line
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

// ros2ComplexTypesIn returns the distinct non-primitive message types referenced
// by the given field lines, in first-seen order. A type is complex iff its type
// token contains '/'. Array and bounded suffixes ("[]", "[36]", "<=10") are
// stripped before testing.
func ros2ComplexTypesIn(fields []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range fields {
		typeTok := f
		if i := strings.IndexAny(f, " \t"); i >= 0 {
			typeTok = f[:i]
		}
		// strip array / bound suffixes: "geometry_msgs/Point[]" -> "geometry_msgs/Point"
		if i := strings.IndexAny(typeTok, "[<"); i >= 0 {
			typeTok = typeTok[:i]
		}
		if !strings.Contains(typeTok, "/") {
			continue // primitive
		}
		if seen[typeTok] {
			continue
		}
		seen[typeTok] = true
		out = append(out, typeTok)
	}
	return out
}

// normalizeMsgType turns a 2-part field reference ("std_msgs/Header") into the
// 3-part form `ros2 interface show` expects ("std_msgs/msg/Header"). A type
// already carrying an interface kind segment (msg/srv/action) is returned as-is.
func normalizeMsgType(ref string) string {
	parts := strings.Split(ref, "/")
	if len(parts) == 2 {
		return parts[0] + "/msg/" + parts[1]
	}
	return ref
}

// assembleROS2MsgSchema joins a root message body and its dependency bodies into
// the concatenated ros2msg schema format Foxglove consumes. Dependencies are
// keyed by their 2-part name ("pkg/Type") and emitted in `order`.
func assembleROS2MsgSchema(rootBody string, depBodies map[string]string, order []string) string {
	sep := strings.Repeat("=", 80)
	var b strings.Builder
	b.WriteString(rootBody)
	for _, name := range order {
		b.WriteString("\n")
		b.WriteString(sep)
		b.WriteString("\nMSG: ")
		b.WriteString(name)
		b.WriteString("\n")
		b.WriteString(depBodies[name])
	}
	return b.String()
}
```

- [ ] **Step 4: Run helper tests to verify pass**

Run: `go test ./go/internal/agent/services/ -run 'TestROS2OwnFields|TestROS2ComplexTypesIn|TestNormalizeMsgType|TestAssembleROS2MsgSchema' -v`
Expected: PASS.

- [ ] **Step 5: Write a failing test for the `GetMessageDefinition` handler**

Append to `ros2_schema_test.go`:

```go
import_block_marker := 0 // (remove if gofmt complains; ensure agentpbv2 + context imported at top)
```

Add the imports `"context"` and `agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"` to the test file's import group, then add:

```go
func TestGetMessageDefinition(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble", DomainID: 0},
		outputs: map[string]string{
			"topic list":                      "/image\n",
			"topic type /image":               "sensor_msgs/msg/Image\n",
			"interface show sensor_msgs/msg/Image": "std_msgs/Header header\n\tbuiltin_interfaces/Time stamp\n\t\tint32 sec\n\tstring frame_id\nuint32 height\n",
			"interface show std_msgs/msg/Header":   "builtin_interfaces/Time stamp\n\tint32 sec\nstring frame_id\n",
			"interface show builtin_interfaces/msg/Time": "int32 sec\nuint32 nanosec\n",
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	resp, err := svc.GetMessageDefinition(context.Background(), &agentpbv2.GetROS2MessageDefinitionRequest{Topic: "/image"})
	if err != nil {
		t.Fatalf("GetMessageDefinition: %v", err)
	}
	if resp.GetMessageType() != "sensor_msgs/msg/Image" {
		t.Fatalf("type = %q", resp.GetMessageType())
	}
	if !strings.Contains(resp.GetSchema(), "MSG: std_msgs/Header") ||
		!strings.Contains(resp.GetSchema(), "MSG: builtin_interfaces/Time") {
		t.Fatalf("schema missing deps:\n%s", resp.GetSchema())
	}
	if !strings.HasPrefix(resp.GetSchema(), "std_msgs/Header header\nuint32 height") {
		t.Fatalf("root body wrong:\n%s", resp.GetSchema())
	}
}
```

Note: `pickSidecarForTopic` runs `topic list` to route; the fake returns `/image` for that key, so routing resolves to the single sidecar.

- [ ] **Step 6: Run to verify it fails**

Run: `go test ./go/internal/agent/services/ -run TestGetMessageDefinition -v`
Expected: FAIL — `svc.GetMessageDefinition` undefined.

- [ ] **Step 7: Implement the handler**

Add to `go/internal/agent/services/ros2_service.go` (near `GetTopicInfo`):

```go
func (s *ROS2Service) GetMessageDefinition(ctx context.Context, req *agentpbv2.GetROS2MessageDefinitionRequest) (*agentpbv2.GetROS2MessageDefinitionResponse, error) {
	scs, err := s.resolveSidecars(ctx, req.DomainId)
	if err != nil {
		return nil, err
	}
	if err := validateROS2GraphName(req.GetTopic()); err != nil {
		return nil, err
	}
	sc := s.pickSidecarForTopic(ctx, scs, req.GetTopic())

	typeOut, err := s.runIn(ctx, sc, "topic", "type", req.GetTopic())
	if err != nil {
		return nil, err
	}
	msgType := strings.TrimSpace(typeOut)
	if msgType == "" {
		return nil, status.Errorf(codes.NotFound, "topic %q has no resolvable type", req.GetTopic())
	}

	rootShow, err := s.runIn(ctx, sc, "interface", "show", msgType)
	if err != nil {
		return nil, err
	}
	rootFields := ros2OwnFields(rootShow)
	rootBody := strings.Join(rootFields, "\n")

	depBodies := map[string]string{}
	var order []string
	queue := ros2ComplexTypesIn(rootFields)
	for len(queue) > 0 {
		dep := queue[0]
		queue = queue[1:]
		if _, done := depBodies[dep]; done {
			continue
		}
		show, derr := s.runIn(ctx, sc, "interface", "show", normalizeMsgType(dep))
		if derr != nil {
			return nil, derr
		}
		fields := ros2OwnFields(show)
		depBodies[dep] = strings.Join(fields, "\n")
		order = append(order, dep)
		queue = append(queue, ros2ComplexTypesIn(fields)...)
	}

	return &agentpbv2.GetROS2MessageDefinitionResponse{
		MessageType: msgType,
		Schema:      assembleROS2MsgSchema(rootBody, depBodies, order),
	}, nil
}
```

- [ ] **Step 8: Run handler test + full package to verify pass**

Run: `go test ./go/internal/agent/services/ -run TestGetMessageDefinition -v && go test ./go/internal/agent/services/`
Expected: PASS (no regressions).

- [ ] **Step 9: Commit**

```bash
git add go/internal/agent/services/ros2_schema.go go/internal/agent/services/ros2_schema_test.go go/internal/agent/services/ros2_service.go
git commit -m "agent: assemble concatenated ros2msg schema + GetMessageDefinition RPC"
```

---

## Task 3: Agent — Python-bytes parser + `SubscribeRaw` handler

**Files:**
- Modify: `go/internal/agent/services/ros2_schema.go` (add `parsePythonBytesLiteral`)
- Modify: `go/internal/agent/services/ros2_schema_test.go` (parser tests)
- Modify: `go/internal/agent/services/ros2_service.go` (add `SubscribeRaw` handler)

**Interfaces:**
- Consumes: same sidecar helpers as Task 2; `io.Pipe`, `bufio.Scanner` pattern from `EchoTopic`.
- Produces: `func parsePythonBytesLiteral(s string) ([]byte, error)`; handler `SubscribeRaw(req, stream)`.

> **⚠️ ON-DEVICE VERIFICATION REQUIRED (do this before trusting Task 3):** This task assumes `ros2 topic echo --raw <topic>` prints each message as a single-line Python `bytes` repr (`b'\x00\x01…'`) followed by a `---` separator line, with the CDR encapsulation header included. **Confirm against a real device/distro early** (e.g. run `wendy device ros2 exec -- topic echo --raw /some_topic` once the `exec` passthrough is available, or via the manual step in Task 7). **If the format differs** (multi-line literal, no `b''` wrapper, or header stripped): adjust `parsePythonBytesLiteral`/the scanner accordingly. **Fallback if `--raw` is unusable at all:** write a tiny generic-subscriber helper to the sidecar at exec time (rclpy/rclcpp `GenericSubscription`) — but that is out of scope unless verification fails; record the finding and escalate before implementing it.

- [ ] **Step 1: Write failing tests for the parser**

Append to `ros2_schema_test.go`:

```go
func TestParsePythonBytesLiteral(t *testing.T) {
	cases := []struct {
		in   string
		want []byte
	}{
		{`b'\x00\x01ABC'`, []byte{0x00, 0x01, 'A', 'B', 'C'}},
		{`b'\n\t\\\''`, []byte{'\n', '\t', '\\', '\''}},
		{`b"\x00hi"`, []byte{0x00, 'h', 'i'}},
		{`b''`, []byte{}},
	}
	for _, c := range cases {
		got, err := parsePythonBytesLiteral(c.in)
		if err != nil {
			t.Fatalf("parse(%q): %v", c.in, err)
		}
		if string(got) != string(c.want) {
			t.Errorf("parse(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParsePythonBytesLiteral_Rejects(t *testing.T) {
	for _, in := range []string{"", "notbytes", "b'unterminated", "'noprefix'"} {
		if _, err := parsePythonBytesLiteral(in); err == nil {
			t.Errorf("parse(%q): expected error", in)
		}
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./go/internal/agent/services/ -run TestParsePythonBytesLiteral -v`
Expected: FAIL — undefined `parsePythonBytesLiteral`.

- [ ] **Step 3: Implement the parser**

Add to `go/internal/agent/services/ros2_schema.go`:

```go
import (
	"fmt"
	"strconv"
	"strings"
)
// (merge "fmt"/"strconv" into the existing import block; "strings" is already imported)

// parsePythonBytesLiteral decodes a Python bytes repr such as b'\x00\x01ABC'
// (single quote or double quote) into its raw bytes. It handles the escape
// forms CPython emits for bytes: \xNN, \n, \r, \t, \\, \', \", and \0.
func parsePythonBytesLiteral(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if len(s) < 3 || s[0] != 'b' || (s[1] != '\'' && s[1] != '"') {
		return nil, fmt.Errorf("not a python bytes literal: %q", s)
	}
	quote := s[1]
	if s[len(s)-1] != quote {
		return nil, fmt.Errorf("unterminated python bytes literal: %q", s)
	}
	body := s[2 : len(s)-1]
	out := make([]byte, 0, len(body))
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' {
			out = append(out, c)
			continue
		}
		i++
		if i >= len(body) {
			return nil, fmt.Errorf("trailing backslash in %q", s)
		}
		switch body[i] {
		case 'x':
			if i+2 >= len(body) {
				return nil, fmt.Errorf("truncated \\x escape in %q", s)
			}
			v, err := strconv.ParseUint(body[i+1:i+3], 16, 8)
			if err != nil {
				return nil, fmt.Errorf("bad \\x escape in %q: %w", s, err)
			}
			out = append(out, byte(v))
			i += 2
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case '\\':
			out = append(out, '\\')
		case '\'':
			out = append(out, '\'')
		case '"':
			out = append(out, '"')
		case '0':
			out = append(out, 0)
		default:
			return nil, fmt.Errorf("unsupported escape \\%c in %q", body[i], s)
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Run parser tests to verify pass**

Run: `go test ./go/internal/agent/services/ -run TestParsePythonBytesLiteral -v`
Expected: PASS.

- [ ] **Step 5: Write a failing test for the `SubscribeRaw` handler**

Append to `ros2_schema_test.go` a streaming test using a fake stream and `execFn` that emits two raw messages:

```go
// rawCollector implements grpc.ServerStreamingServer[agentpbv2.RawROS2Message].
type rawCollector struct {
	grpc.ServerStream
	ctx  context.Context
	msgs []*agentpbv2.RawROS2Message
}

func (c *rawCollector) Context() context.Context { return c.ctx }
func (c *rawCollector) Send(m *agentpbv2.RawROS2Message) error {
	c.msgs = append(c.msgs, m)
	return nil
}

func TestSubscribeRaw(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble", DomainID: 0},
		execFn: func(_ context.Context, opts ROS2ExecOptions, stdout, _ io.Writer) (int, error) {
			if strings.Join(opts.Args, " ") == "topic list" {
				io.WriteString(stdout, "/chatter\n")
				return 0, nil
			}
			// topic echo --raw /chatter
			io.WriteString(stdout, `b'\x00\x01'`+"\n---\n"+`b'\x02\x03'`+"\n---\n")
			return 0, nil
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	col := &rawCollector{ctx: context.Background()}
	if err := svc.SubscribeRaw(&agentpbv2.SubscribeRawROS2Request{Topic: "/chatter"}, col); err != nil {
		t.Fatalf("SubscribeRaw: %v", err)
	}
	if len(col.msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(col.msgs))
	}
	if string(col.msgs[0].GetCdr()) != "\x00\x01" || string(col.msgs[1].GetCdr()) != "\x02\x03" {
		t.Fatalf("payloads wrong: %v %v", col.msgs[0].GetCdr(), col.msgs[1].GetCdr())
	}
	if col.msgs[0].GetTimestampNs() == 0 {
		t.Errorf("expected a non-zero timestamp")
	}
}
```

- [ ] **Step 6: Run to verify it fails**

Run: `go test ./go/internal/agent/services/ -run TestSubscribeRaw -v`
Expected: FAIL — `svc.SubscribeRaw` undefined.

- [ ] **Step 7: Implement `SubscribeRaw`**

Add to `go/internal/agent/services/ros2_service.go` (after `EchoTopic`). It mirrors `EchoTopic` but passes `--raw` and decodes each `b'…'` blob. Ensure `"time"` is imported (it already is).

```go
func (s *ROS2Service) SubscribeRaw(req *agentpbv2.SubscribeRawROS2Request, stream grpc.ServerStreamingServer[agentpbv2.RawROS2Message]) error {
	ctx := stream.Context()
	scs, err := s.resolveSidecars(ctx, req.DomainId)
	if err != nil {
		return err
	}
	if err := validateROS2GraphName(req.GetTopic()); err != nil {
		return err
	}
	sc := s.pickSidecarForTopic(ctx, scs, req.GetTopic())

	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	pr, pw := io.Pipe()
	execDone := make(chan error, 1)
	go func() {
		_, execErr := s.runtime.ExecROS2(execCtx, ROS2ExecOptions{
			DomainID:    sc.domainID,
			SidecarName: sc.name,
			Args:        []string{"topic", "echo", "--raw", req.GetTopic()},
		}, pw, io.Discard)
		pw.CloseWithError(execErr)
		execDone <- execErr
	}()

	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == "---" {
			continue
		}
		cdr, perr := parsePythonBytesLiteral(line)
		if perr != nil {
			// A non-bytes line is unexpected with --raw; log and skip rather than
			// killing the stream.
			s.logger.Warn("subscribe_raw: unparseable line", zap.String("topic", req.GetTopic()), zap.Error(perr))
			continue
		}
		msg := &agentpbv2.RawROS2Message{Cdr: cdr, TimestampNs: time.Now().UnixNano()}
		if serr := stream.Send(msg); serr != nil {
			cancel()
			go func() { _, _ = io.Copy(io.Discard, pr) }()
			pr.CloseWithError(context.Canceled)
			<-execDone
			return serr
		}
	}
	execErr := <-execDone
	if ctx.Err() != nil {
		return nil // client cancelled; not an error
	}
	if execErr != nil {
		return status.Errorf(codes.Internal, "ros2 topic echo --raw: %v", execErr)
	}
	return nil
}
```

- [ ] **Step 8: Run handler test + full package**

Run: `go test ./go/internal/agent/services/ -run TestSubscribeRaw -v && go test ./go/internal/agent/services/`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add go/internal/agent/services/ros2_schema.go go/internal/agent/services/ros2_schema_test.go go/internal/agent/services/ros2_service.go
git commit -m "agent: SubscribeRaw streams raw CDR via ros2 topic echo --raw"
```

---

## Task 4: CLI — Foxglove protocol primitives (pure, testable)

**Files:**
- Create: `go/internal/cli/commands/foxglove_protocol.go`
- Create: `go/internal/cli/commands/foxglove_protocol_test.go`

**Interfaces:**
- Produces: `fgServerInfo`, `fgChannel`, `fgAdvertise` structs (JSON-tagged); `func fgEncodeMessageData(subID uint32, timestampNs uint64, payload []byte) []byte`; `func fgParseClientMessage(data []byte) (fgClientMsg, error)` returning `fgClientMsg{Op string; Subscriptions []fgSub; UnsubscribeIDs []uint32}` with `fgSub{ID uint32; ChannelID uint32}`.

- [ ] **Step 1: Write failing tests**

Create `go/internal/cli/commands/foxglove_protocol_test.go`:

```go
package commands

import (
	"encoding/binary"
	"encoding/json"
	"testing"
)

func TestFGEncodeMessageData(t *testing.T) {
	frame := fgEncodeMessageData(7, 0x0102030405060708, []byte{0xAA, 0xBB})
	if frame[0] != 0x01 {
		t.Fatalf("opcode = %d, want 1", frame[0])
	}
	if got := binary.LittleEndian.Uint32(frame[1:5]); got != 7 {
		t.Fatalf("subID = %d, want 7", got)
	}
	if got := binary.LittleEndian.Uint64(frame[5:13]); got != 0x0102030405060708 {
		t.Fatalf("ts = %x", got)
	}
	if string(frame[13:]) != string([]byte{0xAA, 0xBB}) {
		t.Fatalf("payload mismatch")
	}
}

func TestFGParseClientMessage_Subscribe(t *testing.T) {
	in := `{"op":"subscribe","subscriptions":[{"id":0,"channelId":3},{"id":1,"channelId":4}]}`
	msg, err := fgParseClientMessage([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Op != "subscribe" || len(msg.Subscriptions) != 2 || msg.Subscriptions[1].ChannelID != 4 {
		t.Fatalf("bad parse: %+v", msg)
	}
}

func TestFGParseClientMessage_Unsubscribe(t *testing.T) {
	msg, err := fgParseClientMessage([]byte(`{"op":"unsubscribe","subscriptionIds":[0,1]}`))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Op != "unsubscribe" || len(msg.UnsubscribeIDs) != 2 {
		t.Fatalf("bad parse: %+v", msg)
	}
}

func TestFGAdvertiseJSON(t *testing.T) {
	adv := fgAdvertise{Op: "advertise", Channels: []fgChannel{{
		ID: 1, Topic: "/x", Encoding: "cdr", SchemaName: "std_msgs/msg/String",
		Schema: "string data", SchemaEncoding: "ros2msg",
	}}}
	b, _ := json.Marshal(adv)
	var round map[string]any
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatal(err)
	}
	if round["op"] != "advertise" {
		t.Fatalf("op not serialized: %s", b)
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./go/internal/cli/commands/ -run 'TestFG' -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Implement the primitives**

Create `go/internal/cli/commands/foxglove_protocol.go`:

```go
package commands

import (
	"encoding/binary"
	"encoding/json"
)

// fgServerInfo is the first server->client message (op="serverInfo").
type fgServerInfo struct {
	Op                 string   `json:"op"`
	Name               string   `json:"name"`
	Capabilities       []string `json:"capabilities"`
	SupportedEncodings []string `json:"supportedEncodings"`
}

// fgChannel describes one advertised topic.
type fgChannel struct {
	ID             uint32 `json:"id"`
	Topic          string `json:"topic"`
	Encoding       string `json:"encoding"`
	SchemaName     string `json:"schemaName"`
	Schema         string `json:"schema"`
	SchemaEncoding string `json:"schemaEncoding"`
}

type fgAdvertise struct {
	Op       string      `json:"op"`
	Channels []fgChannel `json:"channels"`
}

type fgUnadvertise struct {
	Op         string   `json:"op"`
	ChannelIDs []uint32 `json:"channelIds"`
}

// fgSub is one client subscription (client-assigned id -> channel).
type fgSub struct {
	ID        uint32 `json:"id"`
	ChannelID uint32 `json:"channelId"`
}

// fgClientMsg is a parsed client->server JSON message (subscribe/unsubscribe).
type fgClientMsg struct {
	Op             string
	Subscriptions  []fgSub
	UnsubscribeIDs []uint32
}

// fgParseClientMessage parses a client text frame into the relevant fields.
func fgParseClientMessage(data []byte) (fgClientMsg, error) {
	var raw struct {
		Op              string   `json:"op"`
		Subscriptions   []fgSub  `json:"subscriptions"`
		SubscriptionIDs []uint32 `json:"subscriptionIds"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fgClientMsg{}, err
	}
	return fgClientMsg{Op: raw.Op, Subscriptions: raw.Subscriptions, UnsubscribeIDs: raw.SubscriptionIDs}, nil
}

// fgEncodeMessageData builds a binary MESSAGE_DATA frame:
// [opcode 0x01][subscriptionId u32 LE][timestamp u64 LE][payload].
func fgEncodeMessageData(subID uint32, timestampNs uint64, payload []byte) []byte {
	frame := make([]byte, 13+len(payload))
	frame[0] = 0x01
	binary.LittleEndian.PutUint32(frame[1:5], subID)
	binary.LittleEndian.PutUint64(frame[5:13], timestampNs)
	copy(frame[13:], payload)
	return frame
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./go/internal/cli/commands/ -run 'TestFG' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/foxglove_protocol.go go/internal/cli/commands/foxglove_protocol_test.go
git commit -m "cli: Foxglove websocket protocol primitives (frames, messages)"
```

---

## Task 5: CLI — Foxglove server + `foxglove serve` command

**Files:**
- Create: `go/internal/cli/commands/foxglove.go`
- Modify: `go.mod`, `go.sum` (add `github.com/coder/websocket`)

**Interfaces:**
- Consumes: `newROS2Client`, `ros2RPCError`, `ros2DomainPtr`, `agentpbv2` types/client, the protocol primitives from Task 4.
- Produces: `func newFoxgloveCmd() *cobra.Command`; `type foxgloveSource interface { … }` (the minimal slice of `ROS2ServiceClient` the server needs); `type foxgloveServer struct{…}` with `func (s *foxgloveServer) handleConn(ctx, c)` and `func (s *foxgloveServer) discoverChannels(ctx) ([]fgChannel, error)`.

- [ ] **Step 1: Add the websocket dependency**

Run: `go get github.com/coder/websocket@latest && go mod tidy`
Expected: `go.mod` gains `github.com/coder/websocket`.

> Decision (from spec): default to `coder/websocket` + hand-rolled protocol. If a quick check shows Foxglove's official Go `ws-protocol` server lib is a clean fit for advertise/subscribe, it may be substituted — but that is optional and not required for P1.

- [ ] **Step 2: Write the server + command**

Create `go/internal/cli/commands/foxglove.go`:

```go
package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/spf13/cobra"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

const fgSubprotocol = "foxglove.websocket.v1"

// foxgloveSource is the slice of the ROS2 gRPC client the server depends on.
// Narrowed to an interface so tests can supply a fake without the full client.
type foxgloveSource interface {
	ListTopics(ctx context.Context, in *agentpbv2.ListROS2TopicsRequest, opts ...grpcCallOption) (*agentpbv2.ListROS2TopicsResponse, error)
	GetMessageDefinition(ctx context.Context, in *agentpbv2.GetROS2MessageDefinitionRequest, opts ...grpcCallOption) (*agentpbv2.GetROS2MessageDefinitionResponse, error)
	SubscribeRaw(ctx context.Context, in *agentpbv2.SubscribeRawROS2Request, opts ...grpcCallOption) (fgRawStream, error)
}
```

> **NOTE for implementer:** `agentpbv2.ROS2ServiceClient`'s methods take `...grpc.CallOption` and `SubscribeRaw` returns `grpc.ServerStreamingClient[agentpbv2.RawROS2Message]`. To let the real client satisfy `foxgloveSource` *and* keep the fake tiny, define the aliases used above in this file:
>
> ```go
> type grpcCallOption = grpc.CallOption                                  // import "google.golang.org/grpc"
> type fgRawStream = grpc.ServerStreamingClient[agentpbv2.RawROS2Message]
> ```
>
> The real `*ros2Client.client` value satisfies `foxgloveSource` directly because method sets match. Add `"google.golang.org/grpc"` to imports.

Continue in `foxglove.go`:

```go
type foxgloveServer struct {
	src      foxgloveSource
	domainID *int32
	topics   []string // explicit filter; empty = all
}

// discoverChannels lists topics (filtered) and fetches each message schema,
// assigning a stable channel id per topic. Channels whose schema fails to load
// are skipped (logged to stderr) so one bad topic does not block the rest.
func (s *foxgloveServer) discoverChannels(ctx context.Context) ([]fgChannel, map[uint32]string, error) {
	resp, err := s.src.ListTopics(ctx, &agentpbv2.ListROS2TopicsRequest{DomainId: s.domainID})
	if err != nil {
		return nil, nil, ros2RPCError(err)
	}
	allow := map[string]bool{}
	for _, t := range s.topics {
		allow[t] = true
	}
	names := make([]string, 0, len(resp.GetTopics()))
	for _, t := range resp.GetTopics() {
		if len(allow) == 0 || allow[t.GetName()] {
			names = append(names, t.GetName())
		}
	}
	sort.Strings(names)

	var channels []fgChannel
	chTopic := map[uint32]string{}
	var id uint32 = 1
	for _, name := range names {
		def, derr := s.src.GetMessageDefinition(ctx, &agentpbv2.GetROS2MessageDefinitionRequest{DomainId: s.domainID, Topic: name})
		if derr != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: %v\n", name, ros2RPCError(derr))
			continue
		}
		channels = append(channels, fgChannel{
			ID: id, Topic: name, Encoding: "cdr",
			SchemaName: def.GetMessageType(), Schema: def.GetSchema(), SchemaEncoding: "ros2msg",
		})
		chTopic[id] = name
		id++
	}
	return channels, chTopic, nil
}

// handleConn drives one Foxglove client connection.
func (s *foxgloveServer) handleConn(ctx context.Context, c *websocket.Conn) {
	defer c.Close(websocket.StatusNormalClosure, "")

	// Serialize all writes through one goroutine (coder/websocket allows a
	// single concurrent writer).
	out := make(chan []byte, 64)
	outText := make(chan []byte, 64)
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-connCtx.Done():
				return
			case b := <-outText:
				if err := c.Write(connCtx, websocket.MessageText, b); err != nil {
					cancel()
					return
				}
			case b := <-out:
				if err := c.Write(connCtx, websocket.MessageBinary, b); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	// serverInfo then advertise.
	info, _ := json.Marshal(fgServerInfo{Op: "serverInfo", Name: "wendy", Capabilities: []string{}, SupportedEncodings: []string{"cdr"}})
	outText <- info
	channels, chTopic, err := s.discoverChannels(connCtx)
	if err != nil {
		c.Close(websocket.StatusInternalError, err.Error())
		cancel()
		wg.Wait()
		return
	}
	adv, _ := json.Marshal(fgAdvertise{Op: "advertise", Channels: channels})
	outText <- adv

	// subscriptionID -> cancel for its SubscribeRaw stream.
	subs := map[uint32]context.CancelFunc{}
	var subsMu sync.Mutex

	for {
		typ, data, rerr := c.Read(connCtx)
		if rerr != nil {
			break
		}
		if typ != websocket.MessageText {
			continue
		}
		msg, perr := fgParseClientMessage(data)
		if perr != nil {
			continue
		}
		switch msg.Op {
		case "subscribe":
			for _, sub := range msg.Subscriptions {
				topic, ok := chTopic[sub.ChannelID]
				if !ok {
					continue
				}
				streamCtx, streamCancel := context.WithCancel(connCtx)
				subsMu.Lock()
				subs[sub.ID] = streamCancel
				subsMu.Unlock()
				wg.Add(1)
				go func(subID uint32, topic string, sctx context.Context) {
					defer wg.Done()
					s.pump(sctx, subID, topic, out)
				}(sub.ID, topic, streamCtx)
			}
		case "unsubscribe":
			subsMu.Lock()
			for _, id := range msg.UnsubscribeIDs {
				if cancelFn, ok := subs[id]; ok {
					cancelFn()
					delete(subs, id)
				}
			}
			subsMu.Unlock()
		}
	}
	cancel()
	wg.Wait()
}

// pump opens a SubscribeRaw stream and forwards each message as a binary frame.
func (s *foxgloveServer) pump(ctx context.Context, subID uint32, topic string, out chan<- []byte) {
	stream, err := s.src.SubscribeRaw(ctx, &agentpbv2.SubscribeRawROS2Request{DomainId: s.domainID, Topic: topic})
	if err != nil {
		fmt.Fprintf(os.Stderr, "subscribe %s: %v\n", topic, ros2RPCError(err))
		return
	}
	for {
		m, rerr := stream.Recv()
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "stream %s ended: %v\n", topic, ros2RPCError(rerr))
			}
			return
		}
		ts := uint64(m.GetTimestampNs())
		if ts == 0 {
			ts = uint64(time.Now().UnixNano())
		}
		frame := fgEncodeMessageData(subID, ts, m.GetCdr())
		select {
		case <-ctx.Done():
			return
		case out <- frame:
		}
	}
}

func newFoxgloveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "foxglove",
		Short: "Stream device ROS 2 data to Foxglove Studio",
		Long: `Bridge a device's live ROS 2 topics to Foxglove Studio.

'serve' hosts a Foxglove WebSocket Protocol server on your machine; connect
Foxglove Studio to ws://localhost:<port> via "Open connection".`,
	}
	cmd.AddCommand(newFoxgloveServeCmd())
	return cmd
}

func newFoxgloveServeCmd() *cobra.Command {
	var (
		port     int
		host     string
		domain   int32
		topics   []string
		poll     time.Duration
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start a Foxglove WebSocket server bridging the device's ROS 2 topics",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			client, err := newROS2Client(ctx)
			if err != nil {
				return err
			}
			defer client.Close()

			srv := &foxgloveServer{src: client.client, domainID: ros2DomainPtr(domain), topics: topics}
			_ = poll // re-discovery loop is a follow-up; see plan note.

			httpSrv := &http.Server{
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					c, aerr := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{fgSubprotocol}})
					if aerr != nil {
						return
					}
					if c.Subprotocol() != fgSubprotocol {
						c.Close(websocket.StatusProtocolError, "client must speak "+fgSubprotocol)
						return
					}
					srv.handleConn(r.Context(), c)
				}),
			}

			ln, lerr := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
			if lerr != nil {
				return lerr
			}
			fmt.Printf("Foxglove server listening on ws://%s — open this in Foxglove Studio\n", ln.Addr())

			go func() { <-ctx.Done(); _ = httpSrv.Close() }()
			if serr := httpSrv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
				return serr
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", 8765, "WebSocket listen port")
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "Bind address")
	cmd.Flags().Int32Var(&domain, "domain", -1, "ROS_DOMAIN_ID override (default: from the app's ros2 config)")
	cmd.Flags().StringSliceVar(&topics, "topic", nil, "Restrict to these topics (repeatable; default: all)")
	cmd.Flags().DurationVar(&poll, "poll", 5*time.Second, "Topic re-discovery interval (0 disables; reserved for follow-up)")
	return cmd
}
```

> **Plan note (scope guard / YAGNI):** P1 advertises channels once at connect time. Periodic re-discovery (`--poll`) and `unadvertise` on topic disappearance are deferred (flag is wired but inert, documented in `--poll` help). This keeps the connection logic simple and is explicitly listed as a deferred decision in the spec.

- [ ] **Step 3: Verify it builds**

Run: `go build ./go/...`
Expected: success. If `*ros2Client.client` does not satisfy `foxgloveSource`, reconcile the `grpcCallOption`/`fgRawStream` aliases with the generated client signatures (check `go/proto/gen/agentpb/v2/ros2_service_grpc.pb.go` for the exact `SubscribeRaw`/`GetMessageDefinition` signatures) and adjust the interface to match.

- [ ] **Step 4: Commit**

```bash
git add go/internal/cli/commands/foxglove.go go.mod go.sum
git commit -m "cli: foxglove serve hosts a Foxglove websocket bridge to ROS 2"
```

---

## Task 6: Register the command + protocol integration test

**Files:**
- Modify: `go/internal/cli/commands/device.go:71-76` (monitor group)
- Create: `go/internal/cli/commands/foxglove_test.go`

**Interfaces:**
- Consumes: `newFoxgloveCmd`, `foxgloveServer`, `foxgloveSource`, protocol primitives.

- [ ] **Step 1: Register the command**

In `go/internal/cli/commands/device.go`, add `newFoxgloveCmd()` to the `monitor` group:

```go
	addToGroup("monitor",
		newDeviceLogsCmd(),
		newDeviceDashboardCmd(),
		newDeviceTelemetryStreamCmd(),
		newROS2Cmd(),
		newFoxgloveCmd(),
	)
```

- [ ] **Step 2: Write a failing end-to-end protocol test**

Create `go/internal/cli/commands/foxglove_test.go`. It starts the server against a fake `foxgloveSource`, connects a real `coder/websocket` client, and asserts the serverInfo → advertise → (subscribe) → message-data sequence.

```go
package commands

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// --- fake source ---

type fakeRawStream struct {
	msgs []*agentpbv2.RawROS2Message
	i    int
	ctx  context.Context
}

func (f *fakeRawStream) Recv() (*agentpbv2.RawROS2Message, error) {
	if f.i >= len(f.msgs) {
		<-f.ctx.Done() // block until cancelled, like a live stream awaiting more
		return nil, io.EOF
	}
	m := f.msgs[f.i]
	f.i++
	return m, nil
}

// implement the rest of grpc.ServerStreamingClient[...] as no-ops:
func (f *fakeRawStream) Header() (interface{ Get(string) []string }, error) { return nil, nil }

type fakeFGSource struct{ ctx context.Context }

func (s fakeFGSource) ListTopics(context.Context, *agentpbv2.ListROS2TopicsRequest, ...grpcCallOption) (*agentpbv2.ListROS2TopicsResponse, error) {
	return &agentpbv2.ListROS2TopicsResponse{Topics: []*agentpbv2.ROS2Topic{{Name: "/chatter"}}}, nil
}
func (s fakeFGSource) GetMessageDefinition(context.Context, *agentpbv2.GetROS2MessageDefinitionRequest, ...grpcCallOption) (*agentpbv2.GetROS2MessageDefinitionResponse, error) {
	return &agentpbv2.GetROS2MessageDefinitionResponse{MessageType: "std_msgs/msg/String", Schema: "string data"}, nil
}
func (s fakeFGSource) SubscribeRaw(ctx context.Context, _ *agentpbv2.SubscribeRawROS2Request, _ ...grpcCallOption) (fgRawStream, error) {
	return &fakeRawStream{ctx: ctx, msgs: []*agentpbv2.RawROS2Message{{Cdr: []byte{0xDE, 0xAD}, TimestampNs: 42}}}, nil
}
```

> **NOTE:** `fgRawStream = grpc.ServerStreamingClient[...]` has more methods than `Recv()`. To keep the fake minimal, in `foxglove.go` define `fgRawStream` as a **local one-method interface** instead of the grpc alias:
>
> ```go
> type fgRawStream interface{ Recv() (*agentpbv2.RawROS2Message, error) }
> ```
>
> The real `grpc.ServerStreamingClient[agentpbv2.RawROS2Message]` satisfies this (it has `Recv`). Then the fake only needs `Recv`. Remove the `Header()` stub above. This is the recommended shape — prefer it over implementing the full grpc stream interface.

Continue the test:

```go
func startTestServer(t *testing.T, src foxgloveSource) (string, func()) {
	t.Helper()
	srv := &foxgloveServer{src: src}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{fgSubprotocol}})
		if err != nil {
			return
		}
		srv.handleConn(r.Context(), c)
	}))
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	return url, ts.Close
}

func TestFoxgloveServer_Handshake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url, closeFn := startTestServer(t, fakeFGSource{ctx: ctx})
	defer closeFn()

	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{fgSubprotocol}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	// serverInfo
	_, info, _ := c.Read(ctx)
	var si map[string]any
	json.Unmarshal(info, &si)
	if si["op"] != "serverInfo" {
		t.Fatalf("first msg op = %v, want serverInfo", si["op"])
	}
	// advertise
	_, advRaw, _ := c.Read(ctx)
	var adv fgAdvertise
	json.Unmarshal(advRaw, &adv)
	if len(adv.Channels) != 1 || adv.Channels[0].Topic != "/chatter" {
		t.Fatalf("advertise = %+v", adv)
	}
	ch := adv.Channels[0]

	// subscribe to that channel
	sub, _ := json.Marshal(map[string]any{"op": "subscribe", "subscriptions": []map[string]any{{"id": 99, "channelId": ch.ID}}})
	if err := c.Write(ctx, websocket.MessageText, sub); err != nil {
		t.Fatal(err)
	}

	// expect one binary MESSAGE_DATA frame for sub id 99 with payload 0xDEAD
	typ, frame, rerr := c.Read(ctx)
	if rerr != nil {
		t.Fatalf("read message-data: %v", rerr)
	}
	if typ != websocket.MessageBinary || frame[0] != 0x01 {
		t.Fatalf("expected binary MESSAGE_DATA, got type=%v op=%d", typ, frame[0])
	}
	if binary.LittleEndian.Uint32(frame[1:5]) != 99 {
		t.Fatalf("subID = %d, want 99", binary.LittleEndian.Uint32(frame[1:5]))
	}
	if string(frame[13:]) != string([]byte{0xDE, 0xAD}) {
		t.Fatalf("payload = % x, want DE AD", frame[13:])
	}
}
```

- [ ] **Step 3: Run to verify it fails, then passes after the `fgRawStream` interface change**

Run: `go test ./go/internal/cli/commands/ -run 'TestFoxgloveServer' -v`
Expected initially: FAIL/compile error guiding you to make `fgRawStream` a one-method interface (per the NOTE). After applying that change in `foxglove.go`: PASS.

- [ ] **Step 4: Verify the command is wired**

Run: `go run ./go/cmd/wendy device foxglove serve --help`
Expected: help text shows `--port`, `--host`, `--domain`, `--topic`, `--poll`.

- [ ] **Step 5: Full build + test sweep**

Run: `go build ./go/... && go test ./go/internal/cli/commands/ ./go/internal/agent/services/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/commands/device.go go/internal/cli/commands/foxglove.go go/internal/cli/commands/foxglove_test.go
git commit -m "cli: register foxglove command + protocol integration test"
```

---

## Task 7: On-device verification + docs

**Files:**
- Modify: any CLI docs/help index if present (check `go/internal/cli/assets/docs/`); otherwise none.

- [ ] **Step 1: Verify `ros2 topic echo --raw` format on a real device (gates Task 3 assumptions)**

With a device running a ROS 2 app, run:
`go run ./go/cmd/wendy device ros2 exec -- topic echo --raw <topic>`
Expected: each message printed as `b'…'` (single line) separated by `---`.
**If the format differs**, fix `parsePythonBytesLiteral`/the `SubscribeRaw` scanner (Task 3) and re-run its unit tests. Record findings in the PR description.

- [ ] **Step 2: Acceptance test against Foxglove Studio**

1. `go run ./go/cmd/wendy device foxglove serve` (note the printed `ws://127.0.0.1:8765`).
2. In Foxglove Studio (desktop or app.foxglove.dev) → "Open connection" → Foxglove WebSocket → `ws://localhost:8765`.
3. Confirm: topics appear in the panel's topic list; add a **Raw Messages** panel on a topic and see live messages; add one structured panel (e.g. **Plot** on a numeric field, or **Image** if an image topic exists) and confirm it renders.
Expected: live data flows; no schema-parse errors in Foxglove's problems panel.

- [ ] **Step 3: Document the command**

If `go/internal/cli/assets/docs/` (or a README) lists device subcommands, add a short `wendy device foxglove serve` entry mirroring the `ros2` entries. If no such index exists, the Cobra `--help` text added in Task 5 is the documentation; skip.

- [ ] **Step 4: Commit (if anything changed)**

```bash
git add -A
git commit -m "docs: document wendy device foxglove serve; record --raw verification"
```

---

## Self-Review (completed by plan author)

- **Spec coverage:** CLI command + flags (Task 5/6) ✓; real Foxglove WS protocol — serverInfo/advertise/MESSAGE_DATA/subscribe (Tasks 4–6) ✓; raw CDR via `SubscribeRaw`/`--raw` (Tasks 1,3) ✓; `ros2msg` schema via recursive `interface show` (Tasks 1,2) ✓; no sidecar image change (Tasks 2,3 reuse `ExecROS2`) ✓; localhost default (Task 5) ✓; error isolation per topic/stream (Tasks 5) ✓; testing strategy — parser, schema, frame, protocol-sequencing (Tasks 2–6) ✓; `--raw` risk + manual verification (Task 3 warning, Task 7) ✓. Deferred per spec: `--poll` re-discovery and `unadvertise` (flagged inert in Task 5) — consistent with spec's deferred decisions.
- **Placeholder scan:** none — every code step carries complete code. The `import_block_marker` line in Task 2 Step 5 is intentionally annotated to be removed; instructions are explicit.
- **Type consistency:** `foxgloveSource`/`fgRawStream` reconciled in Task 5 NOTE and Task 6 NOTE (one-method interface); `fgEncodeMessageData`, `fgParseClientMessage`, `fgChannel`, `fgAdvertise`, `fgServerInfo` names match across Tasks 4–6; agent types (`RawROS2Message.Cdr/TimestampNs`, `GetROS2MessageDefinitionResponse.MessageType/Schema`) match the proto in Task 1.
