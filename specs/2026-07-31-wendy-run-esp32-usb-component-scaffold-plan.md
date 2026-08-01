# Wendy Run ESP-IDF Component Scaffold Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When `wendy run` is invoked in an existing bare ESP-IDF project (no
`wendy.json` yet) with a USB-connected ESP32 detected as the run target,
propose adding selected Wendy Lite ESP-IDF components (as an ESP-IDF Component
Manager git dependency) and generate a minimal `wendy.json` so the project
becomes runnable via the existing `wendy run` path immediately afterward.

**Architecture:** All new code lives in one new file,
`go/internal/cli/commands/wendylite_scaffold.go` (package `commands`), plus a
~15-line edit to the existing `cfgMissing` branch in
`go/internal/cli/commands/run.go`. No new packages, no schema changes, no new
external dependencies — `gopkg.in/yaml.v3` and the existing `tui.ConfirmDefaultYes`
/ `tui.RunChecklist` primitives are reused. Build routing
(`detectProjectType` → `"esp-idf"` → `buildEspIdf`) is untouched; this plan
only adds the missing "no `wendy.json` yet" bootstrap step ahead of it.

**Tech Stack:** Go, `gopkg.in/yaml.v3` (already a dependency), Bubble Tea /
`go/internal/cli/tui` (already a dependency), standard library
`encoding/json`, `os`, `path/filepath`.

## Global Constraints

