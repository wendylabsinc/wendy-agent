package services

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"
)

// Delivery over the mesh is the leg that fails on a long-haul link. Measured on
// a US build host pushing to Jetsons in Canada: EOF partway through "exporting +
// pushing layers", once at 190s and once at 311s, on a multi-gigabyte image.
//
// What can and cannot be retried here is worth stating plainly, because the
// obvious reading is wrong. A blob upload is one HTTP request with a
// gigabyte-sized body streamed from buildkit. Once bytes have been forwarded,
// this proxy cannot replay them -- the body is not seekable and buffering it
// would mean holding the image in memory. Resuming a partial blob is the
// pusher's job (the registry protocol has ranged PATCH for exactly this); it is
// not something a byte-forwarding proxy can retrofit.
//
// What IS safe to retry is establishing the hop: a dial has no side effects, so
// a mesh connection that fails to come up, or a pooled connection found dead
// after an idle period, can simply be dialled again. On a link that drops
// briefly and often, that covers a real share of the failures for a few lines.
const (
	// pushDialAttempts includes the first try. Small on purpose: a genuinely
	// unreachable peer should fail in seconds, not after a long ladder that
	// looks like a hang.
	pushDialAttempts = 4
	// pushDialBackoff is the wait after the first failure, doubled each time
	// (200ms, 400ms, 800ms). Long enough to outlast a brief drop, short enough
	// that a dead peer is still reported promptly.
	pushDialBackoff = 200 * time.Millisecond
	// pushIdleConnTimeout retires pooled connections quickly. A tunnel that
	// drops leaves idle connections that look usable and fail on first write;
	// re-dialling costs one round trip, and being handed a dead connection
	// costs the build.
	pushIdleConnTimeout = 20 * time.Second
)

// retryingDial wraps a dial function so transient failures to establish the hop
// are retried, and reports how many attempts a success took.
//
// ctx cancellation is honoured immediately and never retried: a cancelled build
// must not sit through a backoff ladder.
func retryingDial(
	dial func(context.Context) (net.Conn, error),
	onRetry func(attempt int, err error),
) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		var lastErr error
		backoff := pushDialBackoff
		for attempt := 1; attempt <= pushDialAttempts; attempt++ {
			conn, err := dial(ctx)
			if err == nil {
				return conn, nil
			}
			lastErr = err
			// A cancelled or expired context is a decision, not a blip.
			if ctx.Err() != nil {
				return nil, err
			}
			if attempt == pushDialAttempts {
				break
			}
			if onRetry != nil {
				onRetry(attempt, err)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		}
		return nil, fmt.Errorf("after %d attempts: %w", pushDialAttempts, lastErr)
	}
}

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
