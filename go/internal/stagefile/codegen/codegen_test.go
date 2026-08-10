package codegen

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

func TestGenerateFromOnly(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "debian:12"},
	}}
	images := map[string]string{"debian:12": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM debian:12@sha256:abc123 AS app\nUSER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGenerateAppliesPlatform(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "debian:12"},
	}}
	images := map[string]string{"debian:12": "sha256:abc123"}

	out, err := Generate(f, images, nil, "linux/arm64", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM --platform=linux/arm64 debian:12@sha256:abc123 AS app\nUSER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGenerateErrorsOnMissingDigest(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "debian:12"},
	}}
	if _, err := Generate(f, map[string]string{}, nil, "", nil); err == nil {
		t.Fatal("expected an error for an unresolved image, got nil")
	}
}

func TestGenerateExplicitUserOverridesDefault(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "debian:12", User: "appuser"},
	}}
	images := map[string]string{"debian:12": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM debian:12@sha256:abc123 AS app\nUSER appuser\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGenerateEntrypointOnFinalStage(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "debian:12", Entrypoint: &spec.Entrypoint{Exec: []string{"python3", "app.py"}}},
	}}
	images := map[string]string{"debian:12": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM debian:12@sha256:abc123 AS app\n" +
		`ENTRYPOINT ["python3", "app.py"]` + "\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGenerateAptInstall(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "debian:12", Install: &spec.Install{
			Apt: &spec.AptInstall{Packages: []string{"curl", "git"}},
		}},
	}}
	images := map[string]string{"debian:12": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM debian:12@sha256:abc123 AS app\n" +
		"RUN apt-get update && apt-get install -y --no-install-recommends 'curl' 'git' \\\n" +
		"    && rm -rf /var/lib/apt/lists/*\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGenerateAptInstallWithRecommends(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "debian:12", Install: &spec.Install{
			Apt: &spec.AptInstall{Packages: []string{"curl"}, Recommends: true},
		}},
	}}
	images := map[string]string{"debian:12": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM debian:12@sha256:abc123 AS app\n" +
		"RUN apt-get update && apt-get install -y 'curl' \\\n" +
		"    && rm -rf /var/lib/apt/lists/*\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGenerateApkInstall(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "alpine:3.20", Install: &spec.Install{
			Apk: &spec.ApkInstall{Packages: []string{"curl"}},
		}},
	}}
	images := map[string]string{"alpine:3.20": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM alpine:3.20@sha256:abc123 AS app\n" +
		"RUN apk add --no-cache 'curl'\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGenerateApkInstallWithCache(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "alpine:3.20", Install: &spec.Install{
			Apk: &spec.ApkInstall{Packages: []string{"curl"}, Cache: true},
		}},
	}}
	images := map[string]string{"alpine:3.20": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM alpine:3.20@sha256:abc123 AS app\n" +
		"RUN apk add 'curl'\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGeneratePipInstallFromRequirements(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "python:3.12-slim", Install: &spec.Install{
			Pip: []spec.PipInstall{{Requirements: "requirements.txt"}},
		}},
	}}
	images := map[string]string{"python:3.12-slim": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM python:3.12-slim@sha256:abc123 AS app\n" +
		"COPY requirements.txt requirements.txt\n" +
		"RUN --mount=type=cache,sharing=locked,id=stagefile-pip-96a296d224f285c6,target=/root/.cache/pip pip install -r 'requirements.txt'\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGeneratePipInstallCopyPrecedesExplicitCopy(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "python:3.12-slim", Install: &spec.Install{
			Pip: []spec.PipInstall{{Requirements: "requirements.txt"}},
		}, Copy: []spec.CopyEntry{
			{From: "local", Paths: []string{"app.py"}},
		}},
	}}
	images := map[string]string{"python:3.12-slim": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM python:3.12-slim@sha256:abc123 AS app\n" +
		"COPY requirements.txt requirements.txt\n" +
		"RUN --mount=type=cache,sharing=locked,id=stagefile-pip-96a296d224f285c6,target=/root/.cache/pip pip install -r 'requirements.txt'\n" +
		"COPY app.py app.py\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGeneratePipInstallFromPackages(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "python:3.12-slim", Install: &spec.Install{
			Pip: []spec.PipInstall{{Packages: []string{"flask", "gunicorn"}}},
		}},
	}}
	images := map[string]string{"python:3.12-slim": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM python:3.12-slim@sha256:abc123 AS app\n" +
		"RUN --mount=type=cache,sharing=locked,id=stagefile-pip-96a296d224f285c6,target=/root/.cache/pip pip install 'flask' 'gunicorn'\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGenerateNpmInstallDefaultsToNpm(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "node:20-slim", Install: &spec.Install{Npm: &spec.NpmInstall{}}},
	}}
	images := map[string]string{"node:20-slim": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM node:20-slim@sha256:abc123 AS app\n" +
		"COPY package.json package-lock.json ./\n" +
		"RUN --mount=type=cache,sharing=locked,target=/root/.npm npm ci\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGenerateNpmInstallYarn(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "node:20-slim", Install: &spec.Install{Npm: &spec.NpmInstall{Manager: "yarn"}}},
	}}
	images := map[string]string{"node:20-slim": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM node:20-slim@sha256:abc123 AS app\n" +
		"COPY package.json yarn.lock ./\n" +
		"RUN --mount=type=cache,sharing=locked,target=/root/.cache/yarn yarn install --frozen-lockfile\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGenerateNpmInstallPnpm(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "node:20-slim", Install: &spec.Install{Npm: &spec.NpmInstall{Manager: "pnpm"}}},
	}}
	images := map[string]string{"node:20-slim": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM node:20-slim@sha256:abc123 AS app\n" +
		"COPY package.json pnpm-lock.yaml ./\n" +
		"RUN --mount=type=cache,sharing=locked,target=/root/.local/share/pnpm/store pnpm install --frozen-lockfile\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGenerateCopyFromStageAndLocal(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "deps", From: "python:3.12-slim"},
		{Name: "app", From: "python:3.12-slim", Copy: []spec.CopyEntry{
			{From: "deps", Paths: []string{"/usr/local/lib/python3.12/site-packages"}},
			{From: "local", Paths: []string{"app.py"}},
		}},
	}}
	images := map[string]string{"python:3.12-slim": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM python:3.12-slim@sha256:abc123 AS deps\n\n" +
		"FROM python:3.12-slim@sha256:abc123 AS app\n" +
		"COPY --from=deps /usr/local/lib/python3.12/site-packages /usr/local/lib/python3.12/site-packages\n" +
		"COPY app.py app.py\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGenerateCopyWithExplicitDest(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "debian:12", Copy: []spec.CopyEntry{
			{From: "local", Paths: []string{"a.txt", "b.txt"}, Dest: "/app/"},
		}},
	}}
	images := map[string]string{"debian:12": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM debian:12@sha256:abc123 AS app\n" +
		"COPY a.txt b.txt /app/\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGenerateBuildRustDefaultsToRelease(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "rust:1", Build: &spec.Build{Lang: "rust"}},
	}}
	images := map[string]string{"rust:1": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM rust:1@sha256:abc123 AS app\n" +
		"RUN --mount=type=cache,sharing=locked,target=/root/.cargo cargo build --release\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGenerateBuildRustDebugIsExplicit(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "rust:1", Build: &spec.Build{Lang: "rust", Profile: "debug"}},
	}}
	images := map[string]string{"rust:1": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM rust:1@sha256:abc123 AS app\n" +
		"RUN --mount=type=cache,sharing=locked,target=/root/.cargo cargo build\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGenerateBuildGo(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "golang:1.22", Build: &spec.Build{Lang: "go"}},
	}}
	images := map[string]string{"golang:1.22": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM golang:1.22@sha256:abc123 AS app\n" +
		"RUN --mount=type=cache,sharing=locked,target=/root/.cache/go-build go build ./...\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGenerateBuildSwiftReleaseByDefault(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "swift:6.0", Build: &spec.Build{Lang: "swift"}},
	}}
	images := map[string]string{"swift:6.0": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// The build runs against a scratch path on a cache mount and then installs
	// the product into .build/release, which is where SwiftPM would have put it
	// had it built in place — so an entrypoint naming that path still resolves
	// even though the tree it was compiled in never enters the image.
	for _, want := range []string{
		"swift build --scratch-path " + swiftScratchPath + " --cache-path " + swiftCachePath + " -c release",
		"target=" + swiftScratchPath,
		"target=" + swiftCachePath,
		"mkdir -p .build/release",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// The old cache location. It held SwiftPM's *configuration*, not its build
	// tree or its shared cache, so caching it sped up nothing at all.
	if strings.Contains(out, "/root/.swiftpm") {
		t.Errorf("still caching /root/.swiftpm, which holds no build output:\n%s", out)
	}
}

func TestGenerateBuildSwiftDebugIsExplicit(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "swift:6.0", Build: &spec.Build{Lang: "swift", Profile: "debug"}},
	}}
	images := map[string]string{"swift:6.0": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// -c debug is spelled out rather than left implicit: bare `swift build`
	// already means debug, so omitting the flag is indistinguishable from
	// having forgotten it.
	if !strings.Contains(out, " -c debug") {
		t.Errorf("missing an explicit -c debug in:\n%s", out)
	}
	if strings.Contains(out, "-c release") {
		t.Errorf("debug profile still compiled -c release:\n%s", out)
	}
	// The install destination follows the profile, or a debug build would
	// deposit its binary where a release entrypoint looks for it.
	if !strings.Contains(out, "mkdir -p .build/debug") {
		t.Errorf("debug build does not install into .build/debug:\n%s", out)
	}
}

