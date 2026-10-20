package netgossip

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

func TestConcurrentNeighboursClaimOnlyOneDownload(t *testing.T) {
	for _, kind := range []uint8{Manifest, Service} {
		t.Run(string(rune('0'+kind)), func(t *testing.T) {
			now := time.Now()
			source := NewPool().NewSession(kind)
			have := source.Offer([]byte("signed announcement"), now.Add(time.Minute), now)
			pool := NewPool()
			var wg sync.WaitGroup
			wants := make(chan *Message, 64)
			for range 64 {
				s := pool.NewSession(kind)
				wg.Add(1)
				go func() {
					defer wg.Done()
					reply, _, err := s.Receive(*have, now)
					if err != nil {
						t.Error(err)
					}
					if reply != nil {
						wants <- reply
					}
				}()
			}
			wg.Wait()
			close(wants)
			if len(wants) != 1 {
				t.Fatalf("%d simultaneous full-download requests", len(wants))
			}
		})
	}
}

func TestSlowProgressAndVerificationKeepAlternateSupplierIdle(t *testing.T) {
	now := time.Now()
	source := NewPool().NewSession(Service)
	body := bytes.Repeat([]byte("x"), ChunkSize*3)
	have := source.Offer(body, now.Add(2*time.Minute), now)
	pool := NewPool()
	first, other := pool.NewSession(Service), pool.NewSession(Service)
	want, _, _ := first.Receive(*have, now)
	if want == nil {
		t.Fatal("missing request")
	}
	if q, _, _ := other.Receive(*have, now); q != nil {
		t.Fatal("duplicate request")
	}
	data, _, _ := source.Receive(*want, now)
	chunks := append([]*Message{data}, source.Poll(now)...)
	if len(chunks) != 3 {
		t.Fatal("wrong chunk count", len(chunks))
	}
	for i, chunk := range chunks {
		now = now.Add(20 * time.Second)
		if _, got, err := first.Receive(*chunk, now); err != nil || i < 2 && len(got) > 0 || i == 2 && !bytes.Equal(got, body) {
			t.Fatal(i, len(got), err)
		}
		if got := other.Poll(now); len(got) != 0 {
			t.Fatal("progressing supplier was replaced", got)
		}
	}

	// Complete bytes are held pending authentication, not offered as verified.
	if !other.Pending(now) {
		t.Fatal("unverified artifact marked complete")
	}
	if got := other.Poll(now.Add(IdleTimeout)); len(got) != 0 {
		t.Fatal("certificate wait caused duplicate download")
	}
	first.Resolve(body, now.Add(time.Minute), now, true)
	if other.Pending(now) {
		t.Fatal("verified shared object still pending")
	}
	if q, _, _ := other.Receive(*have, now); q != nil {
		t.Fatal("known body downloaded again")
	}
}

func TestDisconnectStallUnavailableAndObsoleteData(t *testing.T) {
	for _, mode := range []string{"disconnect", "stall", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now()
			source := NewPool().NewSession(Manifest)
			have := source.Offer([]byte("manifest"), now.Add(time.Minute), now)
			pool := NewPool()
			first, other := pool.NewSession(Manifest), pool.NewSession(Manifest)
			old, _, _ := first.Receive(*have, now)
			other.Receive(*have, now)
			switch mode {
			case "disconnect":
				first.Close()
			case "stall":
				now = now.Add(IdleTimeout)
				if q := first.Poll(now); len(q) != 0 {
					t.Fatal("stalled supplier reclaimed before alternate")
				}
			case "unavailable":
				first.Receive(*message("unavailable", old.ID(), old.Request), now)
			}
			qs := other.Poll(now)
			if len(qs) != 1 || qs[0].Request == old.Request {
				t.Fatal("alternate did not acquire a fresh request", qs)
			}
			late, _, _ := source.Receive(*old, now)
			if mode != "disconnect" {
				if _, b, e := first.Receive(*late, now); e != nil || b != nil {
					t.Fatal("late response admitted", e)
				}
			}
			data, _, _ := source.Receive(*qs[0], now)
			_, body, e := other.Receive(*data, now)
			if e != nil || string(body) != "manifest" {
				t.Fatal(string(body), e)
			}
		})
	}
}

func TestDigestAndTypeCannotBypassValidation(t *testing.T) {
	now := time.Now()
	sender := NewPool().NewSession(Manifest)
	have := sender.Offer([]byte("body"), now.Add(time.Minute), now)
	receiver := NewPool().NewSession(Manifest)
	want, _, _ := receiver.Receive(*have, now)
	data, _, _ := sender.Receive(*want, now)
	data.Data[0] ^= 1
	if _, b, err := receiver.Receive(*data, now); err == nil || b != nil {
		t.Fatal("bad digest accepted")
	}
	other := NewPool().NewSession(Service)
	if _, _, err := other.Receive(*have, now); err == nil {
		t.Fatal("artifact type confusion")
	}
	for _, bad := range []Message{{Version: 2, Op: "have", Type: Manifest, Digest: make([]byte, 32)}, {Version: 1, Op: "have", Type: Manifest, Digest: make([]byte, 31)}, {Version: 1, Op: "data", Type: Manifest, Digest: make([]byte, 32), Request: 1, Total: MaxObject + 1, Data: []byte{1}}} {
		if bad.Validate() == nil {
			t.Fatal("malformed gossip accepted", bad)
		}
	}
}

