# CLI Activation Telemetry & Onboarding Nudges — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make CLI activation measurable (separate install from usage, add one-time milestone events) and add first-run/next-step onboarding nudges, per `specs/2026-07-08-cli-activation-retention-design.md` (Tier 0 + Tier 1 UX wins).

**Architecture:** All install-event and activation-milestone logic is centralized in `trackCommand` (`go/cmd/wendy/main.go`), keyed off the cobra command path and success — no edits to individual command files for telemetry. Milestone once-per-install state persists next to `analytics_id` in the config dir. Onboarding nudges live in the root command's first-run block and `PersistentPostRunE`, keyed off the same command path.

**Tech Stack:** Go, cobra, the existing `internal/cli/analytics` package (fire-and-forget HTTP), `internal/shared/env`, `internal/shared/config`.

## Global Constraints

- **Privacy stance (verbatim from spec / existing code):** never collect flag values, positional args, or error message text. Milestone events carry only the event name. See `go/cmd/wendy/main.go:47-59,90-98` and `README.md:209-217`.
- **Analytics kill switches are absolute:** `env.IsCI()` and `WENDY_ANALYTICS=false` fully disable tracking (`analytics.Init`, `go/internal/cli/analytics/analytics.go:62-70`). Milestones must respect `enabled`.
- **Client is fire-and-forget:** `analytics.Track` ignores the HTTP response (`analytics.go:127-135`), so new event names never break the client. **Cloud dependency:** the `/v1/telemetry/events` endpoint and `cli_events` store (Swift `~/git/wendy/cloud`) must accept these new `event` values or the rows are dropped server-side: `install_completed`, `first_run`, `first_real_command`, `discover_success`, `init_success`, `auth_success`, `first_deploy_success`. Verify before relying on T0.3 analysis.
- **Config/analytics dir:** milestone state lives in `config.ConfigDir()` alongside `analytics_id` (`analytics.go:169-189`), mode `0o600`.
- **Test observation:** use `analytics.SetTrackHookForTesting` to observe events. `Track` fires the hook unconditionally (even when disabled), so tests are hermetic without network (`analytics.go:98-101`).

---

### Task 1: `env.IsHomebrewInstall()` helper

Detects the Homebrew post-install context so the automatic `wendy completion install` invocation can be reported as an install, not deliberate usage.

**Files:**
- Modify: `go/internal/shared/env/env.go`
- Test: `go/internal/shared/env/env_test.go` (create if absent)

**Interfaces:**
- Produces: `env.IsHomebrewInstall() bool`; `env.HomebrewEnvVars []string`

- [ ] **Step 1: Write the failing test**

```go
package env

import "testing"

func TestIsHomebrewInstall(t *testing.T) {
	for _, key := range HomebrewEnvVars {
		t.Setenv(key, "")
	}
	if IsHomebrewInstall() {
		t.Fatal("expected false with no Homebrew env vars set")
	}
	t.Setenv("HOMEBREW_PREFIX", "/opt/homebrew")
	if !IsHomebrewInstall() {
		t.Fatal("expected true when HOMEBREW_PREFIX is set")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/shared/env/ -run TestIsHomebrewInstall -v`
Expected: FAIL — `undefined: HomebrewEnvVars` / `undefined: IsHomebrewInstall`.

- [ ] **Step 3: Write minimal implementation**

Add to `go/internal/shared/env/env.go` (after `IsCI`, near line 60):

```go
// HomebrewEnvVars are set by Homebrew during a formula install/post-install
// step. Their presence during a `wendy completion install` invocation means the
// call is the automated post-install hook, not a deliberate user action.
var HomebrewEnvVars = []string{
	"HOMEBREW_PREFIX",
	"HOMEBREW_CELLAR",
	"HOMEBREW_REPOSITORY",
}

// IsHomebrewInstall reports whether the process appears to be running inside a
// Homebrew formula install/post-install step.
func IsHomebrewInstall() bool {
	for _, key := range HomebrewEnvVars {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/shared/env/ -run TestIsHomebrewInstall -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/shared/env/env.go go/internal/shared/env/env_test.go
git commit -m "feat(cli): add env.IsHomebrewInstall for install-event detection"
```

---

### Task 2: One-time milestone events in the analytics package

Adds persistent, once-per-install milestone emission alongside `analytics_id`.

**Files:**
- Modify: `go/internal/cli/analytics/analytics.go`
- Test: `go/internal/cli/analytics/analytics_test.go` (create if absent)

