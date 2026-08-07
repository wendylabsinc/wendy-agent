# Auth-session picker + default org — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When multiple cloud auth sessions exist, replace the hard `pass --cloud-grpc` error with an interactive picker and a persisted default session, so everyday cloud commands run without prompting.

**Architecture:** A single pure resolver `config.ResolveAuth(cfg, cloudGRPC, pick)` implements the precedence `--cloud-grpc > single session > persisted default > injected picker > error`. The picker is a callback injected by the `commands` package (so `config` stays free of any TUI dependency); MCP and non-TTY callers pass `nil` and get a sentinel error. A new `Config.DefaultCloudGRPC` field stores the default; the picker's `d` key and a new `wendy auth use` / `wendy auth default` command pair set/show/clear it.

**Tech Stack:** Go, Cobra (CLI), Bubble Tea + the repo's `tui` picker, standard `testing`.

## Global Constraints

- Build tag on all `commands`/enroll files: `//go:build darwin || linux || windows` (copy verbatim at the top of any new file in `internal/cli/commands`).
- `config` package must NOT import `tui`, `commands`, or any TUI library — keep it pure (stdlib only). The picker is injected as a function value.
- `--cloud-grpc` always wins; never change behavior when the flag is set or when exactly one session exists. Existing scripted usage must keep working.
- Session identity is the `CloudGRPC` endpoint string (exact match). Org for display is `Certificates[0].OrganizationID`.
- Config persistence goes through `config.Load()` / `config.Save()` only (mode 0o600 is handled by `Save`).
- Interactivity is detected via the existing `isInteractiveTerminal()` (backed by the stubbable `isInteractiveTerminalFn`).

---

### Task 1: Config field, default helpers, and the `ResolveAuth` resolver

**Files:**
- Modify: `go/internal/shared/config/config.go`
- Create: `go/internal/shared/config/auth_resolve.go`
- Test: `go/internal/shared/config/auth_resolve_test.go`

**Interfaces:**
- Consumes: existing `Config`, `AuthConfig`, `CertificateInfo`, `Load`, `Save`.
- Produces (relied on by Tasks 2–5):
  - `Config.DefaultCloudGRPC string` (JSON `defaultCloudGRPC,omitempty`)
  - `func (c *Config) DefaultAuth() (*AuthConfig, bool)`
  - `type SessionPicker func(cfg *Config) (*AuthConfig, error)`
  - `var ErrMultipleSessions = errors.New("multiple auth sessions exist")`
  - `var ErrNotLoggedIn = errors.New("not logged in; run 'wendy auth login' first")`
  - `func ResolveAuth(cfg *Config, cloudGRPC string, pick SessionPicker) (*AuthConfig, error)`

- [ ] **Step 1: Add the config field**

In `go/internal/shared/config/config.go`, add the field to `Config` (right after `DefaultDevice`):

```go
	DefaultDevice      string           `json:"defaultDevice,omitempty"`
	// DefaultCloudGRPC names the auth session (by its gRPC endpoint) used when
	// several sessions exist and no --cloud-grpc flag is given. Empty means no
	// default; resolution then falls back to an interactive picker or an error.
	DefaultCloudGRPC   string           `json:"defaultCloudGRPC,omitempty"`
	LastCLIUpdateCheck string           `json:"lastCLIUpdateCheck,omitempty"` // RFC3339
```

- [ ] **Step 2: Write the failing test for the resolver and helpers**

Create `go/internal/shared/config/auth_resolve_test.go`:

```go
package config

import (
	"errors"
	"strings"
	"testing"
)

func twoSessions() *Config {
	return &Config{Auth: []AuthConfig{
		{CloudDashboard: "https://cloud.wendy.dev", CloudGRPC: "prod:443", Certificates: []CertificateInfo{{OrganizationID: 7}}},
		{CloudDashboard: "http://localhost:3000", CloudGRPC: "localhost:50051", Certificates: []CertificateInfo{{OrganizationID: 1}}},
	}}
}

func TestResolveAuthNotLoggedIn(t *testing.T) {
	if _, err := ResolveAuth(&Config{}, "", nil); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("want ErrNotLoggedIn, got %v", err)
	}
}

func TestResolveAuthFlagWins(t *testing.T) {
	cfg := twoSessions()
	cfg.DefaultCloudGRPC = "prod:443"
	auth, err := ResolveAuth(cfg, "localhost:50051", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth.CloudGRPC != "localhost:50051" {
		t.Fatalf("flag must win, got %s", auth.CloudGRPC)
	}
}

func TestResolveAuthFlagNoMatch(t *testing.T) {
	_, err := ResolveAuth(twoSessions(), "missing:443", nil)
	if err == nil || !strings.Contains(err.Error(), "no auth session for missing:443") {
		t.Fatalf("want no-session error, got %v", err)
	}
}

func TestResolveAuthSingleSession(t *testing.T) {
	cfg := &Config{Auth: []AuthConfig{{CloudGRPC: "prod:443", Certificates: []CertificateInfo{{OrganizationID: 7}}}}}
	auth, err := ResolveAuth(cfg, "", nil)
	if err != nil || auth.CloudGRPC != "prod:443" {
		t.Fatalf("single session should resolve, got %v / %v", auth, err)
	}
}

func TestResolveAuthSingleSessionNoCerts(t *testing.T) {
	cfg := &Config{Auth: []AuthConfig{{CloudGRPC: "prod:443"}}}
	if _, err := ResolveAuth(cfg, "", nil); err == nil || !strings.Contains(err.Error(), "no certificates") {
		t.Fatalf("want no-certificates error, got %v", err)
	}
}

func TestResolveAuthValidDefault(t *testing.T) {
	cfg := twoSessions()
	cfg.DefaultCloudGRPC = "localhost:50051"
	auth, err := ResolveAuth(cfg, "", nil)
	if err != nil || auth.CloudGRPC != "localhost:50051" {
		t.Fatalf("default should be used, got %v / %v", auth, err)
	}
}

func TestResolveAuthStaleDefaultFallsThrough(t *testing.T) {
	cfg := twoSessions()
	cfg.DefaultCloudGRPC = "gone:443"
	if _, err := ResolveAuth(cfg, "", nil); !errors.Is(err, ErrMultipleSessions) {
		t.Fatalf("stale default should fall through to ErrMultipleSessions, got %v", err)
	}
}

func TestResolveAuthMultipleNoPicker(t *testing.T) {
	err := func() error { _, e := ResolveAuth(twoSessions(), "", nil); return e }()
	if !errors.Is(err, ErrMultipleSessions) {
		t.Fatalf("want ErrMultipleSessions, got %v", err)
	}
	if !strings.Contains(err.Error(), "--cloud-grpc") {
		t.Fatalf("message must mention --cloud-grpc, got %v", err)
	}
}

func TestResolveAuthMultipleUsesPicker(t *testing.T) {
	cfg := twoSessions()
	called := false
	pick := func(c *Config) (*AuthConfig, error) { called = true; return &c.Auth[1], nil }
	auth, err := ResolveAuth(cfg, "", pick)
	if err != nil || !called || auth.CloudGRPC != "localhost:50051" {
		t.Fatalf("picker should be used, got %v / called=%v / %v", auth, called, err)
	}
}

func TestDefaultAuthLookup(t *testing.T) {
	cfg := twoSessions()
	if _, ok := cfg.DefaultAuth(); ok {
		t.Fatal("no default set should return ok=false")
	}
	cfg.DefaultCloudGRPC = "prod:443"
	a, ok := cfg.DefaultAuth()
	if !ok || a.CloudGRPC != "prod:443" {
		t.Fatalf("want prod session, got %v / %v", a, ok)
	}
	cfg.DefaultCloudGRPC = "gone:443"
	if _, ok := cfg.DefaultAuth(); ok {
		t.Fatal("stale default should return ok=false")
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `cd go && go test ./internal/shared/config/ -run TestResolveAuth -v`
Expected: FAIL — `ResolveAuth`, `ErrMultipleSessions`, `ErrNotLoggedIn`, `DefaultAuth` undefined.

- [ ] **Step 4: Implement the resolver and helpers**

Create `go/internal/shared/config/auth_resolve.go`:

```go
package config

import (
	"errors"
	"fmt"
)

// ErrNotLoggedIn is returned when no auth sessions are stored.
var ErrNotLoggedIn = errors.New("not logged in; run 'wendy auth login' first")

// ErrMultipleSessions wraps the resolver error raised when several sessions
// exist, no --cloud-grpc flag was given, no valid default is set, and no
// interactive picker is available. Callers may match it with errors.Is to
// substitute a surface-specific message (e.g. the MCP tool's cloud_grpc wording).
var ErrMultipleSessions = errors.New("multiple auth sessions exist")

// SessionPicker selects one session interactively. It is injected by callers
// that can show a TUI; non-interactive callers (MCP, non-TTY) pass nil.
type SessionPicker func(cfg *Config) (*AuthConfig, error)

// DefaultAuth resolves DefaultCloudGRPC to a stored session. ok is false when
// no default is set or the named session no longer exists (stale default).
func (c *Config) DefaultAuth() (*AuthConfig, bool) {
	if c == nil || c.DefaultCloudGRPC == "" {
		return nil, false
	}
	for i := range c.Auth {
		if c.Auth[i].CloudGRPC == c.DefaultCloudGRPC {
			return &c.Auth[i], true
		}
	}
	return nil, false
}

