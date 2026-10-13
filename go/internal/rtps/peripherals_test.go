package rtps

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaultRouteInterfaces(t *testing.T) {
	dir := t.TempDir()
	ipv4 := filepath.Join(dir, "route")
	ipv6 := filepath.Join(dir, "ipv6_route")
	// wlan0 carries the IPv4 default route; eth0 only its own subnet, like a
	// robot on a dedicated cable; eth1 carries an IPv6 default route.
	must(t, os.WriteFile(ipv4, []byte(
		"Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n"+
			"wlan0\t00000000\t0164A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0\n"+
			"eth0\t007BA8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n"), 0o600))
	must(t, os.WriteFile(ipv6, []byte(
		"00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000400 00000001 00000000 00450003     eth1\n"+
			"00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200       lo\n"), 0o600))

	got, err := defaultRouteInterfaces(ipv4, ipv6)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]bool{"wlan0": true, "eth1": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("uplinks = %v, want %v", got, want)
	}
	if _, err := defaultRouteInterfaces(filepath.Join(dir, "missing"), ipv6); err == nil {
		t.Error("a missing IPv4 table should be an error")
	}
	if got, err := defaultRouteInterfaces(ipv4, filepath.Join(dir, "missing")); err != nil || !got["wlan0"] {
		t.Errorf("a missing IPv6 table should be ignored: %v, %v", got, err)
	}
}

func TestPeripheralOverride(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := peripheralOverride(dir); ok || err != nil {
		t.Fatalf("no file: ok=%v err=%v, want no override", ok, err)
	}
	write := func(body string) {
		must(t, os.WriteFile(filepath.Join(dir, PeripheralsConfigFile), []byte(body), 0o600))
	}

	write(`{"interfaces": ["eth1", "enP8p1s0"]}`)
	if got, err := PeripheralInterfaces(dir); err != nil || !reflect.DeepEqual(got, []string{"eth1", "enP8p1s0"}) {
		t.Errorf("override = %v, %v", got, err)
	}
	write(`{"interfaces": []}`)
	if got, _, err := peripheralOverride(dir); err != nil || len(got) != 0 {
		t.Errorf("empty override = %v, %v; want none", got, err)
	}
	for _, bad := range []string{`{"interfaces": ["eth0\"/><x"]}`, `{}`, `not json`} {
		write(bad)
		if _, ok, err := peripheralOverride(dir); !ok || err == nil {
			t.Errorf("override %s: want an error", bad)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
