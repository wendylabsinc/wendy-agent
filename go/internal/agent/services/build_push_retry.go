package services

import (
	"errors"
	"fmt"
	"sync/atomic"
)

// deliveryAttempt holds request-local accounting. BuildKit pushes several
// layers concurrently and retries 5xx responses, so combining their bodies
// would produce traffic volume rather than image progress.
type deliveryAttempt struct {
	consumed deliveryCounter
	total    int64
}

type deliveryAttemptContextKey struct{}

// deliveryCounter tracks how many request-body bytes the transport consumed.
type deliveryCounter struct {
	sent atomic.Int64
}

func (d *deliveryCounter) add(n int64) {
	if n > 0 {
		d.sent.Add(n)
	}
}

func (d *deliveryCounter) bytes() int64 { return d.sent.Load() }

// deliveryReader adds every byte read to the request-local counter. A read says
// the proxy consumed the bytes for forwarding; it does not prove the peer
// received them, because a socket write can fail after the transport read ahead.
type deliveryReader struct {
	inner interface{ Read([]byte) (int, error) }
	c     *deliveryCounter
}

func (r *deliveryReader) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	r.c.add(int64(n))
	return n, err
}

// describeBytes renders a byte count at the scale these messages are about.
func describeBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// annotateDeliveryFailure adds request-local context to an outbound error.
//
// Without it every large-request failure reads identically. The counter is
// deliberately described as bytes consumed for forwarding, not bytes delivered
// or whole-image progress; the HTTP transport can read ahead, BuildKit can
// retry, and other layer uploads can be in flight concurrently.
func annotateDeliveryFailure(err error, consumed, total int64) error {
	if err == nil {
		return nil
	}
	return &deliveryFailure{cause: err, consumed: consumed, total: total}
}

type deliveryFailure struct {
	cause    error
	consumed int64
	total    int64
}

func (e *deliveryFailure) Error() string {
	if e.consumed <= 0 {
		return fmt.Sprintf("%v (no request-body bytes had been consumed for forwarding)", e.cause)
	}
	if e.total > 0 {
		return fmt.Sprintf("%v (after consuming %s of a %s request body for forwarding; attempt-local, not whole-image progress)",
			e.cause, describeBytes(e.consumed), describeBytes(e.total))
	}
	return fmt.Sprintf("%v (after consuming %s of this request body for forwarding; attempt-local, not whole-image progress)",
		e.cause, describeBytes(e.consumed))
}

func (e *deliveryFailure) Unwrap() error { return e.cause }

func deliveryFailureStarted(err error) bool {
	var failure *deliveryFailure
	return errors.As(err, &failure) && failure.consumed > 0
}

// errDeliveryIncomplete marks a push that started and did not finish, which is
// the case a retry of the whole command is most likely to fix cheaply: the build
// cache is warm and fully committed layers are skipped. Containerd does not
// resume an incomplete monolithic upload, so that layer starts again.
var errDeliveryIncomplete = errors.New("image delivery was interrupted; re-running reuses the build cache and skips fully committed layers, but a partially uploaded layer starts again")

// deliveryBodyCounter lets a counted body still be closed by the transport.
type deliveryBodyCounter struct {
	*deliveryReader
	closer interface{ Close() error }
}

func (c *deliveryBodyCounter) Close() error { return c.closer.Close() }
