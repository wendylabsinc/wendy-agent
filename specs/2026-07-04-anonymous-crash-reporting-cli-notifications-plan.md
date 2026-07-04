# Anonymous crash reporting + CLI fix-notifications Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Record redacted crash reports for all users over the anonymous telemetry channel, deliver fix-notifications in-CLI + via best-effort OS notification, add a 3-way consent prompt, and remove the gRPC DiagnosticsService.

**Architecture:** Reuse the analytics `anonymous_id` + telemetry HTTP host for submission and a status poll; reuse the update-check "poll → persist to config → notify on next run" pattern for delivery. One JSON payload for both the HTTP POST and the local-file fallback. The interactive crash prompt stays dormant (not called from `main.go`); the poll/notice are inert until a report is submitted.

**Tech Stack:** Go, `net/http`, `net/http/httptest`, `encoding/json`, cobra, Go standard testing.

## Global Constraints

- Design doc: `specs/2026-07-04-anonymous-crash-reporting-cli-notifications-design.md`.
- Every crash/notify code path is **best-effort**: never alter the process exit code, never return a secondary error to the user.
- Capture is gated on analytics being **enabled** (inherits opt-out + CI hard-off) AND terminal interactive AND `cfg.CrashReport.Suppressed == false`.
- The interactive crash prompt is **not** wired into `main.go` in this plan (stays dormant per PR #1228 posture).
- CI enforces `gofmt -l -s .` (`.github/workflows/go-tests.yml`) — run `gofmt -w -s ./go` before every commit.
- Redaction is a safety net; the consent **preview must render exactly the fields the JSON payload serializes**.
- Work happens in worktree `.claude/worktrees/pr-1228` on branch `jo/platform-diagnostics-crash-reporting`. All `go` commands run from `go/`.

---

### Task 1: JSON payload for the bundle (alongside existing cloudpb)

Add JSON serialization without removing the gRPC types yet, so the build stays green.

**Files:**
- Modify: `go/internal/cli/crashreport/bundle.go`
- Modify: `go/internal/shared/platforminfo/platforminfo.go` (add JSON tags to `Info`)
- Test: `go/internal/cli/crashreport/bundle_test.go`

**Interfaces:**
- Consumes: `platforminfo.Info`, existing `Bundle` struct.
- Produces: `type submitPayload struct{…}` and `func (b Bundle) Payload(anonymousID string, notifyOnFix bool) submitPayload`.

- [ ] **Step 1: Write the failing test**

Add to `bundle_test.go`:

```go
func TestBundlePayloadJSON(t *testing.T) {
	b := Build(
		platforminfo.Info{CLIVersion: "1.2.3", DevOS: "darwin"},
		"other", "unrecoverable", "boom",
		[]string{"line1"}, nil,
	)
	p := b.Payload("anon-123", true)
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	for _, want := range []string{`"anonymous_id":"anon-123"`, `"notify_on_fix":true`, `"error_class":"other"`, `"cli_version":"1.2.3"`, `"error_chain":"boom"`} {
		if !strings.Contains(s, want) {
			t.Errorf("payload missing %s: %s", want, s)
		}
	}
}
```

Add imports `encoding/json`, `strings`, and the `platforminfo` import if not present.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/crashreport/ -run TestBundlePayloadJSON`
Expected: FAIL — `b.Payload` undefined.

- [ ] **Step 3: Implement**

In `platforminfo.go`, add JSON tags to the `Info` struct fields (keep field names), matching the old proto names:

```go
type Info struct {
	CLIVersion   string `json:"cli_version,omitempty"`
	DevOS        string `json:"dev_os,omitempty"`
	DevOSVersion string `json:"dev_os_version,omitempty"`
	DevArch      string `json:"dev_arch,omitempty"`
	DevKernel    string `json:"dev_kernel,omitempty"`

	TargetAgentVersion   string `json:"target_agent_version,omitempty"`
	TargetOS             string `json:"target_os,omitempty"`
	TargetOSVersion      string `json:"target_os_version,omitempty"`
	TargetHardware       string `json:"target_hardware,omitempty"`
	TargetGPUVendor      string `json:"target_gpu_vendor,omitempty"`
	TargetJetpackVersion string `json:"target_jetpack_version,omitempty"`
	TargetCUDAVersion    string `json:"target_cuda_version,omitempty"`
	TargetStorageMedium  string `json:"target_storage_medium,omitempty"`
}
```

In `bundle.go`, add (keep the existing `Request()` for now):

```go
// submitPayload is the JSON body POSTed to the telemetry crashreports endpoint.
// It embeds the redacted bundle fields plus the anonymous routing key.
type submitPayload struct {
	AnonymousID     string           `json:"anonymous_id"`
	NotifyOnFix     bool             `json:"notify_on_fix"`
	PlatformInfo    platforminfo.Info `json:"platform_info"`
	ErrorClass      string           `json:"error_class,omitempty"`
	Severity        string           `json:"severity,omitempty"`
	ErrorChain      string           `json:"error_chain,omitempty"`
	LogTail         []string         `json:"log_tail,omitempty"`
	BuildOutputTail []string         `json:"build_output_tail,omitempty"`
	Contact         string           `json:"contact,omitempty"`
}

// Payload builds the JSON submit body from the (already redacted) bundle.
func (b Bundle) Payload(anonymousID string, notifyOnFix bool) submitPayload {
	return submitPayload{
		AnonymousID:     anonymousID,
		NotifyOnFix:     notifyOnFix,
		PlatformInfo:    b.Info,
		ErrorClass:      b.ErrorClass,
		Severity:        b.Severity,
		ErrorChain:      b.ErrorChain,
		LogTail:         b.LogTail,
		BuildOutputTail: b.BuildOutputTail,
		Contact:         b.Contact,
	}
}
```

- [ ] **Step 4: Run tests**

Run: `cd go && gofmt -w -s ./internal/cli/crashreport ./internal/shared/platforminfo && go test ./internal/cli/crashreport/ ./internal/shared/platforminfo/ && go build ./...`
Expected: PASS, build OK.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/crashreport/bundle.go go/internal/cli/crashreport/bundle_test.go go/internal/shared/platforminfo/platforminfo.go
git commit -m "feat(crashreport): JSON submit payload keyed by anonymous_id"
```

---

### Task 2: HTTP submit with local-file fallback

**Files:**
- Modify: `go/internal/cli/crashreport/submit.go` (add `SubmitHTTP`; leave gRPC funcs for now)
- Test: `go/internal/cli/crashreport/submit_test.go`

**Interfaces:**
- Consumes: `Bundle.Payload`, `Result` struct (existing: `TrackingID`, `StatusURL`, `LocalFile`).
- Produces: `func SubmitHTTP(ctx context.Context, endpoint, anonymousID string, b Bundle, notifyOnFix bool) (Result, error)`.

- [ ] **Step 1: Write the failing test**

Add to `submit_test.go`:

```go
func TestSubmitHTTPSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"anonymous_id":"anon-9"`) {
			t.Errorf("missing anonymous_id: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tracking_id":"WDY-ABC123","status_url":"https://x/WDY-ABC123"}`))
	}))
	defer srv.Close()

	res, err := SubmitHTTP(context.Background(), srv.URL, "anon-9", Bundle{ErrorClass: "other"}, true)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.TrackingID != "WDY-ABC123" || res.StatusURL == "" {
		t.Errorf("bad result: %+v", res)
	}
}

func TestSubmitHTTPFallsBackToFile(t *testing.T) {
	// Unreachable endpoint → local-file fallback, nil error.
	res, err := SubmitHTTP(context.Background(), "http://127.0.0.1:0", "anon-9", Bundle{ErrorClass: "other"}, false)
	if err != nil {
		t.Fatalf("fallback must not error: %v", err)
	}
	if res.LocalFile == "" || res.TrackingID != "" {
		t.Errorf("expected local-file fallback, got %+v", res)
	}
}
```

Imports: `context`, `io`, `net/http`, `net/http/httptest`, `strings`, `testing`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/crashreport/ -run TestSubmitHTTP`
Expected: FAIL — `SubmitHTTP` undefined.

- [ ] **Step 3: Implement**

Add to `submit.go`:

```go
// SubmitHTTP POSTs the redacted bundle as JSON to the telemetry crashreports
// endpoint. On any failure it writes the same JSON to a local file and returns
// that path with a nil error — the reporter never produces a secondary error.
func SubmitHTTP(ctx context.Context, endpoint, anonymousID string, b Bundle, notifyOnFix bool) (Result, error) {
	payload := b.Payload(anonymousID, notifyOnFix)
	body, err := json.Marshal(payload)
	if err == nil && endpoint != "" {
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		req, rerr := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(body))
		if rerr == nil {
			req.Header.Set("Content-Type", "application/json")
			resp, derr := http.DefaultClient.Do(req)
			if derr == nil {
				defer resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					var out struct {
						TrackingID string `json:"tracking_id"`
						StatusURL  string `json:"status_url"`
					}
					if json.NewDecoder(resp.Body).Decode(&out) == nil && out.TrackingID != "" {
						return Result{TrackingID: out.TrackingID, StatusURL: out.StatusURL}, nil
					}
				}
			}
		}
	}
	path, ferr := writeLocalBundleJSON(body)
	if ferr != nil {
		return Result{}, ferr
	}
	return Result{LocalFile: path}, nil
}

