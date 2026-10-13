package services

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Add(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }
func newFakeClock() *fakeClock           { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

type sentNotification struct {
	endpoint     string
	notification DetectionNotification
}

type endpointSender struct{ sent chan sentNotification }

func (s *endpointSender) Send(_ context.Context, endpoint string, n DetectionNotification) error {
	s.sent <- sentNotification{endpoint, n}
	return nil
}

func watchYAML(name, inference, extra string) string {
	return "kind: watch\nversion: 1\nname: " + name + "\non: {inference: " + inference + "}\n" + extra
}

func journal(t *testing.T, service *DataService, watches ...string) []data.CampaignNotification {
	t.Helper()
	items, _, _, err := service.manager.NotificationsFor("", "", "", true, watches)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestDocumentLeases(t *testing.T) {
	clock := newFakeClock()
	leases := documentLeases{now: clock.Now}
	if !leases.expired("w") {
		t.Fatal("a watch without a deadline must count as expired")
	}
	deadline := leases.extend("w", time.Minute)
	if !deadline.Equal(clock.Now().Add(time.Minute)) || leases.expired("w") {
		t.Fatalf("extend = %v", deadline)
	}
	clock.Add(30 * time.Second)
	renewed, ok := leases.renew("w", time.Minute)
	if !ok || !renewed.Equal(clock.Now().Add(time.Minute)) {
		t.Fatalf("renew = %v, %v", renewed, ok)
	}
	clock.Add(time.Minute)
	if !leases.expired("w") {
		t.Fatal("not expired at the deadline")
	}
	if _, ok := leases.renew("w", time.Minute); ok {
		t.Fatal("renew revived a lapsed lease")
	}
	leases.extend("x", time.Minute)
	leases.forget("x")
	if _, ok := leases.deadline("x"); ok {
		t.Fatal("forget kept the deadline")
	}
}

func TestWatchDeliverWritesOneEntryPerWatch(t *testing.T) {
	service, factory, _ := newDocumentTestService(t)
	sender := &endpointSender{sent: make(chan sentNotification, 8)}
	startDocumentTestService(t, service, factory, sender)
	saveDocument(t, service, watchYAML("porch", "front-door", "notify:\n  - cloud\n  - webhook: https://example.com/hook\n"))
	saveDocument(t, service, watchYAML("chat-1", "front-door", "lease: 60s\n"))
	service.leases.extend("chat-1", time.Minute)
	saveDocument(t, service, watchYAML("elsewhere", "back-door", ""))

	base := inferenceNotification(data.InferenceDocument{}.Campaign(""), "v4l2:/dev/video0", nil)
	base.Inference, base.Event = "front-door", "front-door"
	service.watches.deliver("front-door", base)

	entries := journal(t, service, "porch", "chat-1", "elsewhere")
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	byWatch := map[string]data.CampaignNotification{}
	for _, entry := range entries {
		byWatch[entry.Watch] = entry
	}
	if byWatch["porch"].Leased || !byWatch["chat-1"].Leased || byWatch["porch"].ID == byWatch["chat-1"].ID {
		t.Fatalf("entries = %+v", byWatch)
	}
	if byWatch["porch"].Campaign != "" || byWatch["porch"].Inference != "front-door" {
		t.Fatalf("porch entry = %+v", byWatch["porch"])
	}
	endpoints := map[string]bool{}
	for i := 0; i < 2; i++ {
		sent := receiveInference(t, sender.sent)
		if sent.notification.Watch != "porch" {
			t.Fatalf("delivered for %q", sent.notification.Watch)
		}
		endpoints[sent.endpoint] = true
	}
	if !endpoints[""] || !endpoints["https://example.com/hook"] {
		t.Fatalf("deliveries went to %v; want Cloud and the webhook", endpoints)
	}
}

func TestLapsedLeaseWritesNothingBeforeRemoval(t *testing.T) {
	service, factory, sender := newDocumentTestService(t)
	clock := newFakeClock()
	service.leases.now = clock.Now
	startDocumentTestService(t, service, factory, sender)
	saveDocument(t, service, watchYAML("chat-1", "front-door", "lease: 15s\n"))
	service.leases.extend("chat-1", 15*time.Second)
	clock.Add(16 * time.Second)
	base := DetectionNotification{Event: "front-door", Inference: "front-door", SourceID: "v4l2:/dev/video0", Count: 1}
	base.OccurredAt = time.Now().UTC().Format(time.RFC3339Nano)
	service.watches.deliver("front-door", base)
	if entries := journal(t, service, "chat-1"); len(entries) != 0 {
		t.Fatalf("a lapsed lease wrote %+v", entries)
	}
	if _, err := service.manager.Document(data.KindWatch, "chat-1"); err != nil {
		t.Fatal("the watch is gone before the expiry pass; this test needs it present")
	}
}

func TestExpireLeasesRemovesTheWatchAndWhatItOwns(t *testing.T) {
	service, factory, sender := newDocumentTestService(t)
	clock := newFakeClock()
	service.leases.now = clock.Now
	startDocumentTestService(t, service, factory, sender)
	saveDocument(t, service, watchYAML("chat-1", "chat-1", "lease: 15s\n"))
	service.leases.extend("chat-1", 15*time.Second)
	saveDocument(t, service, strings.Replace(testInferenceDocumentYAML, "name: front-door", "name: chat-1\nowner: watch/chat-1", 1))
	session := receiveInference(t, factory.sessions)
	clock.Add(16 * time.Second)
	service.expireLeases()
	for _, ref := range [][2]string{{data.KindWatch, "chat-1"}, {data.KindInference, "chat-1"}} {
		if _, err := service.manager.Document(ref[0], ref[1]); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s/%s after expiry: %v", ref[0], ref[1], err)
		}
	}
	select {
	case <-session.closed:
	case <-time.After(8 * time.Second):
		t.Fatal("the owned inference kept running after its watch expired")
	}
}

func TestBootSweepRemovesLeasedWatchesWhatTheyOwnAndOrphans(t *testing.T) {
	service, factory, sender := newDocumentTestService(t)
	saveDocument(t, service, watchYAML("chat-1", "chat-1", "lease: 60s\n"))
	saveDocument(t, service, strings.Replace(testInferenceDocumentYAML, "name: front-door", "name: chat-1\nowner: watch/chat-1", 1))
	saveDocument(t, service, watchYAML("porch", "front-door", ""))
	saveDocument(t, service, strings.Replace(testInferenceDocumentYAML, "name: front-door", "name: orphan\nowner: watch/gone", 1))
	startDocumentTestService(t, service, factory, sender)
	for _, ref := range [][2]string{{data.KindWatch, "chat-1"}, {data.KindInference, "chat-1"}, {data.KindInference, "orphan"}} {
		if _, err := service.manager.Document(ref[0], ref[1]); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s/%s survived the boot sweep: %v", ref[0], ref[1], err)
		}
	}
	if _, err := service.manager.Document(data.KindWatch, "porch"); err != nil {
		t.Fatalf("an unleased watch was swept: %v", err)
	}
	select {
	case <-factory.sessions:
		t.Fatal("an inference started for a swept document")
	case <-time.After(300 * time.Millisecond):
	}
}
