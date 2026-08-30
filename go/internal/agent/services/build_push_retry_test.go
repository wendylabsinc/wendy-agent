package services

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

type fakeConn struct{ net.Conn }

// TestRetryingDial_RecoversFromATransientFailure is the case this exists for: a
// long-haul mesh link that drops briefly. A dial has no side effects, so trying
// again is safe -- unlike the byte forwarding, which cannot be replayed.
func TestRetryingDial_RecoversFromATransientFailure(t *testing.T) {
	var calls int
	var retries []int
	dial := retryingDial(func(context.Context) (net.Conn, error) {
		calls++
		if calls < 3 {
			return nil, errors.New("connection reset by peer")
		}
		return &fakeConn{}, nil
	}, func(attempt int, _ error) { retries = append(retries, attempt) })

	conn, err := dial(context.Background())
	if err != nil {
		t.Fatalf("want success on the third attempt, got %v", err)
	}
	if conn == nil {
		t.Fatal("want a connection")
	}
	if calls != 3 {
		t.Errorf("dialled %d times, want 3", calls)
	}
	if len(retries) != 2 {
		t.Errorf("reported %v retries, want 2", retries)
	}
}

// A genuinely unreachable peer must fail, and say how hard it tried, rather than
// looking like a hang.
func TestRetryingDial_GivesUpAndSaysHowManyAttempts(t *testing.T) {
	var calls int
	dial := retryingDial(func(context.Context) (net.Conn, error) {
		calls++
		return nil, errors.New("no route to host")
	}, nil)

	_, err := dial(context.Background())
	if err == nil {
		t.Fatal("want failure when every attempt fails")
	}
	if calls != pushDialAttempts {
		t.Errorf("dialled %d times, want %d", calls, pushDialAttempts)
	}
	if !strings.Contains(err.Error(), "no route to host") {
		t.Errorf("the underlying cause must survive; got %q", err)
	}
	if !strings.Contains(err.Error(), "attempts") {
		t.Errorf("the message should say it retried; got %q", err)
	}
}

// TestRetryingDial_DoesNotRetryACancelledBuild: a cancelled build must not sit
// through a backoff ladder. Cancellation is a decision, not a blip.
func TestRetryingDial_DoesNotRetryACancelledBuild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var calls int
	start := time.Now()
	_, err := retryingDial(func(context.Context) (net.Conn, error) {
		calls++
		return nil, errors.New("dial failed")
	}, nil)(ctx)

	if err == nil {
		t.Fatal("want failure")
	}
	if calls != 1 {
		t.Errorf("dialled %d times, want exactly 1 on a cancelled context", calls)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v; a cancelled build must not wait through backoff", elapsed)
	}
}

// TestAnnotateDeliveryFailure_SaysHowFarItGot is the whole point of the byte
// counting: "EOF at 190s" gives a developer nothing to act on, and cannot
// distinguish a cold build cache (whole image on the wire) from a warm one.
func TestAnnotateDeliveryFailure_SaysHowFarItGot(t *testing.T) {
	err := annotateDeliveryFailure(errors.New("unexpected EOF"), 1932735283, 2<<30)
	if !strings.Contains(err.Error(), "unexpected EOF") {
		t.Errorf("the cause must survive; got %q", err)
	}
	if !strings.Contains(err.Error(), "1.80 GiB") {
		t.Errorf("want the amount consumed from this attempt; got %q", err)
	}
	if !strings.Contains(err.Error(), "2.00 GiB") || !strings.Contains(err.Error(), "attempt-local") {
		t.Errorf("want the request size and honest scope; got %q", err)
	}
}

// Failing before any bytes moved is a different diagnosis -- unreachable peer or
// refused certificate, not a link that died mid-transfer.
func TestAnnotateDeliveryFailure_DistinguishesNothingSent(t *testing.T) {
	err := annotateDeliveryFailure(errors.New("no route to host"), 0, 0)
	if !strings.Contains(err.Error(), "no request-body bytes had been consumed") {
		t.Errorf("want the zero-progress wording; got %q", err)
	}
}

func TestAnnotateDeliveryFailure_NilStaysNil(t *testing.T) {
	if annotateDeliveryFailure(nil, 123, 456) != nil {
		t.Error("a nil error must stay nil")
	}
}

func TestAnnotateDeliveryFailure_DoesNotAccumulateRetries(t *testing.T) {
	first := annotateDeliveryFailure(errors.New("first EOF"), 500<<20, 700<<20)
	second := annotateDeliveryFailure(errors.New("second EOF"), 200<<20, 700<<20)
	if strings.Contains(second.Error(), "500 MiB") {
		t.Fatalf("second attempt retained the first attempt's bytes: %v", second)
	}
	if !strings.Contains(first.Error(), "500 MiB") ||
		!strings.Contains(second.Error(), "200 MiB") ||
		!strings.Contains(second.Error(), "700 MiB") {
		t.Fatalf("attempt-local amounts missing: first=%v second=%v", first, second)
	}
}

func TestDeliveryFailureStarted(t *testing.T) {
	if deliveryFailureStarted(annotateDeliveryFailure(errors.New("dial"), 0, 100)) {
		t.Fatal("zero consumed bytes must not claim a body transfer started")
	}
	if !deliveryFailureStarted(annotateDeliveryFailure(errors.New("EOF"), 1, 100)) {
		t.Fatal("a consumed request-body byte must mark the transfer as started")
	}
}

// The counter may be read by the proxy error handler while the transport is
// finishing a request-body read, so its accounting must be race-free.
func TestDeliveryCounter_IsSafeUnderConcurrency(t *testing.T) {
	var c deliveryCounter
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			for j := 0; j < 1000; j++ {
				c.add(64)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if got, want := c.bytes(), int64(8*1000*64); got != want {
		t.Errorf("counted %d bytes, want %d", got, want)
	}
}

// Invalid negative additions must not make progress go backwards.
func TestDeliveryCounter_IgnoresNegative(t *testing.T) {
	var c deliveryCounter
	c.add(100)
	c.add(-50)
	if got := c.bytes(); got != 100 {
		t.Errorf("counted %d, want 100", got)
	}
}

func TestDescribeBytes(t *testing.T) {
	for in, want := range map[int64]string{
		1932735283: "1.80 GiB",
		5 << 20:    "5 MiB",
		2048:       "2 KiB",
		512:        "512 B",
	} {
		if got := describeBytes(in); got != want {
			t.Errorf("describeBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