func writeLocalBundleJSON(body []byte) (string, error) {
	dir, err := os.MkdirTemp("", "wendy-crashreport-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "report.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", err
	}
	return path, nil
}
```

Add imports `bytes`, `encoding/json`, `net/http` to `submit.go`. (`os`, `filepath`, `time`, `context` already present.)

- [ ] **Step 4: Run tests**

Run: `cd go && gofmt -w -s ./internal/cli/crashreport && go test ./internal/cli/crashreport/ && go build ./...`
Expected: PASS, build OK.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/crashreport/submit.go go/internal/cli/crashreport/submit_test.go
git commit -m "feat(crashreport): HTTP submit with local-file fallback"
```

---

### Task 3: analytics accessors — DistinctID + TelemetryBaseURL

**Files:**
- Modify: `go/internal/cli/analytics/analytics.go`
- Test: `go/internal/cli/analytics/analytics_test.go`

**Interfaces:**
- Produces: `func DistinctID() (string, error)`, `func TelemetryBaseURL() string`.

- [ ] **Step 1: Write the failing test**

Add to `analytics_test.go`:

```go
func TestDistinctIDStable(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // config.ConfigDir uses HOME/.wendy
	id1, err := DistinctID()
	if err != nil || id1 == "" {
		t.Fatalf("DistinctID: %v id=%q", err, id1)
	}
	id2, _ := DistinctID()
	if id1 != id2 {
		t.Errorf("DistinctID not stable: %q != %q", id1, id2)
	}
}

func TestTelemetryBaseURLOverride(t *testing.T) {
	t.Setenv("WENDY_TELEMETRY_HOST", "http://localhost:8082")
	if got := TelemetryBaseURL(); got != "http://localhost:8082/v1/telemetry" {
		t.Errorf("override base = %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/analytics/ -run 'TestDistinctIDStable|TestTelemetryBaseURLOverride'`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

