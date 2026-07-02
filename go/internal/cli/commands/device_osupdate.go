package commands

import (
	"fmt"
	"strings"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// formatOSUpdateInfo renders the OS-update block for `wendy device info`: the
// live wendyos-update A/B slot snapshot (when the agent returned one) plus a
// one-line summary of the last recorded update outcome. Returns "" when there
// is nothing to show, so devices without OTA support keep their old output.
func formatOSUpdateInfo(resp *agentpb.GetOSUpdateStatusResponse) string {
	if resp == nil {
		return ""
	}
	engine := resp.GetEngineStatus()
	if engine == nil && !resp.GetHasResult() {
		return ""
	}

	var b strings.Builder
	b.WriteString("OS Update:\n")

	for _, s := range engine.GetSlots() {
		b.WriteString("  " + formatOSUpdateSlot(s) + "\n")
	}
	if p := engine.GetPending(); p != nil {
		fmt.Fprintf(&b, "  Pending: %s %s (%s, target slot %s)\n",
			p.GetArtifactName(), p.GetArtifactVersion(), p.GetPhase(), p.GetTargetSlot())
	}

	if resp.GetHasResult() {
		line := "  Last update: " + osUpdateOutcomeLabel(resp.GetOutcome())
		if resp.GetOldOsVersion() != "" && resp.GetNewOsVersion() != "" {
			line += fmt.Sprintf(" (%s → %s)", resp.GetOldOsVersion(), resp.GetNewOsVersion())
		}
		b.WriteString(line + "\n")
		if resp.GetOutcome() != agentpb.GetOSUpdateStatusResponse_OUTCOME_COMMITTED {
			b.WriteString("  Details: wendy device os update-status\n")
		}
	}

	return b.String()
}

// formatOSUpdateSlot renders one A/B slot as a single compact line, e.g.
// "Slot A: booted, rootfs normal, WendyOS 0.17.0".
func formatOSUpdateSlot(s *agentpb.OSUpdateEngineStatus_Slot) string {
	state := "inactive"
	if s.GetBooted() {
		state = "booted"
	}
	parts := []string{state}
	if h := s.GetRootfsHealth(); h != "" {
		parts = append(parts, "rootfs "+h)
	}
	if d := s.GetDistro(); d != "" {
		parts = append(parts, d)
	}
	if r := s.GetRetries(); r != "" {
		parts = append(parts, "retries "+r)
	}
	line := fmt.Sprintf("Slot %s: %s", s.GetSlot(), strings.Join(parts, ", "))
	if n := s.GetNote(); n != "" {
		line += " (" + n + ")"
	}
	return line
}

// osUpdateOutcomeLabel is the compact outcome wording used in `device info`;
// the full record stays with `wendy device os update-status`.
func osUpdateOutcomeLabel(o agentpb.GetOSUpdateStatusResponse_Outcome) string {
	switch o {
	case agentpb.GetOSUpdateStatusResponse_OUTCOME_COMMITTED:
		return "committed"
	case agentpb.GetOSUpdateStatusResponse_OUTCOME_ROLLED_BACK:
		return "rolled back"
	case agentpb.GetOSUpdateStatusResponse_OUTCOME_ROLLBACK_FAILED:
		return "rollback failed"
	case agentpb.GetOSUpdateStatusResponse_OUTCOME_COMMIT_FAILED:
		return "commit failed"
	default:
		return "unknown"
	}
}

// osUpdateJSON is the `--json` counterpart of formatOSUpdateInfo. Returns nil
// when there is nothing to report so the key is omitted entirely.
func osUpdateJSON(resp *agentpb.GetOSUpdateStatusResponse) map[string]any {
	if resp == nil {
		return nil
	}
	engine := resp.GetEngineStatus()
	if engine == nil && !resp.GetHasResult() {
		return nil
	}

	out := map[string]any{}

	if resp.GetHasResult() {
		last := map[string]any{
			"outcome":       osUpdateOutcomeLabel(resp.GetOutcome()),
			"createdAtUnix": resp.GetCreatedAtUnix(),
		}
		if v := resp.GetOldOsVersion(); v != "" {
			last["oldOsVersion"] = v
		}
		if v := resp.GetNewOsVersion(); v != "" {
			last["newOsVersion"] = v
		}
		if n := resp.GetNote(); n != "" {
			last["note"] = n
		}
		out["lastUpdate"] = last
	}

	if engine != nil {
		e := map[string]any{
			"connector":   engine.GetConnector(),
			"currentSlot": engine.GetCurrentSlot(),
		}
		if len(engine.GetSlots()) > 0 {
			slots := make([]map[string]any, len(engine.GetSlots()))
			for i, s := range engine.GetSlots() {
				slots[i] = map[string]any{
					"slot":         s.GetSlot(),
					"booted":       s.GetBooted(),
					"partition":    s.GetPartition(),
					"distro":       s.GetDistro(),
					"kernel":       s.GetKernel(),
					"rootfsHealth": s.GetRootfsHealth(),
					"retries":      s.GetRetries(),
					"note":         s.GetNote(),
				}
			}
			e["slots"] = slots
		}
		if len(engine.GetSystem()) > 0 {
			system := make([]map[string]any, len(engine.GetSystem()))
			for i, kv := range engine.GetSystem() {
				system[i] = map[string]any{"key": kv.GetKey(), "value": kv.GetValue()}
			}
			e["system"] = system
		}
		if p := engine.GetPending(); p != nil {
			e["pending"] = map[string]any{
				"artifactName":    p.GetArtifactName(),
				"artifactVersion": p.GetArtifactVersion(),
				"phase":           p.GetPhase(),
				"targetSlot":      p.GetTargetSlot(),
			}
		}
		out["engine"] = e
	}

	return out
}
