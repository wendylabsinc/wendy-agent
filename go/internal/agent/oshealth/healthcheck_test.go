package oshealth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// fakeSystemctl returns canned `systemctl show` property maps per unit. Each
// call for a unit consumes the next entry in its sequence; the last entry
// repeats once the sequence is exhausted.
type fakeSystemctl struct {
	mu        sync.Mutex
	sequences map[string][]map[string]string
	errs      map[string]error
	calls     map[string]int
}

func (f *fakeSystemctl) show(_ context.Context, unit string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = make(map[string]int)
	}
	n := f.calls[unit]
	f.calls[unit] = n + 1
	if err := f.errs[unit]; err != nil {
		return nil, err
	}
	seq := f.sequences[unit]
	if len(seq) == 0 {
		return map[string]string{"LoadState": "not-found", "ActiveState": "inactive"}, nil
	}
	if n >= len(seq) {
		n = len(seq) - 1
	}
	return seq[n], nil
}

func (f *fakeSystemctl) callCount(unit string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[unit]
}

func newTestChecker(f *fakeSystemctl) *Checker {
	c := NewChecker(zap.NewNop())
	c.PollInterval = 5 * time.Millisecond
	c.SystemctlShow = f.show
	return c
}

func loaded(active string) map[string]string {
	return map[string]string{"LoadState": "loaded", "ActiveState": active, "SubState": active, "UnitFileState": "enabled"}
}

func TestCheckOneActiveIsHealthy(t *testing.T) {
	f := &fakeSystemctl{sequences: map[string][]map[string]string{
		"avahi-daemon.service": {loaded("active")},
	}}
	c := newTestChecker(f)

	got := c.CheckAll(context.Background(), []CriticalService{{Unit: "avahi-daemon.service", Timeout: time.Second}})

	want := []ServiceResult{{Unit: "avahi-daemon.service", Status: StatusHealthy}}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestCheckOneActivatingThenActiveIsHealthy(t *testing.T) {
	f := &fakeSystemctl{sequences: map[string][]map[string]string{
		"containerd.service": {loaded("activating"), loaded("activating"), loaded("active")},
	}}
	c := newTestChecker(f)

	got := c.CheckAll(context.Background(), []CriticalService{{Unit: "containerd.service", Timeout: time.Second}})

	if got[0].Status != StatusHealthy {
		t.Errorf("got %+v, want healthy", got[0])
	}
	if f.callCount("containerd.service") < 3 {
		t.Errorf("expected at least 3 polls, got %d", f.callCount("containerd.service"))
	}
}

func TestCheckOneFailedUntilTimeout(t *testing.T) {
	f := &fakeSystemctl{sequences: map[string][]map[string]string{
		"avahi-daemon.service": {{
			"LoadState": "loaded", "ActiveState": "failed", "SubState": "exited",
			"Result": "exit-code", "UnitFileState": "enabled",
		}},
	}}
	c := newTestChecker(f)

	got := c.CheckAll(context.Background(), []CriticalService{{Unit: "avahi-daemon.service", Timeout: 50 * time.Millisecond}})

	if got[0].Status != StatusFailed {
		t.Fatalf("got %+v, want failed", got[0])
	}
	for _, substr := range []string{"timed out", "failed", "exit-code"} {
		if !strings.Contains(got[0].Reason, substr) {
			t.Errorf("reason %q missing %q", got[0].Reason, substr)
		}
	}
}

func TestCheckOneNotFoundIsSkippedWithoutPolling(t *testing.T) {
	f := &fakeSystemctl{sequences: map[string][]map[string]string{
		"NetworkManager.service": {{"LoadState": "not-found", "ActiveState": "inactive"}},
	}}
	c := newTestChecker(f)

	got := c.CheckAll(context.Background(), []CriticalService{{Unit: "NetworkManager.service", Timeout: time.Second}})

	if got[0].Status != StatusSkipped {
		t.Fatalf("got %+v, want skipped", got[0])
	}
	if n := f.callCount("NetworkManager.service"); n != 1 {
		t.Errorf("expected exactly 1 systemctl call, got %d", n)
	}
}

func TestCheckOneDisabledInactiveIsSkipped(t *testing.T) {
	f := &fakeSystemctl{sequences: map[string][]map[string]string{
		"NetworkManager.service": {{
			"LoadState": "loaded", "ActiveState": "inactive", "SubState": "dead", "UnitFileState": "disabled",
		}},
	}}
	c := newTestChecker(f)

	got := c.CheckAll(context.Background(), []CriticalService{{Unit: "NetworkManager.service", Timeout: time.Second}})

	if got[0].Status != StatusSkipped {
		t.Errorf("got %+v, want skipped", got[0])
	}
}

func TestCheckOneMaskedIsSkipped(t *testing.T) {
	f := &fakeSystemctl{sequences: map[string][]map[string]string{
		"NetworkManager.service": {{
			"LoadState": "masked", "ActiveState": "inactive", "UnitFileState": "masked",
		}},
	}}
	c := newTestChecker(f)

	got := c.CheckAll(context.Background(), []CriticalService{{Unit: "NetworkManager.service", Timeout: time.Second}})

	if got[0].Status != StatusSkipped {
		t.Errorf("got %+v, want skipped", got[0])
	}
}

func TestCheckOneDisabledButActiveIsHealthy(t *testing.T) {
	f := &fakeSystemctl{sequences: map[string][]map[string]string{
		"avahi-daemon.service": {{
			"LoadState": "loaded", "ActiveState": "active", "SubState": "running", "UnitFileState": "disabled",
		}},
	}}
	c := newTestChecker(f)

	got := c.CheckAll(context.Background(), []CriticalService{{Unit: "avahi-daemon.service", Timeout: time.Second}})

	if got[0].Status != StatusHealthy {
		t.Errorf("got %+v, want healthy", got[0])
	}
}

func TestCheckOneSystemctlErrorFailsAfterTimeout(t *testing.T) {
	f := &fakeSystemctl{errs: map[string]error{
		"containerd.service": errors.New("exec: systemctl: not found"),
	}}
	c := newTestChecker(f)

	got := c.CheckAll(context.Background(), []CriticalService{{Unit: "containerd.service", Timeout: 30 * time.Millisecond}})

	if got[0].Status != StatusFailed {
		t.Fatalf("got %+v, want failed", got[0])
	}
	if !strings.Contains(got[0].Reason, "systemctl") {
		t.Errorf("reason %q should mention the systemctl error", got[0].Reason)
	}
}

func TestCheckOneHungSystemctlFailsAtTimeout(t *testing.T) {
	c := NewChecker(zap.NewNop())
	c.PollInterval = 5 * time.Millisecond
	c.SystemctlShow = func(ctx context.Context, unit string) (map[string]string, error) {
		// Simulate `systemctl show` hanging (e.g. D-Bus unresponsive during a
		// bad boot): block until the context is cancelled.
		<-ctx.Done()
		return nil, ctx.Err()
	}

	start := time.Now()
	got := c.CheckAll(context.Background(), []CriticalService{{Unit: "containerd.service", Timeout: 50 * time.Millisecond}})

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("hung systemctl blocked the check for %v, expected return at ~50ms timeout", elapsed)
	}
	if got[0].Status != StatusFailed {
		t.Errorf("got %+v, want failed", got[0])
	}
	if !strings.Contains(got[0].Reason, "timed out") {
		t.Errorf("reason %q should report the timeout", got[0].Reason)
	}
}

