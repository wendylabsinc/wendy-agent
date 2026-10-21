package udpmessage

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

func testPeer(t *testing.T, n int) meshid.Device {
	t.Helper()
	d, err := meshid.DeviceFromPrincipal(fmt.Sprintf("spiffe://wendy.sh/tenant/11111111-1111-4111-8111-111111111111/device/00000000-0000-4000-8000-%012d", n))
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func pass(send func() error) error { return send() }
func manualManager(t *testing.T) (*Manager, *time.Time) {
	t.Helper()
	m := NewManager()
	m.once.Do(func() { close(m.done) })
	now := time.Unix(100, 0)
	m.now = func() time.Time { return now }
	t.Cleanup(m.Close)
	return m, &now
}
func endpoint(t *testing.T, m *Manager, n int) *Endpoint {
	t.Helper()
	e, err := m.Open(testPeer(t, n), pass)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func assertEmpty(t *testing.T, m *Manager) {
	t.Helper()
	s := m.Snapshot()
	if s.RXBytes != 0 || s.TXBytes != 0 || s.RXMessages != 0 || s.TXMessages != 0 {
		t.Fatalf("leaked reservations: %+v", s)
	}
}

func TestCanonicalGeometryAndFullCeiling(t *testing.T) {
	for _, maximum := range []int{MinChunkSize, MaxChunkSize} {
		for _, n := range []int{0, 1, 235, 236, 980, 1000, 1472, 8192, MaxMessageSize} {
			payload := bytes.Repeat([]byte{byte(n)}, n)
			stride := maximum - HeaderSize
			count := max(1, (n+stride-1)/stride)
			for i := range count {
				b := encode(1, payload, stride, i)
				f, err := decode(b, maximum)
				if err != nil || f.total != n || f.count != count || len(b) > maximum {
					t.Fatalf("%d/%d/%d: %+v %v", maximum, n, i, f, err)
				}
			}
		}
	}
	good := encode(1, bytes.Repeat([]byte{1}, 1001), 980, 0)
	for _, mutate := range []func([]byte){func(b []byte) { b[2] = 1 }, func(b []byte) { b[3] = 1 }, func(b []byte) { clear(b[4:12]) }, func(b []byte) { binary.BigEndian.PutUint16(b[12:14], 65535) }, func(b []byte) { binary.BigEndian.PutUint16(b[14:16], 235) }, func(b []byte) { binary.BigEndian.PutUint16(b[16:18], 2) }, func(b []byte) { binary.BigEndian.PutUint16(b[18:20], 1) }} {
		b := bytes.Clone(good)
		mutate(b)
		if _, err := decode(b, 1000); err == nil {
			t.Fatalf("accepted malformed %x", b[:20])
		}
	}
	if _, err := decode(good[:len(good)-1], 1000); err == nil {
		t.Fatal("accepted truncated chunk")
	}
}

func TestReorderDuplicatesConflictsAndTerminalIDs(t *testing.T) {
	m, now := manualManager(t)
	e := endpoint(t, m, 1)
	p := bytes.Repeat([]byte{71}, MaxMessageSize)
	count := (len(p) + 979) / 980
	for i := count - 1; i >= 0; i-- {
		frame := encode(1, p, 980, i)
		got, done, err := e.ReceiveFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if !done || !bytes.Equal(got, p) {
				t.Fatal("full message lost")
			}
		} else if done {
			t.Fatal("partial delivery")
		}
		if i > 0 {
			if _, done, _ := e.ReceiveFrame(frame); done {
				t.Fatal("duplicate delivery")
			}
		}
	}
	if _, done, _ := e.ReceiveFrame(encode(1, p, 980, 0)); done {
		t.Fatal("terminal message resurrected")
	}
	assertEmpty(t, m)
	// A conflicting duplicate poisons the ID, including all future fragments.
	_, _, _ = e.ReceiveFrame(encode(2, p, 980, 0))
	bad := encode(2, p, 980, 0)
	bad[20] ^= 1
	_, _, _ = e.ReceiveFrame(bad)
	for i := 1; i < count; i++ {
		if _, done, _ := e.ReceiveFrame(encode(2, p, 980, i)); done {
			t.Fatal("conflict delivered")
		}
	}
	assertEmpty(t, m)
	_, _, _ = e.ReceiveFrame(encode(3, p, 980, 0))
	*now = now.Add(Lifetime - time.Second)
	_, _, _ = e.ReceiveFrame(encode(3, p, 980, 0))
	*now = now.Add(time.Second)
	_, _, _ = e.ReceiveFrame(encode(3, p, 980, 1))
	assertEmpty(t, m)
	// A valid new ID advances the fixed window. IDs below the floor stay rejected.
	if _, done, _ := e.ReceiveFrame(encode(WindowSize+10, nil, 980, 0)); !done {
		t.Fatal("empty message not delivered")
	}
	if _, done, _ := e.ReceiveFrame(encode(4, nil, 980, 0)); done {
		t.Fatal("old ID accepted")
	}
	// Invalid geometry must never move the high watermark.
	old := e.high
	bad = encode(1, nil, 980, 0)
	binary.BigEndian.PutUint64(bad[4:12], ^uint64(0))
	bad[3] = 1
	_, _, _ = e.ReceiveFrame(bad)
	if e.high != old {
		t.Fatal("malformed frame advanced window")
	}
	s := m.Snapshot()
	if s.ConflictingMessages != 1 || s.ExpiredMessages != 1 || s.DuplicateChunks == 0 || s.LateChunks == 0 || s.ReassembledMessages != 2 {
		t.Fatalf("stats %+v", s)
	}
}

func TestBudgetsEvictBoundedlyAndRelease(t *testing.T) {
	m, _ := manualManager(t)
	e := endpoint(t, m, 1)
	p := make([]byte, MaxMessageSize)
	for range 4 {
		if err := e.Send(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Send(p); !errors.Is(err, ErrCapacity) {
		t.Fatalf("byte limit: %v", err)
	}
	e.Close()
	assertEmpty(t, m)
	e = endpoint(t, m, 1)
	for range FlowTXMessages {
		if err := e.Send(nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Send(nil); !errors.Is(err, ErrCapacity) {
		t.Fatalf("zero-size count limit: %v", err)
	}
	e.Close()
	assertEmpty(t, m)
	partial := bytes.Repeat([]byte{5}, 1001)
	// Exhaust node count with tiny incomplete messages, each peer below a full
	// message's bytes. A new peer must still make progress via bounded eviction.
	for peer := 1; peer <= NodeRXMessages/FlowRXMessages; peer++ {
		e := endpoint(t, m, peer)
		for id := uint64(1); id <= FlowRXMessages; id++ {
			_, _, err := e.ReceiveFrame(encode(id, partial, 980, 0))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if m.Snapshot().RXMessages != NodeRXMessages {
		t.Fatal(m.Snapshot())
	}
	fresh := endpoint(t, m, 100)
	_, _, err := fresh.ReceiveFrame(encode(1, partial, 980, 0))
	if err != nil || len(fresh.assemblies) != 1 || m.Snapshot().EvictedMessages != 1 {
		t.Fatalf("fresh peer starved: %+v %v", m.Snapshot(), err)
	}
	m.Close()
	assertEmpty(t, m)
}

func TestSchedulerQueueFullProgressFairnessAndNoRetransmit(t *testing.T) {
	m, now := manualManager(t)
	a, b := endpoint(t, m, 1), endpoint(t, m, 2)
	slots := 0
	seen := map[string]bool{}
	var admitted []fragment
	var peerOrder []int
	attach := func(e *Endpoint, peer int) {
		if err := e.SetTransport(1000, false, func(frame []byte) error {
			if slots >= 32 {
				return ErrQueueFull
			}
			f, err := decode(frame, 1000)
			if err != nil {
				t.Fatal(err)
			}
			key := fmt.Sprintf("%d/%d/%d", peer, f.id, f.index)
			if seen[key] {
				t.Fatal("retransmitted accepted fragment", key)
			}
			seen[key] = true
			slots++
			admitted = append(admitted, f)
			peerOrder = append(peerOrder, peer)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	attach(a, 1)
	attach(b, 2)
	if err := a.Send(make([]byte, MaxMessageSize)); err != nil {
		t.Fatal(err)
	}
	if err := a.Send([]byte("later")); err != nil {
		t.Fatal(err)
	}
	if err := b.Send([]byte("other peer")); err != nil {
		t.Fatal(err)
	}
	for step := 0; step < 1000 && m.Snapshot().TXMessages > 0; step++ {
		m.step()
		if step%40 == 39 {
			slots = 0
			*now = now.Add(100 * time.Millisecond)
		}
	}
	if m.Snapshot().TXMessages != 0 || len(admitted) != 69 || m.Snapshot().TransportQueueRefusals == 0 {
		t.Fatalf("did not drain full message: %+v admitted=%d", m.Snapshot(), len(admitted))
	}
	if peerOrder[0] == peerOrder[1] {
		t.Fatal("other peer starved")
	}
	smallAt := -1
	lastLarge := -1
	for i, f := range admitted {
		if peerOrder[i] == 1 && f.id == 2 {
			smallAt = i
		}
		if peerOrder[i] == 1 && f.id == 1 {
			lastLarge = i
		}
	}
	if smallAt < 0 || smallAt > 3 || smallAt >= lastLarge {
		t.Fatalf("small later message starved: %d/%d", smallAt, lastLarge)
	}
	assertEmpty(t, m)
}

func TestSchedulerAbsoluteExpiryRevocationAndPathReduction(t *testing.T) {
	m, now := manualManager(t)
	e := endpoint(t, m, 1)
	calls := 0
	if err := e.SetTransport(1000, false, func([]byte) error { calls++; return ErrQueueFull }); err != nil {
		t.Fatal(err)
	}
	_ = e.Send(make([]byte, MaxMessageSize))
	m.step()
	*now = now.Add(Lifetime)
	m.step()
	assertEmpty(t, m)
	if calls != 1 {
		t.Fatal("expired message emitted")
	}
	e = endpoint(t, m, 2)
	authorized := true
	e.guard = func(send func() error) error {
		if !authorized {
			return ErrClosed
		}
		return send()
	}
	calls = 0
	_ = e.SetTransport(1000, false, func([]byte) error { calls++; return nil })
	_ = e.Send(make([]byte, MaxMessageSize))
	m.step()
	authorized = false
	m.step()
	if calls != 1 || !e.closed.Load() {
		t.Fatal("revoked fragments escaped")
	}
	assertEmpty(t, m)
	e = endpoint(t, m, 3)
	calls = 0
	_ = e.SetTransport(1000, false, func(frame []byte) error {
		calls++
		if calls == 1 {
			return &ChunkTooLarge{Maximum: 500}
		}
		if len(frame) > 500 {
			t.Fatal("did not reduce stride")
		}
		return nil
	})
	_ = e.Send(make([]byte, 1000))
	for range 4 {
		m.step()
	}
	assertEmpty(t, m)
	// A local path shrink must not narrow the independent receive capacity.
	if _, done, err := e.ReceiveFrame(encode(1, make([]byte, 980), 980, 0)); err != nil || !done {
		t.Fatal("TX reduction affected RX", err)
	}
	e = endpoint(t, m, 4)
	calls = 0
	_ = e.SetTransport(1000, false, func([]byte) error {
		calls++
		if calls == 2 {
			return &ChunkTooLarge{Maximum: 500}
		}
		return nil
	})
	_ = e.Send(make([]byte, 2000))
	for range 4 {
		m.step()
	}
	if calls != 2 {
		t.Fatal("partially emitted message retried")
	}
	assertEmpty(t, m)
}

func TestConcurrentSendCloseAndAccounting(t *testing.T) {
	m := NewManager()
	e := endpoint(t, m, 1)
	_ = e.SetTransport(1000, false, func([]byte) error { return ErrQueueFull })
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				_ = e.Send(make([]byte, 1000))
			}
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); e.Close() }()
	wg.Wait()
	m.Close()
	assertEmpty(t, m)
}

func FuzzFragmentDecoder(f *testing.F) {
	f.Add(encode(1, nil, 980, 0))
	f.Add(encode(1, make([]byte, MaxMessageSize), 980, 66))
	f.Add([]byte("WAU1"))
	f.Fuzz(func(t *testing.T, b []byte) {
		v, err := decode(b, MaxChunkSize)
		if err != nil {
			return
		}
		if v.count < 1 || v.count > MaxFragments || v.index >= v.count || v.index*v.stride+len(v.data) > v.total {
			t.Fatalf("unsafe geometry %+v", v)
		}
	})
}

func TestSocketBudgetsAndConcurrentRetirement(t *testing.T) {
	m, _ := manualManager(t)
	var held []*Endpoint
	// Eight peers at their 16MiB allowance fill the node's 128MiB allowance.
	for peer := 1; peer <= 8; peer++ {
		for range 8 {
			e := endpoint(t, m, peer)
			if err := e.ReserveSocketBytes(FlowSocketBytes); err != nil {
				t.Fatal(err)
			}
			held = append(held, e)
		}
		extra := endpoint(t, m, peer)
		if !errors.Is(extra.ReserveSocketBytes(1), ErrCapacity) {
			t.Fatal("peer socket limit ignored")
		}
		extra.Close()
	}
	extra := endpoint(t, m, 100)
	if !errors.Is(extra.ReserveSocketBytes(1), ErrCapacity) {
		t.Fatal("node socket limit ignored")
	}
	held[0].Close()
	if err := extra.ReserveSocketBytes(FlowSocketBytes); err != nil {
		t.Fatal("retired socket charge leaked", err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); extra.Close() }()
	}
	wg.Wait()
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); m.Close() }()
	}
	wg.Wait()
	if m.Snapshot().SocketBytes != 0 || m.Snapshot().Flows != 0 {
		t.Fatal(m.Snapshot())
	}
	assertEmpty(t, m)
}

func TestFinalDeliveryJoinsCloseAndNeverNestsGuard(t *testing.T) {
	m, _ := manualManager(t)
	e := endpoint(t, m, 1)
	entered, finish, done, closed := make(chan struct{}), make(chan struct{}), make(chan error, 1), make(chan struct{})
	go func() { done <- e.Deliver(func() error { close(entered); <-finish; return nil }) }()
	<-entered
	go func() { e.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("close passed admitted local delivery")
	case <-time.After(20 * time.Millisecond):
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not join delivery")
	}
	called := false
	if err := e.Deliver(func() error { called = true; return nil }); !errors.Is(err, ErrClosed) || called {
		t.Fatal("retired endpoint delivered")
	}
	if m.Snapshot().LocalDeliveryAdmissions != 1 {
		t.Fatal(m.Snapshot())
	}
}

func TestUnadmittedMessagesRestrideAfterAnotherMessageShrinksPath(t *testing.T) {
	m, now := manualManager(t)
	e := endpoint(t, m, 1)
	rx := endpoint(t, m, 2)
	first := true
	delivered := 0
	_ = e.SetTransport(1000, false, func(frame []byte) error {
		f, err := decode(frame, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if f.id == 1 && first {
			first = false
			return ErrQueueFull
		}
		if len(frame) > 500 {
			return &ChunkTooLarge{Maximum: 500}
		}
		_, complete, err := rx.ReceiveFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		if complete {
			delivered++
		}
		return nil
	})
	_ = e.Send(make([]byte, 2000))
	_ = e.Send(make([]byte, 2000))
	m.step()
	m.step()
	*now = now.Add(100 * time.Millisecond)
	for range 20 {
		m.step()
	}
	if delivered != 2 || m.Snapshot().SizeDrops != 0 || e.maximum != 500 {
		t.Fatalf("unadmitted message dropped after capacity change: delivered=%d %+v", delivered, m.Snapshot())
	}
	assertEmpty(t, m)
}
