package commands

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// fakeNotifStream is a fake cloudpb.NotificationService_SubscribeNotificationsClient.
type fakeNotifStream struct {
	notifications []*cloudpb.Notification
	index         int
	finalErr      error
}

func (f *fakeNotifStream) Recv() (*cloudpb.Notification, error) {
	if f.index < len(f.notifications) {
		n := f.notifications[f.index]
		f.index++
		return n, nil
	}
	if f.finalErr != nil {
		return nil, f.finalErr
	}
	return nil, io.EOF
}

func (f *fakeNotifStream) Header() (metadata.MD, error) { return nil, nil }
func (f *fakeNotifStream) Trailer() metadata.MD         { return nil }
func (f *fakeNotifStream) CloseSend() error             { return nil }
func (f *fakeNotifStream) Context() context.Context     { return context.Background() }
func (f *fakeNotifStream) SendMsg(m any) error          { return nil }
func (f *fakeNotifStream) RecvMsg(m any) error          { return nil }

// fakeNotifSubscriber implements notificationSubscriber.
type fakeNotifSubscriber struct {
	stream  cloudpb.NotificationService_SubscribeNotificationsClient
	lastReq *cloudpb.SubscribeNotificationsRequest
}

func (f *fakeNotifSubscriber) SubscribeNotifications(ctx context.Context, req *cloudpb.SubscribeNotificationsRequest, _ ...grpc.CallOption) (cloudpb.NotificationService_SubscribeNotificationsClient, error) {
	f.lastReq = req
	return f.stream, nil
}

func TestRunNotifyDaemon_FiresNotificationsInOrder(t *testing.T) {
	notifications := []*cloudpb.Notification{
		{Id: 1, Body: "hello", Severity: cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_INFO},
		{Id: 2, Body: "disk full", Severity: cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_WARNING},
	}
	sub := &fakeNotifSubscriber{stream: &fakeNotifStream{notifications: notifications, finalErr: io.EOF}}

	var fired []string
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	state := make(notifyState)
	deps := notifyDaemonDeps{
		newClient:    func(*config.AuthConfig) (notificationSubscriber, func(), error) { return sub, func() {}, nil },
		sendNotif:    func(title, body string) error { fired = append(fired, title+"|"+body); return nil },
		sleep:        func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
		refreshCerts: func(context.Context, *config.AuthConfig) error { return nil },
		loadState:    func() (notifyState, error) { return state, nil },
		saveState:    func(s notifyState) error { state = s; return nil },
	}

	auth := &config.AuthConfig{
		CloudGRPC:    "test:443",
		Certificates: []config.CertificateInfo{{OrganizationID: 1, UserID: "u1"}},
	}

	// The daemon exits when all notifications are consumed (stream returns io.EOF)
	// and the reconnect sleep is cancelled.
	_ = runNotifyDaemon(ctx, auth, deps)

	if len(fired) != 2 {
		t.Fatalf("expected 2 notifications fired, got %d: %v", len(fired), fired)
	}
	if fired[0] != "Wendy|hello" {
		t.Errorf("first notification: got %q, want %q", fired[0], "Wendy|hello")
	}
	if fired[1] != "Wendy — Warning|disk full" {
		t.Errorf("second notification: got %q, want %q", fired[1], "Wendy — Warning|disk full")
	}
}

func TestRunNotifyDaemon_PersistsLastSeenID(t *testing.T) {
	notifications := []*cloudpb.Notification{
		{Id: 5, Body: "ping", Severity: cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_INFO},
	}
	sub := &fakeNotifSubscriber{stream: &fakeNotifStream{notifications: notifications, finalErr: io.EOF}}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var savedState notifyState
	deps := notifyDaemonDeps{
		newClient:    func(*config.AuthConfig) (notificationSubscriber, func(), error) { return sub, func() {}, nil },
		sendNotif:    func(string, string) error { return nil },
		sleep:        func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
		refreshCerts: func(context.Context, *config.AuthConfig) error { return nil },
		loadState:    func() (notifyState, error) { return make(notifyState), nil },
		saveState:    func(s notifyState) error { savedState = s; return nil },
	}

	auth := &config.AuthConfig{
		CloudGRPC:    "cloud.example:443",
		Certificates: []config.CertificateInfo{{OrganizationID: 2, UserID: "u2"}},
	}

	_ = runNotifyDaemon(ctx, auth, deps)

	ep := savedState["cloud.example:443"]
	if ep.LastSeenID != 5 {
		t.Errorf("lastSeenID = %d, want 5", ep.LastSeenID)
	}
}

func TestRunNotifyDaemon_PassesAfterIDOnSubscribe(t *testing.T) {
	sub := &fakeNotifSubscriber{stream: &fakeNotifStream{finalErr: io.EOF}}
	initialState := notifyState{
		"resume:443": {LastSeenID: 42},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	deps := notifyDaemonDeps{
		newClient:    func(*config.AuthConfig) (notificationSubscriber, func(), error) { return sub, func() {}, nil },
		sendNotif:    func(string, string) error { return nil },
		sleep:        func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
		refreshCerts: func(context.Context, *config.AuthConfig) error { return nil },
		loadState:    func() (notifyState, error) { return initialState, nil },
		saveState:    func(notifyState) error { return nil },
	}

	auth := &config.AuthConfig{
		CloudGRPC:    "resume:443",
		Certificates: []config.CertificateInfo{{OrganizationID: 1, UserID: "u1"}},
	}

	_ = runNotifyDaemon(ctx, auth, deps)

	if sub.lastReq == nil {
		t.Fatal("SubscribeNotifications was not called")
	}
	if sub.lastReq.AfterId == nil || *sub.lastReq.AfterId != 42 {
		t.Errorf("AfterId = %v, want 42", sub.lastReq.AfterId)
	}
}

func TestSeverityTitle(t *testing.T) {
	cases := []struct {
		sev  cloudpb.NotificationSeverity
		want string
	}{
		{cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_INFO, "Wendy"},
		{cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_WARNING, "Wendy — Warning"},
		{cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_ERROR, "Wendy — Error"},
		{cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_CRITICAL, "Wendy — Critical"},
		{cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_UNSPECIFIED, "Wendy"},
	}
	for _, tc := range cases {
		got := severityTitle(tc.sev)
		if got != tc.want {
			t.Errorf("severityTitle(%v) = %q, want %q", tc.sev, got, tc.want)
		}
	}
}

func TestLoadSaveNotifyState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	state, err := loadNotifyState()
	if err != nil {
		t.Fatalf("loadNotifyState (empty): %v", err)
	}
	if len(state) != 0 {
		t.Errorf("expected empty state, got %v", state)
	}

	state["cloud.test:443"] = notifyEndpointState{LastSeenID: 99}
	if err := saveNotifyState(state); err != nil {
		t.Fatalf("saveNotifyState: %v", err)
	}

	loaded, err := loadNotifyState()
	if err != nil {
		t.Fatalf("loadNotifyState (after save): %v", err)
	}
	if loaded["cloud.test:443"].LastSeenID != 99 {
		t.Errorf("loaded lastSeenID = %d, want 99", loaded["cloud.test:443"].LastSeenID)
	}
}

// Ensure the errors import is used (it's included per task brief, suppress potential unused-import errors).
var _ = errors.New
