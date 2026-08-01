package commands

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

func TestMergeIdfComponentDependencies_EmptyExistingFile(t *testing.T) {
	dir := t.TempDir()
	mainDir := filepath.Join(dir, "main")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(mainDir, "idf_component.yml")
	// Create a 0-byte file (empty manifest)
	if err := os.WriteFile(manifestPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := mergeIdfComponentDependencies(manifestPath, []string{"wendy_hal"}); err != nil {
		t.Fatalf("mergeIdfComponentDependencies on empty file: %v", err)
	}

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
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

	if dep, ok := parsed.Dependencies["wendy_hal"]; !ok {
		t.Fatalf("expected wendy_hal dependency, got %+v", parsed.Dependencies)
	} else if dep.Git != "https://github.com/wendylabsinc/wendy-lite.git" {
		t.Errorf("git URL = %q, want wendy-lite repo", dep.Git)
	}
}

func TestMergeIdfComponentDependencies_NonMappingDependenciesErrors(t *testing.T) {
	dir := t.TempDir()
	mainDir := filepath.Join(dir, "main")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(mainDir, "idf_component.yml")
	// Create manifest with dependencies: key but no mapping value (empty scalar)
	if err := os.WriteFile(manifestPath, []byte("dependencies:\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := mergeIdfComponentDependencies(manifestPath, []string{"wendy_hal"}); err == nil {
		t.Fatal("expected an error for non-mapping dependencies key, got nil")
	}
}

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
