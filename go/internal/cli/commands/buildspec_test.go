package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const buildSpecCommandFixture = `version = 1

[build]
adapter = "swift"
base = "swift:6.2"

[runtime]
base = "ubuntu:22.04"
entrypoint = ["/app/example"]

[[runtime.artifacts]]
source = ".build/release/example"
destination = "/app/example"
`

func TestCompileBuildSpecCommandCore(t *testing.T) {
	directory := t.TempDir()
	writeOptFile(t, directory, "Wendyfile.toml", buildSpecCommandFixture)
	writeOptFile(t, directory, "Package.swift", "// swift-tools-version: 6.2\nimport PackageDescription\n")

	first, err := compileBuildSpec(buildSpecOptions{Dir: directory})
	if err != nil {
		t.Fatalf("compileBuildSpec: %v", err)
	}
	second, err := compileBuildSpec(buildSpecOptions{Dir: directory})
	if err != nil {
		t.Fatalf("compileBuildSpec second: %v", err)
	}
	if first.Plan.PlanID != second.Plan.PlanID || first.Dockerfile != second.Dockerfile {
		t.Fatal("compileBuildSpec is not deterministic")
	}
	if !strings.Contains(first.Dockerfile, `COPY ["Package.swift","./"]`) {
		t.Fatalf("Dockerfile missing manifest-first copy:\n%s", first.Dockerfile)
	}
}

func TestWriteBuildSpecDockerfile(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "Dockerfile.generated")
	if err := writeBuildSpecDockerfile(output, []byte("FROM scratch\n")); err != nil {
		t.Fatalf("writeBuildSpecDockerfile: %v", err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "FROM scratch\n" {
		t.Fatalf("output = %q", data)
	}
	if err := writeBuildSpecDockerfile(output, []byte("FROM busybox\n")); err != nil {
		t.Fatalf("replace build spec Dockerfile: %v", err)
	}
	data, _ = os.ReadFile(output)
	if string(data) != "FROM busybox\n" {
		t.Fatalf("replaced output = %q", data)
	}
}

func TestProjectRegistersBuildSpecCommands(t *testing.T) {
	project := newProjectCmd()
	buildspec, _, err := project.Find([]string{"buildspec"})
	if err != nil || buildspec == nil {
		t.Fatalf("buildspec command missing: %v", err)
	}
	for _, name := range []string{"validate", "compile"} {
		command, _, err := buildspec.Find([]string{name})
		if err != nil || command == nil {
			t.Fatalf("buildspec %s command missing: %v", name, err)
		}
	}
}

func TestCompileBuildSpecRejectsEscapedPath(t *testing.T) {
	_, err := compileBuildSpec(buildSpecOptions{Dir: t.TempDir(), File: "../Wendyfile.toml"})
	if err == nil || !strings.Contains(err.Error(), "stay within") {
		t.Fatalf("error = %v, want confinement error", err)
	}
}

func TestBuildSpecCompileJSONUsesStdout(t *testing.T) {
	directory := t.TempDir()
	writeOptFile(t, directory, "Wendyfile.toml", buildSpecCommandFixture)
	writeOptFile(t, directory, "Package.swift", "// swift-tools-version: 6.2\nimport PackageDescription\n")
	t.Chdir(directory)

	previousJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = previousJSON })

	command := newBuildSpecCompileCmd()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&bytes.Buffer{})
	if err := command.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, output.String())
	}
	if decoded["dockerfile_sha256"] == nil {
		t.Fatalf("stdout missing dockerfile_sha256: %s", output.String())
	}
}
