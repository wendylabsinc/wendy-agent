//go:build linux

package localmesh

import "encoding/json"

const maxDirectoryReplyFrames = 32
const maxDirectoryReplyBytes = 64 << 10

// queueReplies retains requested protocol data under ordinary backpressure.
// A dropped request/chunk cannot be repaired by resending only the manifest.
// The node actor owns this bounded reserve; admitted bulk frames keep order.
func (n *Node) queueReplies(p *nodePeer, messages []ControlMessage) bool {
	n.flushReplies(p)
	bytes := 0
	for _, message := range messages {
		wire, err := json.Marshal(message)
		if err != nil {
			return false
		}
		bytes += len(wire)
	}
	if len(p.replies)+len(messages) > maxDirectoryReplyFrames || p.replyBytes+bytes > maxDirectoryReplyBytes {
		return false
	}
	p.replies = append(p.replies, messages...)
	p.replyBytes += bytes
	n.flushReplies(p)
	return true
}

func (n *Node) flushReplies(p *nodePeer) {
	for len(p.replies) != 0 {
		select {
		case p.control <- p.replies[0]:
			wire, _ := json.Marshal(p.replies[0])
			p.replyBytes -= len(wire)
			p.replies[0] = ControlMessage{}
			p.replies = p.replies[1:]
		default:
			return
		}
	}
}
