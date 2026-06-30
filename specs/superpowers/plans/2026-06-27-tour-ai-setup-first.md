# Tour AI-Setup-First Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the `wendy tour` wizard's Claude/Codex detection and Wendy MCP setup from the final step to immediately after the welcome screen, skipping silently when no AI CLI is installed.

**Architecture:** The tour is a single Bubble Tea `tourWizardModel` in `go/internal/cli/commands/tour.go`. Flow is driven by phase transitions inside `Update`/`handleKey`, not by enum order. We re-point a handful of transitions so the existing `phaseAICheck` → `phaseAIMCPSetup` screens run right after `phaseWelcome` and lead into `phaseLoadDevices`, and so the post-deploy path goes straight to `phaseCloud`. The supporting commands (`cmdCheckAITools`, `runMCPSetupCmd`, `setupMCPForAllTools`) are reused unchanged.

**Tech Stack:** Go, charmbracelet/bubbletea, standard `testing`.

## Global Constraints

- All code changes live in `go/internal/cli/commands/tour.go`; tests in `go/internal/cli/commands/tour_test.go`. Package `commands`.
- Do NOT reorder the `tourPhase` `iota` enum — flow is transition-driven.
- Do NOT modify `setupMCPForAllTools()` or any MCP config writer in `mcp_setup.go`.
- Do NOT execute the `tea.Cmd` returned by `runMCPSetupCmd()` in tests — it writes real user config files. Assert it is non-nil instead.
- `tourWizardModel.Update` has a value receiver and returns `tea.Model`; type-assert the result back to `tourWizardModel` to read `.phase`.

---

### Task 1: Reposition the AI-setup routing

Re-point the four transitions that currently place AI setup at the end so it runs after welcome and flows into device loading, and broaden the MCP-setup offer to cover Codex (not just Claude). Drive with unit tests against `Update`.

**Files:**
- Modify: `go/internal/cli/commands/tour.go` (handlers in `Update` ~L350-363 and `handleKey` ~L429-436, ~L779-804)
- Test: `go/internal/cli/commands/tour_test.go`

**Interfaces:**
- Consumes (existing, unchanged signatures):
  - `newTourWizardModel() tourWizardModel`
  - `func (m tourWizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd)`
  - `tourAICheckDoneMsg struct{ claudePath, codexPath string }`
  - `tourMCPSetupDoneMsg struct{ results []mcpSetupResult }`
  - `tourRunDoneMsg struct{ err error }`
  - `loadDevicesCmd() tea.Cmd`, `runMCPSetupCmd() tea.Cmd`, `func (m tourWizardModel) cmdCheckAITools() tea.Cmd`
  - phase constants: `phaseWelcome`, `phaseAICheck`, `phaseAIMCPSetup`, `phaseLoadDevices`, `phaseCloud`
- Produces: the new routing behavior asserted by the tests below.

- [ ] **Step 1: Write the failing tests**

Add to `go/internal/cli/commands/tour_test.go`. The file currently has a single-line `import "testing"` — replace it with the block below (merge, do not add a second import block):

