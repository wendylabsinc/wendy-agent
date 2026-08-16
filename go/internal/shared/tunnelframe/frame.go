// Package tunnelframe defines a protocol-agnostic datagram-tunnel frame,
// decoupled from any generated proto type, so the flow-table/ICMP-echo
// relay engine and the CLI's UDP-forward/ping loops can be shared between
// the cloud tunnel broker path and the LAN device tunnel path. Each side
// provides a thin adapter converting its own generated proto stream type
// to/from Frame.
package tunnelframe

// Frame is one message in either direction of a datagram-tunnel session.
// Exactly one field is set.
type Frame struct {
	Datagram    *Datagram
	IcmpRequest *IcmpEchoRequest
	IcmpReply   *IcmpEchoReply
}

// Datagram is one UDP datagram, multiplexed by client-assigned FlowID.
type Datagram struct {
	FlowID, Port uint32
	Payload      []byte
}

// IcmpEchoRequest asks the agent to answer as itself: a reply proves the
// agent is alive with a true end-to-end RTT. No ICMP socket is involved.
type IcmpEchoRequest struct {
	Identifier, Sequence uint32
	Payload              []byte
	OriginateUnixNs      uint64
}

// IcmpEchoReply echoes an IcmpEchoRequest's Identifier/Sequence/Payload/
// OriginateUnixNs verbatim and stamps AgentUnixNs at reply time.
type IcmpEchoReply struct {
	Identifier, Sequence         uint32
	Payload                      []byte
	OriginateUnixNs, AgentUnixNs uint64
}

// Stream is the transport-agnostic seam the shared relay engine and CLI
// loops run against.
type Stream interface {
	Send(Frame) error
	Recv() (Frame, error)
}
