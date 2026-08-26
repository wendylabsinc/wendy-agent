package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

func TestCountAddons(t *testing.T) {
	for n, want := range map[int]string{1: "1 driver add-on", 2: "2 driver add-ons"} {
		if got := countAddons(n); got != want {
			t.Errorf("countAddons(%d) = %q, want %q", n, got, want)
		}
	}
}

// The verdict decides whether an OS update may proceed, so each way of failing to
// stage has to reach it.
func TestDriverPreflightBlocking(t *testing.T) {
	tests := []struct {
		desc string
		pf   driverPreflight
		want bool
	}{
		{"nothing to report", driverPreflight{}, false},
		{"one add-on could not be staged", driverPreflight{
			unstaged: []unstagedAddon{{"acme-nic", "no rebuild published for 0.20.0"}},
		}, true},
		// The worst case: there may be no add-ons, or a critical one. Both look the
		// same from here, so it has to block.
		{"installed set unreadable", driverPreflight{unreadable: "context deadline exceeded"}, true},
	}
	for _, tt := range tests {
		if got := tt.pf.blocking(); got != tt.want {
			t.Errorf("%s: blocking() = %v, want %v", tt.desc, got, tt.want)
		}
	}
}

// The abort message names what the device would lose, so an operator reading a CI
// log knows which driver to go and rebuild.
func TestDriverPreflightSubject(t *testing.T) {
	got := driverPreflightSubject(driverPreflight{
		unstaged: []unstagedAddon{{"acme-nic", "x"}, {"acme-npu", "y"}},
	})
	if got != "acme-nic, acme-npu" {
		t.Errorf("subject = %q, want both names", got)
	}
	if got := driverPreflightSubject(driverPreflight{unreadable: "timeout"}); !strings.Contains(got, "could not be read") {
		t.Errorf("unreadable subject = %q, want it to say the set could not be read", got)
	}
}

// fakeDriverClient lets the pre-flight run against a driver service that fails
// the way a real one does when a device is slow or half-reachable.
type fakeDriverClient struct {
	agentpbv2.WendyDriverServiceClient
	listErr error
	list    *agentpbv2.ListDriversResponse
}

func (f fakeDriverClient) ListDrivers(context.Context, *agentpbv2.ListDriversRequest, ...grpc.CallOption) (*agentpbv2.ListDriversResponse, error) {
	return f.list, f.listErr
}

// The case that used to be silent: a ListDrivers timeout looked exactly like a
// device with no add-ons, so the update proceeded without a word. It must now
// reach the caller as a blocking verdict.
func TestWarnDriverAddons_ListDriversFailureBlocks(t *testing.T) {
	conn := &grpcclient.AgentConnection{
		DriverService: fakeDriverClient{listErr: status.Error(codes.DeadlineExceeded, "context deadline exceeded")},
	}
	pf := warnDriverAddonsBeforeUpdate(context.Background(), conn, "raspberry-pi-5", "0.20.0", 0, true)
	if !pf.blocking() {
		t.Fatal("a ListDrivers failure did not block the update")
	}
	if pf.unreadable == "" {
		t.Error("the failure was not recorded as unreadable")
	}
}

// An agent predating driver support answers Unimplemented. It has no add-ons to
// lose, and blocking would block the very update that adds support.
func TestWarnDriverAddons_UnimplementedDoesNotBlock(t *testing.T) {
	conn := &grpcclient.AgentConnection{
		DriverService: fakeDriverClient{listErr: status.Error(codes.Unimplemented, "unknown service")},
	}
	pf := warnDriverAddonsBeforeUpdate(context.Background(), conn, "raspberry-pi-5", "0.20.0", 0, true)
	if pf.blocking() {
		t.Error("an older agent blocked the update")
	}
}

// A device with add-ons and no way to stage them (no manifest resolved, as with a
// local artifact) must block, naming each one.
func TestWarnDriverAddons_NoManifestBlocksPerAddon(t *testing.T) {
	conn := &grpcclient.AgentConnection{DriverService: fakeDriverClient{
		list: &agentpbv2.ListDriversResponse{
			KernelVersion: "6.18.33-v8-16k",
			Installed: []*agentpbv2.InstalledDriver{
				{Name: "acme-nic", KernelVersion: "6.18.33-v8-16k"},
				{Name: "acme-npu", KernelVersion: "6.18.33-v8-16k"},
			},
		},
	}}
	pf := warnDriverAddonsBeforeUpdate(context.Background(), conn, "", "", 0, true)
	if len(pf.unstaged) != 2 || !pf.blocking() {
		t.Fatalf("unstaged = %+v, want both add-ons blocking", pf.unstaged)
	}
	// --no-drivers is the explicit opt-out and must never block.
	if pf := warnDriverAddonsBeforeUpdate(context.Background(), conn, "", "", 0, false); pf.blocking() {
		t.Error("--no-drivers blocked the update")
	}
}