```go
import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// phaseOf runs one Update and returns the resulting model + command.
func stepTour(t *testing.T, m tourWizardModel, msg tea.Msg) (tourWizardModel, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	tm, ok := next.(tourWizardModel)
	if !ok {
		t.Fatalf("Update returned %T, want tourWizardModel", next)
	}
	return tm, cmd
}

func TestTourAICheckRouting(t *testing.T) {
	t.Run("claude detected goes to AI step", func(t *testing.T) {
		m := newTourWizardModel()
		m.phase = phaseWelcome
		m, _ = stepTour(t, m, tourAICheckDoneMsg{claudePath: "/usr/bin/claude"})
		if m.phase != phaseAICheck {
			t.Fatalf("phase = %v, want phaseAICheck", m.phase)
		}
	})

	t.Run("codex detected goes to AI step", func(t *testing.T) {
		m := newTourWizardModel()
		m.phase = phaseWelcome
		m, _ = stepTour(t, m, tourAICheckDoneMsg{codexPath: "/usr/bin/codex"})
		if m.phase != phaseAICheck {
			t.Fatalf("phase = %v, want phaseAICheck", m.phase)
		}
	})

	t.Run("neither detected skips to device load", func(t *testing.T) {
		m := newTourWizardModel()
		m.phase = phaseWelcome
		m, cmd := stepTour(t, m, tourAICheckDoneMsg{})
		if m.phase != phaseLoadDevices {
			t.Fatalf("phase = %v, want phaseLoadDevices", m.phase)
		}
		if cmd == nil {
			t.Fatal("expected loadDevicesCmd, got nil")
		}
	})
}

func TestTourAIStepTransitions(t *testing.T) {
	t.Run("skip leads to device load", func(t *testing.T) {
		m := newTourWizardModel()
		m.phase = phaseAICheck
		m.codexPath = "/usr/bin/codex"
		m.wifiCursor = 1 // "No, skip"
		m, cmd := stepTour(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		if m.phase != phaseLoadDevices {
			t.Fatalf("phase = %v, want phaseLoadDevices", m.phase)
		}
		if cmd == nil {
			t.Fatal("expected loadDevicesCmd, got nil")
		}
	})

	t.Run("yes with codex only triggers MCP setup", func(t *testing.T) {
		m := newTourWizardModel()
		m.phase = phaseAICheck
		m.codexPath = "/usr/bin/codex" // no claude
		m.wifiCursor = 0               // "Yes, set up MCP"
		m, cmd := stepTour(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			t.Fatal("expected runMCPSetupCmd, got nil") // not executed — would write config
		}
		if m.phase != phaseAICheck {
			t.Fatalf("phase = %v, want phaseAICheck (waiting for setup result)", m.phase)
		}
	})

	t.Run("mcp results screen leads to device load", func(t *testing.T) {
		m := newTourWizardModel()
		m.phase = phaseAIMCPSetup
		m, cmd := stepTour(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		if m.phase != phaseLoadDevices {
			t.Fatalf("phase = %v, want phaseLoadDevices", m.phase)
		}
		if cmd == nil {
			t.Fatal("expected loadDevicesCmd, got nil")
		}
	})
}

func TestTourDeployGoesToCloud(t *testing.T) {
	m := newTourWizardModel()
	m.phase = phaseRunProject
	m, _ = stepTour(t, m, tourRunDoneMsg{})
	if m.phase != phaseCloud {
		t.Fatalf("phase = %v, want phaseCloud", m.phase)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd go && go test ./internal/cli/commands/ -run 'TestTourAICheckRouting|TestTourAIStepTransitions|TestTourDeployGoesToCloud' -v`
Expected: FAIL — `neither detected` lands on `phaseAICheck` (current code routes unconditionally), `skip`/`mcp results` land on `phaseCloud`, `codex only` does not trigger MCP setup, and deploy routes to `phaseAICheck`.

- [ ] **Step 3: Re-point the `tourAICheckDoneMsg` handler**

In `tour.go`, replace:

```go
	case tourAICheckDoneMsg:
		m.claudePath = msg.claudePath
		m.codexPath = msg.codexPath
		m.phase = phaseAICheck
		return m, nil
```

with:

```go
	case tourAICheckDoneMsg:
		m.claudePath = msg.claudePath
		m.codexPath = msg.codexPath
		if m.claudePath != "" || m.codexPath != "" {
			m.phase = phaseAICheck
			return m, nil
		}
		// No AI CLI installed — skip the AI step entirely.
		m.phase = phaseLoadDevices
		return m, loadDevicesCmd()
```

- [ ] **Step 4: Send the deploy-completion path straight to the cloud screen**

Replace:

```go
	case tourRunDoneMsg:
		if msg.err != nil {
			m.err = msg.err
			m.phase = phaseError
			return m, nil
		}
		m.phase = phaseAICheck
		return m, m.cmdCheckAITools()
```

with:

```go
	case tourRunDoneMsg:
		if msg.err != nil {
			m.err = msg.err
			m.phase = phaseError
			return m, nil
		}
		m.phase = phaseCloud
		return m, nil
```

- [ ] **Step 5: Fire the AI check from the welcome screen**

In `handleKey`, replace the `phaseWelcome` enter branch:

```go
	case phaseWelcome:
		switch key {
		case "enter", " ":
			m.phase = phaseLoadDevices
			return m, loadDevicesCmd()
		case "q", "ctrl+c":
			return m, tea.Quit
		}
```

with:

```go
	case phaseWelcome:
		switch key {
		case "enter", " ":
			return m, m.cmdCheckAITools()
		case "q", "ctrl+c":
			return m, tea.Quit
		}
```

- [ ] **Step 6: Update the AI-step key handler (Codex offer + continue to device load)**

Replace the `phaseAICheck` branch:

