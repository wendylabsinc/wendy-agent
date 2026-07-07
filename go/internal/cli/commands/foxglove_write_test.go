package commands

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc"

	"github.com/wendylabsinc/wendy/go/internal/cli/commands/foxglovecdr"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// --- repr parser ---

func TestParseROS2ServiceCallResponse(t *testing.T) {
	out := "requester: making request: std_srvs.srv.SetBool_Request(data=True)\n\nresponse:\nstd_srvs.srv.SetBool_Response(success=True, message='done')\n"
	got, err := parseROS2ServiceCallResponse(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := map[string]any{"success": true, "message": "done"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestParseROS2ServiceCallResponse_Nested(t *testing.T) {
	out := "response:\nfoo.srv.Foo_Response(header=std_msgs.msg.Header(frame_id='base'), values=array('d', [1.5, 2.5]), count=3)"
	got, err := parseROS2ServiceCallResponse(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	hdr, ok := got["header"].(map[string]any)
	if !ok || hdr["frame_id"] != "base" {
		t.Fatalf("nested header wrong: %#v", got["header"])
	}
	vals, ok := got["values"].([]any)
	if !ok || len(vals) != 2 || vals[0] != 1.5 {
		t.Fatalf("array wrong: %#v", got["values"])
	}
	if got["count"] != int64(3) {
		t.Fatalf("count = %#v", got["count"])
	}
}

func TestParseROS2ServiceCallResponse_Empty(t *testing.T) {
	got, err := parseROS2ServiceCallResponse("requester: making request: std_srvs.srv.Empty_Request()\n\nresponse:\nstd_srvs.srv.Empty_Response()")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty response = %#v, err %v", got, err)
	}
}

// --- binary frame layout ---

func TestFGServiceCallRoundTripFrames(t *testing.T) {
	resp := fgEncodeServiceCallResponse(3, 9, "cdr", []byte{0xAA, 0xBB})
	if resp[0] != 0x03 {
		t.Fatalf("opcode = %d, want 3", resp[0])
	}
	// A response frame has the same layout as a request; parse it as one (after
	// swapping the opcode) to check the field offsets.
	req := append([]byte(nil), resp...)
	req[0] = 0x02
	svcID, callID, enc, payload, err := fgParseServiceCallRequest(req)
	if err != nil || svcID != 3 || callID != 9 || enc != "cdr" || string(payload) != "\xAA\xBB" {
		t.Fatalf("parsed (%d,%d,%q,%x) err %v", svcID, callID, enc, payload, err)
	}
}

// --- write-path source ---

type writeSource struct {
	fakeFGSource
	reqSchema, respSchema string
	svcType               string
	lastCall              *agentpbv2.CallROS2ServiceRequest
	callResponse          string
	lastPublish           *agentpbv2.PublishROS2Request
}

func (s *writeSource) ListServices(context.Context, *agentpbv2.ListROS2ServicesRequest, ...grpc.CallOption) (*agentpbv2.ListROS2ServicesResponse, error) {
	return &agentpbv2.ListROS2ServicesResponse{Services: []*agentpbv2.ListROS2ServicesResponse_Service{{Name: "/set_bool", Types: []string{s.svcType}}}}, nil
}
func (s *writeSource) GetServiceDefinition(context.Context, *agentpbv2.GetROS2ServiceDefinitionRequest, ...grpc.CallOption) (*agentpbv2.GetROS2ServiceDefinitionResponse, error) {
	return &agentpbv2.GetROS2ServiceDefinitionResponse{Type: s.svcType, RequestSchema: s.reqSchema, ResponseSchema: s.respSchema}, nil
}
func (s *writeSource) CallService(_ context.Context, in *agentpbv2.CallROS2ServiceRequest, _ ...grpc.CallOption) (*agentpbv2.CallROS2ServiceResponse, error) {
	s.lastCall = in
	return &agentpbv2.CallROS2ServiceResponse{Success: true, Response: s.callResponse}, nil
}
func (s *writeSource) Publish(_ context.Context, in *agentpbv2.PublishROS2Request, _ ...grpc.CallOption) (*agentpbv2.PublishROS2Response, error) {
	s.lastPublish = in
	return &agentpbv2.PublishROS2Response{Success: true}, nil
}

func TestFoxgloveServiceCall(t *testing.T) {
	src := &writeSource{
		svcType:      "std_srvs/srv/SetBool",
		reqSchema:    "bool data",
		respSchema:   "bool success\nstring message",
		callResponse: "response:\nstd_srvs.srv.SetBool_Response(success=True, message='ok')",
	}
	srv := &foxgloveServer{src: src, allowControl: true}
	services, info := srv.discoverServices(context.Background())
	if len(services) != 1 || services[0].Name != "/set_bool" {
		t.Fatalf("discoverServices = %+v", services)
	}

	// Build a CDR request {data: true} with the codec and wrap it in a 0x02 frame.
	reqSchema, reqRoot, _ := foxglovecdr.ParseSchema("bool data")
	reqCDR, err := foxglovecdr.Encode(reqSchema, reqRoot, map[string]any{"data": true})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	frame := buildServiceCallRequest(1, 7, "cdr", reqCDR)

	out := make(chan *[]byte, 4)
	outText := make(chan []byte, 4)
	srv.handleServiceCall(context.Background(), frame, info, out, outText)

	// The agent must have received the request rendered as YAML.
	if src.lastCall == nil || !strings.Contains(src.lastCall.GetRequest(), "data") {
		t.Fatalf("CallService request = %+v", src.lastCall)
	}
	// We must get a 0x03 response frame carrying the encoded response.
	select {
	case p := <-out:
		f := *p
		if f[0] != 0x03 || binary.LittleEndian.Uint32(f[1:5]) != 1 || binary.LittleEndian.Uint32(f[5:9]) != 7 {
			t.Fatalf("bad response frame header: %x", f[:9])
		}
		encLen := binary.LittleEndian.Uint32(f[9:13])
		payload := f[13+encLen:]
		respSchema, respRoot, _ := foxglovecdr.ParseSchema("bool success\nstring message")
		decoded, derr := foxglovecdr.Decode(respSchema, respRoot, payload)
		if derr != nil {
			t.Fatalf("decode response: %v", derr)
		}
		if decoded["success"] != true || decoded["message"] != "ok" {
			t.Fatalf("decoded response = %#v", decoded)
		}
	default:
		select {
		case b := <-outText:
			t.Fatalf("got serviceCallFailure instead of response: %s", b)
		default:
			t.Fatal("no response frame produced")
		}
	}
}

func TestFoxgloveClientPublish(t *testing.T) {
	src := &writeSource{}
	srv := &foxgloveServer{src: src, allowControl: true}

	schema, root, _ := foxglovecdr.ParseSchema("float64 data")
	cdr, err := foxglovecdr.Encode(schema, root, map[string]any{"data": 1.5})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	channels := map[uint32]*fgClientChannel{
		5: {ID: 5, Topic: "/value", Encoding: "cdr", SchemaName: "std_msgs/msg/Float64", Schema: "float64 data", SchemaEncoding: "ros2msg"},
	}
	frame := append([]byte{0x01, 0, 0, 0, 0}, cdr...)
	binary.LittleEndian.PutUint32(frame[1:5], 5)

	srv.handleClientPublish(context.Background(), frame, channels)

	if src.lastPublish == nil {
		t.Fatal("Publish was not called")
	}
	if src.lastPublish.GetTopic() != "/value" || src.lastPublish.GetType() != "std_msgs/msg/Float64" {
		t.Fatalf("publish target = %s %s", src.lastPublish.GetTopic(), src.lastPublish.GetType())
	}
	// The client's raw CDR payload must be forwarded verbatim as Cdr — no
	// CDR->YAML decode on the CLI side anymore; the agent's native bridge
	// (with a YAML fallback of its own) handles the payload.
	if !bytes.Equal(src.lastPublish.GetCdr(), cdr) {
		t.Fatalf("publish cdr = %x, want %x", src.lastPublish.GetCdr(), cdr)
	}
	if src.lastPublish.GetYaml() != "" {
		t.Fatalf("publish yaml = %q, want empty (CDR path)", src.lastPublish.GetYaml())
	}
}

func TestFoxgloveCapabilities(t *testing.T) {
	ro := (&foxgloveServer{}).capabilities()
	for _, c := range ro {
		if c == "services" || c == "clientPublish" {
			t.Fatalf("read-only server must not advertise %q; caps=%v", c, ro)
		}
	}
	rw := (&foxgloveServer{allowControl: true}).capabilities()
	hasServices, hasPublish := false, false
	for _, c := range rw {
		hasServices = hasServices || c == "services"
		hasPublish = hasPublish || c == "clientPublish"
	}
	if !hasServices || !hasPublish {
		t.Fatalf("--allow-control server must advertise services+clientPublish; caps=%v", rw)
	}
}

// buildServiceCallRequest builds a client SERVICE_CALL_REQUEST (0x02) frame.
func buildServiceCallRequest(serviceID, callID uint32, encoding string, payload []byte) []byte {
	frame := make([]byte, 13+len(encoding)+len(payload))
	frame[0] = 0x02
	binary.LittleEndian.PutUint32(frame[1:5], serviceID)
	binary.LittleEndian.PutUint32(frame[5:9], callID)
	binary.LittleEndian.PutUint32(frame[9:13], uint32(len(encoding)))
	copy(frame[13:], encoding)
	copy(frame[13+len(encoding):], payload)
	return frame
}
