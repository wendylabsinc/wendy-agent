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