func stubDriverExtensionsFor(t *testing.T, exts []extensionEntry, err error) {
	t.Helper()
	orig := driverExtensionsForFn
	driverExtensionsForFn = func(string, string, int) ([]extensionEntry, error) { return exts, err }
	t.Cleanup(func() { driverExtensionsForFn = orig })
}

func connWithAddons(names ...string) *grpcclient.AgentConnection {
	installed := make([]*agentpbv2.InstalledDriver, 0, len(names))
	for _, n := range names {
		installed = append(installed, &agentpbv2.InstalledDriver{Name: n, KernelVersion: "6.18.33-v8-16k"})
	}
	return &grpcclient.AgentConnection{DriverService: fakeDriverClient{
		list: &agentpbv2.ListDriversResponse{KernelVersion: "6.18.33-v8-16k", Installed: installed},
	}}
}

// If the published add-ons cannot be read, nothing can be staged — and the device
// is one reboot away from losing a driver it has. That has to block, not warn.
func TestWarnDriverAddons_ManifestFailureBlocks(t *testing.T) {
	stubDriverExtensionsFor(t, nil, errors.New("fetching manifest: 503 Service Unavailable"))

	pf := warnDriverAddonsBeforeUpdate(context.Background(), connWithAddons("acme-nic"), "raspberry-pi-5", "0.20.0", 0, true)
	if !pf.blocking() {
		t.Fatal("a manifest failure did not block the update")
	}
	if len(pf.unstaged) != 1 || !strings.Contains(pf.unstaged[0].reason, "503") {
		t.Errorf("unstaged = %+v, want the add-on named with the underlying cause", pf.unstaged)
	}
	// --no-drivers stays the explicit opt-out.
	if pf := warnDriverAddonsBeforeUpdate(context.Background(), connWithAddons("acme-nic"), "raspberry-pi-5", "0.20.0", 0, false); pf.blocking() {
		t.Error("--no-drivers blocked on a manifest failure")
	}
}

// A manifest that resolves but publishes no rebuild for this add-on is the CI
// race, and looks the same to the device: it must block too.
func TestWarnDriverAddons_NoRebuildPublishedBlocks(t *testing.T) {
	stubDriverExtensionsFor(t, []extensionEntry{
		{Name: "someone-else", KernelVersion: "6.19.0", Path: "p"},
	}, nil)

	pf := warnDriverAddonsBeforeUpdate(context.Background(), connWithAddons("acme-nic"), "raspberry-pi-5", "0.20.0", 0, true)
	if !pf.blocking() || len(pf.unstaged) != 1 {
		t.Fatalf("unstaged = %+v, want acme-nic blocking", pf.unstaged)
	}
	if !strings.Contains(pf.unstaged[0].reason, "no rebuild published") {
		t.Errorf("reason = %q, want it to say no rebuild was published", pf.unstaged[0].reason)
	}
}

// An add-on the target still publishes for the running kernel is unaffected, so
// the update must not be held up.
func TestWarnDriverAddons_UnaffectedAddonDoesNotBlock(t *testing.T) {
	stubDriverExtensionsFor(t, []extensionEntry{
		{Name: "acme-nic", KernelVersion: "6.18.33-v8-16k", Path: "p"},
	}, nil)

	pf := warnDriverAddonsBeforeUpdate(context.Background(), connWithAddons("acme-nic"), "raspberry-pi-5", "0.20.0", 0, true)
	if pf.blocking() {
		t.Errorf("an unaffected add-on blocked the update: %+v", pf.unstaged)
	}
}

// Piped into CI there is nobody to ask, so the pre-flight has to fail with a
// message naming what would be lost and the way to override it.
func TestConfirmDriverPreflight_NonInteractiveAborts(t *testing.T) {
	orig := isInteractiveTerminalFn
	isInteractiveTerminalFn = func() bool { return false }
	t.Cleanup(func() { isInteractiveTerminalFn = orig })

	err := confirmDriverPreflight(driverPreflight{
		unstaged: []unstagedAddon{{"acme-nic", "no rebuild published for 0.20.0"}},
	})
	if err == nil {
		t.Fatal("a non-interactive run was allowed to proceed")
	}
	if !strings.Contains(err.Error(), "acme-nic") || !strings.Contains(err.Error(), "--no-drivers") {
		t.Errorf("error = %q, want the add-on named and --no-drivers offered", err)
	}
}
