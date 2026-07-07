package services

import (
	"testing"
)

// Sample matching the frozen v1 `status --json` contract
// (wendyos-update docs/cli-contract.md), including a pending update.
const sampleEngineStatusJSON = `{
  "connector": "tegrauefi",
  "current_slot": "A",
  "slots": [
    {
      "slot": "A",
      "booted": true,
      "partition": "/dev/nvme0n1p1",
      "distro": "WendyOS 0.17.0",
      "kernel": "5.15.148-tegra",
      "rootfs_health": "normal"
    },
    {
      "slot": "B",
      "booted": false,
      "partition": "/dev/nvme0n1p2",
      "retries": "2",
      "note": "trial boot pending"
    }
  ],
  "system": [
    {"key": "bootloader", "value": "36.3.0"},
    {"key": "capsule status", "value": "none"}
  ],
  "pending": {
    "schema": 1,
    "phase": "installed",
    "target_slot": 1,
    "artifact_name": "wendyos-jetson",
    "artifact_version": "0.18.0",
    "bootloader_update": false
  },
  "diagnostics": {"RootfsStatusSlotA": "0x0"}
}`

func TestParseWendyOSEngineStatus(t *testing.T) {
	st, err := parseWendyOSEngineStatus([]byte(sampleEngineStatusJSON))
	if err != nil {
		t.Fatalf("parseWendyOSEngineStatus: %v", err)
	}

	if st.GetConnector() != "tegrauefi" {
		t.Errorf("connector = %q, want tegrauefi", st.GetConnector())
	}
	if st.GetCurrentSlot() != "A" {
		t.Errorf("current_slot = %q, want A", st.GetCurrentSlot())
	}

	if len(st.GetSlots()) != 2 {
		t.Fatalf("slots = %d, want 2", len(st.GetSlots()))
	}
	a := st.GetSlots()[0]
	if !a.GetBooted() || a.GetPartition() != "/dev/nvme0n1p1" || a.GetRootfsHealth() != "normal" ||
		a.GetDistro() != "WendyOS 0.17.0" || a.GetKernel() != "5.15.148-tegra" {
		t.Errorf("slot A mapped wrong: %+v", a)
	}
	b := st.GetSlots()[1]
	if b.GetBooted() || b.GetRetries() != "2" || b.GetNote() != "trial boot pending" {
		t.Errorf("slot B mapped wrong: %+v", b)
	}

	if len(st.GetSystem()) != 2 || st.GetSystem()[0].GetKey() != "bootloader" || st.GetSystem()[0].GetValue() != "36.3.0" {
		t.Errorf("system entries mapped wrong: %+v", st.GetSystem())
	}

	p := st.GetPending()
	if p == nil {
		t.Fatal("pending = nil, want populated")
	}
	if p.GetArtifactName() != "wendyos-jetson" || p.GetArtifactVersion() != "0.18.0" ||
		p.GetPhase() != "installed" || p.GetTargetSlot() != "B" {
		t.Errorf("pending mapped wrong: %+v", p)
	}
}

func TestParseWendyOSEngineStatusNoPending(t *testing.T) {
	st, err := parseWendyOSEngineStatus([]byte(`{"connector":"ubootenv","current_slot":"B","pending":null,"diagnostics":{}}`))
	if err != nil {
		t.Fatalf("parseWendyOSEngineStatus: %v", err)
	}
	if st.GetPending() != nil {
		t.Errorf("pending = %+v, want nil", st.GetPending())
	}
	if st.GetCurrentSlot() != "B" {
		t.Errorf("current_slot = %q, want B", st.GetCurrentSlot())
	}
}

func TestParseWendyOSEngineStatusRejectsMalformedJSON(t *testing.T) {
	if _, err := parseWendyOSEngineStatus([]byte("not json")); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestEngineStatusToProtoV2RoundTrip(t *testing.T) {
	v1, err := parseWendyOSEngineStatus([]byte(sampleEngineStatusJSON))
	if err != nil {
		t.Fatalf("parseWendyOSEngineStatus: %v", err)
	}
	v2 := engineStatusToProtoV2(v1)
	if v2.GetConnector() != v1.GetConnector() || v2.GetCurrentSlot() != v1.GetCurrentSlot() {
		t.Errorf("v2 header mismatch: %+v", v2)
	}
	if len(v2.GetSlots()) != len(v1.GetSlots()) || len(v2.GetSystem()) != len(v1.GetSystem()) {
		t.Errorf("v2 collection sizes mismatch: %+v", v2)
	}
	if v2.GetPending().GetTargetSlot() != "B" {
		t.Errorf("v2 pending target slot = %q, want B", v2.GetPending().GetTargetSlot())
	}
	if engineStatusToProtoV2(nil) != nil {
		t.Error("engineStatusToProtoV2(nil) should be nil")
	}
}