```go
	case phaseAICheck:
		switch key {
		case "up", "k":
			if m.wifiCursor > 0 {
				m.wifiCursor--
			}
		case "down", "j":
			if m.wifiCursor < 1 {
				m.wifiCursor++
			}
		case "enter", " ":
			if m.claudePath != "" && m.wifiCursor == 0 {
				return m, runMCPSetupCmd()
			}
			m.phase = phaseCloud
		case "q", "ctrl+c":
			return m, tea.Quit
		}
```

with:

```go
	case phaseAICheck:
		switch key {
		case "up", "k":
			if m.wifiCursor > 0 {
				m.wifiCursor--
			}
		case "down", "j":
			if m.wifiCursor < 1 {
				m.wifiCursor++
			}
		case "enter", " ":
			if (m.claudePath != "" || m.codexPath != "") && m.wifiCursor == 0 {
				return m, runMCPSetupCmd()
			}
			m.phase = phaseLoadDevices
			return m, loadDevicesCmd()
		case "q", "ctrl+c":
			return m, tea.Quit
		}
```

- [ ] **Step 7: Continue from the MCP results screen into device load**

Replace the `phaseAIMCPSetup` branch:

```go
	case phaseAIMCPSetup:
		switch key {
		case "enter", " ":
			m.phase = phaseCloud
		case "q", "ctrl+c":
			return m, tea.Quit
		}
```

with:

```go
	case phaseAIMCPSetup:
		switch key {
		case "enter", " ":
			m.phase = phaseLoadDevices
			return m, loadDevicesCmd()
		case "q", "ctrl+c":
			return m, tea.Quit
		}
```

- [ ] **Step 8: Run the tests to verify they pass**

Run: `cd go && go test ./internal/cli/commands/ -run 'TestTourAICheckRouting|TestTourAIStepTransitions|TestTourDeployGoesToCloud' -v`
Expected: PASS (all subtests).

- [ ] **Step 9: Commit**

```bash
git add go/internal/cli/commands/tour.go go/internal/cli/commands/tour_test.go
git commit -m "feat: run tour AI/MCP setup right after welcome"
```

---

### Task 2: Reframe the AI and welcome copy for the front of the tour

The screens now render before any device or project exists. Remove the project-path references from `viewAICheck`, give it a front-of-tour title and a detected-tools line, make the offer cover both assistants, and add an AI line to the welcome blurb. Verified by build + `go vet` (these are presentation strings with no branching logic to unit-test).

**Files:**
- Modify: `go/internal/cli/commands/tour.go` (`viewWelcome` ~L962, `viewAICheck` ~L1426, `viewAIMCPSetup` ~L1464)

**Interfaces:**
- Consumes: `m.claudePath`, `m.codexPath`, `m.wifiCursor`, and the `wiz*Style` lipgloss styles (all existing).
- Produces: no new symbols; presentation only.

- [ ] **Step 1: Reframe `viewAICheck`**

Replace the entire `viewAICheck` function:

```go
func (m tourWizardModel) viewAICheck(w int) string {
	var sb strings.Builder
	sb.WriteString(wizTitleStyle.Render("Step 9 — Continue development") + "\n\n")

	if m.claudePath != "" {
		sb.WriteString(wizSuccessStyle.Render("Claude Code detected") + "\n")
		sb.WriteString(wizBodyStyle.Width(w).Render(
			"You can continue developing with Claude Code. Open your project in it:") + "\n\n")
		sb.WriteString("  " + wizCodeStyle.Render(fmt.Sprintf("cd %s && claude", m.projectPath)) + "\n\n")
		sb.WriteString(wizBodyStyle.Width(w).Render(
			"Set up the Wendy MCP server so Claude can access your device directly?") + "\n\n")
		opts := []string{"Yes, set up MCP now", "No, skip"}
		for i, opt := range opts {
			if i == m.wifiCursor {
				sb.WriteString(wizSelectedStyle.Render("▶ "+opt) + "\n")
			} else {
				sb.WriteString(wizNormalStyle.Render("  "+opt) + "\n")
			}
		}
		sb.WriteString("\n" + wizHintStyle.Render("↑/↓ navigate  ·  Enter select"))
	} else if m.codexPath != "" {
		sb.WriteString(wizSuccessStyle.Render("Codex detected") + "\n")
		sb.WriteString(wizBodyStyle.Width(w).Render(
			"Continue development with Codex from your project directory:") + "\n\n")
		sb.WriteString("  " + wizCodeStyle.Render(fmt.Sprintf("cd %s && codex", m.projectPath)) + "\n\n")
		sb.WriteString(wizHintStyle.Render("Enter to continue"))
	} else {
		sb.WriteString(wizBodyStyle.Width(w).Render(
			"To get AI-assisted development for Wendy apps, install Claude Code:") + "\n\n")
		sb.WriteString("  " + wizCodeStyle.Render("npm install -g @anthropic-ai/claude-code") + "\n\n")
		sb.WriteString(wizBodyStyle.Width(w).Render(
			"Then open your project and run:") + "\n\n")
		sb.WriteString("  " + wizCodeStyle.Render(fmt.Sprintf("cd %s && claude", m.projectPath)) + "\n\n")
		sb.WriteString(wizHintStyle.Render("Enter to continue"))
	}
	return sb.String()
}
```

