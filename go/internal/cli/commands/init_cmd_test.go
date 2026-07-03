package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

func TestNewInitCmd_Flags(t *testing.T) {
	cmd := newInitCmd()

	expectedFlags := []string{
		"app-id",
		"target",
		"language",
		"entitlement",
		"no-extra-entitlements",
		"gpio-pins",
		"i2c-device",
		"persist-name",
		"persist-path",
		"assistant",
		"install-claude-skills",
	}

	for _, name := range expectedFlags {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("missing init flag %q", name)
		}
	}
}

func TestInitCommand_HelpIncludesExamples(t *testing.T) {
	cmd := newInitCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	output := buf.String()
	expected := []string{
		"Examples:",
		"# Interactive wizard",
		"--persist-name demo-data",
		"--gpio-pins 17,27,22",
		"--no-extra-entitlements",
		"--assistant claude",
		"--install-claude-skills",
	}
	for _, want := range expected {
		if !strings.Contains(output, want) {
			t.Fatalf("expected help output to contain %q, got %q", want, output)
		}
	}
}

func TestResolveInitAppID_RejectsWhitespaceFlag(t *testing.T) {
	_, err := resolveInitAppID("/tmp/demo-app", nil, initOptions{
		appID:    "   ",
		appIDSet: true,
	})
	if err == nil {
		t.Fatal("expected empty --app-id to fail")
	}
	if got := err.Error(); got != "app ID cannot be empty or whitespace" {
		t.Fatalf("error = %q", got)
	}
}

func TestResolveInitAppID_TrimsExplicitFlag(t *testing.T) {
	appID, err := resolveInitAppID("/tmp/demo-app", nil, initOptions{
		appID:    "  demo-app  ",
		appIDSet: true,
	})
	if err != nil {
		t.Fatalf("resolveInitAppID: %v", err)
	}
	if appID != "demo-app" {
		t.Fatalf("appID = %q, want %q", appID, "demo-app")
	}
}

func TestPathHasPrefix_IsCaseSensitiveOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows paths are intentionally compared case-insensitively")
	}
	if pathHasPrefix("/tmp/Foo/app", "/tmp/foo") {
		t.Fatal("pathHasPrefix should not case-fold Unix paths")
	}
}

func TestValidateNewProjectName_RejectsNonSubdirectoryNames(t *testing.T) {
	for _, value := range []string{"", "   ", ".", "..", "../outside", "nested/app", `nested\app`, "/tmp/app", "C:app", "demo app", "demo'app", "-demo", ".demo"} {
		t.Run(value, func(t *testing.T) {
			if err := validateNewProjectName(value); err == nil {
				t.Fatalf("validateNewProjectName(%q) = nil, want error", value)
			}
		})
	}
}

func TestValidateNewProjectName_AcceptsPlainDirectoryNames(t *testing.T) {
	for _, value := range []string{"demo-app", "demo.app", "demo_app"} {
		t.Run(value, func(t *testing.T) {
			if err := validateNewProjectName(value); err != nil {
				t.Fatalf("validateNewProjectName(%q): %v", value, err)
			}
		})
	}
}

func TestTemplateRunCommand(t *testing.T) {
	tests := []struct {
		name    string
		cwd     string
		destDir string
		appID   string
		want    string
	}{
		{
			name:    "current directory",
			cwd:     "/tmp/demo-app",
			destDir: "/tmp/demo-app",
			appID:   "demo-app",
			want:    "wendy run",
		},
		{
			name:    "new subdirectory",
			cwd:     "/tmp/workspace",
			destDir: "/tmp/workspace/demo-app",
			appID:   "demo-app",
			want:    "cd 'demo-app' && wendy run",
		},
		{
			name:    "new subdirectory with spaces",
			cwd:     "/tmp/workspace",
			destDir: "/tmp/workspace/demo app",
			appID:   "demo app",
			want:    "cd 'demo app' && wendy run",
		},
		{
			name:    "new subdirectory with apostrophe",
			cwd:     "/tmp/workspace",
			destDir: "/tmp/workspace/demo'app",
			appID:   "demo'app",
			want:    "cd 'demo'\"'\"'app' && wendy run",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := templateRunCommand(tt.cwd, tt.destDir, tt.appID)
			if got != tt.want {
				t.Fatalf("templateRunCommand(%q, %q, %q) = %q, want %q", tt.cwd, tt.destDir, tt.appID, got, tt.want)
			}
		})
	}
}

