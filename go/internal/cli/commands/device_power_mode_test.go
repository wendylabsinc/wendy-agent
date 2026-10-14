package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

type fakePowerModeClient struct {
	agentpb.WendyAgentServiceClient
	get    *agentpb.GetPowerModeResponse
	set    *agentpb.SetPowerModeResponse
	err    error
	setReq *agentpb.SetPowerModeRequest
}

func (f *fakePowerModeClient) GetPowerMode(context.Context, *agentpb.GetPowerModeRequest, ...grpc.CallOption) (*agentpb.GetPowerModeResponse, error) {
	return f.get, f.err
}

func (f *fakePowerModeClient) SetPowerMode(_ context.Context, req *agentpb.SetPowerModeRequest, _ ...grpc.CallOption) (*agentpb.SetPowerModeResponse, error) {
	f.setReq = req
	return f.set, f.err
}

func orinPowerModes() *agentpb.GetPowerModeResponse {
	return &agentpb.GetPowerModeResponse{
		Current: &agentpb.PowerMode{Id: 2, Name: "MODE_30W"},
		Available: []*agentpb.PowerMode{
			{Id: 0, Name: "MAXN"}, {Id: 1, Name: "MODE_15W"}, {Id: 2, Name: "MODE_30W"}, {Id: 3, Name: "MODE_50W"},
		},
		BootDefault: &agentpb.PowerMode{Id: 2, Name: "MODE_30W"},
	}
}

func TestPowerModeShowHuman(t *testing.T) {
	var out bytes.Buffer
	if err := runPowerModeShow(context.Background(), &fakePowerModeClient{get: orinPowerModes()}, &out, false); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"MODE_30W (2)", "MAXN", "MODE_50W", "current, boot default"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestPowerModeShowJSON(t *testing.T) {
	var out bytes.Buffer
	if err := runPowerModeShow(context.Background(), &fakePowerModeClient{get: orinPowerModes()}, &out, true); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Current     map[string]any   `json:"current"`
		Available   []map[string]any `json:"available"`
		BootDefault map[string]any   `json:"bootDefault"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out.String())
	}
	if doc.Current["name"] != "MODE_30W" || doc.Current["id"] != float64(2) || len(doc.Available) != 4 || doc.BootDefault["name"] != "MODE_30W" {
		t.Errorf("doc = %+v", doc)
	}
}

func TestPowerModeSetOutcomes(t *testing.T) {
	maxn := &agentpb.PowerMode{Id: 0, Name: "MAXN"}
	cases := []struct {
		name     string
		resp     *agentpb.SetPowerModeResponse
		reboot   bool
		contains string
	}{
		{"live", &agentpb.SetPowerModeResponse{Mode: maxn}, false, "Power mode set to MAXN (0)"},
		{"reboot needed", &agentpb.SetPowerModeResponse{Mode: maxn, RebootRequired: true}, false, "was not rebooted"},
		{"reboot scheduled", &agentpb.SetPowerModeResponse{Mode: maxn, RebootRequired: true, RebootScheduled: true}, true, "rebooting the device now"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakePowerModeClient{set: tc.resp}
			var out bytes.Buffer
			if err := runPowerModeSet(context.Background(), f, "MAXN", tc.reboot, &out, false); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tc.contains) {
				t.Errorf("output %q missing %q", out.String(), tc.contains)
			}
			if f.setReq.GetMode() != "MAXN" || f.setReq.GetReboot() != tc.reboot {
				t.Errorf("request = %v", f.setReq)
			}
		})
	}
}

func TestPowerModeSetJSON(t *testing.T) {
	f := &fakePowerModeClient{set: &agentpb.SetPowerModeResponse{Mode: &agentpb.PowerMode{Id: 0, Name: "MAXN"}, RebootRequired: true}}
	var out bytes.Buffer
	if err := runPowerModeSet(context.Background(), f, "0", false, &out, true); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["rebootRequired"] != true || doc["rebootScheduled"] != false || doc["mode"].(map[string]any)["name"] != "MAXN" {
		t.Errorf("doc = %v", doc)
	}
}

func TestPowerModeErrors(t *testing.T) {
	cases := []struct {
		code codes.Code
		msg  string
		want string
	}{
		{codes.Unimplemented, "method not implemented", "update the agent"},
		{codes.Unavailable, "power modes are only supported on NVIDIA Jetson devices", "only supported on NVIDIA Jetson"},
		{codes.InvalidArgument, `unknown power mode "x"; available: MAXN (0)`, `unknown power mode "x"`},
	}
	for _, tc := range cases {
		f := &fakePowerModeClient{err: status.Error(tc.code, tc.msg)}
		var out bytes.Buffer
		for name, err := range map[string]error{
			"show": runPowerModeShow(context.Background(), f, &out, false),
			"set":  runPowerModeSet(context.Background(), f, "x", false, &out, false),
		} {
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s %v: err = %v, want containing %q", name, tc.code, err, tc.want)
			}
		}
	}
}

func TestPowerModeCommandShape(t *testing.T) {
	cmd := newDevicePowerModeCmd()
	set, _, err := cmd.Find([]string{"set"})
	if err != nil || set.Name() != "set" {
		t.Fatalf("set subcommand not found: %v", err)
	}
	if set.Flags().Lookup("reboot") == nil {
		t.Error("set has no --reboot flag")
	}
	if set.Flags().Lookup("reboot").DefValue != "false" {
		t.Error("--reboot must default to false")
	}
	if err := set.Args(set, nil); err == nil {
		t.Error("set without a mode should be rejected")
	}
}
