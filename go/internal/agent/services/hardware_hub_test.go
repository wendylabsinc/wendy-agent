package services

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

func TestHardwareEventHub_PublishSubscribe(t *testing.T) {
	hub := NewHardwareEventHub()
	ch, cancel := hub.Subscribe()

	hub.Publish(&agentpbv2.HardwareEvent{Action: "connected"})
	select {
	case ev := <-ch:
		if ev.GetAction() != "connected" {
			t.Errorf("action = %q", ev.GetAction())
		}
	case <-time.After(time.Second):
		t.Fatal("event not delivered")
	}

	// After cancel, publishes are not delivered (and don't panic).
	cancel()
	cancel() // idempotent
	hub.Publish(&agentpbv2.HardwareEvent{Action: "disconnected"})
	select {
	case ev := <-ch:
		t.Errorf("unexpected delivery after cancel: %v", ev)
	default:
	}
}

func TestHardwareEventHub_SlowSubscriberDropsNotBlocks(t *testing.T) {
	hub := NewHardwareEventHub()
	_, cancel := hub.Subscribe() // never drained
	defer cancel()

	done := make(chan struct{})
	go func() {
		for i := 0; i < hardwareHubSubBuffer+10; i++ {
			hub.Publish(&agentpbv2.HardwareEvent{Action: "connected"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
}

func TestHardwareEventHub_NilSafe(t *testing.T) {
	var hub *HardwareEventHub
	hub.Publish(&agentpbv2.HardwareEvent{Action: "connected"}) // must not panic
}

func TestWatchHardware_SnapshotThenLiveEvents(t *testing.T) {
	hub := NewHardwareEventHub()
	store := NewHardwareWatchStore(filepath.Join(t.TempDir(), "watch.json"), nil)
	if err := store.Save([]WatchedDevice{{VendorID: "1d50", ProductID: "606f", Serial: "B"}}); err != nil {
		t.Fatal(err)
	}
	hd := &mockHardwareDiscoverer{caps: []*agentpb.ListHardwareCapabilitiesResponse_HardwareCapability{
		{Category: "usb", DevicePath: "/sys/bus/usb/devices/1-2.4", Description: "canable2 (1d50:606f)",
			Properties: map[string]string{"vendor_id": "1d50", "product_id": "606f", "port_path": "1-2.4"}},
	}}

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	agentpbv2.RegisterWendyDeviceInfoServiceServer(srv, NewDeviceInfoService(zap.NewNop(), hd, store, hub))
	go func() { _ = srv.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { conn.Close(); srv.Stop(); lis.Close() }()
	client := agentpbv2.NewWendyDeviceInfoServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.WatchHardware(ctx, &agentpbv2.WatchHardwareRequest{})
	if err != nil {
		t.Fatalf("WatchHardware: %v", err)
	}

	// First message: the snapshot with devices + watch list.
	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv snapshot: %v", err)
	}
	snap := first.GetSnapshot()
	if snap == nil {
		t.Fatalf("first message is not a snapshot: %v", first)
	}
	if len(snap.GetUsbDevices()) != 1 || snap.GetUsbDevices()[0].GetProperties()["port_path"] != "1-2.4" {
		t.Errorf("snapshot devices = %v", snap.GetUsbDevices())
	}
	if len(snap.GetWatchList()) != 1 || snap.GetWatchList()[0].GetSerial() != "B" {
		t.Errorf("snapshot watch list = %v", snap.GetWatchList())
	}

	// Live event published on the hub arrives on the stream. Publish in a
	// retry loop: the server registers its subscription only after the
	// snapshot send, so the first publishes may race it.
	got := make(chan *agentpbv2.HardwareEvent, 1)
	go func() {
		for {
			resp, err := stream.Recv()
			if err != nil {
				return
			}
			if ev := resp.GetEvent(); ev != nil {
				got <- ev
				return
			}
		}
	}()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case ev := <-got:
			if ev.GetAction() != "disconnected" || ev.GetPortPath() != "1-2.4" {
				t.Errorf("event = %v", ev)
			}
			return
		case <-ticker.C:
			hub.Publish(&agentpbv2.HardwareEvent{Action: "disconnected", PortPath: "1-2.4"})
		case <-ctx.Done():
			t.Fatal("no live event received")
		}
	}
}