func TestTemplateNextSteps(t *testing.T) {
	tests := []struct {
		name    string
		cwd     string
		destDir string
		appID   string
		want    []string
	}{
		{
			name:    "current directory",
			cwd:     "/tmp/demo-app",
			destDir: "/tmp/demo-app",
			appID:   "demo-app",
			want:    []string{"wendy run"},
		},
		{
			name:    "new subdirectory",
			cwd:     "/tmp/workspace",
			destDir: "/tmp/workspace/demo-app",
			appID:   "demo-app",
			want:    []string{"cd 'demo-app'", "wendy run"},
		},
		{
			name:    "new subdirectory with apostrophe",
			cwd:     "/tmp/workspace",
			destDir: "/tmp/workspace/demo'app",
			appID:   "demo'app",
			want:    []string{"cd 'demo'\"'\"'app'", "wendy run"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := templateNextSteps(tt.cwd, tt.destDir, tt.appID)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Fatalf("templateNextSteps(%q, %q, %q) = %#v, want %#v", tt.cwd, tt.destDir, tt.appID, got, tt.want)
			}
		})
	}
}

func TestResolveTemplateLanguage_RejectsUnavailableTemplateLanguage(t *testing.T) {
	meta := &repoMeta{
		Templates: []repoMetaTemplate{
			{Name: "realsense-camera", Languages: []string{langPython}},
		},
		Languages: []repoMetaLanguage{
			{Key: langPython, Name: "Python"},
			{Key: langSwift, Name: "Swift"},
		},
	}

	_, err := resolveTemplateLanguage(targetWendyOS, "realsense-camera", meta, initOptions{
		language:    langSwift,
		languageSet: true,
	})
	if err == nil {
		t.Fatal("expected unavailable template language to fail")
	}
	if got, want := err.Error(), `template "realsense-camera" is not available for language "swift" (available: python)`; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestTemplateTargetMatch_DefaultsToWendyOSOnly(t *testing.T) {
	if !templateTargetMatch(repoMetaTemplate{Name: "simple-api"}, targetWendyOS) {
		t.Fatal("template without targets should match WendyOS")
	}
	if templateTargetMatch(repoMetaTemplate{Name: "simple-api"}, targetDarwin) {
		t.Fatal("template without targets should not match Darwin")
	}
}

func TestTemplateTargetMatch_AcceptsExplicitDarwinTarget(t *testing.T) {
	tmpl := repoMetaTemplate{Name: "mac-llm", Targets: []string{targetDarwin}}
	if !templateTargetMatch(tmpl, targetDarwin) {
		t.Fatal("template with darwin target should match Darwin")
	}
	if templateTargetMatch(tmpl, targetWendyOS) {
		t.Fatal("template with only darwin target should not match WendyOS")
	}
}

func TestResolveTemplateLanguage_DarwinRequiresSwift(t *testing.T) {
	meta := &repoMeta{
		Templates: []repoMetaTemplate{
			{Name: "mac-llm", Languages: []string{langSwift}, Targets: []string{targetDarwin}},
		},
		Languages: []repoMetaLanguage{
			{Key: langPython, Name: "Python"},
			{Key: langSwift, Name: "Swift"},
		},
	}

	language, err := resolveTemplateLanguage(targetDarwin, "mac-llm", meta, initOptions{})
	if err != nil {
		t.Fatalf("resolveTemplateLanguage: %v", err)
	}
	if language != langSwift {
		t.Fatalf("language = %q, want %q", language, langSwift)
	}

	_, err = resolveTemplateLanguage(targetDarwin, "mac-llm", meta, initOptions{
		language:    langPython,
		languageSet: true,
	})
	if err == nil {
		t.Fatal("expected Python Darwin template language to fail")
	}
	if got, want := err.Error(), `darwin templates require swift`; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestResolveTemplateLanguage_AcceptsAvailableTemplateLanguage(t *testing.T) {
	meta := &repoMeta{
		Templates: []repoMetaTemplate{
			{Name: "realsense-camera", Languages: []string{langPython}},
		},
		Languages: []repoMetaLanguage{
			{Key: langPython, Name: "Python"},
			{Key: langSwift, Name: "Swift"},
		},
	}

	language, err := resolveTemplateLanguage(targetWendyOS, "realsense-camera", meta, initOptions{
		language:    langPython,
		languageSet: true,
	})
	if err != nil {
		t.Fatalf("resolveTemplateLanguage: %v", err)
	}
	if language != langPython {
		t.Fatalf("language = %q, want %q", language, langPython)
	}
}

func TestBuildInitEntitlementsFromFlags_RejectsEmptyEntitlementFlag(t *testing.T) {
	_, err := buildInitEntitlementsFromFlags(targetWendyOS, initOptions{
		entitlementsSet: true,
		entitlements:    []string{"", "   "},
	})
	if err == nil {
		t.Fatal("expected empty --entitlement to fail")
	}
	if got := err.Error(); got != "--entitlement requires at least one valid entitlement type" {
		t.Fatalf("error = %q", got)
	}
}

func TestBuildInitEntitlementsFromFlags_IgnoresEmptyEntriesWhenValidEntitlementsExist(t *testing.T) {
	entitlements, err := buildInitEntitlementsFromFlags(targetWendyOS, initOptions{
		entitlementsSet: true,
		entitlements:    []string{"gpu", "", " usb "},
	})
	if err != nil {
		t.Fatalf("buildInitEntitlementsFromFlags: %v", err)
	}

	gotTypes := map[string]bool{}
	for _, ent := range entitlements {
		gotTypes[ent.Type] = true
	}

	for _, want := range []string{
		appconfig.EntitlementNetwork,
		appconfig.EntitlementGPU,
		appconfig.EntitlementUSB,
	} {
		if !gotTypes[want] {
			t.Fatalf("expected entitlement %q in %+v", want, entitlements)
		}
	}
}

func TestInitCommand_NonInteractiveFlagsCreateProject(t *testing.T) {
	tempDir := t.TempDir()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prevWD)
	})
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	cmd := newInitCmd()
	cmd.SetArgs([]string{
		"--app-id", "demo-app",
		"--target", "wendyos",
		"--language", "python",
		"--entitlement", "gpu,usb,persist",
		"--persist-name", "demo-data",
		"--persist-path", "/data",
		"--assistant", "skip",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	cfg, err := appconfig.LoadFromFile(filepath.Join(tempDir, "wendy.json"))
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}

	if cfg.AppID != "demo-app" {
		t.Fatalf("AppID = %q, want %q", cfg.AppID, "demo-app")
	}
	if cfg.Platform != appconfig.PlatformLinux {
		t.Fatalf("Platform = %q, want %q", cfg.Platform, appconfig.PlatformLinux)
	}
	if cfg.Language != "python" {
		t.Fatalf("Language = %q, want %q", cfg.Language, "python")
	}
	if cfg.Python == nil {
		t.Fatal("expected python config to be initialized")
	}

	expectedEntitlements := map[string]bool{
		appconfig.EntitlementNetwork: true,
		appconfig.EntitlementGPU:     true,
		appconfig.EntitlementUSB:     true,
		appconfig.EntitlementPersist: true,
	}
	for _, ent := range cfg.Entitlements {
		delete(expectedEntitlements, ent.Type)
		if ent.Type == appconfig.EntitlementPersist {
			if ent.Name != "demo-data" || ent.Path != "/data" {
				t.Fatalf("persist entitlement = %+v, want name/path populated", ent)
			}
		}
	}
	if len(expectedEntitlements) != 0 {
		t.Fatalf("missing entitlements after init: %v", expectedEntitlements)
	}
}