- Scope is existing bare ESP-IDF projects only — do not scaffold a brand-new
  project from an empty directory (that's explicitly out of scope).
- Install mechanism is the ESP-IDF Component Manager (`idf_component.yml` git
  dependency) only — no vendoring/cloning, no git submodules, no new fetch
  code.
- WASM-only components (`wendy_wasm`, `wendy_hal_export`, `wendy_wasi_shim`,
  `wendy_safety`, `wendy_callback`) must never appear in the offered checklist.
- Component dependency git URL is `https://github.com/wendylabsinc/wendy-lite.git`
  on branch `main`, path `components/<name>` per component — matching the
  existing precedent in `go/internal/cli/commands/init_cmd.go:1610`.
- `wendy.json` fields for this flow: `Platform: appconfig.PlatformWendyLite`
  (`"wendy-lite"`), `Language: "c"` (free-form field, not validated/routed on
  anywhere in the codebase today).
- `idf_component.yml` merges must be non-destructive: preserve any existing
  unrelated content/dependencies in the file, and be idempotent on re-run.
- No new go.mod dependencies — `gopkg.in/yaml.v3` is already vendored
  (see `go/internal/cli/commands/compose.go` for existing `yaml.Node` usage
  precedent).
- All test commands below assume the working directory is the repo root
  (where `go.mod` lives); source lives under `go/internal/...` but the module
  root is the repo root, so import paths are
  `github.com/wendylabsinc/wendy/go/internal/...`.

---

### Task 1: Trigger detection — `shouldOfferWendyLiteESPIDFScaffold`

**Files:**
- Create: `go/internal/cli/commands/wendylite_scaffold.go`
- Test: `go/internal/cli/commands/wendylite_scaffold_test.go`

**Interfaces:**
- Consumes: `SelectedDevice` (`go/internal/cli/commands/helpers.go:723`, fields
  `External *models.ExternalDevice`, `Provider providers.DeviceProvider`),
  `providers.DeviceProvider` interface (`go/internal/cli/providers/provider.go`,
  method `Key() string`), `models.ExternalDevice.ConnectionType() string`
  (`go/internal/shared/models/external_device.go:36`, returns `"USB"`/`"LAN"`/`"BLE"`/`""`).
- Produces: `func shouldOfferWendyLiteESPIDFScaffold(cfgMissing bool, projectType string, target *SelectedDevice) bool`
  — used by Task 5.

- [ ] **Step 1: Write the failing tests**

Create `go/internal/cli/commands/wendylite_scaffold_test.go`:

```go
package commands

import (
	"context"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/providers"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// fakeDeviceProvider is a minimal providers.DeviceProvider test double whose
// Key() is configurable; every other method is a no-op stub.
type fakeDeviceProvider struct{ key string }

func (f fakeDeviceProvider) Key() string         { return f.key }
func (f fakeDeviceProvider) DisplayName() string { return "" }
func (f fakeDeviceProvider) IsAvailable(ctx context.Context) bool         { return true }
func (f fakeDeviceProvider) CheckRequirements(ctx context.Context) error  { return nil }
func (f fakeDeviceProvider) DiscoverDevices(ctx context.Context) ([]models.ExternalDevice, error) {
	return nil, nil
}
func (f fakeDeviceProvider) SupportedBuildTypes() []string  { return nil }
func (f fakeDeviceProvider) CanBuild(projectPath string) bool { return false }
func (f fakeDeviceProvider) Build(ctx context.Context, device models.ExternalDevice, projectPath, projectType, product string, debug bool) (*providers.BuiltApp, error) {
	return nil, nil
}
func (f fakeDeviceProvider) Run(ctx context.Context, app *providers.BuiltApp, detach bool, output chan<- providers.RunOutput) error {
	return nil
}
func (f fakeDeviceProvider) Stop(ctx context.Context, app *providers.BuiltApp) error { return nil }
func (f fakeDeviceProvider) GetDeviceInfo(ctx context.Context, device models.ExternalDevice) (*providers.ProviderDeviceInfo, error) {
	return nil, nil
}

func TestShouldOfferWendyLiteESPIDFScaffold(t *testing.T) {
	usbWendyLite := &SelectedDevice{
		External: &models.ExternalDevice{ConnectionInfo: map[string]string{"type": "USB"}},
		Provider: fakeDeviceProvider{key: "wendy-lite"},
	}
	lanWendyLite := &SelectedDevice{
		External: &models.ExternalDevice{ConnectionInfo: map[string]string{"type": "LAN"}},
		Provider: fakeDeviceProvider{key: "wendy-lite"},
	}
	usbOtherProvider := &SelectedDevice{
		External: &models.ExternalDevice{ConnectionInfo: map[string]string{"type": "USB"}},
		Provider: fakeDeviceProvider{key: "docker"},
	}

	tests := []struct {
		name        string
		cfgMissing  bool
		projectType string
		target      *SelectedDevice
		want        bool
	}{
		{"all conditions met", true, "esp-idf", usbWendyLite, true},
		{"wendy.json already present", false, "esp-idf", usbWendyLite, false},
		{"not an esp-idf project shape", true, "docker", usbWendyLite, false},
		{"wendy-lite over LAN, not USB", true, "esp-idf", lanWendyLite, false},
		{"USB but not the wendy-lite provider", true, "esp-idf", usbOtherProvider, false},
		{"nil target", true, "esp-idf", nil, false},
		{"target with no External/Provider (agent path)", true, "esp-idf", &SelectedDevice{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldOfferWendyLiteESPIDFScaffold(tt.cfgMissing, tt.projectType, tt.target)
			if got != tt.want {
				t.Errorf("shouldOfferWendyLiteESPIDFScaffold(%v, %q, ...) = %v, want %v", tt.cfgMissing, tt.projectType, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/cli/commands/... -run TestShouldOfferWendyLiteESPIDFScaffold -v`
Expected: FAIL — `shouldOfferWendyLiteESPIDFScaffold` and `fakeDeviceProvider` compile errors (undefined).

- [ ] **Step 3: Write minimal implementation**

Create `go/internal/cli/commands/wendylite_scaffold.go`:

```go
package commands

// shouldOfferWendyLiteESPIDFScaffold reports whether wendy run should offer to
// scaffold Wendy Lite ESP-IDF components + a wendy.json for the current
// project. True only when wendy.json is missing, the project directory looks
// like an ESP-IDF project ("esp-idf" projectType, per detectProjectType), and
// the already-resolved run target is a USB-connected wendy-lite provider
// device.
func shouldOfferWendyLiteESPIDFScaffold(cfgMissing bool, projectType string, target *SelectedDevice) bool {
	if !cfgMissing || projectType != "esp-idf" || target == nil {
		return false
	}
	if target.External == nil || target.Provider == nil {
		return false
	}
	if target.Provider.Key() != "wendy-lite" {
		return false
	}
	return target.External.ConnectionType() == "USB"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./go/internal/cli/commands/... -run TestShouldOfferWendyLiteESPIDFScaffold -v`
Expected: PASS (all 7 subtests).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/wendylite_scaffold.go go/internal/cli/commands/wendylite_scaffold_test.go
git commit -m "Add trigger detection for wendy-lite ESP-IDF scaffold offer"
```

---

### Task 2: Merge wendy-lite components into `idf_component.yml`

**Files:**
- Modify: `go/internal/cli/commands/wendylite_scaffold.go`
- Modify: `go/internal/cli/commands/wendylite_scaffold_test.go`

**Interfaces:**
- Consumes: `gopkg.in/yaml.v3` (`yaml.Node`, `yaml.Unmarshal`, `yaml.Marshal`),
  already a repo dependency (see `go/internal/cli/commands/compose.go` for
  existing `yaml.Node` usage).
- Produces: `func mergeIdfComponentDependencies(manifestPath string, components []string) error`
  — used by Task 4. Writes/updates a `dependencies:` mapping keyed by
  component name, each value `{git: "https://github.com/wendylabsinc/wendy-lite.git", path: "components/<name>"}`.

- [ ] **Step 1: Write the failing tests**

Go requires a single, consolidated `import (...)` block at the top of the
file — update the one at the top of
`go/internal/cli/commands/wendylite_scaffold_test.go` (from Task 1) to:

```go
import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/providers"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	"gopkg.in/yaml.v3"
)
```

Then append the following test functions after `TestShouldOfferWendyLiteESPIDFScaffold`:

```go
func TestMergeIdfComponentDependencies_CreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "main", "idf_component.yml")

	if err := mergeIdfComponentDependencies(manifestPath, []string{"wendy_hal", "wendy_usb"}); err != nil {
		t.Fatalf("mergeIdfComponentDependencies: %v", err)
	}

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}

	var parsed struct {
		Dependencies map[string]struct {
			Git  string `yaml:"git"`
			Path string `yaml:"path"`
		} `yaml:"dependencies"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parsing written manifest: %v", err)
	}

	for _, name := range []string{"wendy_hal", "wendy_usb"} {
		dep, ok := parsed.Dependencies[name]
		if !ok {
			t.Fatalf("expected dependency %q, got %+v", name, parsed.Dependencies)
		}
		if dep.Git != "https://github.com/wendylabsinc/wendy-lite.git" {
			t.Errorf("dep %q git = %q, want the wendy-lite repo URL", name, dep.Git)
		}
		if dep.Path != "components/"+name {
			t.Errorf("dep %q path = %q, want %q", name, dep.Path, "components/"+name)
		}
	}
}

func TestMergeIdfComponentDependencies_PreservesExistingContent(t *testing.T) {
	dir := t.TempDir()
	mainDir := filepath.Join(dir, "main")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(mainDir, "idf_component.yml")
	existing := "dependencies:\n  espressif/led_strip:\n    version: \"^2.0.0\"\n"
	if err := os.WriteFile(manifestPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := mergeIdfComponentDependencies(manifestPath, []string{"wendy_hal"}); err != nil {
		t.Fatalf("mergeIdfComponentDependencies: %v", err)
	}

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Dependencies map[string]map[string]string `yaml:"dependencies"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parsing written manifest: %v", err)
	}
	if parsed.Dependencies["espressif/led_strip"]["version"] != "^2.0.0" {
		t.Errorf("existing dependency was clobbered: %+v", parsed.Dependencies)
	}
	if parsed.Dependencies["wendy_hal"]["git"] != "https://github.com/wendylabsinc/wendy-lite.git" {
		t.Errorf("wendy_hal dependency not added: %+v", parsed.Dependencies)
	}
}