func TestGenerateBuildRejectsUnsupportedLang(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "debian:12", Build: &spec.Build{Lang: "cobol"}},
	}}
	images := map[string]string{"debian:12": "sha256:abc123"}

	if _, err := Generate(f, images, nil, "", nil); err == nil {
		t.Fatal("expected an error for an unsupported build.lang, got nil")
	}
}

func TestShellQuoteEscapesEmbeddedSingleQuote(t *testing.T) {
	got := shellQuote(`it's`)
	want := `'it'"'"'s'`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestGenerateAptInstallQuotesPackageNames(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "debian:12", Install: &spec.Install{
			Apt: &spec.AptInstall{Packages: []string{"curl && echo pwned"}},
		}},
	}}
	images := map[string]string{"debian:12": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM debian:12@sha256:abc123 AS app\n" +
		"RUN apt-get update && apt-get install -y --no-install-recommends 'curl && echo pwned' \\\n" +
		"    && rm -rf /var/lib/apt/lists/*\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestGeneratePipInstallQuotesVersionSpecifiers(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "python:3.12-slim", Install: &spec.Install{
			Pip: []spec.PipInstall{{Packages: []string{"flask>=2.0,<3.0"}}},
		}},
	}}
	images := map[string]string{"python:3.12-slim": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM python:3.12-slim@sha256:abc123 AS app\n" +
		"RUN --mount=type=cache,sharing=locked,id=stagefile-pip-96a296d224f285c6,target=/root/.cache/pip pip install 'flask>=2.0,<3.0'\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

// TestGenerateLocksEveryCacheMount pins the sharing mode on every
// RUN --mount=type=cache line the compiler emits. BuildKit treats an
// unqualified cache mount as sharing=shared, so with Wendy building up to four
// services at once — plus BuildKit's own parallel stage execution within each
// Dockerfile — several package managers can land on one cache directory
// simultaneously. sharing=locked makes them queue on the mount explicitly
// instead of contending inside cargo/npm/pip's own internal locking, where the
// stall is invisible.
//
// Covers every cache-mounted primitive in one spec so a newly added one cannot
// quietly ship without a sharing mode.
func TestGenerateLocksEveryCacheMount(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "pipdeps", From: "debian:12", Install: &spec.Install{Pip: []spec.PipInstall{{Packages: []string{"flask"}}}}},
		{Name: "uvdeps", From: "debian:12", Install: &spec.Install{Uv: &spec.UvInstall{}}},
		{Name: "npmdeps", From: "debian:12", Install: &spec.Install{Npm: &spec.NpmInstall{}}},
		{Name: "yarndeps", From: "debian:12", Install: &spec.Install{Npm: &spec.NpmInstall{Manager: "yarn"}}},
		{Name: "pnpmdeps", From: "debian:12", Install: &spec.Install{Npm: &spec.NpmInstall{Manager: "pnpm"}}},
		{Name: "rustbuild", From: "debian:12", Build: &spec.Build{Lang: "rust"}},
		{Name: "gobuild", From: "debian:12", Build: &spec.Build{Lang: "go"}},
		{Name: "swiftbuild", From: "debian:12", Build: &spec.Build{Lang: "swift"}},
	}}

	out, err := Generate(f, map[string]string{"debian:12": "sha256:abc123"}, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "type=cache") {
			continue
		}
		if !strings.Contains(line, "sharing=locked") {
			t.Errorf("cache mount without an explicit sharing mode (defaults to shared):\n%s", line)
		}
	}

	// Every stage above declares something cache-mounted, so a stage with no
	// mount at all means a primitive lost its cache rather than its sharing
	// mode. Counted per stage rather than in total because a single stage may
	// legitimately declare several — a Swift build mounts its object tree and
	// SwiftPM's download cache separately.
	blocks := strings.Split(strings.TrimSpace(out), "\n\n")
	if len(blocks) != len(f.Stages) {
		t.Fatalf("got %d stage blocks, want %d", len(blocks), len(f.Stages))
	}
	for i, block := range blocks {
		if !strings.Contains(block, "type=cache") {
			t.Errorf("stage %q emits no cache mount:\n%s", f.Stages[i].Name, block)
		}
	}
}
