package stagefile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProfileStagefile(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "build.stagefile.yaml"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func anyDigestResolver(string) (string, error) { return "sha256:abc123", nil }

// `wendy run --debug` has to reach a Stagefile whose checked-in profile is
// release, otherwise the deployed binary is optimized and not debuggable.
func TestBuildProfileOverrideFlipsSwiftAndRustToDebug(t *testing.T) {
	dir := writeProfileStagefile(t, `version: 1
stages:
  - name: swiftapp
    from: swift:6.0
    build:
      lang: swift
      profile: release
  - name: rustapp
    from: rust:1.80
    build:
      lang: rust
      profile: release
`)
	dockerfile, _, err := compileFile(dir, SourceName, "", "", BuildProfileDebug, anyDigestResolver, refuseHasher(t))
	if err != nil {
		t.Fatalf("compileFile: %v", err)
	}
	// Matched on the profile flag alone: a Swift build carries --scratch-path
	// and --cache-path between the command and its configuration.
	if !strings.Contains(dockerfile, "-c debug") {
		t.Errorf("swift stage not switched to debug:\n%s", dockerfile)
	}
	if strings.Contains(dockerfile, "-c release") {
		t.Errorf("swift stage still builds release:\n%s", dockerfile)
	}
	if strings.Contains(dockerfile, "cargo build --release") {
		t.Errorf("rust stage still builds release:\n%s", dockerfile)
	}
}

// Without the override the Stagefile's own profile stands, and its default is
// release — a plain `wendy run` must never silently ship a debug binary.
func TestBuildProfileDefaultsToReleaseWithoutOverride(t *testing.T) {
	dir := writeProfileStagefile(t, `version: 1
stages:
  - name: app
    from: swift:6.0
    build:
      lang: swift
`)
	dockerfile, _, err := compileFile(dir, SourceName, "", "", "", anyDigestResolver, refuseHasher(t))
	if err != nil {
		t.Fatalf("compileFile: %v", err)
	}
	if !strings.Contains(dockerfile, "-c release") {
		t.Fatalf("want a release build by default:\n%s", dockerfile)
	}
}

// Node package scripts have no release/debug notion — spec validation rejects a
// profile on them, so the override must not invent one.
func TestBuildProfileOverrideSkipsLanguagesWithoutProfiles(t *testing.T) {
	dir := writeProfileStagefile(t, `version: 1
stages:
  - name: web
    from: node:22
    build:
      lang: npm
  - name: svc
    from: golang:1.23
    build:
      lang: go
`)
	dockerfile, _, err := compileFile(dir, SourceName, "", "", BuildProfileDebug, anyDigestResolver, refuseHasher(t))
	if err != nil {
		t.Fatalf("compileFile: %v", err)
	}
	for _, unwanted := range []string{"debug", "release"} {
		if strings.Contains(dockerfile, unwanted) {
			t.Errorf("go/npm stages should carry no profile, found %q:\n%s", unwanted, dockerfile)
		}
	}
}

// WithBuildProfile lands after spec validation and its value is interpolated
// into the generated RUN line, so anything but the two known profiles is
// dropped rather than passed through.
func TestWithBuildProfileRejectsUnknownValues(t *testing.T) {
	for _, in := range []string{"", "fast", "release; rm -rf /", "Debug"} {
		var o options
		WithBuildProfile(in)(&o)
		if o.buildProfile != "" {
			t.Errorf("WithBuildProfile(%q) set profile to %q, want it ignored", in, o.buildProfile)
		}
	}
	for _, in := range []string{BuildProfileDebug, BuildProfileRelease} {
		var o options
		WithBuildProfile(in)(&o)
		if o.buildProfile != in {
			t.Errorf("WithBuildProfile(%q) = %q", in, o.buildProfile)
		}
	}
}