**Interfaces:**
- Consumes: existing `Track(event string, properties map[string]string)`, `enabled` package var, `config.ConfigDir()`.
- Produces: `analytics.TrackMilestoneOnce(name string)`; `analytics.TrackMilestoneOnceInDir(dir, name string)` (dir-injectable, for tests).

- [ ] **Step 1: Write the failing test**

```go
package analytics

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrackMilestoneOnceInDir_EmitsOnce(t *testing.T) {
	dir := t.TempDir()
	var count int
	SetTrackHookForTesting(func(event string, _ map[string]string) {
		if event == "first_deploy_success" {
			count++
		}
	})
	t.Cleanup(func() { SetTrackHookForTesting(nil) })

	TrackMilestoneOnceInDir(dir, "first_deploy_success")
	TrackMilestoneOnceInDir(dir, "first_deploy_success")

	if count != 1 {
		t.Fatalf("expected milestone to emit exactly once, got %d", count)
	}
	data, err := os.ReadFile(filepath.Join(dir, "milestones"))
	if err != nil {
		t.Fatalf("reading milestones file: %v", err)
	}
	if string(data) != "first_deploy_success\n" {
		t.Fatalf("unexpected milestones file contents: %q", string(data))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/analytics/ -run TestTrackMilestoneOnceInDir -v`
Expected: FAIL — `undefined: TrackMilestoneOnceInDir`.

- [ ] **Step 3: Write minimal implementation**

Add to `go/internal/cli/analytics/analytics.go` (after `Track`, before `Close`):

```go
const milestonesFileName = "milestones"

// milestoneSent reports whether name was already recorded in dir/milestones.
func milestoneSent(dir, name string) bool {
	data, err := os.ReadFile(filepath.Join(dir, milestonesFileName))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}

// recordMilestone appends name to dir/milestones.
func recordMilestone(dir, name string) error {
	f, err := os.OpenFile(filepath.Join(dir, milestonesFileName),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(name + "\n")
	return err
}

// TrackMilestoneOnceInDir emits the named milestone event exactly once for the
// given state dir. The dir is a parameter so tests can use a temp dir.
func TrackMilestoneOnceInDir(dir, name string) {
	if milestoneSent(dir, name) {
		return
	}
	Track(name, map[string]string{"command_name": name})
	_ = recordMilestone(dir, name)
}

// TrackMilestoneOnce emits the named milestone event exactly once per
// installation. It is a no-op when analytics is disabled. Milestone state lives
// alongside analytics_id in the config dir.
func TrackMilestoneOnce(name string) {
	if !enabled {
		return
	}
	dir, err := config.ConfigDir()
	if err != nil {
		return
	}
	TrackMilestoneOnceInDir(dir, name)
}
```