func TestBothTypesShareBoundedDownloadBudget(t *testing.T) {
	now := time.Now()
	pool := NewPool()
	sessions := []*Session{pool.NewSession(Manifest), pool.NewSession(Service), pool.NewSession(Manifest), pool.NewSession(Service), pool.NewSession(Service)}
	count := 0
	for i, s := range sessions {
		source := NewPool().NewSession(s.kind)
		for j := range 12 {
			body := []byte{byte(i), byte(j)}
			have := source.Offer(body, now.Add(time.Minute), now)
			q, _, e := s.Receive(*have, now)
			if e != nil {
				t.Fatal(e)
			}
			if q != nil {
				count++
			}
		}
	}
	if count != maxInFlight {
		t.Fatalf("global budget: %d", count)
	}
	sessions[0].Close()
	if q := sessions[4].Poll(now); len(q) != maxPerPeer {
		t.Fatalf("released capacity not reused: %d", len(q))
	}
}

func TestEvictedBytesDoNotCauseDuplicateDownload(t *testing.T) {
	now := time.Now()
	pool := NewPool()
	s := pool.NewSession(Service)
	first := bytes.Repeat([]byte("x"), MaxObject)
	have := s.Offer(first, now.Add(time.Minute), now)
	for i := range 240 {
		b := bytes.Repeat([]byte{byte(i)}, MaxObject)
		b[0] = byte(i / 256)
		s.Offer(b, now.Add(time.Minute), now.Add(time.Duration(i)*time.Millisecond))
	}
	if pool.bytes > maxBytes {
		t.Fatal("payload bound exceeded", pool.bytes)
	}
	receiver := pool.NewSession(Service)
	if q, _, e := receiver.Receive(*have, now); e != nil || q != nil {
		t.Fatal("redownloaded evicted verified object", e)
	}
}

func TestBackpressureDoesNotAdvanceTransferOrClaim(t *testing.T) {
	now := time.Now()
	source := NewPool().NewSession(Manifest)
	body := bytes.Repeat([]byte("b"), ChunkSize*2)
	have := source.Offer(body, now.Add(time.Minute), now)
	pool := NewPool()
	a, b := pool.NewSession(Manifest), pool.NewSession(Manifest)
	want, _, _ := a.Receive(*have, now)
	b.Receive(*have, now)
	first, _, _ := source.Receive(*want, now)
	a.Receive(*first, now)
	if got := source.Poll(now, 0); len(got) != 0 {
		t.Fatal("advanced DATA into a full queue")
	}
	last := source.Poll(now, 1)
	if len(last) != 1 || last[0].Offset != ChunkSize {
		t.Fatal("skipped or duplicated chunk", last)
	}
	if _, got, err := a.Receive(*last[0], now); err != nil || !bytes.Equal(got, body) {
		t.Fatal("backpressure damaged body", err)
	}
	a.Close()
	if got := b.Poll(now, 0); len(got) != 0 {
		t.Fatal("reserved a request without queue capacity")
	}
	if got := b.Poll(now, 1); len(got) != 1 {
		t.Fatal("available claim was lost", got)
	}
}

func TestUnavailableDoesNotCompleteSnapshotOrImmediatelyRetry(t *testing.T) {
	now := time.Now()
	sender := NewPool().NewSession(Service)
	have := sender.Offer([]byte("service"), now.Add(time.Minute), now)
	receiver := NewPool().NewSession(Service)
	want, _, _ := receiver.Receive(*have, now)
	receiver.Receive(*Unavailable(*want), now)
	if !receiver.Pending(now) {
		t.Fatal("unavailable object prematurely completed snapshot")
	}
	if got := receiver.Poll(now); len(got) != 0 {
		t.Fatal("retried unavailable supplier without a fresh HAVE")
	}
	if q, _, _ := receiver.Receive(*have, now); q == nil {
		t.Fatal("new availability did not allow retry")
	}
}

func TestInvalidSupplierCannotPoisonDigestForAlternate(t *testing.T) {
	now := time.Now()
	source := NewPool().NewSession(Service)
	have := source.Offer([]byte("valid signed bytes"), now.Add(time.Minute), now)
	pool := NewPool()
	first, other := pool.NewSession(Service), pool.NewSession(Service)
	want, _, _ := first.Receive(*have, now)
	other.Receive(*have, now)
	bad, _, _ := source.Receive(*want, now)
	bad.Data[0] ^= 1
	if _, _, err := first.Receive(*bad, now); err == nil {
		t.Fatal("bad bytes accepted")
	}
	first.Close()
	next := other.Poll(now)
	if len(next) != 1 {
		t.Fatal("invalid supplier poisoned the shared digest", next)
	}
	good, _, _ := source.Receive(*next[0], now)
	if _, body, err := other.Receive(*good, now); err != nil || string(body) != "valid signed bytes" {
		t.Fatal(string(body), err)
	}
}
