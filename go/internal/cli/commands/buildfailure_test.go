package commands

import (
	"errors"
	"strings"
	"testing"
)

const pipBuildFailureFixture = `#15 8.239 ERROR: Cannot install unknown 0.0.0 (from git+https://github.com/ultralytics/CLIP.git@488e81a6711eea7346872b46ea928b367da8889d) and unknown 0.0.0 (from git+https://github.com/ultralytics/mobileclip.git@a17446aaa1860d25cab8531ec27c3ecac3c05bf5) because these package versions have conflicting dependencies.
#15 8.239 ERROR: ResolutionImpossible: for help visit https://pip.pypa.io/en/latest/topics/dependency-resolution/
#15 ERROR: process "/bin/sh -c pip install lots-of-packages" did not complete successfully: exit code: 1
------
 > [stagefile-pip-deps-0 5/7] RUN --mount=type=cache,sharing=locked,id=stagefile-pip-96a296d224f285c6,target=/root/.cache/pip pip install fastapi httpx numpy opencv-python pydantic uvicorn ultralytics:
------
Dockerfile.generated.yolo:12
--------------------
ERROR: failed to build: failed to solve: process "/bin/sh -c pip install lots-of-packages" did not complete successfully: exit code: 1
View build details: docker-desktop://dashboard/build/wendy-oci/example
`

func TestSummarizeBuildFailurePipConflict(t *testing.T) {
	got := summarizeBuildFailure(pipBuildFailureFixture, errors.New("docker buildx build failed: exit status 1"))
	if got.cause != "pip dependency conflict: ultralytics/CLIP and ultralytics/mobileclip both report package metadata as unknown 0.0.0" {
		t.Errorf("cause = %q", got.cause)
	}
	if got.step != "stagefile-pip-deps-0 5/7 — RUN pip install …" {
		t.Errorf("step = %q", got.step)
	}
	if got.source != "Dockerfile.generated.yolo:12" {
		t.Errorf("source = %q", got.source)
	}
	if got.detailsURL != "docker-desktop://dashboard/build/wendy-oci/example" {
		t.Errorf("detailsURL = %q", got.detailsURL)
	}
}

func TestSummarizeBuildFailureGenericError(t *testing.T) {
	log := "#5 [3/5] COPY Package.swift .\n#5 ERROR: failed to compute cache key: \"/Package.swift\": not found\n"
	got := summarizeBuildFailure(log, errors.New("container build failed"))
	if got.cause != `failed to compute cache key: "/Package.swift": not found` {
		t.Errorf("cause = %q", got.cause)
	}
	if got.step != "3/5 — COPY Package.swift ." {
		t.Errorf("step = %q", got.step)
	}
}

func TestRenderBuildFailureShowsSummaryAndRetainsFullLog(t *testing.T) {
	original := persistBuildFailureLog
	defer func() { persistBuildFailureLog = original }()
	var savedLabel, savedLog string
	persistBuildFailureLog = func(label, raw string) (string, error) {
		savedLabel, savedLog = label, raw
		return "/tmp/wendy-build-yolo-fruits-123.log", nil
	}

	var out strings.Builder
	renderBuildFailure(&out, "yolo-fruits", pipBuildFailureFixture, errors.New("exit status 1"))
	got := out.String()
	for _, want := range []string{
		"Build failure details: yolo-fruits",
		"Cause: pip dependency conflict",
		"At: Dockerfile.generated.yolo:12",
		"Details: docker-desktop://dashboard/build/wendy-oci/example",
		"Build log: /tmp/wendy-build-yolo-fruits-123.log",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ResolutionImpossible") {
		t.Errorf("verbose raw log leaked into summary:\n%s", got)
	}
	if savedLabel != "yolo-fruits" || savedLog != pipBuildFailureFixture {
		t.Errorf("saved label/log = %q/%q", savedLabel, savedLog)
	}
}

func TestRenderBuildFailureFallsBackToRawWhenLogCannotBeSaved(t *testing.T) {
	original := persistBuildFailureLog
	defer func() { persistBuildFailureLog = original }()
	persistBuildFailureLog = func(string, string) (string, error) {
		return "", errors.New("temporary directory unavailable")
	}

	var out strings.Builder
	renderBuildFailure(&out, "api", "raw diagnostic\n", errors.New("exit status 1"))
	if !strings.Contains(out.String(), "raw diagnostic") {
		t.Fatalf("raw fallback missing:\n%s", out.String())
	}
}

func TestSanitizeBuildLogLabel(t *testing.T) {
	if got := sanitizeBuildLogLabel("API / Prod"); got != "api---prod" {
		t.Errorf("sanitizeBuildLogLabel = %q", got)
	}
}