// NOTE: Native Mac end-to-end deployment requires a Wendy Agent for Mac target
// in CI. Until that exists, keep Darwin coverage at the CLI/config boundary here
// and validate real Mac deploys manually with the companion templates PR.
func TestInitCommand_NonInteractiveDarwinCreatesNativeSwiftProject(t *testing.T) {
	tempDir := t.TempDir()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prevWD)
	})
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	cmd := newInitCmd()
	cmd.SetArgs([]string{
		"--app-id", "mac-app",
		"--target", "macos",
		"--language", "swift",
		"--assistant", "skip",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	cfg, err := appconfig.LoadFromFile(filepath.Join(tempDir, "wendy.json"))
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}

	if cfg.Platform != appconfig.PlatformDarwin {
		t.Fatalf("Platform = %q, want %q", cfg.Platform, appconfig.PlatformDarwin)
	}
	if cfg.Language != langSwift {
		t.Fatalf("Language = %q, want %q", cfg.Language, langSwift)
	}
	if len(cfg.Entitlements) != 0 {
		t.Fatalf("Entitlements = %+v, want none for native macOS", cfg.Entitlements)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "Package.swift")); err != nil {
		t.Fatalf("expected Package.swift: %v", err)
	}
}

func TestBuildInitEntitlementsFromFlags_RejectsDarwinEntitlements(t *testing.T) {
	_, err := buildInitEntitlementsFromFlags(targetDarwin, initOptions{
		entitlementsSet: true,
		entitlements:    []string{"network"},
	})
	if err == nil {
		t.Fatal("expected Darwin entitlements to fail")
	}
	if got, want := err.Error(), `darwin apps do not support WendyOS container entitlements`; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestInitCommand_RejectsPersistWithoutFields(t *testing.T) {
	tempDir := t.TempDir()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prevWD)
	})
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	cmd := newInitCmd()
	cmd.SetArgs([]string{
		"--app-id", "demo-app",
		"--target", "wendyos",
		"--language", "swift",
		"--entitlement", "persist",
		"--assistant", "skip",
	})

	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected missing persist fields to fail")
	}
	if got := err.Error(); got != "persist entitlement requires both --persist-name and --persist-path" {
		t.Fatalf("error = %q", got)
	}
}