In `analytics.go`, add (reuse `loadOrCreateID`):

```go
// DistinctID returns the stable anonymous install id, loading or creating the
// ~/.wendy/analytics_id file. Independent of whether tracking is enabled.
func DistinctID() (string, error) {
	if distinctID != "" {
		return distinctID, nil
	}
	id, err := loadOrCreateID()
	if err != nil {
		return "", err
	}
	distinctID = id
	return id, nil
}

// TelemetryBaseURL returns the base telemetry URL (…/v1/telemetry). It honors
// WENDY_TELEMETRY_HOST (scheme+host override) for local development.
func TelemetryBaseURL() string {
	if host := strings.TrimSpace(os.Getenv("WENDY_TELEMETRY_HOST")); host != "" {
		return strings.TrimRight(host, "/") + "/v1/telemetry"
	}
	// Derive from the events endpoint constant by trimming the trailing path.
	return strings.TrimSuffix(telemetryEndpoint, "/events")
}
```

`strings` and `os` are already imported.

- [ ] **Step 4: Run tests**

Run: `cd go && gofmt -w -s ./internal/cli/analytics && go test ./internal/cli/analytics/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/analytics/analytics.go go/internal/cli/analytics/analytics_test.go
git commit -m "feat(analytics): expose DistinctID and TelemetryBaseURL"
```

---

### Task 4: config CrashReport block

**Files:**
- Modify: `go/internal/shared/config/config.go`
- Test: `go/internal/shared/config/config_test.go`

**Interfaces:**
- Produces: `config.CrashReportConfig{Suppressed bool; SubscribedReports []string; LastCrashStatusCheck string; PendingFixNotices []FixNotice}`, `config.FixNotice{TrackingID, FixedInRelease string}`, and field `Config.CrashReport *CrashReportConfig`.

- [ ] **Step 1: Write the failing test**

Add to `config_test.go`:

```go
func TestCrashReportConfigRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &Config{CrashReport: &CrashReportConfig{
		Suppressed:        true,
		SubscribedReports: []string{"WDY-ABC123"},
		PendingFixNotices: []FixNotice{{TrackingID: "WDY-ABC123", FixedInRelease: "v1.4.0"}},
	}}
	if err := Save(cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.CrashReport == nil || !loaded.CrashReport.Suppressed ||
		len(loaded.CrashReport.SubscribedReports) != 1 ||
		len(loaded.CrashReport.PendingFixNotices) != 1 {
		t.Errorf("round-trip mismatch: %+v", loaded.CrashReport)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/shared/config/ -run TestCrashReportConfigRoundTrip`
Expected: FAIL — undefined types.

- [ ] **Step 3: Implement**

Add the field to `Config` (after `DevicePins`):

```go
	// CrashReport holds opt-in crash-reporting state: the suppression flag,
	// tracking ids awaiting a fix, the last status-poll time, and pending
	// fix notices to surface on the next run. Nil until first used.
	CrashReport *CrashReportConfig `json:"crashReport,omitempty"`
```

Add the types (near `AnalyticsConfig`):

```go
// CrashReportConfig holds opt-in crash-reporting preferences and state.
type CrashReportConfig struct {
	Suppressed           bool        `json:"suppressed,omitempty"`
	SubscribedReports    []string    `json:"subscribedReports,omitempty"`
	LastCrashStatusCheck string      `json:"lastCrashStatusCheck,omitempty"` // RFC3339 UTC
	PendingFixNotices    []FixNotice `json:"pendingFixNotices,omitempty"`
}

// FixNotice records that a reported crash was fixed in a given release.
type FixNotice struct {
	TrackingID     string `json:"trackingId"`
	FixedInRelease string `json:"fixedInRelease,omitempty"`
}
```

- [ ] **Step 4: Run tests**

Run: `cd go && gofmt -w -s ./internal/shared/config && go test ./internal/shared/config/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/shared/config/config.go go/internal/shared/config/config_test.go
git commit -m "feat(config): add CrashReport state block"
```

---

### Task 5: crashflow 3-way consent + HTTP wiring

Rework `crashflow.go` to submit over HTTP, add the 3-way prompt, and drop the gRPC dial/subscribe. After this task the gRPC `Submit`/`Subscribe` in `submit.go` are unused.

**Files:**
- Modify: `go/internal/cli/commands/crashflow.go`
- Test: `go/internal/cli/commands/crashflow_test.go`

**Interfaces:**
- Consumes: `crashreport.SubmitHTTP`, `analytics.DistinctID`, `analytics.TelemetryBaseURL`, `config` CrashReport block, `diag.Classify/Chain/Recent`, `platforminfo.Collect`.
- Produces: `crashConsent` enum (`consentSend`, `consentSkip`, `consentSuppress`) and `func crashConsentPrompt(prompt string) crashConsent`.

- [ ] **Step 1: Write the failing test**

Replace the body of `crashflow_test.go` with tests that don't hit the network (recoverable + suppressed = no-op) and cover the 3-way parser:

