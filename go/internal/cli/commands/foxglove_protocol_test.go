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

func TestFGAppendMessageData(t *testing.T) {
	buf := make([]byte, 0, 4)
	got := fgAppendMessageData(buf[:0], 7, 0x0102030405060708, []byte{0xAA, 0xBB})
	want := fgEncodeMessageData(7, 0x0102030405060708, []byte{0xAA, 0xBB})
	if string(got) != string(want) {
		t.Fatalf("append != encode:\n got %x\nwant %x", got, want)
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