func TestInitCommand_NoExtraEntitlementsSkipsPrompts(t *testing.T) {
	tempDir := t.TempDir()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prevWD)
	})
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	cmd := newInitCmd()
	cmd.SetArgs([]string{
		"--app-id", "lite-app",
		"--target", "wendy-lite",
		"--no-extra-entitlements",
		"--assistant", "skip",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	cfg, err := appconfig.LoadFromFile(filepath.Join(tempDir, "wendy.json"))
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}

	if cfg.Platform != appconfig.PlatformWendyLite {
		t.Fatalf("Platform = %q, want %q", cfg.Platform, appconfig.PlatformWendyLite)
	}
	if cfg.Language != "swift" {
		t.Fatalf("Language = %q, want %q", cfg.Language, "swift")
	}
	if len(cfg.Entitlements) != 1 || cfg.Entitlements[0].Type != appconfig.EntitlementNetwork {
		t.Fatalf("Entitlements = %+v, want only network", cfg.Entitlements)
	}
}

func TestInitCommand_NoExtraEntitlementsFalseStillPrompts(t *testing.T) {
	tempDir := t.TempDir()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prevWD)
	})
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	// Replace the Bubble Tea checklist with a mock that selects GPU.
	origAsk := askEntitlementQuestions
	askEntitlementQuestions = func(target, language string) ([]appconfig.Entitlement, error) {
		return []appconfig.Entitlement{
			{Type: appconfig.EntitlementNetwork},
			{Type: appconfig.EntitlementGPU},
		}, nil
	}
	t.Cleanup(func() { askEntitlementQuestions = origAsk })

	cmd := newInitCmd()
	cmd.SetArgs([]string{
		"--app-id", "demo-app",
		"--target", "wendyos",
		"--language", "swift",
		"--no-extra-entitlements=false",
		"--assistant", "skip",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	cfg, err := appconfig.LoadFromFile(filepath.Join(tempDir, "wendy.json"))
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	if !cfg.HasEntitlement(appconfig.EntitlementGPU) {
		t.Fatalf("expected interactive prompts to run and include %q entitlement, got %+v", appconfig.EntitlementGPU, cfg.Entitlements)
	}
}

func TestBuildInitEntitlementsFromFlags_Input(t *testing.T) {
	entitlements, err := buildInitEntitlementsFromFlags(targetWendyOS, initOptions{
		entitlementsSet: true,
		entitlements:    []string{"input"},
	})
	if err != nil {
		t.Fatalf("buildInitEntitlementsFromFlags: %v", err)
	}

	gotTypes := map[string]bool{}
	for _, ent := range entitlements {
		gotTypes[ent.Type] = true
	}

	for _, want := range []string{
		appconfig.EntitlementNetwork,
		appconfig.EntitlementInput,
	} {
		if !gotTypes[want] {
			t.Fatalf("expected entitlement %q in %+v", want, entitlements)
		}
	}
}

func TestBuildInitEntitlementsFromFlags_AllEntitlements(t *testing.T) {
	entitlements, err := buildInitEntitlementsFromFlags(targetWendyOS, initOptions{
		allEntitlements: true,
		gpioPinsSet:     true,
		gpioPins:        "17,27",
		i2cDeviceSet:    true,
		i2cDevice:       "/dev/i2c-1",
		persistNameSet:  true,
		persistName:     "test-data",
		persistPathSet:  true,
		persistPath:     "/data",
	})
	if err != nil {
		t.Fatalf("buildInitEntitlementsFromFlags: %v", err)
	}

	gotTypes := map[string]bool{}
	for _, ent := range entitlements {
		gotTypes[ent.Type] = true
	}

	for _, q := range wendyOSEntitlementQuestions {
		if !gotTypes[q.entitlement] {
			t.Errorf("expected entitlement %q from --all-entitlements", q.entitlement)
		}
	}
	if !gotTypes[appconfig.EntitlementNetwork] {
		t.Error("expected network entitlement")
	}
}

func TestBuildInitEntitlementsFromFlags_AllConflictsWithEntitlement(t *testing.T) {
	_, err := buildInitEntitlementsFromFlags(targetWendyOS, initOptions{
		allEntitlements: true,
		entitlementsSet: true,
		entitlements:    []string{"gpu"},
	})
	if err == nil {
		t.Fatal("expected error combining --all-entitlements with --entitlement")
	}
}

func TestBuildInitEntitlementsFromFlags_AllMissingFieldFlags(t *testing.T) {
	// --all-entitlements without required field flags for gpio/i2c/persist should error.
	_, err := buildInitEntitlementsFromFlags(targetWendyOS, initOptions{
		allEntitlements: true,
	})
	if err == nil {
		t.Fatal("expected error for --all-entitlements without required field flags")
	}
}

func TestInitCommand_NonInteractiveInput(t *testing.T) {
	tempDir := t.TempDir()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prevWD)
	})
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	cmd := newInitCmd()
	cmd.SetArgs([]string{
		"--app-id", "scanner-app",
		"--target", "wendyos",
		"--language", "swift",
		"--entitlement", "input",
		"--assistant", "skip",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	cfg, err := appconfig.LoadFromFile(filepath.Join(tempDir, "wendy.json"))
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}

	if !cfg.HasEntitlement(appconfig.EntitlementInput) {
		t.Fatalf("expected input entitlement in %+v", cfg.Entitlements)
	}
}