```go
func TestMaybeRunCrashReportSkipsRecoverable(t *testing.T) {
	t.Setenv("CI", "") // ensure not classified as CI
	t.Setenv("WENDY_CRASHREPORT", "true")
	MaybeRunCrashReport(context.Background(), &cobra.Command{Use: "wendy"},
		fmt.Errorf("plain recoverable error"), "other")
	// No panic, returns cleanly.
}

func TestMaybeRunCrashReportSuppressed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = config.Save(&config.Config{CrashReport: &config.CrashReportConfig{Suppressed: true}})
	// Even an unrecoverable build failure must be a no-op when suppressed.
	MaybeRunCrashReport(context.Background(), &cobra.Command{Use: "wendy"},
		diag.MarkBuildFailure(fmt.Errorf("docker build failed")), "other")
}

func TestCrashConsentPrompt(t *testing.T) {
	cases := map[string]crashConsent{"y\n": consentSend, "yes\n": consentSend, "n\n": consentSkip, "\n": consentSkip, "d\n": consentSuppress, "don't\n": consentSuppress}
	for in, want := range cases {
		if got := parseCrashConsent(in); got != want {
			t.Errorf("parseCrashConsent(%q) = %v, want %v", in, got, want)
		}
	}
}
```

Imports: `context`, `fmt`, `testing`, `github.com/spf13/cobra`, `config`, `diag`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run 'TestMaybeRunCrashReportSuppressed|TestCrashConsentPrompt'`
Expected: FAIL — `parseCrashConsent`/`consentSend` undefined; suppressed path not implemented.

- [ ] **Step 3: Implement**

Rewrite `crashflow.go`. Key changes: add the enum + parser, add the suppression early-return, replace `dialDiagnosticsClient`/`offerSubscribe`/`crashreport.Submit` with `SubmitHTTP` + config updates. Full file:

```go
package commands

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/analytics"
	"github.com/wendylabsinc/wendy/go/internal/cli/crashreport"
	"github.com/wendylabsinc/wendy/go/internal/cli/diag"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/env"
	"github.com/wendylabsinc/wendy/go/internal/shared/platforminfo"
)

type crashConsent int

const (
	consentSkip crashConsent = iota
	consentSend
	consentSuppress
)

// MaybeRunCrashReport offers to submit a redacted diagnostic report after an
// unrecoverable failure. Strict no-op for recoverable errors, in CI, when
// analytics is disabled, when suppressed, or non-interactively. Never errors
// or changes the exit code.
func MaybeRunCrashReport(ctx context.Context, executed *cobra.Command, err error, errorClass string) {
	if err == nil || diag.Classify(err) != diag.Unrecoverable {
		return
	}
	if env.IsCI() || !env.CrashReport() || !analytics.Enabled() || !isInteractiveTerminal() {
		return
	}
	cfg, cerr := config.Load()
	if cerr != nil {
		return
	}
	if cfg.CrashReport != nil && cfg.CrashReport.Suppressed {
		return
	}

	out := executed.ErrOrStderr()
	fmt.Fprintln(out, "\nThis looks like an unrecoverable failure.")
	switch crashConsentPrompt("Submit an anonymous, redacted diagnostic report to help us fix it?") {
	case consentSuppress:
		setCrashSuppressed(cfg)
		fmt.Fprintln(out, "Okay — we won't ask again. Re-enable with 'wendy analytics enable' semantics later.")
		return
	case consentSkip:
		return
	case consentSend:
	}

	info := platforminfo.Collect()
	bundle := crashreport.Build(info, errorClass, string(diag.Unrecoverable), diag.Chain(err), diag.Recent(), buildOutputTail())

	fmt.Fprintln(out, "\nThe following (redacted) information will be sent:")
	fmt.Fprintln(out, info.Block())
	fmt.Fprintf(out, "Error: %s\n", bundle.ErrorChain)
	printTail(out, "Recent log lines:", bundle.LogTail)
	printTail(out, "Build output:", bundle.BuildOutputTail)

	if !crashPromptYesNo("Send this report?", false) {
		fmt.Fprintln(out, "Report not sent.")
		return
	}
	notify := crashPromptYesNo("Notify me when a release fixes this?", true)

	anonID, aerr := analytics.DistinctID()
	if aerr != nil {
		fmt.Fprintln(out, "Could not prepare report.")
		return
	}
	endpoint := analytics.TelemetryBaseURL() + "/crashreports"
	res, ferr := crashreport.SubmitHTTP(ctx, endpoint, anonID, bundle, notify)
	if ferr != nil {
		fmt.Fprintf(out, "Could not save report: %v\n", ferr)
		return
	}
	if res.TrackingID != "" {
		fmt.Fprintf(out, "\nReport submitted. Tracking number: %s\n", res.TrackingID)
		if res.StatusURL != "" {
			fmt.Fprintf(out, "Track status: %s\n", res.StatusURL)
		}
		if notify {
			addSubscribedReport(cfg, res.TrackingID)
			fmt.Fprintln(out, "You'll see a note on your next 'wendy' run once it's fixed.")
		}
		return
	}
	fmt.Fprintf(out, "\nCloud unavailable; report saved locally: %s\n", res.LocalFile)
	fmt.Fprintln(out, "Attach it to an issue at https://github.com/wendylabsinc/wendyos/issues")
}

func printTail(out io.Writer, header string, lines []string) {
	if len(lines) == 0 {
		return
	}
	fmt.Fprintln(out, "\n"+header)
	for _, l := range lines {
		fmt.Fprintln(out, l)
	}
}

func setCrashSuppressed(cfg *config.Config) {
	if cfg.CrashReport == nil {
		cfg.CrashReport = &config.CrashReportConfig{}
	}
	cfg.CrashReport.Suppressed = true
	_ = config.Save(cfg)
}

