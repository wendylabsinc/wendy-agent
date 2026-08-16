package rosbattery

import (
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/hoststats"
)

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestCache_EmptyReturnsNil(t *testing.T) {
	c := NewCache((&fakeClock{t: time.Unix(1000, 0)}).now)
	if b := c.Battery(); b != nil {
		t.Errorf("expected nil from an empty cache, got %+v", b)
	}
}

func TestCache_FreshSampleIsReturned(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewCache(clk.now)
	c.Put(&hoststats.Battery{Percent: 78, State: hoststats.BatteryDischarging})

	clk.advance(StaleAfter - time.Second)
	b := c.Battery()
	if b == nil {
		t.Fatal("expected a battery just inside the staleness window")
	}
	if b.Percent != 78 {
		t.Errorf("Percent = %v; want 78", b.Percent)
	}
}

func TestCache_StaleSampleIsDropped(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewCache(clk.now)
	c.Put(&hoststats.Battery{Percent: 78})

	clk.advance(StaleAfter + time.Second)
	if b := c.Battery(); b != nil {
		t.Errorf("expected nil past the staleness window, got %+v", b)
	}
}

func TestCache_PutRefreshesTheWindow(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewCache(clk.now)
	c.Put(&hoststats.Battery{Percent: 78})

	clk.advance(StaleAfter - time.Second)
	c.Put(&hoststats.Battery{Percent: 60})
	clk.advance(StaleAfter - time.Second)

	b := c.Battery()
	if b == nil {
		t.Fatal("expected the refreshed sample to still be live")
	}
	if b.Percent != 60 {
		t.Errorf("Percent = %v; want 60", b.Percent)
	}
}

func TestCache_PutNilClears(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewCache(clk.now)
	c.Put(&hoststats.Battery{Percent: 78})
	c.Put(nil)
	if b := c.Battery(); b != nil {
		t.Errorf("expected nil after Put(nil), got %+v", b)
	}
	if zones := c.ThermalZones(); zones != nil {
		t.Errorf("expected temperatures to clear with the LowState sample, got %+v", zones)
	}
}

func TestCache_ThermalZonesAreFreshAndCopied(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewCache(clk.now)
	zones := []hoststats.ThermalZone{{Name: "go2/imu", TempC: 79}}
	c.putTelemetry(&hoststats.Battery{Percent: 78}, zones)

	zones[0].TempC = 1
	first := c.ThermalZones()
	if len(first) != 1 || first[0].TempC != 79 {
		t.Fatalf("ThermalZones() = %+v, want copied 79C reading", first)
	}
	first[0].TempC = 2
	if second := c.ThermalZones(); second[0].TempC != 79 {
		t.Fatalf("ThermalZones() returned shared storage: %+v", second)
	}

	clk.advance(StaleAfter + time.Second)
	if stale := c.ThermalZones(); stale != nil {
		t.Fatalf("stale temperatures must disappear, got %+v", stale)
	}
}

// Battery must hand back a copy: a caller mutating the result must not corrupt
// what the next caller sees.
func TestCache_BatteryReturnsACopy(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewCache(clk.now)
	c.Put(&hoststats.Battery{Percent: 78})

	first := c.Battery()
	first.Percent = 1

	if second := c.Battery(); second.Percent != 78 {
		t.Errorf("Percent = %v; want 78 — Battery must return a copy", second.Percent)
	}
}

// Put must copy too: the monitor reusing a decode buffer must not mutate what
// the cache already holds.
func TestCache_PutCopiesTheSample(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewCache(clk.now)

	sample := &hoststats.Battery{Percent: 78}
	c.Put(sample)
	sample.Percent = 1

	if b := c.Battery(); b.Percent != 78 {
		t.Errorf("Percent = %v; want 78 — Put must copy", b.Percent)
	}
}

func TestCache_ConcurrentUse(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewCache(clk.now)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 200 {
				c.Put(&hoststats.Battery{Percent: 50})
			}
		}()
		go func() {
			defer wg.Done()
			for range 200 {
				_ = c.Battery()
			}
		}()
	}
	wg.Wait()
}
