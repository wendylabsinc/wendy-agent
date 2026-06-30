# Set up Claude/Codex at the start of the tour

**Date:** 2026-06-27
**Status:** Approved, ready for implementation

## Problem

`wendy tour` (the hidden interactive onboarding wizard in
`go/internal/cli/commands/tour.go`) already detects installed AI coding CLIs
(`claude`, `codex`) and offers to wire up the Wendy MCP server — but it does so
as the **very last step** (`phaseAICheck` → `phaseAIMCPSetup`, "Step 9"), after
the user has flashed WendyOS, booted the device, and deployed a sample app.

That ordering buries the single most valuable thing for a developer: getting
their AI assistant connected to Wendy. The flash + boot + deploy phases are slow
and mostly idle waiting; if the MCP server were configured up front, the user
could already be coding against Wendy (and querying it through Claude/Codex)
while the device comes up.

## Goal

Promote AI-assistant setup to run **immediately after the welcome screen**,
before device selection — but only when at least one supported CLI is installed.

## Decisions (settled during brainstorming)

- **Move to start only.** The AI setup runs once, at the front. The existing
  end-of-tour AI step is removed (not duplicated).
- **Skip silently when neither CLI is installed.** No install-instructions
  screen up front — that would be noise before the user has even picked a
  device. (The previous end-of-tour step showed install hints; we drop that.)
- **Offer MCP setup for Codex too**, not just Claude. Today a Codex-only user
  gets a bare "cd && codex" hint with no MCP wiring; `setupMCPForAllTools()`
  already configures Codex, so we close that gap.
- **The early AI screen is unnumbered** ("Connect your AI coding assistant").
  Renumbering the 16 existing "Step N" labels would be needless churn, and the
  AI step is an optional pre-step that sits before "Step 1 — Select your device".

## Flow

```
Welcome
  → cmdCheckAITools()                    (exec.LookPath claude/codex — instant, local)
       ├─ claude and/or codex found → phaseAICheck (Connect your AI assistant)
       │        ├─ "Yes, set up MCP" → runMCPSetupCmd() → phaseAIMCPSetup (results) → loadDevices
       │        └─ "No, skip"        → loadDevices
       └─ neither found             → loadDevices   (silent skip)
  → Step 1: Select device → … → flash → boot → deploy sample app
  → "You're all set!" (phaseCloud — no AI step at the end)
```

## Code changes (all in `go/internal/cli/commands/tour.go`)

The supporting machinery is reused unchanged: `cmdCheckAITools`,
`runMCPSetupCmd`, and `setupMCPForAllTools()` (in `mcp_setup.go`, already covers
Claude Code, Claude Desktop, Cursor, Windsurf, Codex). No enum reordering — flow
is driven by transitions, not by `iota` order.

1. **`phaseWelcome` enter handler** (`handleKey`, ~L431): instead of
   `m.phase = phaseLoadDevices; return m, loadDevicesCmd()`, fire
   `return m, m.cmdCheckAITools()` (phase stays `phaseWelcome` for the
   sub-millisecond LookPath round-trip).

2. **`tourAICheckDoneMsg` handler** (~L359): route on result.
   - If `claudePath != "" || codexPath != ""` → `m.phase = phaseAICheck`.
   - Else → `m.phase = phaseLoadDevices; return m, loadDevicesCmd()`.

3. **`viewAICheck`** (~L1426): reframe as a pre-step.
   - Title → `"Connect your AI coding assistant"` (drop "Step 9 —").
   - Remove all `m.projectPath` references (no project exists yet).
   - List detected tools (e.g. "Detected: Claude Code, Codex").
   - Show the Yes/No MCP-setup menu whenever **claude or codex** is detected
     (today the menu only appears for claude; codex-only falls through to a
     bare hint).

4. **`phaseAICheck` key handler** (~L779): on "No"/skip, transition to
   `phaseLoadDevices` + `loadDevicesCmd()` instead of `phaseCloud`. The
   "Yes → runMCPSetupCmd()" path is unchanged.

5. **`phaseAIMCPSetup` key handler** (~L798): after showing results, continue to
   `phaseLoadDevices` + `loadDevicesCmd()` instead of `phaseCloud`.
   `viewAIMCPSetup` text needs no project-specific edits (its "restart Claude to
   activate" message is still accurate up front).

6. **`tourRunDoneMsg` handler** (~L350): after the deploy completes, go straight
   to `phaseCloud`. Remove the `phaseAICheck` + `cmdCheckAITools()` detour.

7. **`viewWelcome`** (~L962): add a line to the "This wizard will:" list noting
   it connects the AI coding assistant.

## Testing

The tour is a `tea.Model`, so its `Update` can be driven directly in unit tests
without a terminal. Add tests (new `tour_test.go` or extend existing) that
construct `newTourWizardModel()` and assert the new routing:

1. **claude detected → welcome leads to AI step.** Feed `tourAICheckDoneMsg{claudePath: "/x/claude"}`;
   assert `m.phase == phaseAICheck`.
2. **neither detected → welcome skips to device load.** Feed
   `tourAICheckDoneMsg{}`; assert `m.phase == phaseLoadDevices`.
3. **skip from AI step → device load.** From `phaseAICheck`, send the "No" key
   path; assert `m.phase == phaseLoadDevices`.
4. **MCP results screen → device load.** From `phaseAIMCPSetup`, press Enter;
   assert `m.phase == phaseLoadDevices`.
5. **deploy completion → cloud, not AI.** Feed `tourRunDoneMsg{}`; assert
   `m.phase == phaseCloud`.

LookPath itself is not stubbed — tests drive the message handlers directly,
which is where the routing logic lives.

## Out of scope

- No change to `setupMCPForAllTools()` or any MCP config writer.
- No change to the device/flash/boot/deploy phases.
- No renumbering of existing step labels.
