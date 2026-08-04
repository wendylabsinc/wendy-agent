# `wendy report-bug` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a hidden `wendy report-bug` command that opens a pre-filled GitHub bug-report form, and wire the same mechanism into PR #1228's crash-report flow so it fires automatically when cloud submission is unavailable.

**Architecture:** A pure URL-builder (`reportBugURL`) fills `bug_report.yml`'s Versions/Host information (and, when triggered by a crash, a short error summary + local-file pointer) via GitHub's documented per-field query-param prefill. A small opener (`maybeOpenReportBugURL`) auto-opens that URL via the repo's existing `openBrowser` seam only when `gh` is on PATH (a "this is a dev machine" signal, not the opening mechanism itself); otherwise it's printed for manual use. `wendy report-bug` and `crashflow.go`'s cloud-unavailable branch both call the same two functions.

**Tech Stack:** Go 1.26, Cobra, the existing `platforminfo`/`crashreport`/`browseropen` packages in `go/internal/...`.

## Global Constraints

- Repo/module: `github.com/wendylabsinc/wendy`; CLI commands live in `go/internal/cli/commands` (package `commands`).
- Base branch for all work: `jo/report-bug` (already checked out in this worktree, branched from `origin/jo/platform-diagnostics-crash-reporting`, i.e. PR #1228).
- Issue URL base: `https://github.com/wendylabsinc/WendyOS/issues/new` — always keep this exact host/path/casing.
- Never shell out to `gh issue create`; `gh`'s only role is an `exec.LookPath("gh")` presence check.
- Never add a new consent prompt in `crashflow.go` — the automatic path only fires after the existing "Send this report?" consent already returned yes and cloud submission failed.
- Never inline full log tails into the URL — cap any single query value at 500 runes via `truncateForURL`; the automatic path points at the already-saved local report file instead of embedding the log tail.
- Reuse the existing `openBrowser` package var (`go/internal/cli/commands/auth.go:646`, backed by `browseropen.Open`) — do not write new OS-specific browser-opening code.
- Test seams follow the existing save/restore pattern (see `go/internal/cli/commands/open_browser_test.go`): save the package var, `t.Cleanup` to restore it, reassign for the test.
- `report-bug` is `Hidden: true` on its `cobra.Command`, takes no args (`cobra.NoArgs`), and is added to `root.go`'s hidden-command block — it must not appear in `wendy --help`.

---

### Task 1: Core helpers — `reportBugURL` and `maybeOpenReportBugURL`

**Files:**
- Create: `go/internal/cli/commands/report_bug.go`
- Test: `go/internal/cli/commands/report_bug_test.go`

**Interfaces:**
- Consumes: `platforminfo.Info` (fields `CLIVersion`, `.Block()` — from `go/internal/shared/platforminfo/platforminfo.go`), `crashreport.Bundle` (fields `ErrorClass`, `ErrorChain` — from `go/internal/cli/crashreport/bundle.go`), the existing package-level `openBrowser func(string) error` var (`go/internal/cli/commands/auth.go:646`).
- Produces: `reportBugURL(info platforminfo.Info, bundle *crashreport.Bundle, localFile string) string`, `maybeOpenReportBugURL(cmd *cobra.Command, rawURL string) bool`, and the package-level `lookPath` var — all consumed by Task 2 and Task 3.

- [ ] **Step 1: Write the failing tests for `reportBugURL`**

```go
// go/internal/cli/commands/report_bug_test.go
package commands

import (
	"net/url"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/crashreport"
	"github.com/wendylabsinc/wendy/go/internal/shared/platforminfo"
)

func TestReportBugURLManualNoBundle(t *testing.T) {
	info := platforminfo.Info{CLIVersion: "1.2.3", DevOS: "darwin", DevOSVersion: "15.2", DevArch: "arm64"}
	got := reportBugURL(info, nil, "")

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("invalid URL %q: %v", got, err)
	}
	if u.Scheme != "https" || u.Host != "github.com" || u.Path != "/wendylabsinc/WendyOS/issues/new" {
		t.Fatalf("unexpected URL base: %q", got)
	}
	q := u.Query()
	if q.Get("template") != "bug_report.yml" {
		t.Errorf("template = %q, want bug_report.yml", q.Get("template"))
	}
	if q.Get("version") != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3", q.Get("version"))
	}
	if !strings.Contains(q.Get("host-os"), "darwin") {
		t.Errorf("host-os = %q, want it to mention darwin", q.Get("host-os"))
	}
	if q.Get("what-happened") != "" {
		t.Errorf("what-happened should be unset for a nil bundle, got %q", q.Get("what-happened"))
	}
	if q.Get("logs") != "" {
		t.Errorf("logs should be unset for a nil bundle, got %q", q.Get("logs"))
	}
}

func TestReportBugURLWithBundleAndLocalFile(t *testing.T) {
	info := platforminfo.Info{CLIVersion: "1.2.3"}
	bundle := crashreport.Bundle{ErrorClass: "docker_build_failed", ErrorChain: "exit status 1"}
	got := reportBugURL(info, &bundle, "/tmp/wendy-crashreport-abc/report.json")

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("invalid URL %q: %v", got, err)
	}
	q := u.Query()
	if want := "docker_build_failed: exit status 1"; q.Get("what-happened") != want {
		t.Errorf("what-happened = %q, want %q", q.Get("what-happened"), want)
	}
	if !strings.Contains(q.Get("logs"), "/tmp/wendy-crashreport-abc/report.json") {
		t.Errorf("logs = %q, want it to mention the local file", q.Get("logs"))
	}
}

func TestReportBugURLTruncatesLongErrorChain(t *testing.T) {
	info := platforminfo.Info{}
	bundle := crashreport.Bundle{ErrorClass: "x", ErrorChain: strings.Repeat("a", 1000)}
	got := reportBugURL(info, &bundle, "")

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("invalid URL %q: %v", got, err)
	}
	whatHappened := u.Query().Get("what-happened")
	if rc := len([]rune(whatHappened)); rc > 501 { // 500 + the "…" rune
		t.Errorf("what-happened not truncated: %d runes", rc)
	}
	if !strings.HasSuffix(whatHappened, "…") {
		t.Errorf("what-happened = %q, want a truncation ellipsis", whatHappened)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./go/internal/cli/commands/... -run TestReportBugURL -v`
Expected: FAIL — `undefined: reportBugURL` (the function doesn't exist yet).

- [ ] **Step 3: Implement `reportBugURL` and `truncateForURL` only**

`maybeOpenReportBugURL` is deliberately left out of this step — it gets its own red-green cycle in
Steps 5-8 below.

```go
// go/internal/cli/commands/report_bug.go
package commands

import (
	"net/url"

	"github.com/wendylabsinc/wendy/go/internal/cli/crashreport"
	"github.com/wendylabsinc/wendy/go/internal/shared/platforminfo"
)

// reportBugIssueURL is the GitHub "new issue" endpoint for this repo. Keep the
// path casing exact — GitHub's own links use it and some proxies are case
// sensitive.
const reportBugIssueURL = "https://github.com/wendylabsinc/WendyOS/issues/new"

// reportBugURL builds a GitHub issue-form URL that pre-fills bug_report.yml's
// Versions and Host information fields via GitHub's documented per-field
// query-param prefill (field id as the parameter name). When bundle is
// non-nil (the automatic crash-report path), it also fills a short
// What-happened summary and, when localFile is set, a pointer to the
// locally-saved report file instead of the raw log tail. Component and
// Target hardware are never inferable, so they're left for the user to pick
// from the form's dropdowns.
func reportBugURL(info platforminfo.Info, bundle *crashreport.Bundle, localFile string) string {
	q := url.Values{}
	q.Set("template", "bug_report.yml")
	q.Set("version", info.CLIVersion)
	q.Set("host-os", info.Block())

	if bundle != nil {
		q.Set("what-happened", truncateForURL(bundle.ErrorClass+": "+bundle.ErrorChain, 500))
		if localFile != "" {
			q.Set("logs", "Full redacted diagnostic bundle saved locally at "+localFile+" — attach it here.")
		}
	}

	return reportBugIssueURL + "?" + q.Encode()
}

// truncateForURL shortens s to at most max runes (appending "…") so a single
// query value can't blow past practical URL-length limits.
func truncateForURL(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./go/internal/cli/commands/... -run TestReportBugURL -v`
Expected: PASS (all three cases).

- [ ] **Step 5: Write the failing tests for `maybeOpenReportBugURL`**

```go
// append to go/internal/cli/commands/report_bug_test.go
import (
	"bytes"
	"fmt"
	"os/exec"
	// ...(keep existing imports)

	"github.com/spf13/cobra"
)

func TestMaybeOpenReportBugURLGHPresentSucceeds(t *testing.T) {
	origLookPath, origOpenBrowser := lookPath, openBrowser
	t.Cleanup(func() { lookPath = origLookPath; openBrowser = origOpenBrowser })
	lookPath = func(string) (string, error) { return "/usr/local/bin/gh", nil }
	var openedURL string
	openBrowser = func(u string) error { openedURL = u; return nil }

	cmd := &cobra.Command{Use: "wendy"}
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)

	if !maybeOpenReportBugURL(cmd, "https://example.com/issue") {
		t.Fatal("expected true when gh is present and openBrowser succeeds")
	}
	if openedURL != "https://example.com/issue" {
		t.Errorf("openBrowser called with %q, want the report URL", openedURL)
	}
	if !strings.Contains(stderr.String(), "Opening a pre-filled bug report") {
		t.Errorf("stderr = %q, want the opening message", stderr.String())
	}
}

func TestMaybeOpenReportBugURLGHMissing(t *testing.T) {
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }

	cmd := &cobra.Command{Use: "wendy"}
	if maybeOpenReportBugURL(cmd, "https://example.com/issue") {
		t.Fatal("expected false when gh is missing")
	}
}

func TestMaybeOpenReportBugURLOpenBrowserFails(t *testing.T) {
	origLookPath, origOpenBrowser := lookPath, openBrowser
	t.Cleanup(func() { lookPath = origLookPath; openBrowser = origOpenBrowser })
	lookPath = func(string) (string, error) { return "/usr/local/bin/gh", nil }
	openBrowser = func(string) error { return fmt.Errorf("no display") }

	cmd := &cobra.Command{Use: "wendy"}
	if maybeOpenReportBugURL(cmd, "https://example.com/issue") {
		t.Fatal("expected false when openBrowser fails")
	}
}
```

- [ ] **Step 6: Run the tests to verify they fail**

Run: `go test ./go/internal/cli/commands/... -run TestMaybeOpenReportBugURL -v`
Expected: FAIL to compile — `undefined: maybeOpenReportBugURL` and `undefined: lookPath` (neither exists yet).

- [ ] **Step 7: Implement `lookPath` and `maybeOpenReportBugURL`**

Add to `go/internal/cli/commands/report_bug.go`: extend the import block with `"fmt"`, `"os/exec"`, and
`"github.com/spf13/cobra"`, then append:

```go
// lookPath is a seam so tests can simulate gh being present or absent without
// depending on the real PATH.
var lookPath = exec.LookPath

// maybeOpenReportBugURL opens rawURL in the browser when gh is on PATH (used
// only as a signal that this is an interactive developer machine — gh's own
// issue-create machinery is never invoked). Returns whether it succeeded;
// callers own the fallback message for the false case.
func maybeOpenReportBugURL(cmd *cobra.Command, rawURL string) bool {
	if _, err := lookPath("gh"); err != nil {
		return false
	}
	if err := openBrowser(rawURL); err != nil {
		return false
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Opening a pre-filled bug report in your browser...")
	return true
}
```

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go test ./go/internal/cli/commands/... -run TestMaybeOpenReportBugURL -v`
Expected: PASS (all three cases).

- [ ] **Step 9: Run the full set of Task 1 tests together**

Run: `go test ./go/internal/cli/commands/... -run 'TestReportBugURL|TestMaybeOpenReportBugURL' -v`
Expected: PASS — 6 tests total.

- [ ] **Step 10: Commit**

```bash
git add go/internal/cli/commands/report_bug.go go/internal/cli/commands/report_bug_test.go
git commit -m "feat(cli): add reportBugURL + maybeOpenReportBugURL helpers"
```

---

### Task 2: `wendy report-bug` command

**Files:**
- Modify: `go/internal/cli/commands/report_bug.go` (add `newReportBugCmd`)
- Modify: `go/internal/cli/commands/root.go` (register the hidden command)
- Modify: `go/internal/cli/commands/report_bug_test.go` (add command-level tests)

**Interfaces:**
- Consumes: `reportBugURL`, `maybeOpenReportBugURL`, `lookPath`, `openBrowser` (Task 1), `platforminfo.Collect()` (existing).
- Produces: `newReportBugCmd() *cobra.Command`, consumed only by `root.go`'s `NewRootCmd`.

- [ ] **Step 1: Write the failing command-level tests**

```go
// append to go/internal/cli/commands/report_bug_test.go
func TestReportBugCmdGHMissingPrintsFallback(t *testing.T) {
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }

	cmd := newReportBugCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "`gh` CLI not found") {
		t.Errorf("output = %q, want the gh-missing fallback message", out)
	}
	if !strings.Contains(out, "issues/new?") {
		t.Errorf("output = %q, want the fallback URL", out)
	}
}

