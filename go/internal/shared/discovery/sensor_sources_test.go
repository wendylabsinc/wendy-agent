package discovery

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// scriptedBackend emits svcs once, then stays browsing until ctx ends.
func scriptedBackend(svcs ...MDNSService) func(context.Context, string, func(MDNSService)) error {
	return func(ctx context.Context, _ string, emit func(MDNSService)) error {
		for _, svc := range svcs {
			emit(svc)
		}
		<-ctx.Done()
		return nil
	}
}

func liteService(instance, ip, iface string, txt map[string]string) MDNSService {
	return MDNSService{InstanceName: instance, Hostname: instance + ".local", IPAddress: ip, Port: 5054, InterfaceName: iface, TXTRecords: txt}
}

func TestDiscoverSensorSourcesMergesBothServicesAndKeepsOnlySensors(t *testing.T) {
	const tenant = "11111111-2222-4333-8444-555555555555"
	sensorAgent := wendyService("hub", "Hub", "hub.local", "192.0.2.10", 50052)
	sensorAgent.TXTRecords["caps"] = "sensors"
	sensorAgent.TXTRecords["assetid"] = "7"
	sensorAgent.TXTRecords["tls"] = "true"
	legacyAgent := wendyService("old", "Old", "old.local", "192.0.2.11", 50052)
	legacyAgent.TXTRecords["sensorlink"] = "true"
	plainAgent := wendyService("plain", "Plain", "plain.local", "192.0.2.12", 50051)
	useStreamSeams(t, scriptedBackend(sensorAgent, legacyAgent, plainAgent), nil)

	board := map[string]string{"id": "441bf6804824", "tenant": tenant, "caps": "sensors", "mtls": "true"}
	spoof := map[string]string{"id": "441bf6804824", "tenant": "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "caps": "sensors", "mtls": "true"}
	useBrowseBackend(t, scriptedBackend(
		liteService("cam", "192.0.2.20", "en0", board),
		liteService("cam", "192.0.2.20", "en0", board), // re-announcement
		liteService("cam", "198.51.100.20", "en1", board),
		liteService("cam", "192.0.2.21", "en0", spoof),
		liteService("blinky", "192.0.2.30", "en0", map[string]string{"id": "b1", "caps": "gpio"}),
	))

	got, err := DiscoverSensorSources(context.Background(), 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var agents, boards []string
	for _, d := range got {
		if !d.Sensorlink {
			t.Fatalf("non-sensor sighting returned: %+v", d)
		}
		if d.WendyLite {
			boards = append(boards, d.TenantUUID)
		} else {
			agents = append(agents, d.ID)
		}
	}
	slices.Sort(agents)
	if !slices.Equal(agents, []string{"hub", "old"}) {
		t.Fatalf("agents = %v, want hub and old", agents)
	}
	if len(boards) != 2 {
		t.Fatalf("boards = %v; the real board and the spoof must stay separate", boards)
	}
	for _, d := range got {
		if d.WendyLite && d.TenantUUID == tenant {
			if d.IPAddress != "192.0.2.20" || !slices.Equal(d.Addresses, []string{"192.0.2.20", "198.51.100.20"}) {
				t.Fatalf("multi-homed board not merged by address: %+v", d)
			}
			if d.PrincipalURI != "spiffe://wendy.sh/tenant/"+tenant+"/device/lite-441bf6804824" {
				t.Fatalf("principal hint not derived: %q", d.PrincipalURI)
			}
		}
		if d.ID == "hub" && (d.AssetID != 7 || !d.IsMTLS || d.Port != 50052) {
			t.Fatalf("agent fields lost: %+v", d)
		}
	}
}

func TestDiscoverSensorSourcesSurvivesWendyLiteFailureAndReportsCancellation(t *testing.T) {
	sensorAgent := wendyService("hub", "Hub", "hub.local", "192.0.2.10", 50052)
	sensorAgent.TXTRecords["caps"] = "sensors"
	useStreamSeams(t, scriptedBackend(sensorAgent), nil)
	useBrowseBackend(t, func(context.Context, string, func(MDNSService)) error {
		return errors.New("browse failed")
	})
	got, err := DiscoverSensorSources(context.Background(), 300*time.Millisecond)
	if err != nil || len(got) != 1 || got[0].ID != "hub" {
		t.Fatalf("got %+v, %v; want the agent despite the Wendy Lite failure", got, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DiscoverSensorSources(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
