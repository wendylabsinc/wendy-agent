package spec

import (
	"strings"
	"testing"
)

func oneStage(s Stage) *File {
	return &File{Version: 1, Stages: []Stage{s}}
}

func wantErr(t *testing.T, f *File, fragment string) {
	t.Helper()
	err := f.Validate()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", fragment)
	}
	if !strings.Contains(err.Error(), fragment) {
		t.Fatalf("error %q does not contain %q", err, fragment)
	}
}

func TestValidateWorkdirMustBeAbsolute(t *testing.T) {
	wantErr(t, oneStage(Stage{Name: "app", From: "debian:12", Workdir: "app"}), "absolute")
}

func TestValidatePlatformOnlyBuild(t *testing.T) {
	wantErr(t, oneStage(Stage{Name: "app", From: "debian:12", Platform: "linux/amd64"}), "platform")
	if err := oneStage(Stage{Name: "app", From: "debian:12", Platform: "build"}).Validate(); err != nil {
		t.Fatalf("platform build must validate: %v", err)
	}
}

func TestValidateEnvKeyRules(t *testing.T) {
	wantErr(t, oneStage(Stage{Name: "app", From: "debian:12", Env: map[string]string{"A=B": "x"}}), "=")
	wantErr(t, oneStage(Stage{Name: "app", From: "debian:12", Env: map[string]string{"OK": "line\nbreak"}}), "newline")
	wantErr(t, oneStage(Stage{Name: "app", From: "debian:12", Args: map[string]string{"bad key": "x"}}), "whitespace")
}

func TestValidateHealthcheckRules(t *testing.T) {
	wantErr(t, oneStage(Stage{Name: "app", From: "debian:12", Healthcheck: &Healthcheck{}}), "exec")
	wantErr(t, oneStage(Stage{Name: "app", From: "debian:12", Healthcheck: &Healthcheck{Exec: []string{"true"}, Interval: "half an hour"}}), "duration")
	f := &File{Version: 1, Stages: []Stage{
		{Name: "builder", From: "debian:12", Healthcheck: &Healthcheck{Exec: []string{"true"}}},
		{Name: "app", From: "debian:12"},
	}}
	wantErr(t, f, "final stage")
}

func TestValidateCmdFinalStageOnly(t *testing.T) {
	f := &File{Version: 1, Stages: []Stage{
		{Name: "builder", From: "debian:12", Cmd: []string{"bash"}},
		{Name: "app", From: "debian:12"},
	}}
	wantErr(t, f, "cmd is only allowed")
}

func TestValidateAptRepositoryRules(t *testing.T) {
	good := AptRepository{
		Name: "ros2", URL: "http://packages.ros.org/ros2/ubuntu",
		Suites: []string{"jammy"}, Components: []string{"main"},
		Key: AptRepositoryKey{URL: "https://example.com/key.asc", SHA256: strings.Repeat("a", 64)},
	}
	mk := func(mut func(*AptRepository)) *File {
		r := good
		mut(&r)
		return oneStage(Stage{Name: "app", From: "ubuntu:jammy", Install: &Install{Apt: &AptInstall{
			Packages: []string{"curl"}, Repositories: []AptRepository{r},
		}}})
	}
	wantErr(t, mk(func(r *AptRepository) { r.Name = "../evil" }), "filename-safe")
	wantErr(t, mk(func(r *AptRepository) { r.URL = "ftp://x" }), "http(s)")
	wantErr(t, mk(func(r *AptRepository) { r.Suites = nil }), "suites")
	wantErr(t, mk(func(r *AptRepository) { r.Key.SHA256 = "beef" }), "sha256")
	if err := mk(func(r *AptRepository) {}).Validate(); err != nil {
		t.Fatalf("valid repository must validate: %v", err)
	}
}

func TestValidatePipIndexMustBeURL(t *testing.T) {
	wantErr(t, oneStage(Stage{Name: "app", From: "python:3.12", Install: &Install{Pip: []PipInstall{{
		Packages: []string{"flask"}, Index: "not-a-url",
	}}}}), "http(s)")
}