func TestMergeIdfComponentDependencies_IdempotentReRun(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "main", "idf_component.yml")

	if err := mergeIdfComponentDependencies(manifestPath, []string{"wendy_hal"}); err != nil {
		t.Fatalf("first merge: %v", err)
	}
	first, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := mergeIdfComponentDependencies(manifestPath, []string{"wendy_hal"}); err != nil {
		t.Fatalf("second merge: %v", err)
	}
	second, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	if string(first) != string(second) {
		t.Errorf("re-running with the same selection changed the file:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestMergeIdfComponentDependencies_MalformedYAMLErrors(t *testing.T) {
	dir := t.TempDir()
	mainDir := filepath.Join(dir, "main")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(mainDir, "idf_component.yml")
	if err := os.WriteFile(manifestPath, []byte("not: valid: yaml: [broken"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := mergeIdfComponentDependencies(manifestPath, []string{"wendy_hal"}); err == nil {
		t.Fatal("expected an error for malformed existing YAML, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/cli/commands/... -run TestMergeIdfComponentDependencies -v`
Expected: FAIL — `mergeIdfComponentDependencies` undefined.

- [ ] **Step 3: Write minimal implementation**

Add an `import (...)` block at the top of
`go/internal/cli/commands/wendylite_scaffold.go` (Task 1 left this file with
no imports, so this is the file's first import block):

```go
import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)
```

Then append the following after `shouldOfferWendyLiteESPIDFScaffold`:

```go
const wendyLiteRepoURL = "https://github.com/wendylabsinc/wendy-lite.git"

// mergeIdfComponentDependencies adds or updates one ESP-IDF Component Manager
// git dependency per name in components, pointing at the matching
// subdirectory of the wendy-lite repo, in the idf_component.yml at
// manifestPath. Existing unrelated content is preserved; re-running with the
// same components is a no-op.
func mergeIdfComponentDependencies(manifestPath string, components []string) error {
	var doc yaml.Node
	data, err := os.ReadFile(manifestPath)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parsing %s: %w", manifestPath, err)
		}
	case os.IsNotExist(err):
		doc = yaml.Node{
			Kind:    yaml.DocumentNode,
			Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}},
		}
	default:
		return fmt.Errorf("reading %s: %w", manifestPath, err)
	}

	if len(doc.Content) == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: expected a YAML mapping at the document root", manifestPath)
	}

	deps := findOrAppendMappingKey(root, "dependencies")

	for _, name := range components {
		dep := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		dep.Content = append(dep.Content,
			scalarNode("git"), scalarNode(wendyLiteRepoURL),
			scalarNode("path"), scalarNode("components/"+name),
		)
		setMappingKey(deps, name, dep)
	}

	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(manifestPath), err)
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", manifestPath, err)
	}
	if err := os.WriteFile(manifestPath, out, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", manifestPath, err)
	}
	return nil
}

func scalarNode(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

// findOrAppendMappingKey returns the value node for key in mapping, creating
// an empty mapping node under that key if it doesn't already exist.
func findOrAppendMappingKey(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	value := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapping.Content = append(mapping.Content, scalarNode(key), value)
	return value
}

// setMappingKey sets key to value in mapping, overwriting any existing entry
// with the same key in place (preserving the order of unrelated keys).
func setMappingKey(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, scalarNode(key), value)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./go/internal/cli/commands/... -run TestMergeIdfComponentDependencies -v`
Expected: PASS (all 4 subtests/functions).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/wendylite_scaffold.go go/internal/cli/commands/wendylite_scaffold_test.go
git commit -m "Add idf_component.yml merge for wendy-lite component dependencies"
```

---

### Task 3: Generate `wendy.json` for the scaffolded project

**Files:**
- Modify: `go/internal/cli/commands/wendylite_scaffold.go`
- Modify: `go/internal/cli/commands/wendylite_scaffold_test.go`

**Interfaces:**
- Consumes: `appconfig.AppConfig` struct and `appconfig.PlatformWendyLite`
  constant (`go/internal/shared/appconfig/appconfig.go:116,225-260`).
- Produces: `func newWendyLiteESPIDFAppConfig(appID string) *appconfig.AppConfig`
  and `func writeWendyLiteESPIDFAppConfig(cwd string) error` — used by Task 4.

- [ ] **Step 1: Write the failing tests**

Update the consolidated `import (...)` block at the top of
`go/internal/cli/commands/wendylite_scaffold_test.go` to:

```go
import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/providers"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	"gopkg.in/yaml.v3"
)
```

Then append the following test functions after the Task 2 tests:

```go
func TestNewWendyLiteESPIDFAppConfig(t *testing.T) {
	cfg := newWendyLiteESPIDFAppConfig("my-esp32-app")

	if cfg.AppID != "my-esp32-app" {
		t.Errorf("AppID = %q, want %q", cfg.AppID, "my-esp32-app")
	}
	if cfg.Platform != appconfig.PlatformWendyLite {
		t.Errorf("Platform = %q, want %q", cfg.Platform, appconfig.PlatformWendyLite)
	}
	if cfg.Version == "" {
		t.Error("Version must not be empty")
	}
}

func TestWriteWendyLiteESPIDFAppConfig(t *testing.T) {
	dir := t.TempDir()

	if err := writeWendyLiteESPIDFAppConfig(dir); err != nil {
		t.Fatalf("writeWendyLiteESPIDFAppConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "wendy.json"))
	if err != nil {
		t.Fatalf("reading wendy.json: %v", err)
	}
	var cfg appconfig.AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parsing wendy.json: %v", err)
	}
	if cfg.Platform != appconfig.PlatformWendyLite {
		t.Errorf("Platform = %q, want %q", cfg.Platform, appconfig.PlatformWendyLite)
	}
	if cfg.AppID != filepath.Base(dir) {
		t.Errorf("AppID = %q, want directory base name %q", cfg.AppID, filepath.Base(dir))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/cli/commands/... -run 'TestNewWendyLiteESPIDFAppConfig|TestWriteWendyLiteESPIDFAppConfig' -v`
Expected: FAIL — both functions undefined.

- [ ] **Step 3: Write minimal implementation**

Update the `import (...)` block at the top of
`go/internal/cli/commands/wendylite_scaffold.go` to:

```go
import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"gopkg.in/yaml.v3"
)
```

Then append the following after the Task 2 functions:

```go
// newWendyLiteESPIDFAppConfig builds the minimal wendy.json contents for a
// scaffolded native ESP-IDF wendy-lite project.
func newWendyLiteESPIDFAppConfig(appID string) *appconfig.AppConfig {
	return &appconfig.AppConfig{
		AppID:    appID,
		Version:  "0.1.0",
		Platform: appconfig.PlatformWendyLite,
		Language: "c",
	}
}

// writeWendyLiteESPIDFAppConfig writes a scaffolded wendy.json into cwd,
// deriving the app ID from the directory name (matching ensureAppConfig's
// existing convention in helpers.go).
func writeWendyLiteESPIDFAppConfig(cwd string) error {
	cfgPath := filepath.Join(cwd, "wendy.json")
	dirName := filepath.Base(cwd)

	data, err := json.MarshalIndent(newWendyLiteESPIDFAppConfig(dirName), "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		return fmt.Errorf("writing wendy.json: %w", err)
	}
	fmt.Printf("Created wendy.json for %s\n", dirName)
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./go/internal/cli/commands/... -run 'TestNewWendyLiteESPIDFAppConfig|TestWriteWendyLiteESPIDFAppConfig' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/wendylite_scaffold.go go/internal/cli/commands/wendylite_scaffold_test.go
git commit -m "Add wendy.json generation for scaffolded wendy-lite ESP-IDF projects"
```

---

### Task 4: Interactive confirm + checklist glue

**Files:**
- Modify: `go/internal/cli/commands/wendylite_scaffold.go`
- Modify: `go/internal/cli/commands/wendylite_scaffold_test.go`

**Interfaces:**
- Consumes: `confirmFn` (package var, `go/internal/cli/commands/helpers.go:340`,
  `func(question string) bool`, already stubbable in tests — see
  `go/internal/cli/commands/docker_test.go:302` for the pattern), `tui.ChecklistItem`
  / `tui.RunChecklist` (`go/internal/cli/tui/checklist.go:12,215`),
  `mergeIdfComponentDependencies` (Task 2), `writeWendyLiteESPIDFAppConfig`
  (Task 3).
- Produces: `func promptAndScaffoldWendyLiteESPIDF(cwd string) (bool, error)`
  — used by Task 5. Returns `(true, nil)` iff the user confirmed and
  `wendy.json` was written; `(false, nil)` iff the user declined; `(false, err)`
  on any failure (including checklist cancellation).

- [ ] **Step 1: Write the failing tests**

Update the consolidated `import (...)` block at the top of
`go/internal/cli/commands/wendylite_scaffold_test.go` to:

```go
import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/providers"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	"gopkg.in/yaml.v3"
)
```

Then append the following test functions after the Task 3 tests:

```go
func TestPromptAndScaffoldWendyLiteESPIDF_Declined(t *testing.T) {
	origConfirm := confirmFn
	defer func() { confirmFn = origConfirm }()
	confirmFn = func(string) bool { return false }

	origChecklist := wendyLiteComponentChecklistFn
	defer func() { wendyLiteComponentChecklistFn = origChecklist }()
	wendyLiteComponentChecklistFn = func([]tui.ChecklistItem) ([]tui.ChecklistItem, error) {
		t.Fatal("must not prompt checklist when the initial confirm is declined")
		return nil, nil
	}

	dir := t.TempDir()
	scaffolded, err := promptAndScaffoldWendyLiteESPIDF(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scaffolded {
		t.Error("expected scaffolded=false when confirm is declined")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "wendy.json")); statErr == nil {
		t.Error("wendy.json must not be written when confirm is declined")
	}
}

func TestPromptAndScaffoldWendyLiteESPIDF_AllComponentsSelected(t *testing.T) {
	origConfirm := confirmFn
	defer func() { confirmFn = origConfirm }()
	confirmFn = func(string) bool { return true }

	origChecklist := wendyLiteComponentChecklistFn
	defer func() { wendyLiteComponentChecklistFn = origChecklist }()
	wendyLiteComponentChecklistFn = func(items []tui.ChecklistItem) ([]tui.ChecklistItem, error) {
		return items, nil // simulate the user keeping every pre-selected item
	}

	dir := t.TempDir()
	scaffolded, err := promptAndScaffoldWendyLiteESPIDF(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !scaffolded {
		t.Fatal("expected scaffolded=true")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "wendy.json")); statErr != nil {
		t.Errorf("expected wendy.json to be written: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "main", "idf_component.yml")); statErr != nil {
		t.Errorf("expected main/idf_component.yml to be written: %v", statErr)
	}
}

func TestPromptAndScaffoldWendyLiteESPIDF_NoComponentsSelected(t *testing.T) {
	origConfirm := confirmFn
	defer func() { confirmFn = origConfirm }()
	confirmFn = func(string) bool { return true }

	origChecklist := wendyLiteComponentChecklistFn
	defer func() { wendyLiteComponentChecklistFn = origChecklist }()
	wendyLiteComponentChecklistFn = func([]tui.ChecklistItem) ([]tui.ChecklistItem, error) {
		return nil, nil // simulate the user unchecking every item
	}

	dir := t.TempDir()
	scaffolded, err := promptAndScaffoldWendyLiteESPIDF(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !scaffolded {
		t.Fatal("expected scaffolded=true even with zero components selected")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "wendy.json")); statErr != nil {
		t.Errorf("expected wendy.json to still be written: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "main", "idf_component.yml")); statErr == nil {
		t.Error("idf_component.yml must not be written when zero components are selected")
	}
}

func TestPromptAndScaffoldWendyLiteESPIDF_ChecklistCancelled(t *testing.T) {
	origConfirm := confirmFn
	defer func() { confirmFn = origConfirm }()
	confirmFn = func(string) bool { return true }

	origChecklist := wendyLiteComponentChecklistFn
	defer func() { wendyLiteComponentChecklistFn = origChecklist }()
	wendyLiteComponentChecklistFn = func([]tui.ChecklistItem) ([]tui.ChecklistItem, error) {
		return nil, tui.ErrCancelled
	}

	dir := t.TempDir()
	scaffolded, err := promptAndScaffoldWendyLiteESPIDF(dir)
	if !errors.Is(err, tui.ErrCancelled) {
		t.Fatalf("expected tui.ErrCancelled, got %v", err)
	}
	if scaffolded {
		t.Error("expected scaffolded=false on cancellation")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "wendy.json")); statErr == nil {
		t.Error("wendy.json must not be written when the checklist is cancelled")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/cli/commands/... -run TestPromptAndScaffoldWendyLiteESPIDF -v`
Expected: FAIL — `promptAndScaffoldWendyLiteESPIDF` and `wendyLiteComponentChecklistFn` undefined.

- [ ] **Step 3: Write minimal implementation**

Update the `import (...)` block at the top of
`go/internal/cli/commands/wendylite_scaffold.go` to:

```go
import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"gopkg.in/yaml.v3"
)
```

Then append the following after the Task 3 functions:

```go
// wendyLiteESPIDFComponents lists the non-WASM Wendy Lite ESP-IDF components
// offered by the scaffold checklist. WASM-only pieces (wendy_wasm,
// wendy_hal_export, wendy_wasi_shim, wendy_safety, wendy_callback) are
// intentionally excluded: this flow is for native ESP-IDF firmware, not WASM
// apps.
var wendyLiteESPIDFComponents = []string{
	"wendy_hal",
	"wendy_usb",
	"wendy_wifi",
	"wendy_ble_prov",
	"wendy_cloud_prov",
	"wendy_storage",
	"wendy_uart",
	"wendy_spi",
	"wendy_sys",
	"wendy_otel",
	"wendy_ble",
	"wendy_net",
	"wendy_app_usb",
}

// wendyLiteComponentChecklistFn runs the component-selection checklist. It is
// a package var so tests can stub it (mirrors confirmFn's testability
// pattern in helpers.go).
var wendyLiteComponentChecklistFn = func(items []tui.ChecklistItem) ([]tui.ChecklistItem, error) {
	return tui.RunChecklist("Which Wendy Lite components do you want to add?", items)
}

// promptAndScaffoldWendyLiteESPIDF offers to add wendy-lite ESP-IDF
// components to the project at cwd and, if accepted, writes a wendy.json.
// Returns scaffolded=true iff wendy.json was written.
func promptAndScaffoldWendyLiteESPIDF(cwd string) (bool, error) {
	if !confirmFn("This looks like an ESP-IDF project without a wendy.json, and a USB-connected ESP32 was detected. Add Wendy Lite components and set up 'wendy run' for this project?") {
		return false, nil
	}

	items := make([]tui.ChecklistItem, len(wendyLiteESPIDFComponents))
	for i, name := range wendyLiteESPIDFComponents {
		items[i] = tui.ChecklistItem{Label: name, Value: name, Selected: true}
	}
	selected, err := wendyLiteComponentChecklistFn(items)
	if err != nil {
		return false, err
	}

	names := make([]string, len(selected))
	for i, item := range selected {
		names[i] = item.Value
	}

	if len(names) > 0 {
		manifestPath := filepath.Join(cwd, "main", "idf_component.yml")
		if err := mergeIdfComponentDependencies(manifestPath, names); err != nil {
			return false, fmt.Errorf("adding wendy-lite components: %w", err)
		}
		fmt.Printf("Added %d Wendy Lite component(s) to main/idf_component.yml\n", len(names))
	} else {
		fmt.Println("No Wendy Lite components were added. You can add idf_component.yml dependencies manually later.")
	}

	if err := writeWendyLiteESPIDFAppConfig(cwd); err != nil {
		return false, err
	}
	return true, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./go/internal/cli/commands/... -run TestPromptAndScaffoldWendyLiteESPIDF -v`
Expected: PASS (all 4 subtests).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/wendylite_scaffold.go go/internal/cli/commands/wendylite_scaffold_test.go
git commit -m "Add confirm+checklist glue for wendy-lite ESP-IDF scaffold"
```

---

### Task 5: Wire into `wendy run`'s missing-config path

**Files:**
- Modify: `go/internal/cli/commands/run.go:674-690` (the `cfgMissing` branch in
  `runCommand`)

**Interfaces:**
- Consumes: `shouldOfferWendyLiteESPIDFScaffold` (Task 1),
  `promptAndScaffoldWendyLiteESPIDF` (Task 4).
- Produces: nothing new — this task only wires existing functions into the
  existing control flow.

- [ ] **Step 1: Make the change**

In `go/internal/cli/commands/run.go`, the current block reads:

```go
	if cfgMissing {
		target, err = resolveRunTarget(ctx, runResolveOptions(opts)...)
		if err != nil {
			return err
		}
		if err := preflightMissingAppConfigForMacTarget(ctx, target, projectType); err != nil {
			return err
		}
	}
```

Replace it with:

```go
	if cfgMissing {
		target, err = resolveRunTarget(ctx, runResolveOptions(opts)...)
		if err != nil {
			return err
		}
		if err := preflightMissingAppConfigForMacTarget(ctx, target, projectType); err != nil {
			return err
		}
		if shouldOfferWendyLiteESPIDFScaffold(cfgMissing, projectType, target) {
			scaffolded, err := promptAndScaffoldWendyLiteESPIDF(cwd)
			if err != nil {
				return err
			}
			if scaffolded {
				cfgMissing = false
			}
		}
	}
```

This runs immediately after the existing Mac-target preflight, using the
already-resolved `target` and already-computed `projectType` — no new
discovery or device resolution. If the user declines the offer,
`cfgMissing` stays `true` and the existing `ensureAppConfig` call
(`run.go`, a few lines below) falls through to its current generic
"No wendy.json found... Create one?" behavior, unchanged. If the user
accepts, `cfgMissing` becomes `false` and `ensureAppConfig` simply loads the
`wendy.json` this task's flow just wrote.

- [ ] **Step 2: Build and run the full package test suite**

Run: `go build ./go/... && go test ./go/internal/cli/commands/... -v`
Expected: build succeeds; all tests pass, including the new
`TestShouldOfferWendyLiteESPIDFScaffold`, `TestMergeIdfComponentDependencies*`,
`TestNewWendyLiteESPIDFAppConfig`, `TestWriteWendyLiteESPIDFAppConfig`, and
`TestPromptAndScaffoldWendyLiteESPIDF*` from Tasks 1-4, plus every
pre-existing test in the package (confirming the edit didn't regress the
Mac-target preflight or the generic `ensureAppConfig` path).

- [ ] **Step 3: Manual / hardware verification (not automatable in CI)**

Document in the PR description, exercised manually against a real
USB-connected ESP32 running Wendy Lite firmware:
1. In an empty directory: `idf.py create-project` (or copy an existing bare
   ESP-IDF example) to get a `CMakeLists.txt` + `main/` with no `wendy.json`.
2. Plug in a USB-connected ESP32.
3. Run `wendy run` and confirm the new prompt appears, the checklist shows
   only the 13 non-WASM components, and accepting writes both
   `main/idf_component.yml` (with the selected components as git
   dependencies) and `wendy.json` (`platform: "wendy-lite"`).
4. Re-run `wendy run` and confirm it proceeds straight to
   `idf.py build`/flash using the now-present `wendy.json` (no re-prompt).
5. Confirm declining the initial prompt falls back to today's existing
   generic "No wendy.json found..." behavior.

- [ ] **Step 4: Commit**

```bash
git add go/internal/cli/commands/run.go
git commit -m "Wire wendy-lite ESP-IDF scaffold offer into wendy run's missing-config path"
```
