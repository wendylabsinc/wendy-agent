package commands

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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

func TestFinishTemplateInit_EntersProjectShellForInteractiveNewDirectory(t *testing.T) {
	cwd := t.TempDir()
	destDir := filepath.Join(cwd, "demo-app")
	if err := os.Mkdir(destDir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	var shellDir string
	var shellPath string
	launch := func(dir, shell string) error {
		shellDir = dir
		shellPath = shell
		return nil
	}
	resolveShell := func() (string, error) {
		return "test-shell", nil
	}

	if err := finishTemplateInitWithLauncher(cwd, destDir, "demo-app", true, resolveShell, launch); err != nil {
		t.Fatalf("finishTemplateInit: %v", err)
	}
	wantDir, err := projectShellDir(cwd, destDir)
	if err != nil {
		t.Fatalf("projectShellDir: %v", err)
	}
	if shellDir != wantDir {
		t.Fatalf("project shell dir = %q, want %q", shellDir, wantDir)
	}
	if shellPath != "test-shell" {
		t.Fatalf("project shell = %q, want %q", shellPath, "test-shell")
	}
}

func TestFinishTemplateInit_DoesNotEnterProjectShellForCurrentDirectory(t *testing.T) {
	cwd := t.TempDir()

	called := false
	launch := func(dir, shell string) error {
		called = true
		return nil
	}
	resolveShell := func() (string, error) {
		return "test-shell", nil
	}

	if err := finishTemplateInitWithLauncher(cwd, cwd, "demo-app", true, resolveShell, launch); err != nil {
		t.Fatalf("finishTemplateInit: %v", err)
	}
	if called {
		t.Fatal("startProjectShell called for current-directory init")
	}
}

func TestFinishTemplateInit_ReturnsProjectShellError(t *testing.T) {
	cwd := t.TempDir()
	destDir := filepath.Join(cwd, "demo-app")
	if err := os.Mkdir(destDir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	shellErr := errors.New("shell failed")
	launch := func(dir, shell string) error {
		return shellErr
	}
	resolveShell := func() (string, error) {
		return "test-shell", nil
	}

	err := finishTemplateInitWithLauncher(cwd, destDir, "demo-app", true, resolveShell, launch)
	if !errors.Is(err, shellErr) {
		t.Fatalf("finishTemplateInit error = %v, want %v", err, shellErr)
	}
}

func TestProjectShellDir_RejectsDirectoryOutsideWorkingDirectory(t *testing.T) {
	cwd := t.TempDir()
	outside := t.TempDir()

	_, err := projectShellDir(cwd, outside)
	if err == nil {
		t.Fatal("expected outside project directory to fail")
	}
	if !strings.Contains(err.Error(), "outside working directory") {
		t.Fatalf("error = %q, want outside working directory", err)
	}
}

func TestProjectShellEnv_FiltersSensitiveValues(t *testing.T) {
	t.Setenv("WENDY_TOKEN", "secret")
	t.Setenv("OPENAI_API_KEY", "secret")
	t.Setenv("NORMAL_ENV", "value")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("PATH", "/tmp/evil")
	t.Setenv("SHELL", "/tmp/evil")
	t.Setenv("HOME", "relative-home")
	t.Setenv("USER", "bad user")
	t.Setenv("LOGNAME", "safe-user")
	for _, key := range []string{
		"LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT", "LD_TRACE_LOADED_OBJECTS",
		"DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH", "DYLD_FRAMEWORK_PATH", "DYLD_FALLBACK_LIBRARY_PATH",
		"BASH_ENV", "ENV", "CDPATH", "IFS", "SHELLOPTS", "BASHOPTS", "PS4",
		"ZDOTDIR", "GCONV_PATH", "PYTHONPATH", "RUBYLIB", "RUBYOPT", "NODE_PATH", "NODE_OPTIONS", "PERL5LIB", "PERL5OPT",
	} {
		t.Setenv(key, "injected")
	}

	shell := testInteractiveShell(t)
	env, err := projectShellEnv(shell)
	if err != nil {
		t.Fatalf("projectShellEnv: %v", err)
	}
	joined := "\n" + strings.Join(env, "\n") + "\n"

	for _, key := range []string{
		"WENDY_TOKEN", "OPENAI_API_KEY", "NORMAL_ENV",
		"LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT", "LD_TRACE_LOADED_OBJECTS",
		"DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH", "DYLD_FRAMEWORK_PATH", "DYLD_FALLBACK_LIBRARY_PATH",
		"BASH_ENV", "ENV", "CDPATH", "IFS", "SHELLOPTS", "BASHOPTS", "PS4",
		"ZDOTDIR", "GCONV_PATH", "PYTHONPATH", "RUBYLIB", "RUBYOPT", "NODE_PATH", "NODE_OPTIONS", "PERL5LIB", "PERL5OPT",
	} {
		if strings.Contains(joined, "\n"+key+"=") {
			t.Fatalf("projectShellEnv included disallowed key %q in %q", key, joined)
		}
	}
	for _, kv := range []string{"HOME=relative-home", "USER=bad user", "LOGNAME=safe-user"} {
		if strings.Contains(joined, "\n"+kv+"\n") {
			t.Fatalf("projectShellEnv forwarded parent environment value %q in %q", kv, joined)
		}
	}
	if !strings.Contains(joined, "\nTERM=xterm-256color\n") {
		t.Fatalf("projectShellEnv missing allowed environment value: %q", joined)
	}
	if strings.Contains(joined, "\nPATH=/tmp/evil\n") {
		t.Fatalf("projectShellEnv forwarded parent PATH: %q", joined)
	}
	if !strings.Contains(joined, "\nPATH="+projectShellPath()+"\n") {
		t.Fatalf("projectShellEnv missing safe PATH: %q", joined)
	}
	if runtime.GOOS != "windows" && !strings.Contains(joined, "\nSHELL="+shell+"\n") {
		t.Fatalf("projectShellEnv did not override SHELL with validated shell: %q", joined)
	}
}

func TestProjectShellPath_PreservesUsableParentPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix path filtering semantics are covered on Unix")
	}

	safeDir := t.TempDir()
	groupWritableDir := filepath.Join(t.TempDir(), "group-writable")
	if err := os.Mkdir(groupWritableDir, 0o775); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.Chmod(groupWritableDir, 0o775); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	worldWritableDir := filepath.Join(t.TempDir(), "world-writable")
	if err := os.Mkdir(worldWritableDir, 0o777); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.Chmod(worldWritableDir, 0o777); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	t.Setenv("PATH", strings.Join([]string{safeDir, "/tmp/does-not-exist", groupWritableDir, worldWritableDir, "relative-bin"}, string(os.PathListSeparator)))

	got := filepath.SplitList(projectShellPath())
	if !slices.Contains(got, filepath.Clean(safeDir)) {
		t.Fatalf("projectShellPath() = %q, want safe parent PATH directory %q", got, safeDir)
	}
	for _, disallowed := range []string{"/tmp/does-not-exist", groupWritableDir, worldWritableDir, "relative-bin"} {
		if slices.Contains(got, filepath.Clean(disallowed)) {
			t.Fatalf("projectShellPath() = %q, did not expect %q", got, disallowed)
		}
	}
	for _, fallback := range []string{"/usr/bin", "/bin"} {
		if !slices.Contains(got, fallback) {
			t.Fatalf("projectShellPath() = %q, want fallback directory %q", got, fallback)
		}
	}
}