func TestCheckOneContextCancelled(t *testing.T) {
	f := &fakeSystemctl{sequences: map[string][]map[string]string{
		"avahi-daemon.service": {loaded("activating")},
	}}
	c := newTestChecker(f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	got := c.CheckAll(ctx, []CriticalService{{Unit: "avahi-daemon.service", Timeout: 10 * time.Second}})

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("cancelled check took %v, expected prompt return", elapsed)
	}
	if got[0].Status != StatusFailed {
		t.Errorf("got %+v, want failed on cancellation", got[0])
	}
}

func TestCheckAllRunsConcurrentlyAndPreservesOrder(t *testing.T) {
	// Two units that never become active: serial execution would take the
	// sum of the timeouts, concurrent execution roughly the max.
	f := &fakeSystemctl{sequences: map[string][]map[string]string{
		"a.service": {loaded("activating")},
		"b.service": {loaded("activating")},
		"c.service": {loaded("active")},
	}}
	c := newTestChecker(f)

	start := time.Now()
	got := c.CheckAll(context.Background(), []CriticalService{
		{Unit: "a.service", Timeout: 200 * time.Millisecond},
		{Unit: "b.service", Timeout: 200 * time.Millisecond},
		{Unit: "c.service", Timeout: time.Second},
	})
	elapsed := time.Since(start)

	if elapsed > 350*time.Millisecond {
		t.Errorf("CheckAll took %v, expected concurrent execution (~200ms)", elapsed)
	}
	wantUnits := []string{"a.service", "b.service", "c.service"}
	for i, w := range wantUnits {
		if got[i].Unit != w {
			t.Fatalf("result order %v, want %v", got, wantUnits)
		}
	}
	if got[0].Status != StatusFailed || got[1].Status != StatusFailed || got[2].Status != StatusHealthy {
		t.Errorf("unexpected statuses: %+v", got)
	}
}
