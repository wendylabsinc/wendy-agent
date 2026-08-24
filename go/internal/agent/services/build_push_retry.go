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

// deliveryCounter tracks how many bytes have been forwarded towards the device.
//
// It exists so a failure can say how far it got. "EOF at 190s" gives a developer
// nothing to decide with; "failed after 1.8 GiB" tells them whether to retry the
// same way or change approach, and immediately distinguishes a cold build cache
// (every layer new, full image on the wire) from a warm one.
type deliveryCounter struct {
	sent atomic.Int64
}

func (d *deliveryCounter) add(n int64) {
	if n > 0 {
		d.sent.Add(n)
	}
}

func (d *deliveryCounter) bytes() int64 { return d.sent.Load() }

// deliveryReader adds every byte read to the counter. Wrapping the request body
// counts what is on its way to the device, rather than what buildkit produced:
// layers the device already has are never sent, so this is the number that
// explains the wall-clock time.
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

// annotateDeliveryFailure adds how far the transfer got to an outbound error.
//
// Without it every large-image failure reads identically, whether it died on the
// first byte or the last. errors.Join is not used: this has to survive being
// rendered into a gRPC status message, where only the text arrives.
func annotateDeliveryFailure(err error, sent int64) error {
	if err == nil {
		return nil
	}
	if sent <= 0 {
		return fmt.Errorf("%w (no image data had been sent yet)", err)
	}
	return fmt.Errorf("%w (after sending %s to the device)", err, describeBytes(sent))
}

// errDeliveryIncomplete marks a push that started and did not finish, which is
// the case a retry of the whole command is most likely to fix cheaply: the build
// cache is warm, so the layers already delivered are skipped on the next run.
var errDeliveryIncomplete = errors.New("image delivery was interrupted; re-running the build will resume from the layers the device already has")

// deliveryBodyCounter lets a counted body still be closed by the transport.
type deliveryBodyCounter struct {
	*deliveryReader
	closer interface{ Close() error }
}

func (c *deliveryBodyCounter) Close() error { return c.closer.Close() }