// ResolveAuth chooses the auth session to use. Precedence:
//  1. cloudGRPC flag set      -> exact endpoint match (error if none)
//  2. exactly one session     -> use it
//  3. valid persisted default -> use it
//  4. pick != nil             -> interactive picker
//  5. otherwise               -> ErrMultipleSessions
// The returned session is guaranteed to hold certificate material.
func ResolveAuth(cfg *Config, cloudGRPC string, pick SessionPicker) (*AuthConfig, error) {
	if cfg == nil || len(cfg.Auth) == 0 {
		return nil, ErrNotLoggedIn
	}
	if cloudGRPC != "" {
		for i := range cfg.Auth {
			if cfg.Auth[i].CloudGRPC == cloudGRPC {
				return authWithCerts(&cfg.Auth[i])
			}
		}
		return nil, fmt.Errorf("no auth session for %s; run 'wendy auth login --cloud-grpc %s' first", cloudGRPC, cloudGRPC)
	}
	if len(cfg.Auth) == 1 {
		return authWithCerts(&cfg.Auth[0])
	}
	if def, ok := cfg.DefaultAuth(); ok {
		return authWithCerts(def)
	}
	if pick != nil {
		return pick(cfg)
	}
	return nil, fmt.Errorf("%w; pass --cloud-grpc or run 'wendy auth use' to choose a default", ErrMultipleSessions)
}