func addSubscribedReport(cfg *config.Config, trackingID string) {
	if cfg.CrashReport == nil {
		cfg.CrashReport = &config.CrashReportConfig{}
	}
	cfg.CrashReport.SubscribedReports = append(cfg.CrashReport.SubscribedReports, trackingID)
	_ = config.Save(cfg)
}

// buildOutputTail returns recent build output lines for a report. Nil for now.
func buildOutputTail() []string { return nil }

// crashConsentPrompt prints a 3-way prompt and returns the parsed choice.
func crashConsentPrompt(prompt string) crashConsent {
	fmt.Fprint(os.Stderr, prompt+" [y]es / [n]o / [d]on't ask again: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return consentSkip
	}
	return parseCrashConsent(line)
}

func parseCrashConsent(line string) crashConsent {
	s := strings.ToLower(strings.TrimSpace(line))
	switch {
	case s == "y" || s == "yes":
		return consentSend
	case strings.HasPrefix(s, "d"):
		return consentSuppress
	default:
		return consentSkip
	}
}

// crashPromptYesNo prints a [y/N] or [Y/n] prompt and returns the answer.
func crashPromptYesNo(prompt string, def bool) bool {
	suffix := " [y/N] "
	if def {
		suffix = " [Y/n] "
	}
	fmt.Fprint(os.Stderr, prompt+suffix)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return def
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return strings.EqualFold(line, "y") || strings.EqualFold(line, "yes")
}
```

Add `io` to the import list (used by `printTail`).

- [ ] **Step 4: Run tests**

Run: `cd go && gofmt -w -s ./internal/cli/commands && go test ./internal/cli/commands/ -run 'CrashReport|CrashConsent' && go build ./...`
Expected: PASS, build OK. (gRPC `Submit`/`Subscribe` now unused but still compile.)

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/crashflow.go go/internal/cli/commands/crashflow_test.go
git commit -m "feat(cli): 3-way crash consent and HTTP submission"
```

---

### Task 6: Remove the gRPC DiagnosticsService

Now that nothing calls the gRPC path, delete it.

**Files:**
- Delete: `Proto/cloud/diagnostics.proto`, `go/proto/gen/cloudpb/diagnostics.pb.go`, `go/proto/gen/cloudpb/diagnostics_grpc.pb.go`
- Modify: `go/scripts/generate-proto.sh` (remove the diagnostics line)
- Modify: `go/internal/cli/crashreport/submit.go` (delete gRPC `Submit`, `Subscribe`)
- Modify: `go/internal/cli/crashreport/bundle.go` (delete `Request()`, `reTrackingID`/`ValidTrackingID` if now unused — keep `ValidTrackingID` only if referenced)
- Modify: `go/internal/shared/platforminfo/platforminfo.go` (delete `Proto()`)
- Modify: `go/internal/cli/crashreport/submit_test.go` / `bundle_test.go` (delete tests referencing removed funcs)

- [ ] **Step 1: Find all references**

Run: `cd go && grep -rn "cloudpb\|\.Proto()\|\.Request()\|crashreport.Submit\b\|crashreport.Subscribe\|ValidTrackingID" internal/ cmd/ | grep -iv grpc.pb`
Expected: only the diagnostics-related lines listed above (plus any other cloudpb messages — if `cloudpb` is used by unrelated services, do NOT delete the whole package, only the diagnostics files).

- [ ] **Step 2: Delete files and references**

```bash
rm Proto/cloud/diagnostics.proto go/proto/gen/cloudpb/diagnostics.pb.go go/proto/gen/cloudpb/diagnostics_grpc.pb.go
```

Remove the diagnostics line from `go/scripts/generate-proto.sh`. In `submit.go` delete the gRPC `Submit` and `Subscribe` functions and the `cloudpb`/`protojson`/`grpc` imports and the old `writeLocalBundle` (superseded by `writeLocalBundleJSON`). In `bundle.go` delete `Request()` and the `cloudpb` import; keep `ValidTrackingID`/`reTrackingID` only if still referenced by a kept test, else delete. In `platforminfo.go` delete `Proto()` and the `cloudpb` import. Delete `TestSubmit*` tests that referenced the gRPC client (`bufconn`) and any `Request()`/`Proto()` tests.

- [ ] **Step 3: Build + vet + test**

