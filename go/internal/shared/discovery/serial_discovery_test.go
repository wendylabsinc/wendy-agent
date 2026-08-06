package discovery

import (
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/liteclient"
)

// newTestSerialDiscovery returns a fresh, unshared SerialDiscovery so tests
// don't race on the package singleton returned by GetSerialDiscovery.
func newTestSerialDiscovery() *SerialDiscovery {
	return &SerialDiscovery{
		probing:   make(map[string]bool),
		probeSem:  make(chan struct{}, 4),
		listeners: make(map[ListenerID]func([]SerialDevice)),
	}
}

// waitForDevices polls d.Devices() until want(devices) reports true or the
// timeout elapses, returning the last observed snapshot.
func waitForDevices(t *testing.T, d *SerialDiscovery, timeout time.Duration, want func([]SerialDevice) bool) []SerialDevice {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last []SerialDevice
	for time.Now().Before(deadline) {
		last = d.Devices()
		if want(last) {
			return last
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for expected device state; last snapshot: %+v", last)
	return last
}

func byPort(devices []SerialDevice) map[string]SerialDevice {
	m := make(map[string]SerialDevice, len(devices))
	for _, d := range devices {
		m[d.Port] = d
	}
	return m
}

func TestSerialDiscoveryAddsUnresponsivePort(t *testing.T) {
	origResolve, origProbe := resolvePortsFn, probeIdentityFn
	defer func() { resolvePortsFn, probeIdentityFn = origResolve, origProbe }()

	resolvePortsFn = func() ([]SerialPortInfo, error) {
		return []SerialPortInfo{{Port: "/dev/blank"}, {Port: "/dev/responsive"}}, nil
	}
	probeIdentityFn = func(port string) (*liteclient.DeviceIdentity, bool, error) {
		if port == "/dev/responsive" {
			return &liteclient.DeviceIdentity{ID: "esp", Name: "esp", DisplayName: "esp"}, true, nil
		}
		return nil, true, errProbeTimeout
	}

	d := newTestSerialDiscovery()
	d.StartScan(0)

	devices := waitForDevices(t, d, 2*time.Second, func(devices []SerialDevice) bool {
		return len(devices) == 2
	})

	m := byPort(devices)
	blank, ok := m["/dev/blank"]
	if !ok {
		t.Fatalf("expected /dev/blank in devices, got %+v", devices)
	}
	if blank.Responsive {
		t.Errorf("expected /dev/blank to be unresponsive, got Responsive=true")
	}
	if blank.ID != "" || blank.Name != "" || blank.DisplayName != "" {
		t.Errorf("expected empty identity fields for an unresponsive port, got %+v", blank)
	}

	responsive, ok := m["/dev/responsive"]
	if !ok {
		t.Fatalf("expected /dev/responsive in devices, got %+v", devices)
	}
	if !responsive.Responsive {
		t.Errorf("expected /dev/responsive to be responsive")
	}
	if responsive.ID != "esp" || responsive.DisplayName != "esp" {
		t.Errorf("expected identity to be populated for a responsive port, got %+v", responsive)
	}
}

func TestSerialDiscoveryReprobesUnresponsivePort(t *testing.T) {
	origResolve, origProbe := resolvePortsFn, probeIdentityFn
	defer func() { resolvePortsFn, probeIdentityFn = origResolve, origProbe }()

	resolvePortsFn = func() ([]SerialPortInfo, error) {
		return []SerialPortInfo{{Port: "/dev/blank"}}, nil
	}

	var mu sync.Mutex
	responds := false
	probeIdentityFn = func(port string) (*liteclient.DeviceIdentity, bool, error) {
		mu.Lock()
		defer mu.Unlock()
		if responds {
			return &liteclient.DeviceIdentity{ID: "esp", Name: "esp", DisplayName: "esp"}, true, nil
		}
		return nil, true, errProbeTimeout
	}

	d := newTestSerialDiscovery()
	d.StartScan(0)
	waitForDevices(t, d, 2*time.Second, func(devices []SerialDevice) bool {
		return len(devices) == 1 && !devices[0].Responsive
	})

	// Simulate Wendy Lite firmware landing on the board mid-session, then
	// rescan (as the continuous picker discovery loop would on its next cycle).
	mu.Lock()
	responds = true
	mu.Unlock()
	d.StartScan(0)

	devices := waitForDevices(t, d, 2*time.Second, func(devices []SerialDevice) bool {
		return len(devices) == 1 && devices[0].Responsive
	})
	if devices[0].DisplayName != "esp" {
		t.Errorf("expected the upgraded entry to carry the resolved identity, got %+v", devices[0])
	}
}

func TestSerialDiscoveryDoesNotReprobeResponsivePort(t *testing.T) {
	origResolve, origProbe := resolvePortsFn, probeIdentityFn
	defer func() { resolvePortsFn, probeIdentityFn = origResolve, origProbe }()

	resolvePortsFn = func() ([]SerialPortInfo, error) {
		return []SerialPortInfo{{Port: "/dev/responsive"}}, nil
	}

	var mu sync.Mutex
	calls := 0
	probeIdentityFn = func(port string) (*liteclient.DeviceIdentity, bool, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return &liteclient.DeviceIdentity{ID: "esp", Name: "esp", DisplayName: "esp"}, true, nil
	}

	d := newTestSerialDiscovery()
	d.StartScan(0)
	waitForDevices(t, d, 2*time.Second, func(devices []SerialDevice) bool {
		return len(devices) == 1 && devices[0].Responsive
	})

	d.StartScan(0)
	// Give a second scan a moment to run, then confirm it skipped the probe.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Errorf("expected exactly 1 probe of an already-responsive port, got %d", got)
	}
}

func TestSerialDiscoveryRemovesUnpluggedPorts(t *testing.T) {
	origResolve, origProbe := resolvePortsFn, probeIdentityFn
	defer func() { resolvePortsFn, probeIdentityFn = origResolve, origProbe }()

	var mu sync.Mutex
	ports := []SerialPortInfo{{Port: "/dev/blank"}, {Port: "/dev/responsive"}}
	resolvePortsFn = func() ([]SerialPortInfo, error) {
		mu.Lock()
		defer mu.Unlock()
		out := make([]SerialPortInfo, len(ports))
		copy(out, ports)
		return out, nil
	}
	probeIdentityFn = func(port string) (*liteclient.DeviceIdentity, bool, error) {
		if port == "/dev/responsive" {
			return &liteclient.DeviceIdentity{ID: "esp", Name: "esp", DisplayName: "esp"}, true, nil
		}
		return nil, true, errProbeTimeout
	}

	d := newTestSerialDiscovery()
	d.StartScan(0)
	waitForDevices(t, d, 2*time.Second, func(devices []SerialDevice) bool {
		return len(devices) == 2
	})

	// Unplug the blank board.
	mu.Lock()
	ports = []SerialPortInfo{{Port: "/dev/responsive"}}
	mu.Unlock()
	d.StartScan(0)

	devices := waitForDevices(t, d, 2*time.Second, func(devices []SerialDevice) bool {
		return len(devices) == 1
	})
	if devices[0].Port != "/dev/responsive" {
		t.Errorf("expected only /dev/responsive to remain, got %+v", devices)
	}
}

// TestSerialDiscoverySkipsContendedPort locks in the fix for a false
// "unflashed" report: a port that a fully-flashed, working board occupies can
// still fail to open (e.g. "resource busy" — a concurrent probe from another
// wendy process, or a subprocess that inherited the fd) with no bearing on
// whether firmware is installed. That must not be recorded as unresponsive.
func TestSerialDiscoverySkipsContendedPort(t *testing.T) {
	origResolve, origProbe := resolvePortsFn, probeIdentityFn
	defer func() { resolvePortsFn, probeIdentityFn = origResolve, origProbe }()

	resolvePortsFn = func() ([]SerialPortInfo, error) {
		return []SerialPortInfo{{Port: "/dev/contended"}}, nil
	}
	probeIdentityFn = func(port string) (*liteclient.DeviceIdentity, bool, error) {
		return nil, false, errPortBusy
	}

	d := newTestSerialDiscovery()
	d.StartScan(0)

	// Give the probe goroutine time to run and (not) record anything.
	time.Sleep(100 * time.Millisecond)

	if devices := d.Devices(); len(devices) != 0 {
		t.Errorf("expected a port that failed to open to be left out entirely, got %+v", devices)
	}
}

// errProbeTimeout stands in for the real serial timeout/connection error a
// blank board's failed handshake returns.
var errProbeTimeout = &probeError{"identity probe timed out"}

// errPortBusy stands in for the real "resource busy" error macOS/Linux return
// when another process already has the serial port open exclusively.
var errPortBusy = &probeError{"open serial: resource busy"}

type probeError struct{ msg string }

func (e *probeError) Error() string { return e.msg }