func TestValidatePipBuildPackages(t *testing.T) {
	stage := func(buildPackages ...string) *File {
		return oneStage(Stage{Name: "app", From: "python:3.12", Install: &Install{Pip: []PipInstall{{
			Packages: []string{"native"}, BuildPackages: buildPackages,
		}}}})
	}
	wantErr(t, stage("gcc\nRUN evil"), "newline")
	wantErr(t, stage("--privileged"), "must not start")
	if err := stage("gcc", "python3-dev").Validate(); err != nil {
		t.Fatalf("valid pip build packages must validate: %v", err)
	}
}

func TestValidatePipBuildPackagesNeedOneOSPackageManager(t *testing.T) {
	wantErr(t, oneStage(Stage{Name: "app", From: "custom", Install: &Install{
		Apt: &AptInstall{Packages: []string{"curl"}},
		Apk: &ApkInstall{Packages: []string{"curl"}},
		Pip: []PipInstall{{Packages: []string{"native"}, BuildPackages: []string{"gcc"}}},
	}}), "both install.apt and install.apk")
}

func TestValidateBuildFieldCombinations(t *testing.T) {
	wantErr(t, oneStage(Stage{Name: "app", From: "rust:1", Build: &Build{Lang: "rust", Script: "build"}}), "build.script")
	wantErr(t, oneStage(Stage{Name: "app", From: "node:22", Build: &Build{Lang: "npm", Product: "x"}}), "build.product")
	wantErr(t, oneStage(Stage{Name: "app", From: "node:22", Build: &Build{Lang: "npm", Profile: "release"}}), "build.profile")
	if err := oneStage(Stage{Name: "app", From: "node:22", Build: &Build{Lang: "pnpm", Script: "bundle"}}).Validate(); err != nil {
		t.Fatalf("node build with script must validate: %v", err)
	}
}

func TestValidateCopyOwnerAndMode(t *testing.T) {
	wantErr(t, oneStage(Stage{Name: "app", From: "debian:12", Copy: []CopyEntry{
		{From: "local", Paths: []string{"a"}, Mode: "rwx"},
	}}), "octal")
	wantErr(t, oneStage(Stage{Name: "app", From: "debian:12", Copy: []CopyEntry{
		{From: "local", Paths: []string{"a"}, Owner: "user name"},
	}}), "whitespace")
}

func TestValidateEntrypointSourceNoNewline(t *testing.T) {
	wantErr(t, oneStage(Stage{Name: "app", From: "ros:humble", Entrypoint: &Entrypoint{
		Exec: []string{"ros2"}, Source: "/opt\n/evil",
	}}), "newline")
}

// A build stage runs its RUN in the stage's workdir, and a stage without one
// has no WORKDIR at all — so source copied to /app with no `workdir: /app`
// compiles to a build launched in an empty /. That failed inside the container,
// minutes in, with the build tool's own "no manifest here" error.
func TestValidateBuildWithoutWorkdirWhereSourceLands(t *testing.T) {
	build := &Build{Lang: "swift", Product: "app"}
	copyTo := func(dest string) []CopyEntry {
		return []CopyEntry{{From: "local", Paths: []string{"."}, Dest: dest}}
	}

	err := oneStage(Stage{Name: "app", From: "swift:6.1", Build: build, Copy: copyTo("/app/")}).Validate()
	if err == nil {
		t.Fatal("a build stage with source at /app and no workdir must not validate")
	}
	// The message has to name the fix: the failure it replaces gave no hint
	// that a missing workdir was the cause.
	for _, want := range []string{"workdir: /app", "/app/"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// With the workdir declared, the same stage is fine.
	if err := oneStage(Stage{Name: "app", From: "swift:6.1", Workdir: "/app",
		Build: build, Copy: copyTo("/app/")}).Validate(); err != nil {
		t.Fatalf("workdir matching the copy dest must validate: %v", err)
	}

	// Building at / is legal, and a stage that means it lands source there.
	for _, dest := range []string{"/", ""} {
		if err := oneStage(Stage{Name: "app", From: "swift:6.1",
			Build: build, Copy: copyTo(dest)}).Validate(); err != nil {
			t.Errorf("copy dest %q with no workdir must validate: %v", dest, err)
		}
	}

	// A stage with no build: has nothing to run in the wrong place.
	if err := oneStage(Stage{Name: "app", From: "swift:6.1", Copy: copyTo("/app/")}).Validate(); err != nil {
		t.Fatalf("a stage without build: must validate: %v", err)
	}
}