with:

```go
func (m tourWizardModel) viewAICheck(w int) string {
	var sb strings.Builder
	sb.WriteString(wizTitleStyle.Render("Connect your AI coding assistant") + "\n\n")

	var detected []string
	if m.claudePath != "" {
		detected = append(detected, "Claude Code")
	}
	if m.codexPath != "" {
		detected = append(detected, "Codex")
	}
	sb.WriteString(wizSuccessStyle.Render("Detected: "+strings.Join(detected, ", ")) + "\n\n")
	sb.WriteString(wizBodyStyle.Width(w).Render(
		"Set up the Wendy MCP server so your assistant can talk to your devices "+
			"directly — list devices, manage containers, read telemetry, and more?") + "\n\n")

	opts := []string{"Yes, set up MCP now", "No, skip"}
	for i, opt := range opts {
		if i == m.wifiCursor {
			sb.WriteString(wizSelectedStyle.Render("▶ "+opt) + "\n")
		} else {
			sb.WriteString(wizNormalStyle.Render("  "+opt) + "\n")
		}
	}
	sb.WriteString("\n" + wizHintStyle.Render("↑/↓ navigate  ·  Enter select"))
	return sb.String()
}
```

- [ ] **Step 2: Make the MCP-results copy assistant-agnostic**

In `viewAIMCPSetup`, replace:

```go
		sb.WriteString(wizBodyStyle.Width(w).Render(
			"Restart Claude Code to activate the Wendy MCP server.\n"+
				"Claude will now have tools to list devices, manage containers,\n"+
				"read telemetry, and more.") + "\n\n")
```

with:

```go
		sb.WriteString(wizBodyStyle.Width(w).Render(
			"Restart your AI assistant to activate the Wendy MCP server.\n"+
				"It will then have tools to list devices, manage containers,\n"+
				"read telemetry, and more.") + "\n\n")
```

- [ ] **Step 3: Add the AI line to the welcome blurb**

In `viewWelcome`, replace:

```go
	sb.WriteString(wizBodyStyle.Width(w).Render(
		"This wizard will:\n"+
			"  1. Flash WendyOS onto your device\n"+
			"  2. Boot it and connect over the network\n"+
			"  3. Deploy a sample Python app\n\n"+
			"If anything goes wrong you can restart at any time with:\n") + "\n")
```

with:

```go
	sb.WriteString(wizBodyStyle.Width(w).Render(
		"This wizard will:\n"+
			"  1. Connect your AI coding assistant (if installed)\n"+
			"  2. Flash WendyOS onto your device\n"+
			"  3. Boot it and connect over the network\n"+
			"  4. Deploy a sample Python app\n\n"+
			"If anything goes wrong you can restart at any time with:\n") + "\n")
```

- [ ] **Step 4: Build, vet, and run the package tests**

Run: `cd go && go build ./internal/cli/... && go vet ./internal/cli/commands/ && go test ./internal/cli/commands/ -run Tour -v`
Expected: build succeeds, vet is clean (no unused `fmt` import — `fmt` is still used elsewhere in the file), Tour tests PASS.

- [ ] **Step 5: Manually verify the rendered screens (optional but recommended)**

Run: `cd go && go run ./cmd/wendy tour` (requires an interactive terminal). Confirm: welcome lists "Connect your AI coding assistant" first; pressing Enter shows the AI screen when `claude`/`codex` is on PATH (or jumps straight to device selection when neither is); choosing "No, skip" lands on device selection.

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/commands/tour.go
git commit -m "feat: reframe tour AI screen as a front-of-tour pre-step"
```

---

## Notes for the implementer

- After Task 1, `m.projectPath` is no longer read by `viewAICheck` once Task 2 lands; it is still set and used by other phases (`createPythonProject`, `viewCreateProject`), so leave the field in place.
- The `fmt` import remains required by other view functions; do not remove it when deleting the `fmt.Sprintf` calls from `viewAICheck`.
- Run the full package suite once at the end: `cd go && go test ./internal/cli/commands/`.