Run: `cd go && gofmt -w -s ./... && go build ./... && go vet ./... && go test ./internal/cli/crashreport/ ./internal/shared/platforminfo/ ./internal/cli/commands/`
Expected: PASS. If build complains about an unused `cloudpb` import elsewhere, that file used a different message — revert its deletion; only diagnostics types should disappear.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "refactor(crashreport): remove gRPC DiagnosticsService (HTTP-only)"
```

---

### Task 7: Fix-status poll

**Files:**
- Create: `go/internal/cli/crashreport/status.go`
- Create: `go/internal/cli/commands/crash_status_check.go`
- Test: `go/internal/cli/crashreport/status_test.go`, `go/internal/cli/commands/crash_status_check_test.go`

**Interfaces:**
- Produces: `crashreport.FetchStatus(ctx context.Context, endpoint, anonymousID string) ([]FixedReport, error)` where `type FixedReport struct{ TrackingID, FixedInRelease string }`; `commands.dueCrashStatusCheck(cfg *config.Config) bool`; `commands.scheduleCrashStatusCheck(cfg *config.Config)`.

- [ ] **Step 1: Write the failing tests**

`status_test.go`:

```go
func TestFetchStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("anonymous_id") != "anon-1" {
			t.Errorf("missing anonymous_id: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"fixed":[{"tracking_id":"WDY-ABC123","fixed_in_release":"v1.4.0"}]}`))
	}))
	defer srv.Close()
	got, err := FetchStatus(context.Background(), srv.URL, "anon-1")
	if err != nil || len(got) != 1 || got[0].TrackingID != "WDY-ABC123" || got[0].FixedInRelease != "v1.4.0" {
		t.Fatalf("FetchStatus = %+v, err=%v", got, err)
	}
}
```

`crash_status_check_test.go`:

```go
func TestDueCrashStatusCheck(t *testing.T) {
	if dueCrashStatusCheck(&config.Config{}) {
		t.Error("no subscriptions → not due")
	}
	recent := &config.Config{CrashReport: &config.CrashReportConfig{
		SubscribedReports:    []string{"WDY-ABC123"},
		LastCrashStatusCheck: time.Now().UTC().Format(time.RFC3339),
	}}
	if dueCrashStatusCheck(recent) {
		t.Error("checked just now → not due")
	}
	stale := &config.Config{CrashReport: &config.CrashReportConfig{
		SubscribedReports:    []string{"WDY-ABC123"},
		LastCrashStatusCheck: time.Now().UTC().Add(-7 * time.Hour).Format(time.RFC3339),
	}}
	if !dueCrashStatusCheck(stale) {
		t.Error("stale + subscriptions → due")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd go && go test ./internal/cli/crashreport/ -run TestFetchStatus ./internal/cli/commands/ -run TestDueCrashStatusCheck`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

`crashreport/status.go`:

```go
package crashreport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// FixedReport is a tracking id whose crash was fixed in a given release.
type FixedReport struct {
	TrackingID     string `json:"tracking_id"`
	FixedInRelease string `json:"fixed_in_release"`
}

// FetchStatus asks the telemetry status endpoint which of this install's
// subscribed reports are now fixed. Best-effort; returns nil on any failure.
func FetchStatus(ctx context.Context, endpoint, anonymousID string) ([]FixedReport, error) {
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	u := endpoint + "?anonymous_id=" + url.QueryEscape(anonymousID)
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil
	}
	var out struct {
		Fixed []FixedReport `json:"fixed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Fixed, nil
}
```

`commands/crash_status_check.go` (mirror `update.go`; move fixed reports into `PendingFixNotices`):

```go
package commands

import (
	"context"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/analytics"
	"github.com/wendylabsinc/wendy/go/internal/cli/crashreport"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

const crashStatusCheckInterval = 6 * time.Hour

func dueCrashStatusCheck(cfg *config.Config) bool {
	if cfg.CrashReport == nil || len(cfg.CrashReport.SubscribedReports) == 0 {
		return false
	}
	last := cfg.CrashReport.LastCrashStatusCheck
	if last == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, last)
	if err != nil {
		return true
	}
	now := time.Now().UTC()
	if t.After(now) {
		return true
	}
	return now.Sub(t) >= crashStatusCheckInterval
}

func scheduleCrashStatusCheck(cfg *config.Config) {
	go func() {
		anonID, err := analytics.DistinctID()
		if err != nil {
			return
		}
		fixed, ferr := crashreport.FetchStatus(context.Background(), analytics.TelemetryBaseURL()+"/crashreports/status", anonID)
		cfg.CrashReport.LastCrashStatusCheck = time.Now().UTC().Format(time.RFC3339)
		if ferr == nil {
			applyFixedReports(cfg, fixed)
		}
		_ = config.Save(cfg)
	}()
}

// applyFixedReports moves fixed tracking ids from SubscribedReports into
// PendingFixNotices, de-duplicating against notices already pending.
func applyFixedReports(cfg *config.Config, fixed []crashreport.FixedReport) {
	if len(fixed) == 0 {
		return
	}
	pending := map[string]bool{}
	for _, n := range cfg.CrashReport.PendingFixNotices {
		pending[n.TrackingID] = true
	}
	remaining := cfg.CrashReport.SubscribedReports[:0]
	fixedSet := map[string]string{}
	for _, f := range fixed {
		fixedSet[f.TrackingID] = f.FixedInRelease
	}
	for _, id := range cfg.CrashReport.SubscribedReports {
		if rel, ok := fixedSet[id]; ok {
			if !pending[id] {
				cfg.CrashReport.PendingFixNotices = append(cfg.CrashReport.PendingFixNotices, config.FixNotice{TrackingID: id, FixedInRelease: rel})
			}
			continue
		}
		remaining = append(remaining, id)
	}
	cfg.CrashReport.SubscribedReports = remaining
}
```

- [ ] **Step 4: Run tests**

Run: `cd go && gofmt -w -s ./internal/cli/crashreport ./internal/cli/commands && go test ./internal/cli/crashreport/ ./internal/cli/commands/ && go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/crashreport/status.go go/internal/cli/crashreport/status_test.go go/internal/cli/commands/crash_status_check.go go/internal/cli/commands/crash_status_check_test.go
git commit -m "feat(cli): poll telemetry for fixed crash reports"
```

---

### Task 8: In-CLI fix notice + root wiring

**Files:**
- Create: `go/internal/cli/commands/crash_fix_notice.go`
- Modify: `go/internal/cli/commands/root.go` (PreRunE poll + PostRunE notice)
- Test: `go/internal/cli/commands/crash_fix_notice_test.go`

**Interfaces:**
- Produces: `func notifyCrashFix(cmd *cobra.Command)` — prints + clears `PendingFixNotices`, fires OS notification (Task 9 supplies `osnotify.Notify`; until then call a local stub added here and swapped in Task 9).
- Consumes: `dueCrashStatusCheck`, `scheduleCrashStatusCheck`.

- [ ] **Step 1: Write the failing test**

```go
func TestNotifyCrashFixPrintsAndClears(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = config.Save(&config.Config{CrashReport: &config.CrashReportConfig{
		PendingFixNotices: []config.FixNotice{{TrackingID: "WDY-ABC123", FixedInRelease: "v1.4.0"}},
	}})
	var buf bytes.Buffer
	cmd := &cobra.Command{Use: "wendy"}
	cmd.SetErr(&buf)
	notifyCrashFix(cmd)
	if !strings.Contains(buf.String(), "WDY-ABC123") || !strings.Contains(buf.String(), "v1.4.0") {
		t.Errorf("notice not printed: %q", buf.String())
	}
	loaded, _ := config.Load()
	if loaded.CrashReport != nil && len(loaded.CrashReport.PendingFixNotices) != 0 {
		t.Errorf("notices not cleared: %+v", loaded.CrashReport.PendingFixNotices)
	}
}
```

Imports: `bytes`, `strings`, `testing`, cobra, config.

- [ ] **Step 2: Run to verify failure**

Run: `cd go && go test ./internal/cli/commands/ -run TestNotifyCrashFixPrintsAndClears`
Expected: FAIL — `notifyCrashFix` undefined.

- [ ] **Step 3: Implement**

`crash_fix_notice.go`:

```go
package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/osnotify"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// notifyCrashFix surfaces any pending "your crash was fixed" notices recorded
// by the background status poll: one stderr line each plus a best-effort OS
// notification, then clears them. Best-effort; never errors.
func notifyCrashFix(cmd *cobra.Command) {
	cfg, err := config.Load()
	if err != nil || cfg.CrashReport == nil || len(cfg.CrashReport.PendingFixNotices) == 0 {
		return
	}
	for _, n := range cfg.CrashReport.PendingFixNotices {
		rel := n.FixedInRelease
		if rel == "" {
			rel = "a recent release"
		}
		cmd.PrintErrf("\n✓ A crash you reported (%s) is fixed in %s. Update the CLI to get the fix.\n", n.TrackingID, rel)
		osnotify.Notify("Wendy: crash fixed", fmt.Sprintf("%s fixed in %s", n.TrackingID, rel))
	}
	cfg.CrashReport.PendingFixNotices = nil
	_ = config.Save(cfg)
}
```

(Task 9 creates the `osnotify` package. To keep this task independently green, create a minimal `go/internal/cli/osnotify/osnotify.go` now with a no-op `func Notify(title, body string) {}` and flesh it out in Task 9.)

Wire into `root.go`:
- In `PersistentPreRunE`, after `dueCLIUpdateCheck` block:

```go
				if dueCrashStatusCheck(cfg) {
					scheduleCrashStatusCheck(cfg)
				}
```

- In `PersistentPostRunE`, after `maybePromptInstallCompletions(...)`:

```go
				notifyCrashFix(cmd)
```

- [ ] **Step 4: Run tests**

Run: `cd go && gofmt -w -s ./internal/cli/commands ./internal/cli/osnotify && go test ./internal/cli/commands/ && go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/crash_fix_notice.go go/internal/cli/commands/crash_fix_notice_test.go go/internal/cli/commands/root.go go/internal/cli/osnotify/osnotify.go
git commit -m "feat(cli): surface fixed-crash notices on next run"
```

---

### Task 9: Best-effort OS notification

**Files:**
- Modify: `go/internal/cli/osnotify/osnotify.go` (dispatch + injectable runner)
- Create: `go/internal/cli/osnotify/osnotify_darwin.go`, `_linux.go`, `_windows.go`
- Test: `go/internal/cli/osnotify/osnotify_test.go`

**Interfaces:**
- Produces: `func Notify(title, body string)`; internal `var runner = execRunner` with `type cmdRunner func(name string, args ...string) error` for test injection; `func lookPath(string) (string, error)` indirection.

- [ ] **Step 1: Write the failing test**

```go
func TestNotifyUsesRunnerWhenToolPresent(t *testing.T) {
	var gotName string
	var gotArgs []string
	origRun, origLook := runner, lookPath
	t.Cleanup(func() { runner, lookPath = origRun, origLook })
	lookPath = func(string) (string, error) { return "/usr/bin/tool", nil }
	runner = func(name string, args ...string) error { gotName = name; gotArgs = args; return nil }

	Notify("T", "B")
	if gotName == "" {
		t.Fatal("expected a notifier command to run")
	}
	joined := gotName + " " + strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "T") && !strings.Contains(joined, "B") {
		t.Errorf("title/body not passed: %q", joined)
	}
}

func TestNotifyNoopWhenToolAbsent(t *testing.T) {
	origRun, origLook := runner, lookPath
	t.Cleanup(func() { runner, lookPath = origRun, origLook })
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	called := false
	runner = func(string, ...string) error { called = true; return nil }
	Notify("T", "B") // must not panic; must not run anything
	if called {
		t.Error("runner should not be called when no tool is present")
	}
}
```

Imports: `errors`, `strings`, `testing`.

- [ ] **Step 2: Run to verify failure**

Run: `cd go && go test ./internal/cli/osnotify/`
Expected: FAIL — `runner`/`lookPath` undefined.

- [ ] **Step 3: Implement**

`osnotify.go`:

```go
// Package osnotify sends best-effort desktop notifications. Missing tooling is
// a silent no-op; it never errors and never blocks meaningfully.
package osnotify

import (
	"os/exec"
	"time"
)

type cmdRunner func(name string, args ...string) error

var (
	runner   cmdRunner = execRunner
	lookPath           = exec.LookPath
)

func execRunner(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Detach: don't block the CLI on the notifier.
	go func() { _ = cmd.Wait() }()
	_ = time.AfterFunc // keep import; see note
	return nil
}

// Notify shows a desktop notification if platform tooling is available.
func Notify(title, body string) {
	notify(title, body) // platform-specific
}
```

Remove the unused `time` import if not needed (the `time.AfterFunc` line is a placeholder — delete it and the import). `osnotify_darwin.go`:

```go
package osnotify

import "fmt"

func notify(title, body string) {
	if _, err := lookPath("osascript"); err == nil {
		script := fmt.Sprintf("display notification %q with title %q", body, title)
		_ = runner("osascript", "-e", script)
		return
	}
	if _, err := lookPath("terminal-notifier"); err == nil {
		_ = runner("terminal-notifier", "-title", title, "-message", body)
	}
}
```

`osnotify_linux.go`:

```go
package osnotify

func notify(title, body string) {
	if _, err := lookPath("notify-send"); err == nil {
		_ = runner("notify-send", title, body)
	}
}
```

`osnotify_windows.go`:

```go
package osnotify

import "fmt"

func notify(title, body string) {
	if _, err := lookPath("powershell"); err == nil {
		script := fmt.Sprintf(
			`[void][System.Reflection.Assembly]::LoadWithPartialName('System.Windows.Forms');`+
				`$n=New-Object System.Windows.Forms.NotifyIcon;$n.Icon=[System.Drawing.SystemIcons]::Information;`+
				`$n.Visible=$true;$n.ShowBalloonTip(5000,%q,%q,[System.Windows.Forms.ToolTipIcon]::Info)`,
			title, body)
		_ = runner("powershell", "-NoProfile", "-Command", script)
	}
}
```

- [ ] **Step 4: Run tests + cross-build**

Run: `cd go && gofmt -w -s ./internal/cli/osnotify && go test ./internal/cli/osnotify/ && GOOS=linux go build ./internal/cli/osnotify/ && GOOS=windows go build ./internal/cli/osnotify/ && GOOS=darwin go build ./internal/cli/osnotify/`
Expected: PASS, all three OS builds OK.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/osnotify/
git commit -m "feat(osnotify): best-effort cross-platform desktop notifications"
```

---

### Task 10: Docs

**Files:**
- Modify: `go/internal/cli/assets/docs/clients/wendy-cli/analytics.md`

- [ ] **Step 1: Update the crash-reports section**

Replace the "Availability" note added in PR #1228 and the "never requires an account" line to reflect the shipped model: capture works for all users over the anonymous telemetry channel (gated on analytics being enabled), 3-way consent (send / not now / don't ask again), fix-notification delivered in-CLI on next run + best-effort OS notification, disable via `WENDY_CRASHREPORT=false` or by choosing "don't ask again". Keep the "not yet active until the cloud endpoints ship" note. Remove any mention of macOS APNS and of gRPC tracking URLs that imply an account.

Concretely: change the sentence "Subscription is per-report and never requires creating an account." to "Fix notifications are keyed to your anonymous install id and never require an account or a cloud login." and add a bullet: "Choosing **don't ask again** permanently disables the crash-report prompt (stored in `~/.wendy/config.json`)."

- [ ] **Step 2: Verify no stale references**

Run: `grep -n "APNS\|gRPC\|tracking number\|never requires" go/internal/cli/assets/docs/clients/wendy-cli/analytics.md`
Expected: no APNS/gRPC references; the account line reads the new wording.

- [ ] **Step 3: Commit**

```bash
git add go/internal/cli/assets/docs/clients/wendy-cli/analytics.md
git commit -m "docs: describe anonymous crash reporting + CLI fix notifications"
```

---

## Self-Review

**Spec coverage:**
- Anonymous capture over telemetry → Tasks 1–3, 5. ✓
- 3-way consent + Suppressed → Tasks 4, 5. ✓
- Fix-notification poll → Task 7; in-CLI banner → Task 8; OS notification → Task 9. ✓
- Config block → Task 4. ✓
- gRPC removal → Task 6. ✓
- Preview-fidelity invariant → preserved in Task 5 (preview renders `info.Block()` + the same bundle fields the payload serializes). ✓
- Docs → Task 10. ✓
- Best-effort/never-alter-exit-code → enforced in Tasks 2, 5, 7, 8, 9. ✓

**Placeholder scan:** `buildOutputTail()` intentionally returns nil (documented follow-up from PR #1228, not new scope). The `time.AfterFunc` line in Task 9 Step 3 is explicitly flagged for deletion. No other placeholders.

**Type consistency:** `SubmitHTTP(ctx, endpoint, anonymousID, b, notifyOnFix)`, `FetchStatus(ctx, endpoint, anonymousID) []FixedReport`, `FixedReport{TrackingID, FixedInRelease}`, `config.FixNotice{TrackingID, FixedInRelease}`, `config.CrashReportConfig{Suppressed, SubscribedReports, LastCrashStatusCheck, PendingFixNotices}` used consistently across Tasks 2, 4, 5, 7, 8. `osnotify.Notify(title, body)` consistent Tasks 8–9.

**Note on the dormant gate:** the interactive prompt is not wired into `main.go` (matches PR #1228). Because `SubscribedReports` only populates via that prompt, the poll (Task 7) and notice (Task 8) are inert in production until the prompt is re-enabled and the cloud endpoints ship — safe to land wired.
