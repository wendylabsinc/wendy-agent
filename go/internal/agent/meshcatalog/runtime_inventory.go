package meshcatalog

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/wendylabsinc/WendyOS/go/babel"

	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

const inventoryRepairTimeout = 15 * time.Second
const maxInventoryQueueFrames = 16
const maxInventoryQueueBytes = 128 << 10

type catalogPeer struct {
	id           babel.LinkID
	device       meshid.Device
	syncer       *Synchronizer
	inventory    receivedInventory
	initial      []inventoryMessage
	records      []SignedRecord
	completed    bool
	pendingSince time.Time
	started      time.Time
	out          chan []byte
	queued       atomic.Int64
	replies      [][]byte
	replyBytes   int
	cancel       context.CancelFunc
}

func (r *Runtime) syncPeers(ctx context.Context) {
	live := make(map[babel.LinkID]bool)
	for _, info := range r.hub.Peers() {
		live[info.ID] = true
		if r.peers[info.ID] != nil {
			continue
		}
		peerCtx, cancel := context.WithCancel(ctx)
		p := &catalogPeer{id: info.ID, device: info.Device, syncer: NewSynchronizer(r.catalog, r.catalog.cache), initial: inventoryMessages(r.catalog.cache.Fingerprints(time.Now(), maxInventoryEntries)), out: make(chan []byte, maxInventoryQueueFrames), cancel: cancel, started: time.Now()}
		r.peers[info.ID] = p
		go func() {
			for {
				select {
				case <-peerCtx.Done():
					return
				case data := <-p.out:
					// This waits for room in the neighbour's bounded control queue,
					// shared with routing and application traffic. Queue delay is
					// not a failed link: the transport bounds physical writes and
					// peerCtx cancels on disconnect or runtime shutdown. Finish
					// every message in order rather than timing out halfway and
					// closing the shared authenticated connection.
					err := r.hub.Send(peerCtx, p.id, data)
					p.queued.Add(-int64(len(data)))
					r.signal()
					if err != nil {
						r.hub.ClosePeer(p.id)
						return
					}
				}
			}
		}()
	}
	for id, p := range r.peers {
		if !live[id] {
			p.cancel()
			p.syncer.Close()
			delete(r.peers, id)
		}
	}
}
func (r *Runtime) queue(p *catalogPeer, messages ...inventoryMessage) bool {
	frames := make([][]byte, 0, len(messages))
	for _, m := range messages {
		b, err := inventoryWire(m)
		if err != nil {
			return false
		}
		frames = append(frames, b)
	}
	return r.queueFrames(p, frames)
}

func (r *Runtime) queueFrames(p *catalogPeer, frames [][]byte) bool {
	if len(p.out)+len(frames) > cap(p.out) {
		return false
	}
	bytes := 0
	for _, frame := range frames {
		bytes += len(frame)
	}
	if p.queued.Load()+int64(bytes) > maxInventoryQueueBytes {
		return false
	}
	p.queued.Add(int64(bytes))
	for _, b := range frames {
		p.out <- b
	}
	return true
}

// Replies get a bounded backlog separate from the bulk inventory writer.
// Normal backpressure must not discard a requested certificate chunk or close
// the shared routing connection. Sustained protocol overload remains bounded.
func (r *Runtime) queueReplies(p *catalogPeer, messages []Message) bool {
	if len(messages) == 0 {
		return true
	}
	frames := make([][]byte, 0, len(messages))
	bytes := 0
	for _, m := range messages {
		frame, err := inventoryWire(inventoryMessage{Message: m})
		if err != nil {
			return false
		}
		frames = append(frames, frame)
		bytes += len(frame)
	}
	if len(p.replies) == 0 && r.queueFrames(p, frames) {
		return true
	}
	if len(p.replies)+len(frames) > maxInventoryQueueFrames || p.replyBytes+bytes > maxInventoryQueueBytes {
		return false
	}
	p.replies = append(p.replies, frames...)
	p.replyBytes += bytes
	return true
}
func (r *Runtime) queueMessages(p *catalogPeer, messages []Message) bool {
	batch := make([]inventoryMessage, 0, len(messages))
	for _, m := range messages {
		batch = append(batch, inventoryMessage{Message: m})
	}
	return r.queue(p, batch...)
}
func (r *Runtime) sendRecord(p *catalogPeer, w SignedRecord) bool {
	// Record's historical API commits optimistically. Restore both send maps if
	// the atomic batch cannot enter the reliable writer queue.
	var rec Record
	if json.Unmarshal(w.Body, &rec) != nil {
		return true
	}
	old, had := p.syncer.sentRecords[rec.Key]
	oldSent := p.syncer.sent
	sent := make(map[string]bool, len(oldSent))
	for k, v := range oldSent {
		sent[k] = v
	}
	p.syncer.sent = sent
	msgs := p.syncer.Record(w, time.Now())
	if r.queueMessages(p, msgs) {
		return true
	}
	p.syncer.sent = oldSent
	if had {
		p.syncer.sentRecords[rec.Key] = old
	} else {
		delete(p.syncer.sentRecords, rec.Key)
	}
	return false
}
func (r *Runtime) pump(p *catalogPeer) {
	for len(p.replies) > 0 {
		if !r.queueFrames(p, p.replies[:1]) {
			return
		}
		p.replyBytes -= len(p.replies[0])
		p.replies[0] = nil
		p.replies = p.replies[1:]
	}
	for len(p.initial) > 0 {
		if !r.queue(p, p.initial[0]) {
			return
		}
		p.initial = p.initial[1:]
	}
	if !p.inventory.done {
		return
	}
	for len(p.records) > 0 {
		if !r.sendRecord(p, p.records[0]) {
			return
		}
		p.records[0] = SignedRecord{}
		p.records = p.records[1:]
	}
	if !p.completed && r.queue(p, inventoryMessage{Message: Message{Kind: "snapshot-done"}}) {
		p.completed = true
	}
}
