package central

import (
	"testing"
	"time"
)

func TestWriteDeadlineResetIgnoresAlreadyFiredTimer(t *testing.T) {
	c := &l2capNetConn{writeActive: true}
	c.SetWriteDeadline(time.Now().Add(time.Hour))
	stale := c.writeGeneration
	c.SetWriteDeadline(time.Time{})
	// Model a stopped timer whose callback had already been queued. No transport
	// exists in this test, so attempting to close it would also panic.
	c.expireWrite(stale)
	if c.closed.Load() {
		t.Fatal("stale callback closed the connection")
	}
}

func TestWriteDeadlineDoesNotCloseIdleStream(t *testing.T) {
	c := &l2capNetConn{}
	c.SetWriteDeadline(time.Now().Add(-time.Second))
	if _, err := c.Write([]byte("x")); err == nil {
		t.Fatal("expired write allowed")
	}
	if c.closed.Load() {
		t.Fatal("idle stream closed")
	}
}
