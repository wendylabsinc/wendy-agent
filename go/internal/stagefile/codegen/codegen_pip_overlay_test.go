package codegen

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

func namedStageBlock(t *testing.T, out, name string) string {
	t.Helper()
	marker := " AS " + name + "\n"
	for _, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		if strings.Contains(block, marker) {
			return block
		}
	}
	t.Fatalf("generated output has no stage %q:\n%s", name, out)
	return ""
}

func pipOverlayStage(aptPackages, pipPackages []string) spec.Stage {
	return spec.Stage{
		Name: "app", From: "python:3.12-slim",
		Install: &spec.Install{
			Apt: &spec.AptInstall{Packages: aptPackages},
			Pip: []spec.PipInstall{{Packages: pipPackages}},
		},
	}
}

func TestPipOverlayDoesNotChangeWhenRuntimeAPTChanges(t *testing.T) {
	before := genOne(t, pipOverlayStage([]string{"libgomp1"}, []string{"flask==3.1.0"}), nil)
	after := genOne(t, pipOverlayStage([]string{"libgomp1", "curl"}, []string{"flask==3.1.0"}), nil)

	beforePip := namedStageBlock(t, before, "stagefile-pip-deps-0")
	afterPip := namedStageBlock(t, after, "stagefile-pip-deps-0")
	if beforePip != afterPip {
		t.Fatalf("runtime APT edit changed pip's independent stage\n--- before ---\n%s\n--- after ---\n%s", beforePip, afterPip)
	}
}

func TestPipOverlayUsesBaseImageDefaultInstallScheme(t *testing.T) {
	out := genOne(t, pipOverlayStage(nil, []string{"flask==3.1.0"}), nil)
	pip := namedStageBlock(t, out, "stagefile-pip-deps-0")

	if !strings.Contains(pip, "pip install --root '/opt/stagefile/pip/root' 'flask==3.1.0'") {
		t.Fatalf("pip dependency stage does not install beneath the overlay root:\n%s", pip)
	}
	if strings.Contains(pip, "--prefix") {
		t.Fatalf("pip dependency stage overrides the base image install scheme:\n%s", pip)
	}
}

func TestRuntimeAPTDoesNotChangeWhenPipChanges(t *testing.T) {
	before := genOne(t, pipOverlayStage([]string{"libgomp1"}, []string{"flask==3.1.0"}), nil)
	after := genOne(t, pipOverlayStage([]string{"libgomp1"}, []string{"flask==3.2.0"}), nil)

	beforeApp := namedStageBlock(t, before, "app")
	afterApp := namedStageBlock(t, after, "app")
	if beforeApp != afterApp {
		t.Fatalf("pip edit changed the runtime APT stage\n--- before ---\n%s\n--- after ---\n%s", beforeApp, afterApp)
	}
}

func TestPipBuildPackagesStayInDependencyStage(t *testing.T) {
	out := genOne(t, spec.Stage{
		Name: "app", From: "python:3.12-slim",
		Install: &spec.Install{
			Apt: &spec.AptInstall{Packages: []string{"libgomp1"}},
			Pip: []spec.PipInstall{
				{Packages: []string{"native-one"}, BuildPackages: []string{"gcc", "python3-dev"}},
				{Packages: []string{"native-two"}, BuildPackages: []string{"gcc"}},
			},
		},
	}, nil)

	pip := namedStageBlock(t, out, "stagefile-pip-deps-0")
	app := namedStageBlock(t, out, "app")
	for _, pkg := range []string{"'gcc'", "'python3-dev'"} {
		if !strings.Contains(pip, pkg) {
			t.Errorf("pip dependency stage is missing build package %s:\n%s", pkg, pip)
		}
		if strings.Contains(app, pkg) {
			t.Errorf("build-only package %s leaked into runtime stage:\n%s", pkg, app)
		}
	}
	if strings.Contains(pip, "'gcc' 'python3-dev' 'gcc'") {
		t.Errorf("build packages were not de-duplicated:\n%s", pip)
	}
}

func TestPipBuildPackagesUseAPKWhenDeclared(t *testing.T) {
	out := genOne(t, spec.Stage{
		Name: "app", From: "python:3.12-alpine",
		Install: &spec.Install{
			Apk: &spec.ApkInstall{Packages: []string{"libstdc++"}},
			Pip: []spec.PipInstall{{Packages: []string{"native"}, BuildPackages: []string{"build-base"}}},
		},
	}, nil)
	pip := namedStageBlock(t, out, "stagefile-pip-deps-0")
	if !strings.Contains(pip, "apk add --no-cache") || !strings.Contains(pip, "'build-base'") {
		t.Fatalf("pip build packages did not use apk:\n%s", pip)
	}
	if strings.Contains(pip, "apt-get") {
		t.Fatalf("apk pip dependency stage unexpectedly uses apt:\n%s", pip)
	}
}

func TestPipGeneratedStageNameCannotCollideWithUserStage(t *testing.T) {
	s := pipOverlayStage([]string{"libgomp1"}, []string{"flask"})
	s.Name = "stagefile-pip-deps-0"
	out := genOne(t, s, nil)
	if !strings.Contains(out, " AS stagefile-pip-deps-0-2\n") ||
		!strings.Contains(out, "COPY --link --from=stagefile-pip-deps-0-2") {
		t.Fatalf("generated pip stage collided with user stage:\n%s", out)
	}
}
