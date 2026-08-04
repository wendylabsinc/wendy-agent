# `wendy report-bug` + automatic GitHub issue prefill — design

Date: 2026-08-04
Status: approved (brainstorming)
Builds on `specs/2026-06-28-platform-diagnostics-crash-reporting-design.md` and
`specs/2026-07-04-anonymous-crash-reporting-cli-notifications-design.md` (PR #1228). Supersedes the
`--host-info` flag proposed in PR #1563 (`max/fix-issue-template-host-info`, still open) — that PR's
`host_info_*.go` files duplicate work `platforminfo` already does on this branch and should not be
merged; #1563 should be closed once this ships, keeping only its `bug_report.yml` wording fix.

## Context

PR #1563 added a root `wendy --host-info` flag that prints CLI/OS/arch/Go diagnostics for the GitHub
bug report form, and pointed `bug_report.yml`'s "Host information" field at it. Two problems:

- It duplicates `platforminfo.Collect()` / `.Block()`, which PR #1228 already built for the crash
  banner and crash-report bundles (dev OS, OS version, arch, kernel).
- It still leaves the user to copy-paste output into a browser by hand. #1228's crash-report flow
  already hits this exact wall: when `crashreport.SubmitHTTP` fails (cloud unavailable), it falls back
  to a local JSON file and tells the user to "attach it to an issue" — a dead end that no one follows
  through on in practice.

`gh` (the GitHub CLI) can turn both of these into one step: build the issue title/body and let `gh`
open a prefilled compose page in the browser. This design adds a `report-bug` hidden command for
manual use, and wires the same body-builder into `MaybeRunCrashReport`'s cloud-unavailable branch so
the flow "spawns automatically" right where a report currently goes nowhere.

## Goals

1. A hidden `wendy report-bug` command that collects local diagnostics (reusing `platforminfo`) and,
   when `gh` is installed, opens a prefilled GitHub issue compose page mirroring `bug_report.yml`'s
   sections.
2. When `gh` is missing, print the diagnostic block plus a manual issue-creation link — no dead end.
3. Wire the same body-builder into #1228's `MaybeRunCrashReport`: when cloud submission fails after
   the user has already consented to send a report, automatically open a prefilled issue (with the
   actual redacted bundle contents, not placeholders) instead of just printing instructions.
4. Fix `bug_report.yml`'s "Host information" field to reference `report-bug` instead of `--host-info`,
   and carry over #1563's `wendy version` → `wendy --version` typo fix.

## Non-goals

- Submitting the issue automatically without the user looking at it first — `gh issue create --web`
  always opens an editable compose page; nothing is filed without the user pressing submit in the
  browser.
- Reimplementing OS-specific browser-opening — `gh issue create --web` already does this reliably
  cross-platform, which is the main reason to shell out to `gh` rather than opening a URL ourselves.
- Any change to `--host-info` on `main` or to #1228's existing consent/redaction/submission logic —
  only the cloud-unavailable branch's *output* changes.

## Architecture

```
wendy report-bug (manual)                MaybeRunCrashReport (#1228, automatic)
        │                                            │
        ▼                                            ▼
platforminfo.Collect()                    (already have: bundle from crashreport.Build)
        │                                            │
        ▼                                            ▼
        └──────────────► reportBugBody(info, bundle*) ◄──────────────┘
                                   │
                                   ▼
                     exec.LookPath("gh") found?
                          │             │
                         yes            no
                          │             │
                          ▼             ▼
              gh issue create      print diagnostic block +
              --repo wendylabsinc/WendyOS   issues/new?template=bug_report.yml
              --title <t> --body <b> --web       (manual fallback)
```

`bundle*` is nil for the manual `report-bug` path (no error context exists) and populated for the
automatic path (error class, severity, chain, log tail already collected by the existing crash flow).

## Components

### 1. `reportBugBody(info platforminfo.Info, bundle *crashreport.Bundle) (title, body string)`

New function in `go/internal/cli/commands/report_bug.go`. Builds markdown mirroring
`bug_report.yml`'s field labels as `###` headers:

- `What happened?`, `Steps to reproduce`, `Component`, `Target hardware` — placeholder prompt text
  (e.g. `_Describe what happened here._`) when `bundle == nil`; when `bundle != nil`, `What happened?`
  is pre-filled with `bundle.ErrorChain` and a `_(auto-filled from the failure that triggered this
  report; edit as needed)_` note, the rest stay placeholders (component/hardware aren't inferable).
- `Versions` — `info.CLIVersion`.
- `Host information` — `info.Block()`.
- `Relevant logs or output` — omitted when `bundle == nil`; filled with `bundle.LogTail` (and
  `bundle.BuildOutputTail` when present) otherwise.

Title: `"bug: <short summary>"` placeholder for manual use; `"bug: " + bundle.ErrorClass` for the
automatic path.

Pure function, no I/O — fully unit-testable against fixed `Info`/`Bundle` values.

### 2. `openPrefilledIssue(cmd *cobra.Command, title, body string) bool`

- Looks up `gh` via a package-level `lookGH = exec.LookPath` seam (swappable in tests).
- If found: runs `gh issue create --repo wendylabsinc/WendyOS --title <title> --body <body> --web`,
  streaming its stderr through so the user sees `gh`'s own "Opening in your browser." message. Returns
  `true` on success.
- If not found, or `gh` errors: returns `false` and does nothing else — callers own the fallback
  message, since the manual and automatic call sites want different wording.

### 3. `wendy report-bug` (`newReportBugCmd`, hidden, added to root.go's hidden-command block)

```
info := platforminfo.Collect()
title, body := reportBugBody(info, nil)
if !openPrefilledIssue(cmd, title, body) {
    fmt.Fprintln(out, "`gh` CLI not found — install it from https://cli.github.com for a pre-filled report.")
    fmt.Fprintln(out, "")
    fmt.Fprintln(out, info.Block())
    fmt.Fprintln(out, "")
    fmt.Fprintln(out, "Open a bug report: https://github.com/wendylabsinc/WendyOS/issues/new?template=bug_report.yml")
}
```

### 4. `crashflow.go` change (PR #1228 branch)

In `MaybeRunCrashReport`, replace:

```go
fmt.Fprintf(out, "\nCloud unavailable; report saved locally: %s\n", res.LocalFile)
fmt.Fprintln(out, "Attach it to an issue at https://github.com/wendylabsinc/wendyos/issues")
```

with:

```go
fmt.Fprintf(out, "\nCloud unavailable; report saved locally: %s\n", res.LocalFile)
title, body := reportBugBody(info, &bundle)
if !openPrefilledIssue(executed, title, body) {
    fmt.Fprintln(out, "Attach it to an issue at https://github.com/wendylabsinc/WendyOS/issues/new?template=bug_report.yml")
}
```

No new consent prompt: the user already answered "Send this report?" yes; this is what "sending"
degrades to when the cloud path is unavailable.

### 5. `bug_report.yml`

- `version` field description: `wendy version` → `wendy --version` (carried over from #1563).
- `host-os` field description: replace the `--host-info` reference with:
  `Run 'wendy report-bug' to open a pre-filled report, or paste your OS name, version, and architecture manually.`

## Testing

- `report_bug_body_test.go`: table tests for `reportBugBody` — nil bundle (placeholders) vs populated
  bundle (error chain/log tail filled in), asserting exact section presence/absence.
- `report_bug_test.go`: `lookGH` stubbed to simulate gh-present/gh-absent/gh-error; asserts the correct
  fallback text and that the constructed `gh` argv matches expectations (via a stubbed exec runner,
  not a real subprocess).
- Update existing `crashflow_test.go` cloud-unavailable case for the new call, using the same `lookGH`
  stub.
- `go test ./go/internal/cli/...`, `go vet ./go/internal/cli/...`.
- Manual: run `wendy report-bug` locally with and without `gh` on PATH; confirm the browser opens with
  the expected sections pre-filled and nothing is submitted without pressing the button.

## Open questions / follow-ups

- `gh issue create --web --title/--body` on a form-only repo bypasses the YAML form UI (GitHub treats
  it as a plain issue with the given body) rather than pre-filling individual form fields. This is
  intentional here — it's the reliable, documented behavior versus relying on per-field query-param
  prefill, which is undocumented for `gh`'s flag surface. Worth revisiting if GitHub/gh add first-class
  form-field prefill support.
- #1563 should be closed by its author (`max`) once this lands, keeping only the `bug_report.yml`
  wording fix folded in here.
