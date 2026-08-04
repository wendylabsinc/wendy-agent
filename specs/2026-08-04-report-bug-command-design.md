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

GitHub issue forms (the YAML-based templates like `bug_report.yml`) support pre-filling individual
fields via URL query parameters matching each field's `id` (documented:
https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/creating-an-issue#creating-an-issue-from-a-url-query).
This repo also already has a tested, cross-platform `openBrowser(url string) error` seam
(`go/internal/cli/commands/open_browser.go`, backed by `browseropen.Open`, used by `wendy utils
open-browser`). Combined, these mean we can build a properly-prefilled issue-form URL and open it
without shelling out to `gh` at all. `gh`'s presence is used only as a signal that this is an
interactive developer machine (not a headless/SSH/CI-like session) worth auto-opening a browser on —
not as the mechanism that does the filling or opening.

This design adds a `report-bug` hidden command for manual use, and wires the same URL-builder into
`MaybeRunCrashReport`'s cloud-unavailable branch so the flow "spawns automatically" right where a
report currently goes nowhere.

## Goals

1. A hidden `wendy report-bug` command that collects local diagnostics (reusing `platforminfo`) and,
   when `gh` is installed, opens a pre-filled GitHub issue-form URL in the browser (via the existing
   `openBrowser` seam) with `bug_report.yml`'s Versions and Host information fields filled in.
2. When `gh` is missing, print the diagnostic block plus the same URL as a manual link — no auto-open,
   no dead end.
3. Wire the same URL-builder into #1228's `MaybeRunCrashReport`: when cloud submission fails after the
   user has already consented to send a report, automatically open a pre-filled issue (Versions, Host
   information, a short auto-filled "What happened", and a pointer to the locally-saved report file for
   the full log tail) instead of just printing instructions.
4. Fix `bug_report.yml`'s "Host information" field to reference `report-bug` instead of `--host-info`,
   and carry over #1563's `wendy version` → `wendy --version` typo fix.

## Non-goals

- Submitting the issue automatically without the user looking at it first — this only ever opens an
  editable compose page; nothing is filed without the user pressing submit in the browser.
- Shelling out to `gh issue create` — `gh`'s own `--title`/`--body` flags bypass individual form fields
  in favor of one combined body blob, which is a worse fill than the URL-query-param approach. `gh` is
  used only via `exec.LookPath` as a presence check.
- Cramming full log tails into the URL — browsers/servers cap request-target length (~8KB is a common
  practical ceiling); the automatic path links to the already-saved local report file instead of trying
  to inline hundreds of log lines as a query parameter.
- Any change to `--host-info` on `main` or to #1228's existing consent/redaction/submission logic —
  only the cloud-unavailable branch's *output* changes.

## Architecture

```
wendy report-bug (manual)                MaybeRunCrashReport (#1228, automatic)
        │                                            │
        ▼                                            ▼
platforminfo.Collect()                    (already have: bundle, res.LocalFile)
        │                                            │
        ▼                                            ▼
        └──────────────► reportBugURL(info, bundle*, localFile) ◄──────────────┘
                                   │
                                   ▼
                       lookGH() found on PATH?
                          │             │
                         yes            no
                          │             │
                          ▼             ▼
                openBrowser(url)   print diagnostic block + url
              "Opening a pre-filled    (manual fallback, no
               bug report in your       auto-open)
               browser..."
```

`bundle*`/`localFile` are empty for the manual `report-bug` path (no error context exists) and
populated for the automatic path (bundle from `crashreport.Build`, `localFile` from
`crashreport.Result.LocalFile`).

## Components

### 1. `reportBugURL(info platforminfo.Info, bundle *crashreport.Bundle, localFile string) string`

New function in `go/internal/cli/commands/report_bug.go`. Builds:

`https://github.com/wendylabsinc/WendyOS/issues/new?template=bug_report.yml&<query>`

where `<query>` is built with `net/url.Values`, field ids matching `bug_report.yml`:

- `version` — `info.CLIVersion`.
- `host-os` — `info.Block()`.
- `what-happened` — omitted when `bundle == nil` (manual path leaves it for the user to fill in from
  the form itself); when `bundle != nil`, set to `bundle.ErrorClass + ": " + bundle.ErrorChain`,
  truncated to 500 characters with a `…` suffix if longer (`truncate(s string, max int) string`
  helper).
- `logs` — omitted when `bundle == nil`; when `bundle != nil` and `localFile != ""`, set to
  `"Full redacted diagnostic bundle saved locally at " + localFile + " — attach it here."` (never the
  raw log tail — see Non-goals on URL length).
- `component`, `hardware` — never set; not inferable from either path, left for the user to pick from
  the form's dropdowns.

All values are URL-encoded by `url.Values.Encode()`. Pure function, no I/O — fully unit-testable
against fixed `Info`/`Bundle` values asserting the decoded query string.

### 2. `maybeOpenReportBugURL(cmd *cobra.Command, url string) bool`

- Looks up `gh` via a package-level `lookGH = func() (string, error) { return exec.LookPath("gh") }`
  seam (swappable in tests).
- If found: calls `openBrowser(url)` (existing seam from `open_browser.go`), prints `"Opening a
  pre-filled bug report in your browser..."` to `cmd.ErrOrStderr()`, returns `true`.
- If `gh` not found, or `openBrowser` errors: returns `false` and does nothing else — callers own the
  fallback message, since the manual and automatic call sites want different wording.

### 3. `wendy report-bug` (`newReportBugCmd`, hidden, added to root.go's hidden-command block)

```go
info := platforminfo.Collect()
url := reportBugURL(info, nil, "")
if !maybeOpenReportBugURL(cmd, url) {
    fmt.Fprintln(out, "`gh` CLI not found — install it from https://cli.github.com, or open this link to file a report:")
    fmt.Fprintln(out, url)
    fmt.Fprintln(out, "")
    fmt.Fprintln(out, info.Block())
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
url := reportBugURL(info, &bundle, res.LocalFile)
if !maybeOpenReportBugURL(executed, url) {
    fmt.Fprintln(out, "Open a bug report:", url)
}
```

No new consent prompt: the user already answered "Send this report?" yes; this is what "sending"
degrades to when the cloud path is unavailable.

### 5. `bug_report.yml`

- `version` field description: `wendy version` → `wendy --version` (carried over from #1563).
- `host-os` field description: replace the `--host-info` reference with:
  `Run 'wendy report-bug' to open a pre-filled report, or paste your OS name, version, and architecture manually.`

## Testing

- `report_bug_url_test.go`: table tests for `reportBugURL` — nil bundle (only version/host-os set) vs
  populated bundle (+ what-happened, + logs pointing at localFile), asserting the decoded query string
  via `url.ParseQuery`. Includes a case for the 500-char truncation.
- `report_bug_test.go`: `lookGH` and `openBrowser` stubbed to simulate gh-present/gh-absent/
  openBrowser-error; asserts the correct stdout/stderr text in each case (following the pattern in
  `open_browser_test.go`).
- Update existing `crashflow_test.go` cloud-unavailable case for the new call, using the same stubs.
- `go test ./go/internal/cli/...`, `go vet ./go/internal/cli/...`.
- Manual: run `wendy report-bug` locally with and without `gh` on PATH; confirm the browser opens with
  Versions/Host information pre-filled in their actual form fields and nothing is submitted without
  pressing the button.

## Open questions / follow-ups

- #1563 should be closed by its author (`max`) once this lands, keeping only the `bug_report.yml`
  wording fix folded in here.
