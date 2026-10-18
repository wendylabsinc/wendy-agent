//go:build linux

package bluetooth

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"go.uber.org/zap"
)

func TestFindAdvertisingAdapter(t *testing.T) {
	advMgr := map[string]map[string]dbus.Variant{advManagerIface: {}, gattManagerIface: {}}
	adapterOnly := map[string]map[string]dbus.Variant{adapterIface: {}}

	tests := []struct {
		name    string
		managed map[dbus.ObjectPath]map[string]map[string]dbus.Variant
		want    string
	}{
		{
			name: "advertising-capable adapter at hci0",
			managed: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
				"/org/bluez/hci0": advMgr,
			},
			want: "/org/bluez/hci0",
		},
		{
			name: "advertising-capable adapter at hci1 while hci0 lacks the interface",
			managed: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
				"/org/bluez/hci0": adapterOnly,
				"/org/bluez/hci1": advMgr,
			},
			want: "/org/bluez/hci1",
		},
		{
			name: "no adapter implements LEAdvertisingManager1",
			managed: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
				"/org/bluez/hci0": adapterOnly,
			},
			want: "",
		},
		{
			name: "managers on separate adapters cannot publish usable metadata",
			managed: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
				"/org/bluez/hci0": {advManagerIface: {}},
				"/org/bluez/hci1": {gattManagerIface: {}},
			},
			want: "",
		},
		{
			name: "multiple capable adapters returns the lowest path deterministically",
			managed: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
				"/org/bluez/hci2": advMgr,
				"/org/bluez/hci0": advMgr,
				"/org/bluez/hci1": advMgr,
			},
			want: "/org/bluez/hci0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := findAdvertisingAdapter(tt.managed); got != tt.want {
				t.Errorf("findAdvertisingAdapter() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveAdvertisingAdapterOverride(t *testing.T) {
	managed := managedObjects{
		"/org/bluez/hci0": {advManagerIface: {}},
		"/org/bluez/hci1": {advManagerIface: {}, gattManagerIface: {}},
		"/org/bluez/hci2": {advManagerIface: {}, gattManagerIface: {}},
	}
	for _, tt := range []struct {
		override string
		want     string
	}{
		{override: "", want: "/org/bluez/hci1"},
		{override: "/org/bluez/hci2", want: "/org/bluez/hci2"},
		{override: "/org/bluez/hci0"},
		{override: "/org/bluez/hci9"},
		{override: "invalid object path"},
	} {
		t.Run(tt.override, func(t *testing.T) {
			t.Setenv("WENDY_BT_ADAPTER", tt.override)
			got, err := resolveAdvertisingAdapter(managed)
			if got != tt.want || (err != nil) != (tt.want == "") {
				t.Errorf("resolveAdvertisingAdapter() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

type registrationCall struct {
	method string
	path   dbus.ObjectPath
}

type registrationObject struct {
	mu       sync.Mutex
	calls    []registrationCall
	call     func(context.Context, string) error
	closed   chan struct{}
	closeErr error
}

func (o *registrationObject) CallWithContext(ctx context.Context, method string, _ dbus.Flags, args ...any) *dbus.Call {
	if _, ok := ctx.Deadline(); !ok {
		return &dbus.Call{Err: errors.New("D-Bus call has no deadline")}
	}
	if err := ctx.Err(); err != nil {
		return &dbus.Call{Err: err}
	}
	o.mu.Lock()
	o.calls = append(o.calls, registrationCall{method: method, path: args[0].(dbus.ObjectPath)})
	o.mu.Unlock()
	if o.call != nil {
		return &dbus.Call{Err: o.call(ctx, method)}
	}
	return &dbus.Call{}
}

func (o *registrationObject) close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, registrationCall{method: "close"})
	close(o.closed)
	return o.closeErr
}

func (o *registrationObject) checkCalls(t *testing.T, want []registrationCall) {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if !reflect.DeepEqual(o.calls, want) {
		t.Errorf("D-Bus lifecycle = %v, want %v", o.calls, want)
	}
}

func TestBLERegistrationLifecycle(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit cleanup", true: "context canceled"}[cancelFirst], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			object := &registrationObject{closed: make(chan struct{})}
			cleanup, err := registerBLEPeripheral(ctx, zap.NewNop(), object, object.close, nil)
			if err != nil {
				t.Fatal(err)
			}
			if cancelFirst {
				cancel()
				select {
				case <-object.closed:
				case <-time.After(time.Second):
					t.Fatal("context cancellation did not clean up registration")
				}
			}
			var wg sync.WaitGroup
			for range 4 {
				wg.Go(func() {
					if err := cleanup(); err != nil {
						t.Errorf("cleanup: %v", err)
					}
				})
			}
			wg.Wait()
			object.checkCalls(t, []registrationCall{
				{gattManagerIface + ".RegisterApplication", gattApplicationPath},
				{advManagerIface + ".RegisterAdvertisement", advObjectPath},
				{advManagerIface + ".UnregisterAdvertisement", advObjectPath},
				{gattManagerIface + ".UnregisterApplication", gattApplicationPath},
				{method: "close"},
			})
		})
	}
}

func TestBLERegistrationFailureCleansUpApplication(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	object := &registrationObject{closed: make(chan struct{})}
	object.call = func(_ context.Context, method string) error {
		if method == advManagerIface+".RegisterAdvertisement" {
			cancel()
			return context.Canceled
		}
		return nil
	}
	cleanup, err := registerBLEPeripheral(ctx, zap.NewNop(), object, object.close, nil)
	if cleanup != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("registration = %v, cleanup present = %v", err, cleanup != nil)
	}
	object.checkCalls(t, []registrationCall{
		{gattManagerIface + ".RegisterApplication", gattApplicationPath},
		{advManagerIface + ".RegisterAdvertisement", advObjectPath},
		{gattManagerIface + ".UnregisterApplication", gattApplicationPath},
		{method: "close"},
	})
}