func TestProjectShellEnv_RejectsInvalidShell(t *testing.T) {
	if _, err := projectShellEnv("test-shell"); err == nil {
		t.Fatal("expected invalid shell to fail")
	}
}

func TestAppendAllowedProjectShellEnv_DropsLinkerAndInterpreterVariables(t *testing.T) {
	env := []string{"PATH=/bin"}
	for _, kv := range []struct {
		key   string
		value string
	}{
		{"LD_PRELOAD", "/tmp/lib.so"},
		{"DYLD_INSERT_LIBRARIES", "/tmp/lib.dylib"},
		{"BASH_ENV", "/tmp/env"},
		{"ZDOTDIR", "/tmp/zsh"},
		{"GCONV_PATH", "/tmp/gconv"},
		{"PYTHONPATH", "/tmp/python"},
		{"RUBYOPT", "-r/tmp/ruby.rb"},
		{"NODE_PATH", "/tmp/node"},
		{"NODE_OPTIONS", "--require /tmp/node.js"},
	} {
		env = appendAllowedProjectShellEnv(env, kv.key, kv.value)
	}

	if strings.Join(env, "\n") != "PATH=/bin" {
		t.Fatalf("appendAllowedProjectShellEnv() = %#v, want only PATH", env)
	}
}

func TestAppendWithoutExistingEnv_DropsEmptyValue(t *testing.T) {
	got := appendWithoutExistingEnv([]string{"PATH=/bin", "TERM=xterm"}, "PATH", "")
	if strings.Join(got, "\n") != "TERM=xterm" {
		t.Fatalf("appendWithoutExistingEnv() = %#v, want existing PATH removed without adding empty PATH", got)
	}
}

func TestStartProjectShell_RejectsInvalidShell(t *testing.T) {
	err := startProjectShell(t.TempDir(), "test-shell")
	if err == nil {
		t.Fatal("expected invalid shell to fail")
	}
	if !strings.Contains(err.Error(), "is no longer valid") {
		t.Fatalf("error = %q, want shell validation failure", err)
	}
}

func TestValidateInteractiveShell_RejectsUnknownShellName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-a-shell")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if shell, ok := validateInteractiveShell(path); ok {
		t.Fatalf("validateInteractiveShell(%q) = %q, true; want false", path, shell)
	}
}

func TestValidateInteractiveShell_AcceptsDefaultShell(t *testing.T) {
	shell := testInteractiveShell(t)
	if got, ok := validateInteractiveShell(shell); !ok || got != shell {
		t.Fatalf("validateInteractiveShell(%q) = %q, %t; want %q, true", shell, got, ok, shell)
	}
}

func testInteractiveShell(t *testing.T) string {
	t.Helper()
	shell, err := defaultInteractiveShell()
	if err != nil {
		t.Skipf("no supported interactive shell: %v", err)
	}
	return shell
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
	if cfg.Platform != appconfig.PlatformWendyOS {
		t.Fatalf("Platform = %q, want %q", cfg.Platform, appconfig.PlatformWendyOS)
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
