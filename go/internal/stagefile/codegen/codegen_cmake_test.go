package codegen

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

func TestGenerateCMakeInstallPinnedDeterministicAndBeforePip(t *testing.T) {
	commit := strings.Repeat("a", 40)
	out := genOne(t, spec.Stage{
		Name: "app", From: "python:3.11-slim",
		Install: &spec.Install{
			Apt: &spec.AptInstall{Packages: []string{"cmake", "git"}},
			CMake: []spec.CMakeInstall{{
				Repository: "https://github.com/eclipse-cyclonedds/cyclonedds.git",
				Commit:     commit,
				Prefix:     "/opt/native libs",
				Defines: map[string]string{
					"BUILD_TESTING":  "OFF",
					"BUILD_EXAMPLES": "OFF",
				},
				Jobs: 2,
			}},
			Pip: &spec.PipInstall{Packages: []string{"cyclonedds==0.10.2"}},
		},
	}, nil)

	wants := []string{
		"git init '/tmp/stagefile-cmake-0/source'",
		"remote add origin 'https://github.com/eclipse-cyclonedds/cyclonedds.git'",
		"fetch --depth 1 origin '" + commit + "'",
		"checkout --detach FETCH_HEAD",
		"'-DCMAKE_BUILD_TYPE=Release' '-DCMAKE_INSTALL_PREFIX=/opt/native libs' '-DBUILD_EXAMPLES=OFF' '-DBUILD_TESTING=OFF'",
		"cmake --build '/tmp/stagefile-cmake-0/build' --parallel 2",
		"cmake --install '/tmp/stagefile-cmake-0/build'",
		"rm -rf '/tmp/stagefile-cmake-0'",
	}
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}

	aptAt := strings.Index(out, "apt-get install")
	cmakeAt := strings.Index(out, "git init '/tmp/stagefile-cmake-0/source'")
	pipAt := strings.Index(out, "pip install")
	if !(aptAt >= 0 && aptAt < cmakeAt && cmakeAt < pipAt) {
		t.Fatalf("install order must be apt -> cmake -> pip:\n%s", out)
	}
}

func TestGenerateCMakeInstallQuotesUserControlledValues(t *testing.T) {
	out := genOne(t, spec.Stage{
		Name: "app", From: "debian:12",
		Install: &spec.Install{CMake: []spec.CMakeInstall{{
			Repository: "https://example.com/project.git?x='safe'",
			Commit:     strings.Repeat("b", 40),
			Defines:    map[string]string{"LABEL": "a; touch /pwned"},
		}}},
	}, nil)
	for _, want := range []string{
		`'https://example.com/project.git?x='"'"'safe'"'"''`,
		`'-DLABEL=a; touch /pwned'`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing safely quoted %q in:\n%s", want, out)
		}
	}
}
