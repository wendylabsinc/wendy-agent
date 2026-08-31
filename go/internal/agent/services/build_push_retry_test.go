package services

import (
	"errors"
	"strings"
	"testing"
)

// A bare EOF says nothing about the failed HTTP request. The diagnostic should
// report that attempt's body consumption and its declared size without calling
// either number whole-image progress.
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
