package discovery

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// txtWire encodes entries into DNS-SD TXT wire format.
func txtWire(entries ...string) []byte {
	var out []byte
	for _, e := range entries {
		out = append(out, byte(len(e)))
		out = append(out, e...)
	}
	return out
}

func TestParseTXTRecord(t *testing.T) {
	cases := []struct {
		name string
		txt  []byte
		want map[string]string
	}{
		{name: "empty", txt: nil, want: map[string]string{}},
		{
			name: "key=value pairs",
			txt:  txtWire("tls=true", "assetid=338"),
			want: map[string]string{"tls": "true", "assetid": "338"},
		},
		{
			// The reason this parser exists: dns-sd's display format escaped
			// spaces as "\ " and the old code had to undo that by hand.
			name: "value containing spaces is preserved verbatim",
			txt:  txtWire("displayname=Tom Rpi4"),
			want: map[string]string{"displayname": "Tom Rpi4"},
		},
		{
			name: "value containing an equals sign keeps it",
			txt:  txtWire("token=abc=def"),
			want: map[string]string{"token": "abc=def"},
		},
		{
			name: "attribute with no value maps to empty string",
			txt:  txtWire("standalone"),
			want: map[string]string{"standalone": ""},
		},
		{
			name: "first occurrence of a repeated key wins (RFC 6763 6.4)",
			txt:  txtWire("dup=first", "dup=second"),
			want: map[string]string{"dup": "first"},
		},
		{
			name: "entry with an empty key is skipped",
			txt:  txtWire("=orphaned", "tls=true"),
			want: map[string]string{"tls": "true"},
		},
		{
			// A zero-length string must not mask the entries after it.
			name: "zero length string is skipped, later entries still decoded",
			txt:  append(append(txtWire("displayname=Tom Rpi4"), 0x00), txtWire("tls=true")...),
			want: map[string]string{"displayname": "Tom Rpi4", "tls": "true"},
		},
		{
			name: "length overrunning the buffer keeps what was decoded",
			txt:  append(txtWire("tls=true"), 0x40, 'a', 'b'),
			want: map[string]string{"tls": "true"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseTXTRecord(tc.txt); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseTXTRecord(%v) = %v, want %v", tc.txt, got, tc.want)
			}
		})
	}
}

func TestPreferIPv4Addr(t *testing.T) {
	cases := []struct {
		name  string
		addrs []string
		want  string
	}{
		{name: "empty", addrs: nil, want: ""},
		{
			name:  "IPv4 preferred over an earlier IPv6",
			addrs: []string{"2600:1011:a003:4221:be41:6859:13c0:f7", "192.168.0.159"},
			want:  "192.168.0.159",
		},
		{
			name:  "first IPv4 wins",
			addrs: []string{"192.168.0.159", "10.0.0.5"},
			want:  "192.168.0.159",
		},
		{
			name:  "falls back to first address when no IPv4",
			addrs: []string{"2001:db8::1", "2001:db8::2"},
			want:  "2001:db8::1",
		},
		{
			name:  "unparseable entries are skipped for the IPv4 scan",
			addrs: []string{"not-an-ip", "192.168.0.159"},
			want:  "192.168.0.159",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := preferIPv4Addr(tc.addrs); got != tc.want {
				t.Errorf("preferIPv4Addr(%v) = %q, want %q", tc.addrs, got, tc.want)
			}
		})
	}
}

// fakeBrowseBackend is a scripted browseBackendFn double: the test pushes
// services onto ch and the backend hands them to whichever generic function
// is under test, staying alive until ctx ends. Mirrors stream_test.go's
// fakeBackend, but kept separate because browseBackendFn is its own seam.
type fakeBrowseBackend struct{ ch chan MDNSService }

func newFakeBrowseBackend() *fakeBrowseBackend { return &fakeBrowseBackend{ch: make(chan MDNSService)} }

func (f *fakeBrowseBackend) fn(ctx context.Context, _ string, emit func(MDNSService)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case svc := <-f.ch:
			emit(svc)
		}
	}
}

// emit pushes svc to the backend, failing the test if it is never picked up
// (a hung consumer would otherwise deadlock the test).
func (f *fakeBrowseBackend) emit(t *testing.T, svc MDNSService) {
	t.Helper()
	select {
	case f.ch <- svc:
	case <-time.After(2 * time.Second):
		t.Fatalf("backend emit blocked: %+v", svc)
	}
}

