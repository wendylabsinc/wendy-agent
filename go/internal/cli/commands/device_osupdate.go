package commands

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// Outcome/health words carry their severity as color: green for a healthy or
// committed state, amber for a rollback that saved the device, red for
// anything that needs the user's attention.
var (
	osUpdateGoodStyle = lipgloss.NewStyle().Foreground(tui.ColorAccent).Bold(true)
	osUpdateWarnStyle = lipgloss.NewStyle().Foreground(tui.ColorNotice).Bold(true)
	osUpdateBadStyle  = lipgloss.NewStyle().Foreground(tui.ColorError).Bold(true)
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
	b.WriteString(tui.Dim("OS Update:") + "\n")

	for _, s := range engine.GetSlots() {
		b.WriteString("  " + formatOSUpdateSlot(s) + "\n")
	}
	if p := engine.GetPending(); p != nil {
		// "failed" means the deployment was marked failed and needs a
		// rollback — the one pending phase that is bad news.
		phase := tui.Dim(p.GetPhase())
		if p.GetPhase() == "failed" {
			phase = osUpdateBadStyle.Render(p.GetPhase())
		}
		fmt.Fprintf(&b, "  %s %s %s%s%s\n",
			tui.Dim("Pending:"),
			tui.Value(p.GetArtifactName()+" "+p.GetArtifactVersion()),
			tui.Dim("("), phase, tui.Dim(", target slot "+p.GetTargetSlot()+")"))
	}

	if resp.GetHasResult() {
		line := "  " + tui.Dim("Last update:") + " " + styledOSUpdateOutcome(resp.GetOutcome())
		if resp.GetOldOsVersion() != "" && resp.GetNewOsVersion() != "" {
			line += " " + tui.Dim(fmt.Sprintf("(%s → %s)", resp.GetOldOsVersion(), resp.GetNewOsVersion()))
		}
		b.WriteString(line + "\n")
		if resp.GetOutcome() != agentpb.GetOSUpdateStatusResponse_OUTCOME_COMMITTED {
			b.WriteString("  " + tui.Dim("Details:") + " " + tui.Command("wendy device os update-status") + "\n")
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
	parts := []string{tui.Value(state)}
	if h := s.GetRootfsHealth(); h != "" {
		style := osUpdateBadStyle
		if h == "normal" {
			style = osUpdateGoodStyle
		}
		parts = append(parts, tui.Value("rootfs ")+style.Render(h))
	}
	if d := s.GetDistro(); d != "" {
		parts = append(parts, tui.Value(d))
	}
	if r := s.GetRetries(); r != "" {
		parts = append(parts, tui.Value("retries "+r))
	}
	line := tui.Dim(fmt.Sprintf("Slot %s:", s.GetSlot())) + " " + strings.Join(parts, ", ")
	if n := s.GetNote(); n != "" {
		line += " " + tui.Dim("("+n+")")
	}
	return line
}

// styledOSUpdateOutcome colors the outcome word by severity: committed green,
// rolled back amber (the safety net worked), both failure modes red.
func styledOSUpdateOutcome(o agentpb.GetOSUpdateStatusResponse_Outcome) string {
	label := osUpdateOutcomeLabel(o)
	switch o {
	case agentpb.GetOSUpdateStatusResponse_OUTCOME_COMMITTED:
		return osUpdateGoodStyle.Render(label)
	case agentpb.GetOSUpdateStatusResponse_OUTCOME_ROLLED_BACK:
		return osUpdateWarnStyle.Render(label)
	case agentpb.GetOSUpdateStatusResponse_OUTCOME_ROLLBACK_FAILED,
		agentpb.GetOSUpdateStatusResponse_OUTCOME_COMMIT_FAILED:
		return osUpdateBadStyle.Render(label)
	default:
		return tui.Value(label)
	}
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
