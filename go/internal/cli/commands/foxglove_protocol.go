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
	Op             string
	Subscriptions  []fgSub
	UnsubscribeIDs []uint32
	ParameterNames []string
	Parameters     []fgParameter
	ID             string
}

// fgParseClientMessage parses a client text frame into the relevant fields.
func fgParseClientMessage(data []byte) (fgClientMsg, error) {
	var raw struct {
		Op              string        `json:"op"`
		Subscriptions   []fgSub       `json:"subscriptions"`
		SubscriptionIDs []uint32      `json:"subscriptionIds"`
		ParameterNames  []string      `json:"parameterNames"`
		Parameters      []fgParameter `json:"parameters"`
		ID              string        `json:"id"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fgClientMsg{}, err
	}
	return fgClientMsg{
		Op:             raw.Op,
		Subscriptions:  raw.Subscriptions,
		UnsubscribeIDs: raw.SubscriptionIDs,
		ParameterNames: raw.ParameterNames,
		Parameters:     raw.Parameters,
		ID:             raw.ID,
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
