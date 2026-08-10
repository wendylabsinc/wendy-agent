package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/discoverycache"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

func TestIsWithinProbeFalseForAPlainContext(t *testing.T) {
	if IsWithinProbe(context.Background()) {
		t.Fatal("a plain context reported as being inside a probe")
	}
}

func TestWithinProbeMarksAContextAndSurvivesDerivation(t *testing.T) {
	ctx := WithinProbe(context.Background())
	if !IsWithinProbe(ctx) {
		t.Fatal("WithinProbe did not mark the context")
	}
	// The mark has to survive the derivations the dial path makes below it —
	// that whole path is why the mark exists.
	derived, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if !IsWithinProbe(derived) {
		t.Fatal("mark lost across context.WithTimeout")
	}
}

// The mark must reach the prober itself: a prober that cannot tell it is
// running inside discovery would re-enter discovery, which is the recursion
// this exists to stop.
func TestProberReceivesAProbeMarkedContext(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/devices.json"
	seedCache(t, path, discoverycache.Entry{
		ID:          "urn:wendy:org:1:asset:1",
		DisplayName: "probe-marker",
		Hostname:    "probe-marker.local",
		IP:          "192.168.0.2",
		Port:        50051,
	})
	// Emits nothing — the cached row is what drives the probe. It must still
	// honour cancellation, or the session's wg.Wait never returns and the test
	// hangs instead of failing.
	useStreamSeams(t, func(ctx context.Context, _ string, _ func(MDNSService)) error {
		<-ctx.Done()
		return ctx.Err()
	}, cacheLoaderFor(path))

	marked := make(chan bool, 1)
	prober := func(ctx context.Context, dev models.LANDevice) (models.LANDevice, error) {
		select {
		case marked <- IsWithinProbe(ctx):
		default:
		}
		return dev, nil
	}

	startStream(t, StreamOptions{UseCache: true, Prober: prober})

	select {
	case got := <-marked:
		if !got {
			t.Fatal("prober received a context that was NOT marked as inside a probe")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prober was never called")
	}
}