func TestBLECleanupContinuesAfterUnregisterFailure(t *testing.T) {
	unregisterErr := errors.New("BlueZ unavailable")
	closeErr := errors.New("close failed")
	object := &registrationObject{closed: make(chan struct{}), closeErr: closeErr}
	object.call = func(_ context.Context, method string) error {
		if method == advManagerIface+".UnregisterAdvertisement" {
			return unregisterErr
		}
		return nil
	}
	cleanup, err := registerBLEPeripheral(context.Background(), zap.NewNop(), object, object.close, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := cleanup(); !errors.Is(err, unregisterErr) || !errors.Is(err, closeErr) {
			t.Errorf("cleanup error = %v, want both unregister and close errors", err)
		}
	}
	object.checkCalls(t, []registrationCall{
		{gattManagerIface + ".RegisterApplication", gattApplicationPath},
		{advManagerIface + ".RegisterAdvertisement", advObjectPath},
		{advManagerIface + ".UnregisterAdvertisement", advObjectPath},
		{gattManagerIface + ".UnregisterApplication", gattApplicationPath},
		{method: "close"},
	})
}

func TestBLERegistrationRetryStopsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	object := &registrationObject{closed: make(chan struct{})}
	object.call = func(context.Context, string) error {
		return dbus.NewError("org.bluez.Error.NotReady", nil)
	}
	_, err := registerBLEPeripheral(ctx, zap.NewNop(), object, object.close, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("registration error = %v, want deadline exceeded", err)
	}
	object.checkCalls(t, []registrationCall{
		{gattManagerIface + ".RegisterApplication", gattApplicationPath},
		{method: "close"},
	})
}

func ownerChangedSignal(oldOwner, newOwner string) *dbus.Signal {
	return &dbus.Signal{
		Sender: "org.freedesktop.DBus", Name: "org.freedesktop.DBus.NameOwnerChanged",
		Body: []any{bluezService, oldOwner, newOwner},
	}
}

func TestBLERegistrationRestoresAfterBlueZRestart(t *testing.T) {
	ownerChanges := make(chan *dbus.Signal, 4)
	restored := make(chan struct{})
	object := &registrationObject{closed: make(chan struct{})}
	var advertisements atomic.Int32
	object.call = func(_ context.Context, method string) error {
		if method == advManagerIface+".RegisterAdvertisement" && advertisements.Add(1) == 2 {
			close(restored)
		}
		return nil
	}
	cleanup, err := registerBLEPeripheral(context.Background(), zap.NewNop(), object, object.close, ownerChanges)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanup() })
	ownerChanges <- ownerChangedSignal(":1.10", "")
	ownerChanges <- ownerChangedSignal("", ":1.20")
	select {
	case <-restored:
	case <-time.After(time.Second):
		t.Fatal("bluetoothd restart did not restore BLE registration")
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	// The synchronous cleanup also joins the watcher. Later signals cannot
	// register objects on a closed bus or revive a stopped endpoint.
	ownerChanges <- ownerChangedSignal(":1.20", ":1.30")
	object.checkCalls(t, []registrationCall{
		{gattManagerIface + ".RegisterApplication", gattApplicationPath},
		{advManagerIface + ".RegisterAdvertisement", advObjectPath},
		{gattManagerIface + ".RegisterApplication", gattApplicationPath},
		{advManagerIface + ".RegisterAdvertisement", advObjectPath},
		{advManagerIface + ".UnregisterAdvertisement", advObjectPath},
		{gattManagerIface + ".UnregisterApplication", gattApplicationPath},
		{method: "close"},
	})
}

