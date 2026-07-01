package commands

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
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

// fgParameter is one Foxglove parameter (getParameters/setParameters/
// parameterValues). Value is the raw JSON value (number/bool/string/array).
// Type is optional ("byte_array" | "float64" | "float64_array").
type fgParameter struct {
	Name  string `json:"name"`
	Value any    `json:"value,omitempty"`
	Type  string `json:"type,omitempty"`
}

// fgParameterValues is the server->client op="parameterValues" reply.
type fgParameterValues struct {
	Op         string        `json:"op"`
	Parameters []fgParameter `json:"parameters"`
	ID         string        `json:"id,omitempty"`
}

// fgClientMsg is a parsed client->server JSON message. Fields are populated per
// op: subscribe/unsubscribe use Subscriptions/UnsubscribeIDs; the parameter ops
// use ParameterNames/Parameters/ID.
type fgClientMsg struct {
	Op                   string
	Subscriptions        []fgSub
	UnsubscribeIDs       []uint32
	ParameterNames       []string
	Parameters           []fgParameter
	ID                   string
	ClientChannels       []fgClientChannel // op="advertise" (client publish)
	ClientUnadvertiseIDs []uint32          // op="unadvertise"
}

// fgParseClientMessage parses a client text frame into the relevant fields.
func fgParseClientMessage(data []byte) (fgClientMsg, error) {
	var raw struct {
		Op              string            `json:"op"`
		Subscriptions   []fgSub           `json:"subscriptions"`
		SubscriptionIDs []uint32          `json:"subscriptionIds"`
		ParameterNames  []string          `json:"parameterNames"`
		Parameters      []fgParameter     `json:"parameters"`
		ID              string            `json:"id"`
		Channels        []fgClientChannel `json:"channels"`
		ChannelIDs      []uint32          `json:"channelIds"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fgClientMsg{}, err
	}
	return fgClientMsg{
		Op:                   raw.Op,
		Subscriptions:        raw.Subscriptions,
		UnsubscribeIDs:       raw.SubscriptionIDs,
		ParameterNames:       raw.ParameterNames,
		Parameters:           raw.Parameters,
		ID:                   raw.ID,
		ClientChannels:       raw.Channels,
		ClientUnadvertiseIDs: raw.ChannelIDs,
	}, nil
}

// fgAppendMessageData appends a binary MESSAGE_DATA frame to dst and returns the
// grown slice: [opcode 0x01][subscriptionId u32 LE][timestamp u64 LE][payload].
// Passing a pooled dst[:0] lets the caller avoid a per-frame allocation on the
// high-rate subscribe path.
func fgAppendMessageData(dst []byte, subID uint32, timestampNs uint64, payload []byte) []byte {
	var hdr [13]byte
	hdr[0] = 0x01
	binary.LittleEndian.PutUint32(hdr[1:5], subID)
	binary.LittleEndian.PutUint64(hdr[5:13], timestampNs)
	dst = append(dst, hdr[:]...)
	return append(dst, payload...)
}

// fgEncodeMessageData builds a binary MESSAGE_DATA frame in a fresh slice.
func fgEncodeMessageData(subID uint32, timestampNs uint64, payload []byte) []byte {
	return fgAppendMessageData(make([]byte, 0, 13+len(payload)), subID, timestampNs, payload)
}

// --- Services (P2) ---

// fgSchemaRef describes the request or response payload of a service.
type fgSchemaRef struct {
	Encoding       string `json:"encoding"`       // "cdr"
	SchemaName     string `json:"schemaName"`     // e.g. "std_srvs/srv/SetBool_Request"
	SchemaEncoding string `json:"schemaEncoding"` // "ros2msg"
	Schema         string `json:"schema"`
}

// fgService is one advertised service.
type fgService struct {
	ID       uint32      `json:"id"`
	Name     string      `json:"name"`
	Type     string      `json:"type"`
	Request  fgSchemaRef `json:"request"`
	Response fgSchemaRef `json:"response"`
}

type fgAdvertiseServices struct {
	Op       string      `json:"op"` // "advertiseServices"
	Services []fgService `json:"services"`
}

type fgUnadvertiseServices struct {
	Op         string   `json:"op"` // "unadvertiseServices"
	ServiceIDs []uint32 `json:"serviceIds"`
}

// fgServiceCallFailure is sent when a service call cannot be completed.
type fgServiceCallFailure struct {
	Op        string `json:"op"` // "serviceCallFailure"
	ServiceID uint32 `json:"serviceId"`
	CallID    uint32 `json:"callId"`
	Message   string `json:"message"`
}

// fgParseServiceCallRequest parses a client SERVICE_CALL_REQUEST (opcode 0x02):
// [0x02][serviceId u32 LE][callId u32 LE][encodingLen u32 LE][encoding][payload].
func fgParseServiceCallRequest(frame []byte) (serviceID, callID uint32, encoding string, payload []byte, err error) {
	if len(frame) < 13 || frame[0] != 0x02 {
		return 0, 0, "", nil, fmt.Errorf("not a service call request frame")
	}
	serviceID = binary.LittleEndian.Uint32(frame[1:5])
	callID = binary.LittleEndian.Uint32(frame[5:9])
	encLen := binary.LittleEndian.Uint32(frame[9:13])
	if uint64(13)+uint64(encLen) > uint64(len(frame)) {
		return 0, 0, "", nil, fmt.Errorf("service call request: truncated encoding (len %d)", encLen)
	}
	encoding = string(frame[13 : 13+encLen])
	payload = frame[13+encLen:]
	return serviceID, callID, encoding, payload, nil
}

// fgEncodeServiceCallResponse builds a SERVICE_CALL_RESPONSE (opcode 0x03) with
// the same layout as the request.
func fgEncodeServiceCallResponse(serviceID, callID uint32, encoding string, payload []byte) []byte {
	frame := make([]byte, 13+len(encoding)+len(payload))
	frame[0] = 0x03
	binary.LittleEndian.PutUint32(frame[1:5], serviceID)
	binary.LittleEndian.PutUint32(frame[5:9], callID)
	binary.LittleEndian.PutUint32(frame[9:13], uint32(len(encoding)))
	copy(frame[13:], encoding)
	copy(frame[13+len(encoding):], payload)
	return frame
}

// --- Client publish (P3) ---

// fgClientChannel is a channel the client advertises for publishing.
type fgClientChannel struct {
	ID             uint32 `json:"id"`
	Topic          string `json:"topic"`
	Encoding       string `json:"encoding"` // "cdr"
	SchemaName     string `json:"schemaName"`
	Schema         string `json:"schema"`
	SchemaEncoding string `json:"schemaEncoding"` // "ros2msg"
}

// fgParseClientMessageData parses a client CLIENT_MESSAGE_DATA frame (opcode
// 0x01, client->server): [0x01][channelId u32 LE][payload].
func fgParseClientMessageData(frame []byte) (channelID uint32, payload []byte, err error) {
	if len(frame) < 5 || frame[0] != 0x01 {
		return 0, nil, fmt.Errorf("not a client message data frame")
	}
	channelID = binary.LittleEndian.Uint32(frame[1:5])
	payload = frame[5:]
	return channelID, payload, nil
}
