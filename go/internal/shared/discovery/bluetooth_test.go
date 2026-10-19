package discovery

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/ble/agentinfo"
	"github.com/wendylabsinc/wendy/go/internal/shared/ble/scan"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

func fakeAgentInfo() *agentinfo.Info {
	return &agentinfo.Info{ProtocolVersion: 1, PSM: 128, Identity: "urn:wendy:org:7:asset:42", Hostname: "wendy-pi", Enrolled: true, Capabilities: []string{"wifi"}}
}

func swapAgentBLE(t *testing.T, sightings <-chan []scan.BLEDeviceInfo, probe func(string, time.Duration) (*agentinfo.Info, error)) {
	t.Helper()
	oldScan, oldProbe, oldSupport := agentBLEScan, agentBLEProbe, agentBLESupported
	agentBLESupported = true
	agentBLEProbe = probe
	agentBLEScan = func(ctx context.Context, opts scan.Options) (<-chan []scan.BLEDeviceInfo, error) {
		if !reflect.DeepEqual(opts.Services, []string{agentinfo.ServiceUUID}) || opts.Preflight == nil {
			t.Error("missing service filter or permission preflight")
		}
		return sightings, nil
	}
	t.Cleanup(func() { agentBLEScan, agentBLEProbe, agentBLESupported = oldScan, oldProbe, oldSupport })
}

func TestAgentBLEDiscoveryRequiresCompatibleMetadata(t *testing.T) {
	sightings := make(chan []scan.BLEDeviceInfo, 1)
	sightings <- []scan.BLEDeviceInfo{{Address: "good", Name: "Living room", RSSI: -40}, {Address: "legacy", RSSI: -20}, {Address: "future", RSSI: -10}}
	probed := make(chan string, 3)
	swapAgentBLE(t, sightings, func(address string, _ time.Duration) (*agentinfo.Info, error) {
		defer func() { probed <- address }()
		info := fakeAgentInfo()
		switch address {
		case "legacy":
			return nil, errors.New("missing characteristic")
		case "future":
			info.ProtocolVersion = 2
		}
		return info, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := DiscoverBluetoothContinuous(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found []models.BluetoothDevice
	for len(found) == 0 {
		select {
		case found = <-stream:
		case <-ctx.Done():
			t.Fatal("valid agent was not discovered")
		}
	}
	if len(found) != 1 || found[0].Address != "good" || found[0].Identity != fakeAgentInfo().Identity || found[0].HostKey() != "wendy-pi" || found[0].DisplayName != "Living room" || !found[0].Enrolled {
		t.Fatalf("unexpected discovery result: %+v", found)
	}
	for range 3 {
		select {
		case <-probed:
		case <-ctx.Done():
			t.Fatal("probe worker stopped")
		}
	}
	cancel()
	for next := range stream {
		for _, d := range next {
			if d.Address != "good" {
				t.Fatalf("offered incompatible agent: %+v", d)
			}
		}
	}
}

func TestAgentBLEDiscoveryCancellationWhileProbeIsBusy(t *testing.T) {
	sightings := make(chan []scan.BLEDeviceInfo, 1)
	sightings <- []scan.BLEDeviceInfo{{Address: "busy"}}
	entered, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	swapAgentBLE(t, sightings, func(string, time.Duration) (*agentinfo.Info, error) {
		close(entered)
		<-release
		close(exited)
		return fakeAgentInfo(), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := DiscoverBluetoothContinuous(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(release); <-exited }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	cancel()
	done := make(chan struct{})
	go func() {
		for range stream {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked on GATT probe")
	}
}

func TestAgentBLEDiscoveryReportsStartupFailure(t *testing.T) {
	swapAgentBLE(t, nil, nil)
	want := errors.New("Bluetooth permission denied")
	agentBLEScan = func(context.Context, scan.Options) (<-chan []scan.BLEDeviceInfo, error) { return nil, want }
	if _, err := DiscoverBluetoothContinuous(context.Background()); !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
	agentBLESupported = false
	if _, err := DiscoverBluetoothContinuous(context.Background()); err == nil {
		t.Fatal("unsupported transport offered")
	}
}

func TestAgentBLEDiscoveryHonorsCallerDeadline(t *testing.T) {
	sightings := make(chan []scan.BLEDeviceInfo)
	close(sightings)
	swapAgentBLE(t, sightings, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	want, _ := ctx.Deadline()
	agentBLEScan = func(ctx context.Context, _ scan.Options) (<-chan []scan.BLEDeviceInfo, error) {
		got, ok := ctx.Deadline()
		if !ok || !got.Equal(want) {
			t.Errorf("scan deadline %v, want caller deadline %v", got, want)
		}
		return sightings, nil
	}
	if _, err := discoverBluetooth(ctx, true); err != nil {
		t.Fatal(err)
	}
}