func TestBLECleanupCancelsReRegistration(t *testing.T) {
	ownerChanges := make(chan *dbus.Signal, 1)
	restoring := make(chan struct{})
	object := &registrationObject{closed: make(chan struct{})}
	var applications atomic.Int32
	object.call = func(ctx context.Context, method string) error {
		if method == gattManagerIface+".RegisterApplication" && applications.Add(1) == 2 {
			close(restoring)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	cleanup, err := registerBLEPeripheral(context.Background(), zap.NewNop(), object, object.close, ownerChanges)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanup() })
	ownerChanges <- ownerChangedSignal(":1.10", ":1.20")
	select {
	case <-restoring:
	case <-time.After(time.Second):
		t.Fatal("re-registration did not start")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- cleanup() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup did not interrupt and join re-registration")
	}
	object.checkCalls(t, []registrationCall{
		{gattManagerIface + ".RegisterApplication", gattApplicationPath},
		{advManagerIface + ".RegisterAdvertisement", advObjectPath},
		{gattManagerIface + ".RegisterApplication", gattApplicationPath},
		{method: "close"},
	})
}

func TestBLERegistrationAcceptsQueuedOwnerChange(t *testing.T) {
	ownerChanges := make(chan *dbus.Signal, 1)
	restored := make(chan struct{})
	object := &registrationObject{closed: make(chan struct{})}
	var applications, advertisements atomic.Int32
	object.call = func(_ context.Context, method string) error {
		if method == gattManagerIface+".RegisterApplication" && applications.Add(1) > 1 {
			return dbus.NewError("org.bluez.Error.AlreadyExists", nil)
		}
		if method == advManagerIface+".RegisterAdvertisement" && advertisements.Add(1) > 1 {
			close(restored)
			return dbus.NewError("org.bluez.Error.AlreadyExists", nil)
		}
		return nil
	}
	// A restart notification can arrive during initial registration, after
	// the new daemon has already registered both objects successfully.
	ownerChanges <- ownerChangedSignal(":1.10", ":1.20")
	cleanup, err := registerBLEPeripheral(context.Background(), zap.NewNop(), object, object.close, ownerChanges)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanup() })
	select {
	case <-restored:
	case <-time.After(time.Second):
		t.Fatal("queued owner change got stuck retrying existing objects")
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	object.checkCalls(t, []registrationCall{
		{gattManagerIface + ".RegisterApplication", gattApplicationPath},
		{advManagerIface + ".RegisterAdvertisement", advObjectPath},
		{gattManagerIface + ".RegisterApplication", gattApplicationPath},
		{advManagerIface + ".RegisterAdvertisement", advObjectPath},
		{advManagerIface + ".UnregisterAdvertisement", advObjectPath},
		{gattManagerIface + ".UnregisterApplication", gattApplicationPath},
		{method: "close"},
	})
}

func TestBlueZOwnerChangeRejectsUnrelatedSignals(t *testing.T) {
	for _, signal := range []*dbus.Signal{
		nil,
		{Sender: "untrusted", Name: "org.freedesktop.DBus.NameOwnerChanged", Body: []any{bluezService, ":1.10", ":1.20"}},
		{Sender: "org.freedesktop.DBus", Name: "other", Body: []any{bluezService, ":1.10", ":1.20"}},
		{Sender: "org.freedesktop.DBus", Name: "org.freedesktop.DBus.NameOwnerChanged", Body: []any{"other", ":1.10", ":1.20"}},
		{Sender: "org.freedesktop.DBus", Name: "org.freedesktop.DBus.NameOwnerChanged", Body: []any{bluezService}},
		ownerChangedSignal(":1.10", ":1.10"),
	} {
		if _, changed := bluezOwnerChange(signal); changed {
			t.Errorf("accepted unrelated owner signal: %+v", signal)
		}
	}
}