func TestEntitlementDescriptions_IncludesInput(t *testing.T) {
	desc, ok := entitlementDescriptions[appconfig.EntitlementInput]
	if !ok {
		t.Fatal("entitlementDescriptions missing EntitlementInput entry")
	}
	if desc == "" {
		t.Fatal("entitlementDescriptions[EntitlementInput] is empty")
	}
}

func TestWendyOSEntitlementQuestions_IncludesInput(t *testing.T) {
	found := false
	for _, q := range wendyOSEntitlementQuestions {
		if q.entitlement == appconfig.EntitlementInput {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("wendyOSEntitlementQuestions missing EntitlementInput entry")
	}
}

func TestInitCommand_InstallClaudeSkillsFalseDoesNotRequireClaude(t *testing.T) {
	tempDir := t.TempDir()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prevWD)
	})
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	cmd := newInitCmd()
	cmd.SetArgs([]string{
		"--app-id", "lite-app",
		"--target", "wendy-lite",
		"--no-extra-entitlements",
		"--assistant", "skip",
		"--install-claude-skills=false",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func writeTemplateWendyJSON(t *testing.T, content string) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "wendy.json")
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("writing wendy.json: %v", err)
	}
	return cfgPath
}

func readEntitlements(t *testing.T, cfgPath string) []appconfig.Entitlement {
	t.Helper()
	cfg, err := appconfig.LoadFromFile(cfgPath)
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	return cfg.Entitlements
}

