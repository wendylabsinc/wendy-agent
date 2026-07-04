package services

import (
	"context"
	"encoding/json"
	"os/exec"
	"time"

	"go.uber.org/zap"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// wendyOSStatusProbeTimeout bounds the `wendyos-update status --json` probe
// used to enrich GetOSUpdateStatus. Matches the detect() probe budget.
const wendyOSStatusProbeTimeout = 10 * time.Second

// Wire shape of `wendyos-update status --json`, per the frozen v1 CLI
// contract (wendyos-update docs/cli-contract.md). Additive-only, so unknown
// fields are safely ignored.
type wendyOSEngineStatus struct {
	Connector   string                `json:"connector"`
	CurrentSlot string                `json:"current_slot"`
	Slots       []wendyOSEngineSlot   `json:"slots"`
	System      []wendyOSEngineKV     `json:"system"`
	Pending     *wendyOSEnginePending `json:"pending"`
}

type wendyOSEngineSlot struct {
	Slot         string `json:"slot"`
	Booted       bool   `json:"booted"`
	Partition    string `json:"partition"`
	Distro       string `json:"distro"`
	Kernel       string `json:"kernel"`
	RootfsHealth string `json:"rootfs_health"`
	Retries      string `json:"retries"`
	Note         string `json:"note"`
}

type wendyOSEngineKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type wendyOSEnginePending struct {
	ArtifactName    string `json:"artifact_name"`
	ArtifactVersion string `json:"artifact_version"`
	Phase           string `json:"phase"`
	TargetSlot      int    `json:"target_slot"`
}

// parseWendyOSEngineStatus decodes `wendyos-update status --json` output into
// the v1 proto message. Pure function, unit-tested.
func parseWendyOSEngineStatus(out []byte) (*agentpb.OSUpdateEngineStatus, error) {
	var st wendyOSEngineStatus
	if err := json.Unmarshal(out, &st); err != nil {
		return nil, err
	}
	resp := &agentpb.OSUpdateEngineStatus{
		Connector:   st.Connector,
		CurrentSlot: st.CurrentSlot,
	}
	for _, s := range st.Slots {
		resp.Slots = append(resp.Slots, &agentpb.OSUpdateEngineStatus_Slot{
			Slot:         s.Slot,
			Booted:       s.Booted,
			Partition:    s.Partition,
			Distro:       s.Distro,
			Kernel:       s.Kernel,
			RootfsHealth: s.RootfsHealth,
			Retries:      s.Retries,
			Note:         s.Note,
		})
	}
	for _, kv := range st.System {
		resp.System = append(resp.System, &agentpb.OSUpdateEngineStatus_SystemEntry{
			Key:   kv.Key,
			Value: kv.Value,
		})
	}
	if st.Pending != nil {
		resp.Pending = &agentpb.OSUpdateEngineStatus_PendingUpdate{
			ArtifactName:    st.Pending.ArtifactName,
			ArtifactVersion: st.Pending.ArtifactVersion,
			Phase:           st.Pending.Phase,
			TargetSlot:      slotName(st.Pending.TargetSlot),
		}
	}
	return resp, nil
}

// slotName renders the engine's numeric slot (state-schema target_slot) the
// way the engine itself does: 0 → "A", anything else → "B".
func slotName(slot int) string {
	if slot == 0 {
		return "A"
	}
	return "B"
}

// probeWendyOSEngineStatus runs `wendyos-update status --json` and returns the
// parsed snapshot, or nil when the device does not use wendyos-update or the
// probe fails. Callers treat the result as best-effort enrichment: a nil
// return never fails the surrounding RPC.
func probeWendyOSEngineStatus(ctx context.Context, logger *zap.Logger) *agentpb.OSUpdateEngineStatus {
	binary, found := resolveWendyOSBinary()
	if !found {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, wendyOSStatusProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "status", "--json")
	cmd.Env = envWithPath("/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	out, err := cmd.Output()
	if err != nil {
		logger.Debug("wendyos-update status probe failed; omitting engine status", zap.Error(err))
		return nil
	}
	status, err := parseWendyOSEngineStatus(out)
	if err != nil {
		logger.Warn("wendyos-update status --json output could not be parsed", zap.Error(err))
		return nil
	}
	return status
}

// engineStatusToProtoV2 mirrors the v1 engine-status message into its
// identical v2 counterpart.
func engineStatusToProtoV2(v1 *agentpb.OSUpdateEngineStatus) *agentpbv2.OSUpdateEngineStatus {
	if v1 == nil {
		return nil
	}
	resp := &agentpbv2.OSUpdateEngineStatus{
		Connector:   v1.GetConnector(),
		CurrentSlot: v1.GetCurrentSlot(),
	}
	for _, s := range v1.GetSlots() {
		resp.Slots = append(resp.Slots, &agentpbv2.OSUpdateEngineStatus_Slot{
			Slot:         s.GetSlot(),
			Booted:       s.GetBooted(),
			Partition:    s.GetPartition(),
			Distro:       s.GetDistro(),
			Kernel:       s.GetKernel(),
			RootfsHealth: s.GetRootfsHealth(),
			Retries:      s.GetRetries(),
			Note:         s.GetNote(),
		})
	}
	for _, kv := range v1.GetSystem() {
		resp.System = append(resp.System, &agentpbv2.OSUpdateEngineStatus_SystemEntry{
			Key:   kv.GetKey(),
			Value: kv.GetValue(),
		})
	}
	if p := v1.GetPending(); p != nil {
		resp.Pending = &agentpbv2.OSUpdateEngineStatus_PendingUpdate{
			ArtifactName:    p.GetArtifactName(),
			ArtifactVersion: p.GetArtifactVersion(),
			Phase:           p.GetPhase(),
			TargetSlot:      p.GetTargetSlot(),
		}
	}
	return resp
}
