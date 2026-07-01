package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/cli/commands/foxglovecdr"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// fgServiceInfo caches the parsed request/response schemas for one advertised
// service so a call can decode the CDR request and encode the CDR response
// without re-fetching or re-parsing.
type fgServiceInfo struct {
	name       string
	typ        string
	reqSchema  map[string]*foxglovecdr.Message
	reqRoot    *foxglovecdr.Message
	respSchema map[string]*foxglovecdr.Message
	respRoot   *foxglovecdr.Message
}

// discoverServices lists services (in the configured domain), fetches each
// service's request/response schemas, and returns the advertise payload plus a
// serviceID -> info map for handling calls. Services whose schema fails to load
// or parse are skipped (logged) so one bad service never blocks the rest.
func (s *foxgloveServer) discoverServices(ctx context.Context) ([]fgService, map[uint32]*fgServiceInfo) {
	resp, err := s.src.ListServices(ctx, &agentpbv2.ListROS2ServicesRequest{DomainId: s.domainID})
	if err != nil {
		fmt.Fprintf(os.Stderr, "foxglove: list services: %v\n", ros2RPCError(err))
		return nil, nil
	}
	names := make([]string, 0, len(resp.GetServices()))
	for _, svc := range resp.GetServices() {
		names = append(names, svc.GetName())
	}
	sort.Strings(names)

	var advertised []fgService
	info := map[uint32]*fgServiceInfo{}
	var id uint32 = 1
	for _, name := range names {
		def, derr := s.src.GetServiceDefinition(ctx, &agentpbv2.GetROS2ServiceDefinitionRequest{DomainId: s.domainID, Service: name})
		if derr != nil {
			fmt.Fprintf(os.Stderr, "foxglove: skipping service %s: %v\n", name, ros2RPCError(derr))
			continue
		}
		reqSchema, reqRoot, rerr := foxglovecdr.ParseSchema(def.GetRequestSchema())
		respSchema, respRoot, perr := foxglovecdr.ParseSchema(def.GetResponseSchema())
		if rerr != nil || perr != nil {
			fmt.Fprintf(os.Stderr, "foxglove: skipping service %s: schema parse failed (%v / %v)\n", name, rerr, perr)
			continue
		}
		advertised = append(advertised, fgService{
			ID: id, Name: name, Type: def.GetType(),
			Request:  fgSchemaRef{Encoding: "cdr", SchemaEncoding: "ros2msg", SchemaName: def.GetType() + "_Request", Schema: def.GetRequestSchema()},
			Response: fgSchemaRef{Encoding: "cdr", SchemaEncoding: "ros2msg", SchemaName: def.GetType() + "_Response", Schema: def.GetResponseSchema()},
		})
		info[id] = &fgServiceInfo{name: name, typ: def.GetType(), reqSchema: reqSchema, reqRoot: reqRoot, respSchema: respSchema, respRoot: respRoot}
		id++
	}
	return advertised, info
}

// handleServiceCall processes one SERVICE_CALL_REQUEST binary frame: decode the
// CDR request, call the service via the agent (YAML), encode the response back
// to CDR, and enqueue a SERVICE_CALL_RESPONSE. Any failure enqueues a
// serviceCallFailure text message instead — never a malformed CDR response.
func (s *foxgloveServer) handleServiceCall(ctx context.Context, frame []byte, info map[uint32]*fgServiceInfo, outBin chan<- *[]byte, outText chan<- []byte) {
	svcID, callID, _, payload, err := fgParseServiceCallRequest(frame)
	if err != nil {
		return
	}
	fail := func(msg string) {
		if b, mErr := json.Marshal(fgServiceCallFailure{Op: "serviceCallFailure", ServiceID: svcID, CallID: callID, Message: msg}); mErr == nil {
			select {
			case outText <- b:
			case <-ctx.Done():
			}
		}
	}
	si, ok := info[svcID]
	if !ok {
		fail(fmt.Sprintf("unknown service id %d", svcID))
		return
	}
	reqVal, err := foxglovecdr.Decode(si.reqSchema, si.reqRoot, payload)
	if err != nil {
		fail(fmt.Sprintf("decode request: %v", err))
		return
	}
	yamlReq, err := foxglovecdr.ToYAML(reqVal)
	if err != nil {
		fail(fmt.Sprintf("render request: %v", err))
		return
	}
	resp, err := s.src.CallService(ctx, &agentpbv2.CallROS2ServiceRequest{DomainId: s.domainID, Service: si.name, Type: si.typ, Request: yamlReq})
	if err != nil {
		fail(ros2RPCError(err).Error())
		return
	}
	if !resp.GetSuccess() {
		fail(resp.GetResponse())
		return
	}
	respVal, err := parseROS2ServiceCallResponse(resp.GetResponse())
	if err != nil {
		fail(fmt.Sprintf("parse response %q: %v", resp.GetResponse(), err))
		return
	}
	respCDR, err := foxglovecdr.Encode(si.respSchema, si.respRoot, respVal)
	if err != nil {
		fail(fmt.Sprintf("encode response: %v", err))
		return
	}
	out := fgEncodeServiceCallResponse(svcID, callID, "cdr", respCDR)
	select {
	case outBin <- &out:
	case <-ctx.Done():
	}
}