func TestReportBugCmdGHPresentOpensBrowser(t *testing.T) {
	origLookPath, origOpenBrowser := lookPath, openBrowser
	t.Cleanup(func() { lookPath = origLookPath; openBrowser = origOpenBrowser })
	lookPath = func(string) (string, error) { return "/usr/local/bin/gh", nil }
	var openedURL string
	openBrowser = func(u string) error { openedURL = u; return nil }

	cmd := newReportBugCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if openedURL == "" || !strings.Contains(openedURL, "issues/new?") {
		t.Errorf("openBrowser called with %q, want a bug_report.yml issue URL", openedURL)
	}
	if stdout.String() != "" {
		t.Errorf("expected no stdout output when gh succeeds, got %q", stdout.String())
	}
}

func TestReportBugCmdRejectsArgs(t *testing.T) {
	cmd := newReportBugCmd()
	cmd.SetArgs([]string{"unexpected"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for a stray positional argument")
	}
}

func TestReportBugCmdIsHidden(t *testing.T) {
	cmd := newReportBugCmd()
	if !cmd.Hidden {
		t.Error("report-bug must be Hidden so it doesn't appear in `wendy --help`")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./go/internal/cli/commands/... -run TestReportBugCmd -v`
Expected: FAIL with `undefined: newReportBugCmd`.

- [ ] **Step 3: Implement `newReportBugCmd`**

```go
// append to go/internal/cli/commands/report_bug.go
func newReportBugCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "report-bug",
		Short:  "Open a pre-filled GitHub bug report",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := platforminfo.Collect()
			rawURL := reportBugURL(info, nil, "")
			if maybeOpenReportBugURL(cmd, rawURL) {
				return nil
			}

			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "`gh` CLI not found — install it from https://cli.github.com, or open this link to file a report:")
			fmt.Fprintln(out, rawURL)
			fmt.Fprintln(out)
			fmt.Fprintln(out, info.Block())
			return nil
		},
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./go/internal/cli/commands/... -run TestReportBugCmd -v`
Expected: PASS (all four cases).

- [ ] **Step 5: Register the command in `root.go`**

In `go/internal/cli/commands/root.go`, add alongside the other hidden commands (near `tourCmd := newTourCmd()` / `mcpCmd := newMCPCmd()`, around line 172-175):

```go
	tourCmd := newTourCmd()
	tourCmd.Hidden = true
	mcpCmd := newMCPCmd()
	mcpCmd.Hidden = true
	reportBugCmd := newReportBugCmd()
```

(`newReportBugCmd` already sets `Hidden: true` itself, so no separate assignment is needed — this mirrors how `jsonCmd`/`authCmd` are declared just before their own `.Hidden = true` line elsewhere in this block; keep the ordering consistent with the surrounding declarations.)

Then add `reportBugCmd` to the `root.AddCommand(...)` hidden-command list (around line 226-240), e.g. directly after `infoCmd,`:

```go
		infoCmd,
		reportBugCmd,
		utilsCmd,
```

- [ ] **Step 6: Verify the command is wired up end-to-end**

Run: `go build ./go/... && go run ./go/cmd/wendy report-bug --help`
Expected: prints cobra's help for `report-bug` (proving it's registered and runnable), and does NOT appear in `go run ./go/cmd/wendy --help`'s command list (proving `Hidden` took effect).

- [ ] **Step 7: Run the full commands package test suite**

Run: `go test ./go/internal/cli/commands/... -v 2>&1 | tail -60`
Expected: PASS, no regressions in `root_test.go` or elsewhere from the new registration.

- [ ] **Step 8: Commit**

```bash
git add go/internal/cli/commands/report_bug.go go/internal/cli/commands/report_bug_test.go go/internal/cli/commands/root.go
git commit -m "feat(cli): add hidden wendy report-bug command"
```

---

### Task 3: Wire the automatic fallback into `crashflow.go`

**Files:**
- Modify: `go/internal/cli/commands/crashflow.go:103-104` (replace the cloud-unavailable print block)
- Modify: `go/internal/cli/commands/crashflow_test.go` (add tests for the extracted helper)

**Interfaces:**
- Consumes: `reportBugURL`, `maybeOpenReportBugURL` (Task 1); existing `platforminfo.Info`, `crashreport.Bundle` types; `crashreport.Result.LocalFile` (existing field, `go/internal/cli/crashreport/submit.go`).
- Produces: `reportCrashLocally(cmd *cobra.Command, out io.Writer, info platforminfo.Info, bundle crashreport.Bundle, localFile string)`, called only from `MaybeRunCrashReport`.

- [ ] **Step 1: Write the failing tests for `reportCrashLocally`**

```go
// append to go/internal/cli/commands/crashflow_test.go
import (
	"os/exec"
	// ...(keep existing imports)

	"github.com/wendylabsinc/wendy/go/internal/cli/crashreport"
	"github.com/wendylabsinc/wendy/go/internal/shared/platforminfo"
)

func TestReportCrashLocallyOpensBrowserWhenGHPresent(t *testing.T) {
	origLookPath, origOpenBrowser := lookPath, openBrowser
	t.Cleanup(func() { lookPath = origLookPath; openBrowser = origOpenBrowser })
	lookPath = func(string) (string, error) { return "/usr/local/bin/gh", nil }
	var openedURL string
	openBrowser = func(u string) error { openedURL = u; return nil }

	cmd := &cobra.Command{Use: "wendy"}
	var out bytes.Buffer
	info := platforminfo.Info{CLIVersion: "1.2.3"}
	bundle := crashreport.Bundle{ErrorClass: "docker_build_failed", ErrorChain: "boom"}

	reportCrashLocally(cmd, &out, info, bundle, "/tmp/report.json")

	if !strings.Contains(out.String(), "/tmp/report.json") {
		t.Errorf("out = %q, want it to mention the local file", out.String())
	}
	if openedURL == "" || !strings.Contains(openedURL, "issues/new?") {
		t.Errorf("openBrowser called with %q, want a bug_report.yml issue URL", openedURL)
	}
}

func TestReportCrashLocallyPrintsURLWhenGHMissing(t *testing.T) {
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }

	cmd := &cobra.Command{Use: "wendy"}
	var out bytes.Buffer
	info := platforminfo.Info{CLIVersion: "1.2.3"}
	bundle := crashreport.Bundle{ErrorClass: "docker_build_failed", ErrorChain: "boom"}

	reportCrashLocally(cmd, &out, info, bundle, "/tmp/report.json")

	if !strings.Contains(out.String(), "Open a bug report:") {
		t.Errorf("out = %q, want the manual fallback line", out.String())
	}
	if !strings.Contains(out.String(), "issues/new?") {
		t.Errorf("out = %q, want the report URL", out.String())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./go/internal/cli/commands/... -run TestReportCrashLocally -v`
Expected: FAIL with `undefined: reportCrashLocally`.

- [ ] **Step 3: Extract `reportCrashLocally` and update the call site**

In `go/internal/cli/commands/crashflow.go`, replace lines 103-104:

```go
	fmt.Fprintf(out, "\nCloud unavailable; report saved locally: %s\n", res.LocalFile)
	fmt.Fprintln(out, "Attach it to an issue at https://github.com/wendylabsinc/wendyos/issues")
```

with:

```go
	reportCrashLocally(executed, out, info, bundle, res.LocalFile)
```

Then add the new function (e.g. directly below `MaybeRunCrashReport`, before `printTail`):

```go
// reportCrashLocally is the cloud-unavailable fallback: point the user at a
// pre-filled GitHub issue (the same mechanism as `wendy report-bug`) carrying
// the redacted bundle's error summary and the local report path, instead of
// the previous dead-end "attach it to an issue" instruction.
func reportCrashLocally(cmd *cobra.Command, out io.Writer, info platforminfo.Info, bundle crashreport.Bundle, localFile string) {
	fmt.Fprintf(out, "\nCloud unavailable; report saved locally: %s\n", localFile)
	rawURL := reportBugURL(info, &bundle, localFile)
	if !maybeOpenReportBugURL(cmd, rawURL) {
		fmt.Fprintln(out, "Open a bug report:", rawURL)
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./go/internal/cli/commands/... -run TestReportCrashLocally -v`
Expected: PASS (both cases).

- [ ] **Step 5: Run the full crashflow test suite to confirm no regressions**

Run: `go test ./go/internal/cli/commands/... -run TestMaybeRunCrashReport -v`
Expected: PASS — `TestMaybeRunCrashReportSkipsRecoverable`, `TestMaybeRunCrashReportSuppressed`, `TestMaybeRunCrashReportNotSuppressedProceeds`, `TestCrashConsentPrompt` all still pass unchanged (none of them reach the cloud-unavailable branch, since they stop at the consent-prompt EOF).

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/commands/crashflow.go go/internal/cli/commands/crashflow_test.go
git commit -m "fix(cli): open a pre-filled bug report when crash-report cloud submission is unavailable"
```

---

### Task 4: `bug_report.yml` wording fix

**Files:**
- Create/Modify: `.github/ISSUE_TEMPLATE/bug_report.yml` (does not exist on this branch yet — `jo/platform-diagnostics-crash-reporting` predates its addition on `main`; bring the current `main` version over first, then edit)

**Interfaces:**
- None (YAML config, not Go code). No other task depends on this one.

- [ ] **Step 1: Bring the current file over from `main`**

```bash
mkdir -p .github/ISSUE_TEMPLATE
git show main:.github/ISSUE_TEMPLATE/bug_report.yml > .github/ISSUE_TEMPLATE/bug_report.yml
```

- [ ] **Step 2: Confirm the version/host-os block matches what this step expects to edit**

Run: `sed -n '55,68p' .github/ISSUE_TEMPLATE/bug_report.yml`
Expected output:

```yaml
  - type: input
    id: version
    attributes:
      label: Versions
      description: Output of `wendy version` (and agent version if relevant)
    validations:
      required: true
  - type: input
    id: host-os
    attributes:
      label: Host OS
      placeholder: macOS 15.2 (Apple Silicon), Ubuntu 24.04, Windows 11
    validations:
      required: true
```

If the line numbers differ, locate the same block by searching for `id: version`.

- [ ] **Step 3: Edit the block**

Replace it with:

```yaml
  - type: input
    id: version
    attributes:
      label: Versions
      description: Output of `wendy --version` (and agent version if relevant)
    validations:
      required: true
  - type: textarea
    id: host-os
    attributes:
      label: Host information
      description: Run 'wendy report-bug' to open a pre-filled report, or paste your OS name, version, and architecture manually.
      render: shell
    validations:
      required: true
```

- [ ] **Step 4: Validate the YAML parses and the diff has no whitespace errors**

Run:
```bash
python3 -c "import yaml; yaml.safe_load(open('.github/ISSUE_TEMPLATE/bug_report.yml'))" && echo "YAML OK"
git diff --check
```
Expected: `YAML OK` printed, `git diff --check` produces no output (no trailing-whitespace/tab errors).

- [ ] **Step 5: Commit**

```bash
git add .github/ISSUE_TEMPLATE/bug_report.yml
git commit -m "docs: point bug_report.yml at wendy --version and wendy report-bug"
```

Note for the eventual PR description: this branch had never picked up `main`'s addition of `bug_report.yml` (PR #1228 predates it), so this commit's diff will show the file as newly added rather than modified. When #1228/this branch is eventually rebased or merged against current `main`, expect a create/create conflict on this one file — resolve by keeping this branch's version (it is `main`'s version plus the two wording edits above).

---

### Task 5: Final verification

**Files:** None (verification only).

- [ ] **Step 1: Run the full CLI test suite**

Run: `go test ./go/internal/cli/... -v 2>&1 | tail -100`
Expected: PASS, no failures or skips beyond any pre-existing ones unrelated to this change.

- [ ] **Step 2: Run `go vet`**

Run: `go vet ./go/internal/cli/...`
Expected: no output (clean).

- [ ] **Step 3: Build the CLI binary**

Run: `go build -o /tmp/wendy-report-bug-check ./go/cmd/wendy`
Expected: builds with no errors.

- [ ] **Step 4: Manual smoke test — gh present**

Run: `/tmp/wendy-report-bug-check report-bug` (with `gh` on `PATH`)
Expected: stderr prints "Opening a pre-filled bug report in your browser...", and the browser opens to a GitHub new-issue page with the Versions and Host information fields already filled in, nothing else.

- [ ] **Step 5: Manual smoke test — gh absent**

Run: `PATH=/usr/bin:/bin /tmp/wendy-report-bug-check report-bug` (a `PATH` without `gh`)
Expected: stdout prints the "`gh` CLI not found" message, the issue URL, and the diagnostic block; nothing opens automatically.

- [ ] **Step 6: Confirm `report-bug` stays hidden**

Run: `/tmp/wendy-report-bug-check --help`
Expected: `report-bug` does not appear in the command list.

- [ ] **Step 7: Review the full diff before opening a PR**

Run: `git diff origin/jo/platform-diagnostics-crash-reporting...HEAD --stat`
Expected: only the files touched by Tasks 1-4 (`report_bug.go`, `report_bug_test.go`, `root.go`, `crashflow.go`, `crashflow_test.go`, `bug_report.yml`, plus the two spec docs from brainstorming/planning).
