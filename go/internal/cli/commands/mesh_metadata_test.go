package commands

import (
	"context"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/netmesh"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

func TestMeshDisplayName(t *testing.T) {
	for raw, want := range map[string]string{"ci-mesh-1": "Ci Mesh 1", "wendyos-agx-orin-joannis": "Agx Orin Joannis", "ci-orin-nano.local": "Ci Orin Nano", "": "", "école-pi": "École Pi"} {
		if got := meshDeviceRow(netmesh.Device{Name: raw}).DisplayName; got != want {
			t.Fatalf("%q: got %q, want %q", raw, got, want)
		}
	}
}
func meshMetadataEvent(t *testing.T, ctx context.Context, events <-chan discovery.LANEvent) discovery.LANEvent {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("stream closed")
		}
		return event
	case <-ctx.Done():
		t.Fatal("waiting for mesh metadata event")
		return discovery.LANEvent{}
	}
}
func TestMeshMetadataProbeAndRendering(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	input := make(chan discovery.LANEvent, 1)
	release := make(chan struct{})
	events := probeMeshDiscovery(ctx, input, func(ctx context.Context, dev models.LANDevice) (models.LANDevice, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return dev, ctx.Err()
		}
		dev.AgentVersion = "agent-version"
		dev.OS = "linux"
		dev.OSVersion = "WendyOS-0.19.5"
		return dev, nil
	}, time.Hour)
	input <- discovery.LANEvent{Kind: discovery.LANFound, Device: models.LANDevice{ID: "robot", DisplayName: "Ci Mesh 1", Hostname: "mesh:robot", Mesh: true, IsMTLS: true}}
	pending := meshMetadataEvent(t, ctx, events)
	if state, insecure := lanRowState(pending); state != tui.ProbePending || insecure {
		t.Fatalf("pending mesh row: %v, %v", state, insecure)
	}
	close(release)
	event := meshMetadataEvent(t, ctx, events)
	if !event.Probed || event.Device.AgentVersion != "agent-version" || event.Device.OSVersion != "WendyOS-0.19.5" || !event.Device.Mesh || event.Device.Hostname != "mesh:robot" {
		t.Fatalf("lost metadata or mesh selector: %+v", event)
	}
	if state, insecure := lanRowState(event); state != tui.ProbeOK || insecure {
		t.Fatalf("probed mesh row: %v, %v", state, insecure)
	}
	item := lanPickerItem(event.Device, false, tui.ProbeOK)
	if item.AgentVersion != "agent-version" || item.OSVersion != "WendyOS-0.19.5" || item.Name != "Ci Mesh 1" {
		t.Fatalf("picker missing metadata: %+v", item)
	}
	cancel()
	for range events {
	}
}
func TestMeshMetadataDiscardsWithdrawnProbe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	input := make(chan discovery.LANEvent, 2)
	started, stopped := make(chan struct{}), make(chan struct{})
	events := probeMeshDiscovery(ctx, input, func(ctx context.Context, dev models.LANDevice) (models.LANDevice, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		dev.AgentVersion = "stale"
		return dev, nil
	}, time.Hour)
	device := models.LANDevice{ID: "robot", Mesh: true}
	input <- discovery.LANEvent{Kind: discovery.LANFound, Device: device}
	meshMetadataEvent(t, ctx, events)
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("probe did not start")
	}
	input <- discovery.LANEvent{Kind: discovery.LANRetracted, Device: device}
	if event := meshMetadataEvent(t, ctx, events); event.Kind != discovery.LANRetracted {
		t.Fatal(event)
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("withdrawal did not cancel probe")
	}
	// The next source event must arrive without the withdrawn row reappearing.
	input <- discovery.LANEvent{Kind: discovery.LANRetracted, Device: models.LANDevice{ID: "sentinel"}}
	if event := meshMetadataEvent(t, ctx, events); event.Device.ID != "sentinel" {
		t.Fatalf("stale result resurrected row: %+v", event)
	}
	cancel()
	for event := range events {
		t.Fatalf("unexpected event after withdrawal: %+v", event)
	}
}

func TestMeshMetadataBoundsProbesWithoutBlockingPresence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	input := make(chan discovery.LANEvent, 8)
	starts := make(chan struct{}, 8)
	events := probeMeshDiscovery(ctx, input, func(ctx context.Context, dev models.LANDevice) (models.LANDevice, error) {
		starts <- struct{}{}
		<-ctx.Done()
		return dev, ctx.Err()
	}, time.Hour)
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		input <- discovery.LANEvent{Kind: discovery.LANFound, Device: models.LANDevice{ID: id, Mesh: true}}
	}
	for i := 0; i < 8; i++ {
		if event := meshMetadataEvent(t, ctx, events); event.Kind != discovery.LANFound {
			t.Fatal(event)
		}
	}
	for i := 0; i < 4; i++ {
		select {
		case <-starts:
		case <-ctx.Done():
			t.Fatal("probe did not start")
		}
	}
	cancel()
	for range events {
	}
	if len(starts) != 0 {
		t.Fatal("more than four metadata probes started")
	}
}