// handleClientPublish processes one CLIENT_MESSAGE_DATA binary frame: decode the
// CDR payload using the client-advertised channel's schema and publish it via
// the agent (`ros2 topic pub --once`).
func (s *foxgloveServer) handleClientPublish(ctx context.Context, frame []byte, channels map[uint32]*fgClientChannel) {
	channelID, payload, err := fgParseClientMessageData(frame)
	if err != nil {
		return
	}
	ch, ok := channels[channelID]
	if !ok {
		fmt.Fprintf(os.Stderr, "foxglove: publish to unknown client channel %d\n", channelID)
		return
	}
	schema, root, perr := foxglovecdr.ParseSchema(ch.Schema)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "foxglove: publish %s: schema parse: %v\n", ch.Topic, perr)
		return
	}
	val, derr := foxglovecdr.Decode(schema, root, payload)
	if derr != nil {
		fmt.Fprintf(os.Stderr, "foxglove: publish %s: decode: %v\n", ch.Topic, derr)
		return
	}
	yamlMsg, yerr := foxglovecdr.ToYAML(val)
	if yerr != nil {
		fmt.Fprintf(os.Stderr, "foxglove: publish %s: render: %v\n", ch.Topic, yerr)
		return
	}
	resp, err := s.src.Publish(ctx, &agentpbv2.PublishROS2Request{DomainId: s.domainID, Topic: ch.Topic, Type: fgSchemaNameToType(ch.SchemaName), Yaml: yamlMsg})
	if err != nil {
		fmt.Fprintf(os.Stderr, "foxglove: publish %s: %v\n", ch.Topic, ros2RPCError(err))
		return
	}
	if !resp.GetSuccess() {
		fmt.Fprintf(os.Stderr, "foxglove: publish %s failed: %s\n", ch.Topic, resp.GetMessage())
	}
}

// fgSchemaNameToType maps a Foxglove ROS 2 schema name to the type `ros2 topic
// pub` expects. Foxglove uses "pkg/msg/Type"; if a client sends the 2-part
// "pkg/Type", insert the "msg" segment.
func fgSchemaNameToType(schemaName string) string {
	parts := strings.Split(schemaName, "/")
	if len(parts) == 2 {
		return parts[0] + "/msg/" + parts[1]
	}
	return schemaName
}

// parseROS2ServiceCallResponse extracts and parses the "response:" section of
// `ros2 service call` output, which is a Python object repr such as:
//
//	response:
//	std_srvs.srv.SetBool_Response(success=True, message='done')
//
// into a value map suitable for foxglovecdr.Encode. Best-effort: it handles
// nested constructors, lists, array('x',[...]) numeric arrays, quoted strings,
// True/False/None, and numbers.
func parseROS2ServiceCallResponse(out string) (map[string]any, error) {
	body := out
	if i := strings.Index(out, "response:"); i >= 0 {
		body = out[i+len("response:"):]
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return map[string]any{}, nil // Empty-response service
	}
	p := &reprParser{s: body}
	v, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("response is not a message")
	}
	return m, nil
}

// reprParser is a small recursive-descent parser for Python object reprs.
type reprParser struct {
	s string
	i int
}

func (p *reprParser) ws() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\n' || p.s[p.i] == '\t' || p.s[p.i] == '\r') {
		p.i++
	}
}