// The WDY-1810 repro: --template simple-api --entitlement gpu must produce a
// wendy.json containing both the template's network entitlement and gpu.
func TestMergeTemplateEntitlements_AddsFlagEntitlementToTemplateConfig(t *testing.T) {
	cfgPath := writeTemplateWendyJSON(t, `{
  "appId": "autotest-gpu",
  "version": "0.1.0",
  "platform": "linux",
  "language": "python",
  "entitlements": [{"type": "network"}]
}`)

	requested, err := templateEntitlementsFromFlags(targetWendyOS, initOptions{
		entitlementsSet: true,
		entitlements:    []string{"gpu"},
	})
	if err != nil {
		t.Fatalf("templateEntitlementsFromFlags: %v", err)
	}

	added, err := mergeTemplateEntitlements(cfgPath, requested)
	if err != nil {
		t.Fatalf("mergeTemplateEntitlements: %v", err)
	}
	if len(added) != 1 || added[0] != appconfig.EntitlementGPU {
		t.Fatalf("added = %v, want [gpu]", added)
	}

	entitlements := readEntitlements(t, cfgPath)
	gotTypes := map[string]bool{}
	for _, ent := range entitlements {
		gotTypes[ent.Type] = true
	}
	if !gotTypes[appconfig.EntitlementNetwork] || !gotTypes[appconfig.EntitlementGPU] {
		t.Fatalf("entitlements = %+v, want network and gpu", entitlements)
	}
}

func TestMergeTemplateEntitlements_NoFlagsIsNoOp(t *testing.T) {
	requested, err := templateEntitlementsFromFlags(targetWendyOS, initOptions{})
	if err != nil {
		t.Fatalf("templateEntitlementsFromFlags: %v", err)
	}
	if requested != nil {
		t.Fatalf("requested = %+v, want nil", requested)
	}

	// No requested entitlements: the file must not even be read.
	added, err := mergeTemplateEntitlements(filepath.Join(t.TempDir(), "missing", "wendy.json"), nil)
	if err != nil {
		t.Fatalf("mergeTemplateEntitlements: %v", err)
	}
	if added != nil {
		t.Fatalf("added = %v, want nil", added)
	}
}

func TestMergeTemplateEntitlements_MissingConfigFails(t *testing.T) {
	_, err := mergeTemplateEntitlements(
		filepath.Join(t.TempDir(), "wendy.json"),
		[]appconfig.Entitlement{{Type: appconfig.EntitlementGPU}},
	)
	if err == nil {
		t.Fatal("expected merge into a missing wendy.json to fail")
	}
}