// authWithCerts rejects sessions with no certificate material.
func authWithCerts(a *AuthConfig) (*AuthConfig, error) {
	if len(a.Certificates) == 0 {
		return nil, fmt.Errorf("auth session %s has no certificates; re-run 'wendy auth login'", a.CloudGRPC)
	}
	return a, nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd go && go test ./internal/shared/config/ -v`
Expected: PASS (all existing + new tests).

- [ ] **Step 6: Commit**

```bash
cd go && git add internal/shared/config/config.go internal/shared/config/auth_resolve.go internal/shared/config/auth_resolve_test.go
git commit -m "feat(config): add DefaultCloudGRPC and ResolveAuth session resolver"
```

---

### Task 2: CLI auth picker + rewire `pickAuthEntry`

**Files:**
- Create: `go/internal/cli/commands/auth_picker.go`
- Modify: `go/internal/cli/commands/device.go:638-661` (`pickAuthEntry`)
- Test: `go/internal/cli/commands/auth_picker_test.go`

**Interfaces:**
- Consumes: `config.ResolveAuth`, `config.SessionPicker`, `config.Config`, `tui.NewPickerWithTitle`, `tui.PickerItem`, `isInteractiveTerminal()`.
- Produces (relied on by Task 3):
  - `func authSessionLabel(a *config.AuthConfig) string` → `"org <N> — <endpoint>"`, or just `<endpoint>` when no certs.
  - `func authPickerItems(cfg *config.Config) []tui.PickerItem`
  - `var pickAuthSessionFn = pickAuthSession` (stubbable) with `func pickAuthSession(cfg *config.Config) (*config.AuthConfig, error)`

The 5 existing callers of `pickAuthEntry(cloudGRPC)` (`device.go:474`, `device.go:770`, `cloud.go:96`, `cloud_forward.go:67`, `cloud_tunnel.go:71`, `cloud_discover.go:37`) keep the same signature and are NOT edited.

- [ ] **Step 1: Write the failing test for the pure helpers**

Create `go/internal/cli/commands/auth_picker_test.go`:

```go
//go:build darwin || linux || windows

package commands

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestAuthSessionLabel(t *testing.T) {
	withOrg := &config.AuthConfig{CloudGRPC: "prod:443", Certificates: []config.CertificateInfo{{OrganizationID: 7}}}
	if got := authSessionLabel(withOrg); got != "org 7 — prod:443" {
		t.Fatalf("got %q", got)
	}
	noCerts := &config.AuthConfig{CloudGRPC: "local:50051"}
	if got := authSessionLabel(noCerts); got != "local:50051" {
		t.Fatalf("got %q", got)
	}
}

func TestAuthPickerItems(t *testing.T) {
	cfg := &config.Config{Auth: []config.AuthConfig{
		{CloudDashboard: "https://cloud.wendy.dev", CloudGRPC: "prod:443", Certificates: []config.CertificateInfo{{OrganizationID: 7}}},
		{CloudGRPC: "local:50051", Certificates: []config.CertificateInfo{{OrganizationID: 1}}},
	}}
	items := authPickerItems(cfg)
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].Name != "https://cloud.wendy.dev" {
		t.Errorf("item 0 name = %q", items[0].Name)
	}
	if !strings.Contains(items[0].Description, "org 7") {
		t.Errorf("item 0 desc = %q", items[0].Description)
	}
	if items[0].Value.(string) != "prod:443" || items[0].DedupKey != "prod:443" {
		t.Errorf("item 0 value/dedup wrong: %+v", items[0])
	}
	// Session with no dashboard falls back to its endpoint for the Name column.
	if items[1].Name != "local:50051" {
		t.Errorf("item 1 name = %q", items[1].Name)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run 'TestAuthSessionLabel|TestAuthPickerItems' -v`
Expected: FAIL — `authSessionLabel`, `authPickerItems` undefined.

- [ ] **Step 3: Implement the picker file**

Create `go/internal/cli/commands/auth_picker.go`:

```go
//go:build darwin || linux || windows

package commands

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// authSessionLabel renders a session for humans: "org <N> — <endpoint>", or
// just the endpoint when the session holds no certificate (and thus no org).
func authSessionLabel(a *config.AuthConfig) string {
	if len(a.Certificates) > 0 {
		return fmt.Sprintf("org %d — %s", a.Certificates[0].OrganizationID, a.CloudGRPC)
	}
	return a.CloudGRPC
}

// authPickerItems builds picker rows for every stored session. The Name column
// shows the dashboard URL (falling back to the gRPC endpoint), the Description
// shows "org N — endpoint", and both Value and DedupKey carry the endpoint so
// selection and default-marking key on the session identity.
func authPickerItems(cfg *config.Config) []tui.PickerItem {
	items := make([]tui.PickerItem, 0, len(cfg.Auth))
	for i := range cfg.Auth {
		a := &cfg.Auth[i]
		name := a.CloudDashboard
		if name == "" {
			name = a.CloudGRPC
		}
		items = append(items, tui.PickerItem{
			Name:        name,
			Description: authSessionLabel(a),
			DedupKey:    a.CloudGRPC,
			Value:       a.CloudGRPC,
		})
	}
	return items
}

// pickAuthSession shows the interactive session picker. 'd' marks the
// highlighted session as the persisted default (written immediately, mirroring
// the device picker), 'x' clears it, and Enter selects a session for this
// invocation only. Returns the selected session (cert-validated).
func pickAuthSession(cfg *config.Config) (*config.AuthConfig, error) {
	picker := tui.NewPickerWithTitle("Select the Wendy Cloud session to use")
	picker.DefaultKey = strings.ToLower(cfg.DefaultCloudGRPC)
	picker.OnSetDefault = func(item tui.PickerItem) {
		endpoint, _ := item.Value.(string)
		if endpoint == "" {
			return
		}
		if c, err := config.Load(); err == nil {
			c.DefaultCloudGRPC = endpoint
			_ = config.Save(c)
		}
	}
	picker.OnUnsetDefault = func() {
		if c, err := config.Load(); err == nil {
			c.DefaultCloudGRPC = ""
			_ = config.Save(c)
		}
	}

	p := tea.NewProgram(picker)
	go func() {
		p.Send(tui.PickerAddMsg{Items: authPickerItems(cfg)})
		p.Send(tui.PickerDoneMsg{})
	}()

	finalModel, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("picker: %w", err)
	}
	pm := finalModel.(tui.PickerModel)
	if pm.Cancelled() {
		return nil, ErrUserCancelled
	}
	if pm.Selected() == nil {
		return nil, fmt.Errorf("no session selected")
	}
	endpoint := pm.Selected().Value.(string)
	for i := range cfg.Auth {
		if cfg.Auth[i].CloudGRPC == endpoint {
			if len(cfg.Auth[i].Certificates) == 0 {
				return nil, fmt.Errorf("auth session %s has no certificates; re-run 'wendy auth login'", endpoint)
			}
			return &cfg.Auth[i], nil
		}
	}
	return nil, fmt.Errorf("selected session %s no longer exists", endpoint)
}

// pickAuthSessionFn is the indirection point so tests can stub the picker.
var pickAuthSessionFn = pickAuthSession
```

- [ ] **Step 4: Rewire `pickAuthEntry` in `device.go`**

Replace the body of `pickAuthEntry` (`go/internal/cli/commands/device.go:638-661`) with:

```go
func pickAuthEntry(cloudGRPC string) (*config.AuthConfig, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	// A default that points at a removed session is treated as unset; warn so
	// the user understands why the picker appeared instead of auto-selecting.
	if cloudGRPC == "" && cfg.DefaultCloudGRPC != "" {
		if _, ok := cfg.DefaultAuth(); !ok {
			fmt.Fprintf(os.Stderr, "warning: default session %s no longer exists; clear it with 'wendy auth default --clear'\n", cfg.DefaultCloudGRPC)
		}
	}
	var pick config.SessionPicker
	if isInteractiveTerminal() {
		pick = pickAuthSessionFn
	}
	return config.ResolveAuth(cfg, cloudGRPC, pick)
}
```

`os` and `fmt` are already imported in `device.go`. Confirm with `grep -n '"os"' go/internal/cli/commands/device.go`.

- [ ] **Step 5: Run tests + build**

Run: `cd go && go test ./internal/cli/commands/ -run 'TestAuthSessionLabel|TestAuthPickerItems' -v && go build ./...`
Expected: PASS, build succeeds.

- [ ] **Step 6: Commit**

```bash
cd go && git add internal/cli/commands/auth_picker.go internal/cli/commands/auth_picker_test.go internal/cli/commands/device.go
git commit -m "feat(cli): interactive auth-session picker with set-default key"
```

---

### Task 3: `wendy auth use` and `wendy auth default` commands

**Files:**
- Modify: `go/internal/cli/commands/auth.go` (add `newAuthUseCmd`, `newAuthDefaultCmd`, `matchAuthSelector`; register both in `newAuthCmd`)
- Test: `go/internal/cli/commands/auth_default_test.go`

**Interfaces:**
- Consumes: `authSessionLabel`, `pickAuthSessionFn`, `isInteractiveTerminal`, `config.Load/Save`, `config.AuthConfig`.
- Produces: `func matchAuthSelector(cfg *config.Config, selector string) (*config.AuthConfig, error)`.

- [ ] **Step 1: Write the failing test for `matchAuthSelector`**

Create `go/internal/cli/commands/auth_default_test.go`:

```go
//go:build darwin || linux || windows

package commands

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func selectorConfig() *config.Config {
	return &config.Config{Auth: []config.AuthConfig{
		{CloudDashboard: "https://cloud.wendy.dev", CloudGRPC: "prod.example.com:443", Certificates: []config.CertificateInfo{{OrganizationID: 7}}},
		{CloudDashboard: "http://localhost:3000", CloudGRPC: "localhost:50051", Certificates: []config.CertificateInfo{{OrganizationID: 1}}},
	}}
}

func TestMatchAuthSelectorByOrgID(t *testing.T) {
	a, err := matchAuthSelector(selectorConfig(), "7")
	if err != nil || a.CloudGRPC != "prod.example.com:443" {
		t.Fatalf("org match failed: %v / %v", a, err)
	}
}

func TestMatchAuthSelectorByEndpointSubstring(t *testing.T) {
	a, err := matchAuthSelector(selectorConfig(), "localhost")
	if err != nil || a.CloudGRPC != "localhost:50051" {
		t.Fatalf("substring match failed: %v / %v", a, err)
	}
}

func TestMatchAuthSelectorNoMatch(t *testing.T) {
	if _, err := matchAuthSelector(selectorConfig(), "nope"); err == nil || !strings.Contains(err.Error(), "no auth session matches") {
		t.Fatalf("want no-match error, got %v", err)
	}
}

func TestMatchAuthSelectorAmbiguous(t *testing.T) {
	cfg := selectorConfig()
	cfg.Auth[1].Certificates[0].OrganizationID = 7 // two sessions in org 7
	_, err := matchAuthSelector(cfg, "7")
	if err == nil || !strings.Contains(err.Error(), "matches multiple sessions") {
		t.Fatalf("want ambiguous error, got %v", err)
	}
	// The error lists each candidate so the user can disambiguate.
	if !strings.Contains(err.Error(), "prod.example.com:443") || !strings.Contains(err.Error(), "localhost:50051") {
		t.Fatalf("ambiguous error should list candidates, got %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestMatchAuthSelector -v`
Expected: FAIL — `matchAuthSelector` undefined.

- [ ] **Step 3: Implement `matchAuthSelector` and the two commands**

In `go/internal/cli/commands/auth.go`, add `"strconv"` to the import block, then add:

```go
// matchAuthSelector resolves a user-supplied selector to exactly one session.
// An all-digit selector matches a certificate OrganizationID; otherwise it is a
// case-insensitive substring of the gRPC endpoint or dashboard URL. It errors
// when nothing matches or when more than one session matches.
func matchAuthSelector(cfg *config.Config, selector string) (*config.AuthConfig, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return nil, fmt.Errorf("empty selector")
	}
	var matches []*config.AuthConfig
	if orgID, err := strconv.Atoi(selector); err == nil {
		for i := range cfg.Auth {
			for _, c := range cfg.Auth[i].Certificates {
				if c.OrganizationID == orgID {
					matches = append(matches, &cfg.Auth[i])
					break
				}
			}
		}
	} else {
		q := strings.ToLower(selector)
		for i := range cfg.Auth {
			if strings.Contains(strings.ToLower(cfg.Auth[i].CloudGRPC), q) ||
				strings.Contains(strings.ToLower(cfg.Auth[i].CloudDashboard), q) {
				matches = append(matches, &cfg.Auth[i])
			}
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no auth session matches %q", selector)
	case 1:
		return matches[0], nil
	default:
		var b strings.Builder
		for _, m := range matches {
			fmt.Fprintf(&b, "\n  - %s", authSessionLabel(m))
		}
		return nil, fmt.Errorf("selector %q matches multiple sessions:%s", selector, b.String())
	}
}

func newAuthUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use [selector]",
		Short: "Set the default Wendy Cloud session",
		Long:  "Sets the default session used when several exist and no --cloud-grpc flag is given. The selector is an organization ID or a substring of the gRPC endpoint or dashboard URL. With no selector in an interactive terminal, a picker is shown.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			if len(cfg.Auth) == 0 {
				return fmt.Errorf("not logged in; run 'wendy auth login' first")
			}

			var chosen *config.AuthConfig
			if len(args) == 1 {
				chosen, err = matchAuthSelector(cfg, args[0])
				if err != nil {
					return err
				}
			} else {
				if !isInteractiveTerminal() {
					return fmt.Errorf("provide a selector (org ID or endpoint substring) when not running interactively")
				}
				chosen, err = pickAuthSessionFn(cfg)
				if err != nil {
					return err
				}
			}

			cfg.DefaultCloudGRPC = chosen.CloudGRPC
			if err := config.Save(cfg); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}
			fmt.Println(tui.SuccessMessage(fmt.Sprintf("Default session set to %s.", authSessionLabel(chosen))))
			return nil
		},
	}
}

func newAuthDefaultCmd() *cobra.Command {
	var clear bool
	cmd := &cobra.Command{
		Use:   "default",
		Short: "Show or clear the default Wendy Cloud session",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			if clear {
				cfg.DefaultCloudGRPC = ""
				if err := config.Save(cfg); err != nil {
					return fmt.Errorf("saving config: %w", err)
				}
				fmt.Println(tui.SuccessMessage("Default session cleared."))
				return nil
			}
			if cfg.DefaultCloudGRPC == "" {
				fmt.Println("No default session set.")
				return nil
			}
			def, ok := cfg.DefaultAuth()
			if !ok {
				fmt.Println(tui.WarningMessage(fmt.Sprintf("Default session %s no longer exists; clearing it.", cfg.DefaultCloudGRPC)))
				cfg.DefaultCloudGRPC = ""
				if err := config.Save(cfg); err != nil {
					return fmt.Errorf("saving config: %w", err)
				}
				return nil
			}
			fmt.Printf("Default session: %s\n", authSessionLabel(def))
			return nil
		},
	}
	cmd.Flags().BoolVar(&clear, "clear", false, "Unset the default session")
	return cmd
}
```

Then register both in `newAuthCmd` (`go/internal/cli/commands/auth.go:38-43`):

```go
	cmd.AddCommand(
		newAuthLoginCmd(),
		newAuthLogoutCmd(),
		newAuthRefreshCertsCmd(),
		newAuthStatusCmd(),
		newAuthUseCmd(),
		newAuthDefaultCmd(),
	)
```

- [ ] **Step 4: Run the tests + build**

Run: `cd go && go test ./internal/cli/commands/ -run TestMatchAuthSelector -v && go build ./...`
Expected: PASS, build succeeds.

- [ ] **Step 5: Commit**

```bash
cd go && git add internal/cli/commands/auth.go internal/cli/commands/auth_default_test.go
git commit -m "feat(cli): add 'wendy auth use' and 'wendy auth default' commands"
```

---

### Task 4: MCP honors the default, errors otherwise

**Files:**
- Modify: `go/internal/cli/mcp/tools_cloud.go:392-414` (`cloudAuthEntry`)
- Test: `go/internal/cli/mcp/tools_cloud_test.go` (add cases)

**Interfaces:**
- Consumes: `config.ResolveAuth`, `config.ErrMultipleSessions`.

- [ ] **Step 1: Write the failing test**

Add to `go/internal/cli/mcp/tools_cloud_test.go`:

```go
func TestCloudAuthEntry_UsesDefaultWhenMultiple(t *testing.T) {
	srv := New(&config.Config{
		DefaultCloudGRPC: "two:123",
		Auth: []config.AuthConfig{
			{CloudGRPC: "one:123", Certificates: []config.CertificateInfo{{OrganizationID: 1}}},
			{CloudGRPC: "two:123", Certificates: []config.CertificateInfo{{OrganizationID: 2}}},
		},
	}, nil)
	auth, err := srv.cloudAuthEntry("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth.CloudGRPC != "two:123" {
		t.Fatalf("want default session two:123, got %s", auth.CloudGRPC)
	}
}

func TestCloudAuthEntry_ErrorsMentionsCloudGRPCParam(t *testing.T) {
	srv := New(&config.Config{
		Auth: []config.AuthConfig{
			{CloudGRPC: "one:123", Certificates: []config.CertificateInfo{{OrganizationID: 1}}},
			{CloudGRPC: "two:123", Certificates: []config.CertificateInfo{{OrganizationID: 2}}},
		},
	}, nil)
	_, err := srv.cloudAuthEntry("")
	if err == nil || !strings.Contains(err.Error(), "cloud_grpc") {
		t.Fatalf("want error mentioning cloud_grpc param, got %v", err)
	}
}
```

Ensure `strings` is imported in the test file (add to the import block if missing).

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd go && go test ./internal/cli/mcp/ -run TestCloudAuthEntry -v`
Expected: FAIL — default not honored / message lacks `cloud_grpc`.

- [ ] **Step 3: Rewrite `cloudAuthEntry`**

Replace `cloudAuthEntry` (`go/internal/cli/mcp/tools_cloud.go:392-414`) with:

```go
func (s *mcpServer) cloudAuthEntry(cloudGRPC string) (*config.AuthConfig, error) {
	// MCP is non-interactive: pass a nil picker so resolution stops at the
	// persisted default (or errors when several sessions remain ambiguous).
	auth, err := config.ResolveAuth(s.cfg, cloudGRPC, nil)
	if errors.Is(err, config.ErrMultipleSessions) {
		return nil, fmt.Errorf("multiple auth sessions exist; pass cloud_grpc to select one, or set a default with 'wendy auth use'")
	}
	return auth, err
}
```

Add `"errors"` to the import block of `tools_cloud.go` if not already present (check with `grep -n '"errors"' go/internal/cli/mcp/tools_cloud.go`). `config` and `fmt` are already imported.

- [ ] **Step 4: Run the tests**

Run: `cd go && go test ./internal/cli/mcp/ -run 'TestCloudAuthEntry|TestCloudDiscover' -v`
Expected: PASS (including the existing `TestCloudDiscover_RequiresCloudGRPCWhenMultipleAuthSessionsExist`, which has no default and still errors).

- [ ] **Step 5: Commit**

```bash
cd go && git add internal/cli/mcp/tools_cloud.go internal/cli/mcp/tools_cloud_test.go
git commit -m "feat(mcp): honor default cloud session, route through ResolveAuth"
```

---

### Task 5: Enrollment uses the shared resolver (keeps skip option)

**Files:**
- Modify: `go/internal/cli/commands/os_install_enroll.go:54-104` (`selectEnrollmentAuth`)
- Test: `go/internal/cli/commands/os_install_enroll_test.go` (add one case; all existing cases must still pass)

**Interfaces:**
- Consumes: `config.ResolveAuth`, `config.ErrMultipleSessions`, existing `promptEnrollmentSession`, `authEntryWithCerts`, `skipEnrollmentValue`.

Behavior: short-circuit (no sessions / flag / single / valid default / cert validation) is delegated to `config.ResolveAuth`. Only the multi-session-no-default-interactive case keeps the bespoke skip-capable picker (WDY-1476). When a valid default is set, enrollment auto-uses it (the user still gets the separate `confirmPreEnroll` yes/no before this is called — see `resolvePreEnrollment`).

- [ ] **Step 1: Write the failing test for default auto-use**

Add to `go/internal/cli/commands/os_install_enroll_test.go`:

```go
func TestSelectEnrollmentAuthUsesDefault(t *testing.T) {
	stubEnrollPrompts(t) // picker stub fails the test if invoked
	cfg := twoSessionConfig()
	cfg.DefaultCloudGRPC = "localhost:50051"
	auth, err := selectEnrollmentAuth(cfg, "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth == nil || auth.CloudGRPC != "localhost:50051" {
		t.Fatalf("default should be used without the picker, got %+v", auth)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestSelectEnrollmentAuthUsesDefault -v`
Expected: FAIL — current code shows the picker (stub calls `t.Fatal`) instead of using the default.

- [ ] **Step 3: Rewrite `selectEnrollmentAuth`**

Replace `selectEnrollmentAuth` (`go/internal/cli/commands/os_install_enroll.go:54-104`) with:

```go
func selectEnrollmentAuth(cfg *config.Config, cloudGRPC string, interactive bool) (*config.AuthConfig, error) {
	// Short-circuit for: not-logged-in, --cloud-grpc, single session, and a
	// valid persisted default. A nil picker makes the multi-session-no-default
	// case return ErrMultipleSessions so we can fall back to the skip-capable
	// picker below (WDY-1476).
	auth, err := config.ResolveAuth(cfg, cloudGRPC, nil)
	if err == nil {
		return auth, nil
	}
	if !errors.Is(err, config.ErrMultipleSessions) {
		return nil, err
	}
	if !interactive {
		return nil, err // message mentions --cloud-grpc
	}

	items := make([]tui.PickerItem, 0, len(cfg.Auth)+1)
	for i := range cfg.Auth {
		a := &cfg.Auth[i]
		name := a.CloudDashboard
		if name == "" {
			name = a.CloudGRPC
		}
		desc := a.CloudGRPC
		if len(a.Certificates) > 0 {
			desc = fmt.Sprintf("org %d — %s", a.Certificates[0].OrganizationID, a.CloudGRPC)
		}
		items = append(items, tui.PickerItem{Name: name, Description: desc, Value: strconv.Itoa(i)})
	}
	items = append(items, tui.PickerItem{
		Name:        "Skip enrollment",
		Description: "continue installing without enrolling this device",
		Value:       skipEnrollmentValue,
	})

	picked, err := promptEnrollmentSession(items)
	if err != nil {
		return nil, err
	}
	if picked == skipEnrollmentValue {
		return nil, nil
	}
	idx, convErr := strconv.Atoi(picked)
	if convErr != nil || idx < 0 || idx >= len(cfg.Auth) {
		return nil, fmt.Errorf("invalid session selection %q", picked)
	}
	return authEntryWithCerts(&cfg.Auth[idx])
}
```

Note: the `not logged in` / `no certificates` / `no auth session for ...` error strings now come from `config.ResolveAuth`, which produces the same substrings the existing tests assert (`not logged in`, `no certificates`, `no auth session for`, `--cloud-grpc`).

- [ ] **Step 4: Run the full enrollment test suite**

Run: `cd go && go test ./internal/cli/commands/ -run 'TestSelectEnrollmentAuth|TestResolvePreEnrollment' -v`
Expected: PASS — the new default test plus all 10 existing `TestSelectEnrollmentAuth*` / pre-enrollment cases.

- [ ] **Step 5: Commit**

```bash
cd go && git add internal/cli/commands/os_install_enroll.go internal/cli/commands/os_install_enroll_test.go
git commit -m "feat(cli): enrollment auto-uses default session via ResolveAuth"
```

---

### Task 6: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Build everything**

Run: `cd go && go build ./...`
Expected: success.

- [ ] **Step 2: Vet**

Run: `cd go && go vet ./internal/cli/... ./internal/shared/config/...`
Expected: no findings.

- [ ] **Step 3: Run the affected package test suites**

Run: `cd go && go test ./internal/shared/config/... ./internal/cli/commands/... ./internal/cli/mcp/...`
Expected: PASS.

- [ ] **Step 4: Manual smoke (documented, optional if no multi-session config available)**

With two sessions logged in:
- `wendy auth default` → "No default session set."
- `wendy auth use 7` → "Default session set to org 7 — …"
- `wendy auth default` → "Default session: org 7 — …"
- `wendy device enroll` (or any cloud cmd) → runs without prompting, using org 7.
- `wendy auth default --clear` → "Default session cleared."
- `wendy cloud discover` on a TTY → picker appears; `d` sets default, Enter selects.

- [ ] **Step 5: Commit any doc/cleanup if needed** (skip if nothing changed).

---

## Self-Review

**Spec coverage:**
- Shared resolution helper → Task 1 (`ResolveAuth`).
- Config `DefaultCloudGRPC` + helpers → Task 1.
- Picker with `d`/`✦`/Enter → Task 2.
- Call-site wiring (device/cloud/enroll/MCP) → Tasks 2, 4, 5 (the 5 CLI cloud commands reuse the unchanged `pickAuthEntry` signature).
- `wendy auth use` / `wendy auth default` → Task 3.
- Stale-default warning → Task 2 (`pickAuthEntry`) and Task 3 (`auth default`).
- MCP/non-TTY default-or-error → Task 4 + the `pick == nil` branch in Task 1.
- Testing → tests in Tasks 1–5; full suite in Task 6.

**Type consistency:** `config.SessionPicker = func(*config.Config) (*config.AuthConfig, error)` matches `pickAuthSession`'s signature and `pickAuthSessionFn`'s type. `ResolveAuth` and `DefaultAuth` signatures are used identically across Tasks 2–5. `authSessionLabel`/`authPickerItems`/`matchAuthSelector` signatures are consistent between definition (Tasks 2–3) and use.

**Placeholder scan:** none — every code step contains complete code.

**Scope:** single plan, one subsystem (auth-session selection). No decomposition needed.
