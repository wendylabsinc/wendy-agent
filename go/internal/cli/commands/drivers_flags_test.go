package commands

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

func TestDriversInvalidFlagsBeforeConnection(t *testing.T) {
	orig := resolveDriversTargetFn
	resolveDriversTargetFn = func(context.Context, ...resolveOption) (*SelectedDevice, error) {
		t.Fatal("invalid flags must not connect to a device")
		return nil, nil
	}
	t.Cleanup(func() { resolveDriversTargetFn = orig })
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--nightly", "--pr", "123"}, "mutually exclusive"},
		{[]string{"list", "--available", "--nightly", "--pr", "123"}, "mutually exclusive"},
		{[]string{"install", "driver", "--nightly", "--pr", "123"}, "mutually exclusive"},
		{[]string{"--pr", "0"}, "positive PR number"},
		{[]string{"list", "--available", "--pr", "-1"}, "positive PR number"},
		{[]string{"install", "driver", "--pr", "0"}, "positive PR number"},
		{[]string{"list", "--nightly"}, "require --available"},
		{[]string{"list", "--pr", "123"}, "require --available"},
		{[]string{"install", "driver", "--file", "driver.raw", "--nightly"}, "--file cannot be combined"},
		{[]string{"install", "driver", "--file", "driver.raw", "--pr", "123"}, "--file cannot be combined"},
		{[]string{"unexpected"}, "unknown command"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			cmd := newDriversCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(tc.args)
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDriversChannelFlagsReachCatalog(t *testing.T) {
	origResolve, origCatalog := resolveDriversTargetFn, driverExtensionsForChannelFn
	origInteractive, origJSON := isInteractiveTerminalFn, jsonOutput
	origPick, origAction := pickDriverFn, pickDriverActionFn
	t.Cleanup(func() {
		resolveDriversTargetFn, driverExtensionsForChannelFn = origResolve, origCatalog
		isInteractiveTerminalFn, jsonOutput = origInteractive, origJSON
		pickDriverFn, pickDriverActionFn = origPick, origAction
	})
	isInteractiveTerminalFn = func() bool { return true }
	jsonOutput = false
	pickDriverFn = func(string, []tui.PickerItem) (string, error) { return "driver", nil }
	pickDriverActionFn = func(string, []tui.PickerItem) (string, error) { return "cancel", nil }

	for _, tc := range []struct {
		args    []string
		nightly bool
		pr      int
		install bool
	}{
		{args: []string{"--nightly"}, nightly: true},
		{args: []string{"--pr", "123"}, pr: 123},
		{args: []string{"list", "--available", "--nightly"}, nightly: true},
		{args: []string{"--nightly", "list", "--available"}, nightly: true},
		{args: []string{"install", "driver", "--nightly"}, nightly: true, install: true},
		{args: []string{"--nightly", "install", "driver"}, nightly: true, install: true},
		{args: []string{"install", "driver", "--pr", "123"}, pr: 123, install: true},
		{args: []string{"install", "driver"}, install: true},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var installs []*fakeInstallStream
			deviceType := "raspberry-pi-5"
			resolveDriversTargetFn = func(context.Context, ...resolveOption) (*SelectedDevice, error) {
				return &SelectedDevice{Agent: &grpcclient.AgentConnection{
					AgentService: &fakeAgentVersionClient{resp: &agentpb.GetAgentVersionResponse{DeviceType: &deviceType}},
					DriverService: fakeDriverClient{
						list:     &agentpbv2.ListDriversResponse{BaseVersion: "0.17.0", KernelVersion: "kernel"},
						installs: &installs,
					},
				}}, nil
			}
			calls := 0
			driverExtensionsForChannelFn = func(device, version string, nightly bool, pr int) ([]extensionEntry, error) {
				calls++
				if device != "raspberry-pi-5" || version != "0.17.0" || nightly != tc.nightly || pr != tc.pr {
					t.Fatalf("catalog coordinates = %q %q nightly=%v pr=%d", device, version, nightly, pr)
				}
				return []extensionEntry{
					{Name: "driver", KernelVersion: "other-kernel", Path: "wrong.raw"},
					{Name: "driver", KernelVersion: "kernel", Path: "correct.raw"},
				}, nil
			}
			cmd := newDriversCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(tc.args)
			captureStdout(t, func() {
				if err := cmd.Execute(); err != nil {
					t.Fatal(err)
				}
			})
			if calls != 1 {
				t.Fatalf("catalog calls = %d, want 1", calls)
			}
			if tc.install {
				if len(installs) != 1 || len(installs[0].specs) != 1 || installs[0].specs[0].GetArtifactUrl() != gcsBaseURL+"/correct.raw" {
					t.Fatalf("install did not send the compatible published driver: %+v", installs)
				}
			} else if len(installs) != 0 {
				t.Fatal("listing or cancelling unexpectedly installed a driver")
			}
		})
	}
}
