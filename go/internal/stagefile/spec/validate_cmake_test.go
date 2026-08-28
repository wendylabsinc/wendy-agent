package spec

import (
	"strings"
	"testing"
)

func validCMakeInstall() CMakeInstall {
	return CMakeInstall{
		Repository: "https://github.com/eclipse-cyclonedds/cyclonedds.git",
		Commit:     strings.Repeat("a", 40),
		Prefix:     "/usr/local",
		BuildType:  "Release",
		Defines:    map[string]string{"BUILD_TESTING": "OFF"},
		Jobs:       2,
	}
}

func cmakeStage(c CMakeInstall) *File {
	return oneStage(Stage{Name: "app", From: "debian:12", Install: &Install{CMake: []CMakeInstall{c}}})
}

func TestValidateCMakeInstall(t *testing.T) {
	f := cmakeStage(validCMakeInstall())
	if err := f.Validate(); err != nil {
		t.Fatalf("valid CMake install must validate: %v", err)
	}
}

func TestParseCMakeInstall(t *testing.T) {
	src := []byte(`version: 1
stages:
  - name: app
    from: python:3.11-slim-bookworm
    install:
      cmake:
        - repository: https://github.com/eclipse-cyclonedds/cyclonedds.git
          commit: 2cdd114cbd18340c606573b4cc8dc20cc161ec5a
          prefix: /usr/local
          buildType: Release
          defines:
            BUILD_EXAMPLES: "OFF"
            BUILD_TESTING: "OFF"
          jobs: 2
`)
	f, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := f.Stages[0].Install.CMake
	if len(got) != 1 || got[0].Commit != "2cdd114cbd18340c606573b4cc8dc20cc161ec5a" || got[0].Defines["BUILD_TESTING"] != "OFF" {
		t.Fatalf("unexpected parsed CMake install: %+v", got)
	}
}

func TestValidateCMakeInstallRequiresPinnedHTTPSGitSource(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*CMakeInstall)
		want string
	}{
		{"repository", func(c *CMakeInstall) { c.Repository = "git@github.com:owner/repo.git" }, "http(s)"},
		{"short commit", func(c *CMakeInstall) { c.Commit = "v0.10.5" }, "full 40-hex"},
		{"non-hex commit", func(c *CMakeInstall) { c.Commit = strings.Repeat("z", 40) }, "full 40-hex"},
		{"relative prefix", func(c *CMakeInstall) { c.Prefix = "usr/local" }, "absolute path"},
		{"build type", func(c *CMakeInstall) { c.BuildType = "Fast" }, "buildType"},
		{"negative jobs", func(c *CMakeInstall) { c.Jobs = -1 }, "non-negative"},
		{"define key", func(c *CMakeInstall) { c.Defines = map[string]string{"BAD;RUN": "ON"} }, "CMake identifier"},
		{"define newline", func(c *CMakeInstall) { c.Defines = map[string]string{"SAFE": "ON\nRUN evil"} }, "newline"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validCMakeInstall()
			tt.mut(&c)
			wantErr(t, cmakeStage(c), tt.want)
		})
	}
}
