//go:build windows

package commands

import (
	"os"
	"path/filepath"
	"testing"
)

// stubGSTRegistry overrides the registry lookup for the duration of the test.
func stubGSTRegistry(t *testing.T, roots []string) {
	t.Helper()
	prev := gstRegistryRootsFn
	gstRegistryRootsFn = func() []string { return roots }
	t.Cleanup(func() { gstRegistryRootsFn = prev })
}

func TestGstLaunchFallbackPaths_UsesInstallerEnvRoot(t *testing.T) {
	root := t.TempDir()

	stubGSTRegistry(t, nil) // ignore any GStreamer installed on the test host
	for _, env := range gstRootEnvVars {
		t.Setenv(env, "")
	}
	t.Setenv("GSTREAMER_1_0_ROOT_MSVC_X86_64", root)

	prevDefaults := gstDefaultRoots
	gstDefaultRoots = nil
	t.Cleanup(func() { gstDefaultRoots = prevDefaults })
	t.Setenv("LOCALAPPDATA", "")
	t.Setenv("ProgramFiles", "")

	want := filepath.Join(root, "bin", gstLaunchName)
	paths := gstLaunchFallbackPaths()
	if len(paths) == 0 || paths[0] != want {
		t.Fatalf("expected first candidate %q, got %v", want, paths)
	}
}

func TestSanitizeGSTRoot(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		want  string
		valid bool
	}{
		{"empty", "", "", false},
		{"whitespace", "   ", "", false},
		{"relative", `gstreamer\1.0`, "", false},
		{"traversal", `C:\legit\..\Users\attacker`, "", false},
		{"absolute is cleaned", `C:\gstreamer\1.0\msvc_x86_64\`, `C:\gstreamer\1.0\msvc_x86_64`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := sanitizeGSTRoot(tc.in)
			if ok != tc.valid {
				t.Fatalf("valid=%v, want %v (got=%q)", ok, tc.valid, got)
			}
			if tc.valid && got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResolveGSTLaunch_FoundViaRegistry reproduces the winget per-user install:
// GStreamer is not on PATH and sets no env vars, but its InstallLocation is
// recorded in the registry. resolveGSTLaunch must find it via that location.
func TestResolveGSTLaunch_FoundViaRegistry(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // not on PATH
	for _, env := range gstRootEnvVars {
		t.Setenv(env, "")
	}

	installRoot := t.TempDir() // mimics %LOCALAPPDATA%\Programs\gstreamer\1.0\msvc_x86_64
	binDir := filepath.Join(installRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	exe := filepath.Join(binDir, gstLaunchName)
	if err := os.WriteFile(exe, []byte("stub"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	stubGSTRegistry(t, []string{installRoot})

	got, err := resolveGSTLaunch()
	if err != nil {
		t.Fatalf("expected resolution via registry InstallLocation, got: %v", err)
	}
	if got != exe {
		t.Errorf("got %q, want %q", got, exe)
	}
}