// useBrowseBackend points browseBackendFn at a fake backend for the duration
// of the test.
func useBrowseBackend(t *testing.T, backend func(context.Context, string, func(MDNSService)) error) {
	t.Helper()
	orig := browseBackendFn
	t.Cleanup(func() { browseBackendFn = orig })
	browseBackendFn = backend
}

// TestBrowseMDNSServicesContinuousForwardsUntilCancel pins the property the
// cutover changes on purpose: BrowseMDNSServicesContinuous no longer
// deduplicates (that was darwin-specific behavior baked into the per-platform
// implementation this replaces) — it forwards every emission from the
// backend as-is, matching mdnsStreamBackend's own no-dedup contract, and
// stops only when ctx is cancelled.
func TestBrowseMDNSServicesContinuousForwardsUntilCancel(t *testing.T) {
	fb := newFakeBrowseBackend()
	useBrowseBackend(t, fb.fn)

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := BrowseMDNSServicesContinuous(ctx, "_wendyos._udp")
	if err != nil {
		t.Fatalf("BrowseMDNSServicesContinuous: %v", err)
	}

	svc := MDNSService{InstanceName: "orin", Hostname: "orin.local", Port: 50051}

	// The same instance twice: a real deduping implementation would collapse
	// this to one emission on the channel.
	go func() {
		fb.emit(t, svc)
		fb.emit(t, svc)
	}()

	for i := 0; i < 2; i++ {
		select {
		case got, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed after %d of 2 emissions", i)
			}
			if got.InstanceName != svc.InstanceName || got.Hostname != svc.Hostname || got.Port != svc.Port {
				t.Errorf("emission %d = %+v, want %+v", i, got, svc)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for emission %d", i)
		}
	}

	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to close after ctx cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close after ctx cancellation")
	}
}

// TestBrowseMDNSServicesSettlesEarlyAndDedups pins BrowseMDNSServices'
// early-exit and dedup behavior: it must return browseSettle after the last
// newly discovered service rather than waiting for the full timeout, and it
// must collapse a repeated announcement of the same instance
// (InstanceName+Hostname+Port) into a single result — the property every
// per-platform batch browse this replaces already had.
func TestBrowseMDNSServicesSettlesEarlyAndDedups(t *testing.T) {
	shrinkDuration(t, &browseSettle, 30*time.Millisecond)

	fb := newFakeBrowseBackend()
	useBrowseBackend(t, fb.fn)

	orin := MDNSService{InstanceName: "orin", Hostname: "orin.local", Port: 50051}
	nano := MDNSService{InstanceName: "nano", Hostname: "nano.local", Port: 50051}

	go func() {
		fb.emit(t, orin)
		fb.emit(t, orin) // repeat announcement — must not produce a second entry
		fb.emit(t, nano)
	}()

	start := time.Now()
	got, err := BrowseMDNSServices(context.Background(), "_wendyos._udp", 5*time.Second)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("BrowseMDNSServices error: %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("BrowseMDNSServices took %v to settle, want well under the 5s timeout", elapsed)
	}
	if len(got) != 2 {
		t.Fatalf("got %d services, want 2 (deduped): %+v", len(got), got)
	}
}

// TestBrowseMDNSServicesTimeoutCap pins the other half of BrowseMDNSServices'
// early-exit policy: when nothing is ever found, it must still return once
// timeout elapses rather than blocking forever waiting for a settle that will
// never come.
func TestBrowseMDNSServicesTimeoutCap(t *testing.T) {
	fb := newFakeBrowseBackend() // stays silent: nothing on the network
	useBrowseBackend(t, fb.fn)

	const timeout = 100 * time.Millisecond
	start := time.Now()
	got, err := BrowseMDNSServices(context.Background(), "_wendyos._udp", timeout)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("BrowseMDNSServices error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d services, want 0: %+v", len(got), got)
	}
	if elapsed < timeout {
		t.Fatalf("returned after %v, want >= the %v timeout", elapsed, timeout)
	}
	if elapsed > timeout+time.Second {
		t.Fatalf("returned after %v, want close to the %v timeout", elapsed, timeout)
	}
}