func (p *reprParser) parseValue() (any, error) {
	p.ws()
	if p.i >= len(p.s) {
		return nil, fmt.Errorf("unexpected end of repr")
	}
	c := p.s[p.i]
	switch {
	case c == '\'' || c == '"':
		return p.parseString()
	case c == '[':
		return p.parseList()
	case c == '-' || (c >= '0' && c <= '9'):
		return p.parseNumber()
	default:
		return p.parseIdentValue()
	}
}

func (p *reprParser) parseString() (any, error) {
	quote := p.s[p.i]
	p.i++
	var b strings.Builder
	for p.i < len(p.s) {
		ch := p.s[p.i]
		if ch == '\\' && p.i+1 < len(p.s) {
			p.i++
			b.WriteByte(p.s[p.i])
			p.i++
			continue
		}
		if ch == quote {
			p.i++
			return b.String(), nil
		}
		b.WriteByte(ch)
		p.i++
	}
	return nil, fmt.Errorf("unterminated string")
}

func (p *reprParser) parseNumber() (any, error) {
	start := p.i
	for p.i < len(p.s) {
		ch := p.s[p.i]
		if (ch >= '0' && ch <= '9') || ch == '.' || ch == '-' || ch == '+' || ch == 'e' || ch == 'E' {
			p.i++
			continue
		}
		break
	}
	tok := p.s[start:p.i]
	if strings.ContainsAny(tok, ".eE") {
		f, err := strconv.ParseFloat(tok, 64)
		return f, err
	}
	n, err := strconv.ParseInt(tok, 10, 64)
	return n, err
}

func (p *reprParser) parseList() (any, error) {
	p.i++ // consume '['
	out := []any{}
	for {
		p.ws()
		if p.i >= len(p.s) {
			return nil, fmt.Errorf("unterminated list")
		}
		if p.s[p.i] == ']' {
			p.i++
			return out, nil
		}
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.ws()
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
		}
	}
}

// parseIdentValue handles True/False/None and dotted identifiers that begin a
// constructor call: Ident(.Ident)*(args) or array('x', [..]).
func (p *reprParser) parseIdentValue() (any, error) {
	start := p.i
	for p.i < len(p.s) {
		ch := p.s[p.i]
		if ch == '(' || ch == ',' || ch == ')' || ch == ']' {
			break
		}
		p.i++
	}
	ident := strings.TrimSpace(p.s[start:p.i])
	// array('d', [1.0, 2.0]) -> the list.
	if strings.HasPrefix(ident, "array") && p.i < len(p.s) && p.s[p.i] == '(' {
		return p.parseArrayCall()
	}
	if p.i < len(p.s) && p.s[p.i] == '(' {
		return p.parseConstructorArgs()
	}
	switch ident {
	case "True":
		return true, nil
	case "False":
		return false, nil
	case "None", "":
		return nil, nil
	}
	return ident, nil // bare enum/token
}

// parseArrayCall parses array('typecode', [values]) and returns the value list.
func (p *reprParser) parseArrayCall() (any, error) {
	p.i++                                     // consume '('
	if _, err := p.parseValue(); err != nil { // typecode string
		return nil, err
	}
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == ',' {
		p.i++
	}
	list, err := p.parseValue() // the [..] list
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == ')' {
		p.i++
	}
	return list, nil
}

// parseConstructorArgs parses (field=value, ...) into a map.
func (p *reprParser) parseConstructorArgs() (any, error) {
	p.i++ // consume '('
	out := map[string]any{}
	for {
		p.ws()
		if p.i >= len(p.s) {
			return nil, fmt.Errorf("unterminated constructor")
		}
		if p.s[p.i] == ')' {
			p.i++
			return out, nil
		}
		// field name up to '='
		nameStart := p.i
		for p.i < len(p.s) && p.s[p.i] != '=' && p.s[p.i] != ')' {
			p.i++
		}
		if p.i >= len(p.s) || p.s[p.i] != '=' {
			return nil, fmt.Errorf("expected '=' in constructor args")
		}
		field := strings.TrimSpace(p.s[nameStart:p.i])
		p.i++ // consume '='
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		out[field] = v
		p.ws()
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
		}
	}
}
