package commands

import (
	"strings"
	"testing"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func fullOSUpdateStatus() *agentpb.GetOSUpdateStatusResponse {
	return &agentpb.GetOSUpdateStatusResponse{
		HasResult:     true,
		Outcome:       agentpb.GetOSUpdateStatusResponse_OUTCOME_COMMITTED,
		OldOsVersion:  "0.16.0",
		NewOsVersion:  "0.17.0",
		CreatedAtUnix: 1_750_000_000,
		EngineStatus: &agentpb.OSUpdateEngineStatus{
			Connector:   "tegrauefi",
			CurrentSlot: "A",
			Slots: []*agentpb.OSUpdateEngineStatus_Slot{
				{Slot: "A", Booted: true, Partition: "/dev/nvme0n1p1", Distro: "WendyOS 0.17.0", RootfsHealth: "normal"},
				{Slot: "B", Booted: false, Partition: "/dev/nvme0n1p2", Retries: "2", Note: "trial boot pending"},
			},
			System: []*agentpb.OSUpdateEngineStatus_SystemEntry{
				{Key: "bootloader", Value: "36.3.0"},
			},
			Pending: &agentpb.OSUpdateEngineStatus_PendingUpdate{
				ArtifactName:    "wendyos-jetson",
				ArtifactVersion: "0.18.0",
				Phase:           "installed",
				TargetSlot:      "B",
			},
		},
	}
}

func TestFormatOSUpdateInfo(t *testing.T) {
	got := formatOSUpdateInfo(fullOSUpdateStatus())

	for _, want := range []string{
		"OS Update:\n",
		"Slot A: booted, rootfs normal, WendyOS 0.17.0",
		"Slot B: inactive, retries 2 (trial boot pending)",
		"Pending: wendyos-jetson 0.18.0 (installed, target slot B)",
		"Last update: committed (0.16.0 → 0.17.0)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("formatOSUpdateInfo() missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Details:") {
		t.Errorf("committed outcome should not print the details hint:\n%s", got)
	}
}

func TestFormatOSUpdateInfoFailureShowsHint(t *testing.T) {
	resp := &agentpb.GetOSUpdateStatusResponse{
		HasResult: true,
		Outcome:   agentpb.GetOSUpdateStatusResponse_OUTCOME_ROLLED_BACK,
	}
	got := formatOSUpdateInfo(resp)
	if !strings.Contains(got, "Last update: rolled back") {
		t.Errorf("missing rolled-back line:\n%s", got)
	}
	if !strings.Contains(got, "wendy device os update-status") {
		t.Errorf("failure outcome should point at the detailed command:\n%s", got)
	}
}

func TestFormatOSUpdateInfoEmpty(t *testing.T) {
	if got := formatOSUpdateInfo(nil); got != "" {
		t.Errorf("formatOSUpdateInfo(nil) = %q, want empty", got)
	}
	// No record and no engine snapshot (e.g. a device with no update
	// history) — the section must disappear entirely.
	if got := formatOSUpdateInfo(&agentpb.GetOSUpdateStatusResponse{}); got != "" {
		t.Errorf("formatOSUpdateInfo(empty) = %q, want empty", got)
	}
}

func TestFormatOSUpdateInfoRecordOnly(t *testing.T) {
	// Old agents report only the persisted record.
	resp := &agentpb.GetOSUpdateStatusResponse{
		HasResult:    true,
		Outcome:      agentpb.GetOSUpdateStatusResponse_OUTCOME_COMMITTED,
		OldOsVersion: "0.15.0",
		NewOsVersion: "0.16.0",
	}
	got := formatOSUpdateInfo(resp)
	if !strings.Contains(got, "Last update: committed (0.15.0 → 0.16.0)") {
		t.Errorf("missing last-update line:\n%s", got)
	}
	if strings.Contains(got, "Slot") || strings.Contains(got, "Pending") {
		t.Errorf("record-only response should not render slot lines:\n%s", got)
	}
}

func TestOSUpdateJSON(t *testing.T) {
	out := osUpdateJSON(fullOSUpdateStatus())
	if out == nil {
		t.Fatal("osUpdateJSON returned nil for a populated response")
	}

	last, ok := out["lastUpdate"].(map[string]any)
	if !ok {
		t.Fatalf("lastUpdate missing: %+v", out)
	}
	if last["outcome"] != "committed" || last["oldOsVersion"] != "0.16.0" || last["newOsVersion"] != "0.17.0" {
		t.Errorf("lastUpdate mapped wrong: %+v", last)
	}

	engine, ok := out["engine"].(map[string]any)
	if !ok {
		t.Fatalf("engine missing: %+v", out)
	}
	if engine["connector"] != "tegrauefi" || engine["currentSlot"] != "A" {
		t.Errorf("engine header mapped wrong: %+v", engine)
	}
	slots, ok := engine["slots"].([]map[string]any)
	if !ok || len(slots) != 2 {
		t.Fatalf("slots mapped wrong: %+v", engine["slots"])
	}
	if slots[0]["slot"] != "A" || slots[0]["booted"] != true || slots[0]["rootfsHealth"] != "normal" {
		t.Errorf("slot A mapped wrong: %+v", slots[0])
	}
	pending, ok := engine["pending"].(map[string]any)
	if !ok {
		t.Fatalf("pending missing: %+v", engine)
	}
	if pending["artifactVersion"] != "0.18.0" || pending["targetSlot"] != "B" {
		t.Errorf("pending mapped wrong: %+v", pending)
	}

	if osUpdateJSON(nil) != nil {
		t.Error("osUpdateJSON(nil) should be nil")
	}
	if osUpdateJSON(&agentpb.GetOSUpdateStatusResponse{}) != nil {
		t.Error("osUpdateJSON(empty) should be nil")
	}
}
