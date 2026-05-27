package services

import (
	"context"
	"testing"
	"time"
)

func TestFrameSlot_PutThenTake(t *testing.T) {
	s := newFrameSlot()
	s.put([]byte{0x42})
	frame, ok := s.take(context.Background())
	if !ok {
		t.Fatal("take returned ok=false")
	}
	if len(frame) != 1 || frame[0] != 0x42 {
		t.Errorf("expected frame [0x42], got %v", frame)
	}
}

func TestFrameSlot_DropsOldestUnconsumedFrame(t *testing.T) {
	s := newFrameSlot()
	// Three frames produced before the consumer takes any: only the freshest
	// must survive — the older two are dropped.
	s.put([]byte{0x01})
	s.put([]byte{0x02})
	s.put([]byte{0x03})

	frame, ok := s.take(context.Background())
	if !ok {
		t.Fatal("take returned ok=false")
	}
	if len(frame) != 1 || frame[0] != 0x03 {
		t.Errorf("expected freshest frame [0x03], got %v", frame)
	}

	// Nothing stale remains after the freshest frame is consumed.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, ok := s.take(ctx); ok {
		t.Error("expected no second frame after consuming the freshest")
	}
}

func TestFrameSlot_TakeBlocksUntilPut(t *testing.T) {
	s := newFrameSlot()
	got := make(chan []byte, 1)
	go func() {
		frame, ok := s.take(context.Background())
		if ok {
			got <- frame
		}
	}()

	// take must still be blocked: nothing has been produced yet.
	select {
	case <-got:
		t.Fatal("take returned before any frame was put")
	case <-time.After(50 * time.Millisecond):
	}

	s.put([]byte{0xAB})
	select {
	case frame := <-got:
		if len(frame) != 1 || frame[0] != 0xAB {
			t.Errorf("take returned wrong frame: %v", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("take did not return after put")
	}
}

func TestFrameSlot_TakeReturnsFalseOnContextCancel(t *testing.T) {
	s := newFrameSlot()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() {
		_, ok := s.take(ctx)
		done <- ok
	}()

	select {
	case <-done:
		t.Fatal("take returned before context was cancelled")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Error("take returned ok=true after context cancel; want false")
		}
	case <-time.After(time.Second):
		t.Fatal("take did not return after context cancel")
	}
}