func TestMergeTemplateEntitlements_CoveredEntitlementsLeaveFileUntouched(t *testing.T) {
	content := `{
  "appId": "demo",
  "entitlements": [{"type": "network", "mode": "host"}, {"type": "gpu"}]
}`
	cfgPath := writeTemplateWendyJSON(t, content)

	added, err := mergeTemplateEntitlements(cfgPath, []appconfig.Entitlement{
		{Type: appconfig.EntitlementNetwork},
		{Type: appconfig.EntitlementGPU},
	})
	if err != nil {
		t.Fatalf("mergeTemplateEntitlements: %v", err)
	}
	if added != nil {
		t.Fatalf("added = %v, want nil", added)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != content {
		t.Fatalf("wendy.json was rewritten:\n%s", data)
	}
}

func TestMergeTemplateEntitlements_PreservesUnknownKeysAndTemplateEntries(t *testing.T) {
	cfgPath := writeTemplateWendyJSON(t, `{
  "appId": "demo",
  "futureKey": {"nested": true},
  "entitlements": [{"type": "network", "mode": "host", "futureEntKey": 7}]
}`)

	added, err := mergeTemplateEntitlements(cfgPath, []appconfig.Entitlement{
		{Type: appconfig.EntitlementNetwork},
		{Type: appconfig.EntitlementAudio},
	})
	if err != nil {
		t.Fatalf("mergeTemplateEntitlements: %v", err)
	}
	if len(added) != 1 || added[0] != appconfig.EntitlementAudio {
		t.Fatalf("added = %v, want [audio]", added)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := raw["futureKey"]; !ok {
		t.Fatalf("unknown top-level key dropped: %s", data)
	}
	if !strings.Contains(string(raw["entitlements"]), "futureEntKey") {
		t.Fatalf("unknown entitlement key dropped: %s", raw["entitlements"])
	}

	entitlements := readEntitlements(t, cfgPath)
	if len(entitlements) != 2 {
		t.Fatalf("entitlements = %+v, want template network + audio", entitlements)
	}
	if entitlements[0].Type != appconfig.EntitlementNetwork || entitlements[0].Mode != "host" {
		t.Fatalf("template network entry changed: %+v", entitlements[0])
	}
	if entitlements[1].Type != appconfig.EntitlementAudio {
		t.Fatalf("entitlements[1] = %+v, want audio", entitlements[1])
	}
}

func TestMergeTemplateEntitlements_PersistDedupedByName(t *testing.T) {
	cfgPath := writeTemplateWendyJSON(t, `{
  "appId": "demo",
  "entitlements": [
    {"type": "network"},
    {"type": "persist", "name": "data", "path": "/data"}
  ]
}`)

	added, err := mergeTemplateEntitlements(cfgPath, []appconfig.Entitlement{
		{Type: appconfig.EntitlementPersist, Name: "data", Path: "/other"},
		{Type: appconfig.EntitlementPersist, Name: "cache", Path: "/cache"},
	})
	if err != nil {
		t.Fatalf("mergeTemplateEntitlements: %v", err)
	}
	if len(added) != 1 || added[0] != appconfig.EntitlementPersist {
		t.Fatalf("added = %v, want [persist]", added)
	}

	entitlements := readEntitlements(t, cfgPath)
	var persistNames []string
	for _, ent := range entitlements {
		if ent.Type == appconfig.EntitlementPersist {
			persistNames = append(persistNames, ent.Name)
		}
	}
	if len(persistNames) != 2 || persistNames[0] != "data" || persistNames[1] != "cache" {
		t.Fatalf("persist names = %v, want [data cache]", persistNames)
	}
}

func TestTemplateEntitlementCovers_GPIOPins(t *testing.T) {
	allPins := []appconfig.Entitlement{{Type: appconfig.EntitlementGPIO}}
	somePins := []appconfig.Entitlement{{Type: appconfig.EntitlementGPIO, Pins: []int{17, 27}}}

	req := appconfig.Entitlement{Type: appconfig.EntitlementGPIO, Pins: []int{17}}
	if !templateEntitlementCovers(allPins, req) {
		t.Fatal("pinless template gpio entry should cover any requested pins")
	}
	if !templateEntitlementCovers(somePins, req) {
		t.Fatal("template gpio pins [17 27] should cover requested [17]")
	}

	req.Pins = []int{17, 22}
	if templateEntitlementCovers(somePins, req) {
		t.Fatal("template gpio pins [17 27] should not cover requested [17 22]")
	}
}

func TestTemplateEntitlementsFromFlags_DarwinRejectsEntitlementFlags(t *testing.T) {
	_, err := templateEntitlementsFromFlags(targetDarwin, initOptions{
		entitlementsSet: true,
		entitlements:    []string{"gpu"},
	})
	if err == nil {
		t.Fatal("expected darwin + --entitlement to fail")
	}
}