(`os`, `path/filepath`, `strings`, and `config` are already imported in this file — see `analytics.go:4-22`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/analytics/ -run TestTrackMilestoneOnceInDir -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/analytics/analytics.go go/internal/cli/analytics/analytics_test.go
git commit -m "feat(cli): add one-time activation milestone events"
```

---

### Task 3: Report the Homebrew post-install as `install_completed`

Splits the single `command_executed` event so an automatic `wendy completion install` during `brew install` is no longer counted as CLI usage.

**Files:**
- Modify: `go/cmd/wendy/main.go` (add `eventNameFor`; wire into `trackCommand` at `main.go:60-75`; add `env` import at `main.go:3-21`)
- Test: `go/cmd/wendy/main_test.go` (create if absent)

**Interfaces:**
- Consumes: `env.IsHomebrewInstall()` (Task 1).
- Produces: `eventNameFor(commandPath string, homebrew bool) string`.

- [ ] **Step 1: Write the failing test**

```go
package main

import "testing"

func TestEventNameFor(t *testing.T) {
	cases := []struct {
		path     string
		homebrew bool
		want     string
	}{
		{"wendy completion install", true, "install_completed"},
		{"wendy completion install", false, "command_executed"},
		{"wendy run", true, "command_executed"},
		{"wendy device info", false, "command_executed"},
	}
	for _, c := range cases {
		if got := eventNameFor(c.path, c.homebrew); got != c.want {
			t.Errorf("eventNameFor(%q, %v) = %q, want %q", c.path, c.homebrew, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./cmd/wendy/ -run TestEventNameFor -v`
Expected: FAIL — `undefined: eventNameFor`.

- [ ] **Step 3: Write minimal implementation**

Add the `env` import to the block at `main.go:14-15`:

```go
	"github.com/wendylabsinc/wendy/go/internal/cli/commands"
	"github.com/wendylabsinc/wendy/go/internal/shared/env"
```

Add the function (after `commandRoot`, near `main.go:88`):

```go
// eventNameFor returns the analytics event name for a command invocation. A
// Homebrew post-install `wendy completion install` is reported as
// install_completed so it is not counted as deliberate CLI usage.
func eventNameFor(commandPath string, homebrew bool) string {
	if homebrew && commandPath == "wendy completion install" {
		return "install_completed"
	}
	return "command_executed"
}
```

Change `trackCommand` (`main.go:60-75`) to compute and use the event name:

```go
func trackCommand(executed *cobra.Command, err error, dur time.Duration) {
	if executed == nil {
		return
	}
	path := executed.CommandPath()
	event := eventNameFor(path, env.IsHomebrewInstall())
	props := map[string]string{
		"command_name": path,
		"command_root": commandRoot(executed),
		"duration_ms":  strconv.FormatInt(dur.Milliseconds(), 10),
		"success":      strconv.FormatBool(err == nil),
		"is_dev_build": strconv.FormatBool(version.IsDev(version.Version)),
	}
	if err != nil {
		props["error_class"] = errorClass(err)
	}
	analytics.Track(event, props)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./cmd/wendy/ -run TestEventNameFor -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/cmd/wendy/main.go go/cmd/wendy/main_test.go
git commit -m "feat(cli): report Homebrew post-install as install_completed"
```

---

### Task 4: Emit activation milestones from `trackCommand`

Wires the one-time milestones (Task 2) to real command outcomes, centrally.

**Files:**
- Modify: `go/cmd/wendy/main.go` (add `isSetupCommand`, `milestoneFor`; extend `trackCommand`)
- Test: `go/cmd/wendy/main_test.go`

**Interfaces:**
- Consumes: `analytics.TrackMilestoneOnce(name string)` (Task 2); `eventNameFor` (Task 3).
- Produces: `isSetupCommand(path string) bool`; `milestoneFor(commandPath string, success bool) string`.

- [ ] **Step 1: Write the failing test**

```go
func TestMilestoneFor(t *testing.T) {
	cases := []struct {
		path    string
		success bool
		want    string
	}{
		{"wendy discover", true, "discover_success"},
		{"wendy init", true, "init_success"},
		{"wendy run", true, "first_deploy_success"},
		{"wendy cloud login", true, "auth_success"},
		{"wendy auth login", true, "auth_success"},
		{"wendy run", false, ""},
		{"wendy device info", true, ""},
	}
	for _, c := range cases {
		if got := milestoneFor(c.path, c.success); got != c.want {
			t.Errorf("milestoneFor(%q, %v) = %q, want %q", c.path, c.success, got, c.want)
		}
	}
}

func TestIsSetupCommand(t *testing.T) {
	setup := []string{"wendy completion install", "wendy __complete", "wendy help", "wendy analytics disable"}
	real := []string{"wendy run", "wendy discover", "wendy device info"}
	for _, p := range setup {
		if !isSetupCommand(p) {
			t.Errorf("isSetupCommand(%q) = false, want true", p)
		}
	}
	for _, p := range real {
		if isSetupCommand(p) {
			t.Errorf("isSetupCommand(%q) = true, want false", p)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./cmd/wendy/ -run 'TestMilestoneFor|TestIsSetupCommand' -v`
Expected: FAIL — `undefined: milestoneFor` / `undefined: isSetupCommand`.

- [ ] **Step 3: Write minimal implementation**

Add after `eventNameFor` in `main.go`:

```go
// isSetupCommand reports whether a command path is a meta/setup command that
// does not represent deliberate product use. The first "real" command is the
// first invocation whose path is not one of these.
func isSetupCommand(path string) bool {
	switch path {
	case "wendy completion install",
		"wendy completion bash", "wendy completion zsh",
		"wendy completion fish", "wendy completion powershell",
		"wendy __complete", "wendy __completeNoDesc",
		"wendy __ble-check", "wendy help":
		return true
	}
	return strings.HasPrefix(path, "wendy analytics") ||
		strings.HasPrefix(path, "wendy cache")
}

// milestoneFor maps a successful command invocation to a one-time activation
// milestone event name, or "" if the command is not a milestone.
func milestoneFor(commandPath string, success bool) string {
	if !success {
		return ""
	}
	switch commandPath {
	case "wendy discover":
		return "discover_success"
	case "wendy init":
		return "init_success"
	case "wendy run":
		return "first_deploy_success"
	case "wendy cloud login", "wendy auth login":
		return "auth_success"
	}
	return ""
}
```

Extend `trackCommand` — add, right after `analytics.Track(event, props)`:

```go
	// Activation milestones (one-time per install, best-effort). first_run and
	// first_real_command are only counted for genuine invocations, not the
	// Homebrew install artifact.
	if event == "command_executed" {
		analytics.TrackMilestoneOnce("first_run")
		if !isSetupCommand(path) {
			analytics.TrackMilestoneOnce("first_real_command")
		}
	}
	if m := milestoneFor(path, err == nil); m != "" {
		analytics.TrackMilestoneOnce(m)
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./cmd/wendy/ -run 'TestMilestoneFor|TestIsSetupCommand' -v`
Expected: PASS.

- [ ] **Step 5: Verify the whole binary still builds & vets**

Run: `cd go && go build ./cmd/wendy/ && go vet ./cmd/wendy/ ./internal/cli/analytics/ ./internal/shared/env/`
Expected: no output (success).

- [ ] **Step 6: Commit**

```bash
git add go/cmd/wendy/main.go go/cmd/wendy/main_test.go
git commit -m "feat(cli): emit activation milestones from trackCommand"
```

---

### Task 5: Un-hide `wendy tour` and add a first-run pointer

Surfaces the existing onboarding wizard and nudges new users toward it.

**Files:**
- Modify: `go/internal/cli/commands/root.go` (tour registration at `:162-163`; first-run block at `:56-68`)
- Test: `go/internal/cli/commands/root_test.go` (create if absent)

**Interfaces:**
- Consumes: `newTourCmd()` (existing), the `firstRun` bool computed in `PersistentPreRunE`.

- [ ] **Step 1: Write the failing test**

```go
package commands

import "testing"

func TestTourCommandIsVisible(t *testing.T) {
	root := NewRootCmd()
	for _, c := range root.Commands() {
		if c.Name() == "tour" {
			if c.Hidden {
				t.Fatal("tour command should be visible (not hidden)")
			}
			return
		}
	}
	t.Fatal("tour command not registered on root")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestTourCommandIsVisible -v`
Expected: FAIL — "tour command should be visible (not hidden)".

- [ ] **Step 3: Write minimal implementation**

In `root.go:162-163`, change:

```go
	tourCmd := newTourCmd()
	tourCmd.Hidden = true
```

to:

```go
	tourCmd := newTourCmd()
	tourCmd.GroupID = "develop"
```

Then in the first-run block (`root.go:56-68`), add a pointer to the wizard after the analytics notice, before `cfg.Analytics = ...`:

```go
			cmd.PrintErrln("")
			cmd.PrintErrln("New to Wendy? Run `wendy tour` for a guided setup.")
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/commands/ -run TestTourCommandIsVisible -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/root.go go/internal/cli/commands/root_test.go
git commit -m "feat(cli): surface wendy tour and point first-run users to it"
```

---

### Task 6: Next-step nudges after inspection/deploy commands

Adds a one-line "what to do next" hint after the commands where one-day users stall (`discover`, `device info/top/apps list`, `run`).

**Files:**
- Modify: `go/internal/cli/commands/root.go` (`PersistentPostRunE` at `:83-97`; add `nextStepHint`, `maybeShowNextStep`)
- Test: `go/internal/cli/commands/root_test.go`

**Interfaces:**
- Consumes: package-level `jsonOutput`, `isInteractiveTerminal()`, `env.IsCI()` (all already used in this file).
- Produces: `nextStepHint(commandPath string) string`; `maybeShowNextStep(cmd *cobra.Command)`.

- [ ] **Step 1: Write the failing test**

```go
func TestNextStepHint(t *testing.T) {
	cases := map[string]string{
		"wendy discover":         "Next: run `wendy init` to create an app, then `wendy run` to deploy it.",
		"wendy device info":      "Next: run `wendy run` to build and deploy an app to this device.",
		"wendy device top":       "Next: run `wendy run` to build and deploy an app to this device.",
		"wendy device apps list": "Next: run `wendy run` to build and deploy an app to this device.",
		"wendy run":              "Next: run `wendy device logs` to stream your app's logs.",
		"wendy analytics status": "",
	}
	for path, want := range cases {
		if got := nextStepHint(path); got != want {
			t.Errorf("nextStepHint(%q) = %q, want %q", path, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestNextStepHint -v`
Expected: FAIL — `undefined: nextStepHint`.

- [ ] **Step 3: Write minimal implementation**

Add near `maybeShowOptimizeTip` in `root.go`:

```go
// nextStepHint returns a one-line suggestion for the next command to run after
// commandPath succeeds, or "" when there is no suggestion. Keyed off the full
// cobra command path (e.g. "wendy device info").
func nextStepHint(commandPath string) string {
	switch commandPath {
	case "wendy discover":
		return "Next: run `wendy init` to create an app, then `wendy run` to deploy it."
	case "wendy device info", "wendy device top", "wendy device apps list":
		return "Next: run `wendy run` to build and deploy an app to this device."
	case "wendy run":
		return "Next: run `wendy device logs` to stream your app's logs."
	}
	return ""
}

// maybeShowNextStep prints a next-step hint after a successful command. cobra
// only runs PersistentPostRunE when RunE succeeded, so this is success-only. It
// is suppressed for JSON output, non-interactive terminals, and CI.
func maybeShowNextStep(cmd *cobra.Command) {
	if jsonOutput || !isInteractiveTerminal() || env.IsCI() {
		return
	}
	if hint := nextStepHint(cmd.CommandPath()); hint != "" {
		cmd.PrintErrln(hint)
	}
}
```

Wire it into `PersistentPostRunE` (`root.go:83-97`), after `maybeShowOptimizeTip(cmd)`:

```go
			maybeShowOptimizeTip(cmd)
			maybeShowNextStep(cmd)
```

(Confirm `env` is imported in `root.go`; if not, add `"github.com/wendylabsinc/wendy/go/internal/shared/env"`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/commands/ -run TestNextStepHint -v`
Expected: PASS.

- [ ] **Step 5: Verify build & vet**

Run: `cd go && go build ./... && go vet ./internal/cli/commands/`
Expected: no output (success).

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/commands/root.go go/internal/cli/commands/root_test.go
git commit -m "feat(cli): add next-step nudges after inspection and deploy commands"
```

---

### Task 7 (analysis, no code): Re-baseline the drop-off with install ghosts excluded

After Tasks 1–4 ship and telemetry accrues, re-run the drop-off analysis with the artifact removed, to establish the true activation baseline.

**Not a code task** — run via the analytics MCP tools (`mcp_wendy_analytics_cloud_sql_readonly_query`).

- [ ] **Step 1: Confirm the cloud accepts the new event names.** Query `cli_events` for `event IN ('install_completed','first_run','first_real_command','discover_success','init_success','auth_success','first_deploy_success')` after a release ships. If counts are zero while `command_executed` still flows, the Swift cloud endpoint/store is filtering unknown events — file a cloud task before proceeding (see Global Constraints).
- [ ] **Step 2: Recompute one-and-done excluding ghosts.** Exclude `event='install_completed'` and, for historical rows predating the fix, `event='command_executed' AND command_name='wendy completion install'`. Recompute: unique users, one-day users, and the segment that ran ≥1 real command.
- [ ] **Step 3: Report the activation funnel** from milestones: `install_completed` → `first_real_command` → `discover_success` → `init_success` → `first_deploy_success`, as counts and conversion rates.
- [ ] **Step 4: Record the corrected baseline** back into the design doc's Context section so Tier 2/3 planning uses clean numbers.

---

## Out of scope (follow-on plans)

- **T1.3 — error sub-classification** (`error_class="other"` → `build_failed`/`deploy_failed`/`no_device`/`validation`). This touches many error sites in `go/internal/cli/commands/run.go` and needs sentinel errors wired through the run path; it is a distinct subsystem and gets its own spec + plan.
- **Tier 2** (reliability on `grpc_unavailable`/`run`/`os install`; re-engagement; retained-core investment) and **Tier 3** (dashboard instrumentation; positioning + marketing→CLI funnel join) per the design doc.

## Self-review notes

- **Spec coverage:** T0.1 → Tasks 1,3; T0.2 → Tasks 2,4; T0.3 → Task 7; T1.1 → Task 5; T1.2 → Task 6. T1.3 explicitly deferred (Out of scope). Tiers 2–3 deferred.
- **Type consistency:** `eventNameFor`, `milestoneFor`, `isSetupCommand`, `nextStepHint`, `maybeShowNextStep`, `TrackMilestoneOnce`, `TrackMilestoneOnceInDir`, `IsHomebrewInstall`, `HomebrewEnvVars` are each defined in exactly one task and consumed with matching signatures.
- **No placeholders:** every code step carries full code; every run step carries the exact command and expected result.
