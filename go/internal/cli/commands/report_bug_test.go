package commands

import (
	"bytes"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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

func TestMaybeOpenReportBugURLGHPresentSucceeds(t *testing.T) {
	origLookPath, origOpenBrowser := lookPath, openBrowser
	t.Cleanup(func() { lookPath = origLookPath; openBrowser = origOpenBrowser })
	lookPath = func(string) (string, error) { return "/usr/local/bin/gh", nil }
	var openedURL string
	openBrowser = func(u string) error { openedURL = u; return nil }

	cmd := &cobra.Command{Use: "wendy"}
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)

	if got := maybeOpenReportBugURL(cmd, "https://example.com/issue"); got != reportBugOpened {
		t.Fatalf("expected reportBugOpened when gh is present and openBrowser succeeds, got %v", got)
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
	if got := maybeOpenReportBugURL(cmd, "https://example.com/issue"); got != reportBugNoGH {
		t.Fatalf("expected reportBugNoGH when gh is missing, got %v", got)
	}
}

func TestMaybeOpenReportBugURLOpenBrowserFails(t *testing.T) {
	origLookPath, origOpenBrowser := lookPath, openBrowser
	t.Cleanup(func() { lookPath = origLookPath; openBrowser = origOpenBrowser })
	lookPath = func(string) (string, error) { return "/usr/local/bin/gh", nil }
	openBrowser = func(string) error { return fmt.Errorf("no display") }

	cmd := &cobra.Command{Use: "wendy"}
	if got := maybeOpenReportBugURL(cmd, "https://example.com/issue"); got != reportBugOpenFailed {
		t.Fatalf("expected reportBugOpenFailed when openBrowser fails, got %v", got)
	}
}

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

func TestReportBugCmdGHPresentButOpenBrowserFails(t *testing.T) {
	origLookPath, origOpenBrowser := lookPath, openBrowser
	t.Cleanup(func() { lookPath = origLookPath; openBrowser = origOpenBrowser })
	lookPath = func(string) (string, error) { return "/usr/local/bin/gh", nil }
	openBrowser = func(string) error { return fmt.Errorf("no display") }

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
	if !strings.Contains(out, "Could not open the browser automatically") {
		t.Errorf("output = %q, want the open-failed fallback message", out)
	}
	if strings.Contains(out, "`gh` CLI not found") {
		t.Errorf("output = %q, must not claim gh is missing when it is present", out)
	}
	if !strings.Contains(out, "issues/new?") {
		t.Errorf("output = %q, want the fallback URL", out)
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
