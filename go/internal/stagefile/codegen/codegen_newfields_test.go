package codegen

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

func genOne(t *testing.T, s spec.Stage, images map[string]string) string {
	t.Helper()
	if images == nil {
		images = map[string]string{s.From: "sha256:abc123"}
	}
	out, err := Generate(&spec.File{Version: 1, Stages: []spec.Stage{s}}, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return out
}

func TestGenerateEnvArgsWorkdirSortedAndQuoted(t *testing.T) {
	out := genOne(t, spec.Stage{
		Name: "app", From: "debian:12",
		Workdir: "/app",
		Args:    map[string]string{"B_ARG": "two", "A_ARG": ""},
		Env:     map[string]string{"ZETA": "z val", "ALPHA": "1"},
	}, nil)
	want := "FROM debian:12@sha256:abc123 AS app\n" +
		"ARG A_ARG\n" +
		"ARG B_ARG=\"two\"\n" +
		"ENV ALPHA=\"1\"\n" +
		"ENV ZETA=\"z val\"\n" +
		"WORKDIR /app\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestGenerateCmdAndHealthcheck(t *testing.T) {
	out := genOne(t, spec.Stage{
		Name: "app", From: "debian:12",
		Healthcheck: &spec.Healthcheck{
			Exec: []string{"curl", "-f", "http://localhost/"}, Interval: "30s", Timeout: "5s", StartPeriod: "10s", Retries: 3,
		},
		Entrypoint: &spec.Entrypoint{Exec: []string{"/entry.sh"}},
		Cmd:        []string{"bash"},
	}, nil)
	for _, want := range []string{
		`HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["curl", "-f", "http://localhost/"]`,
		`ENTRYPOINT ["/entry.sh"]`,
		`CMD ["bash"]`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestGenerateEntrypointSourceWrapper(t *testing.T) {
	out := genOne(t, spec.Stage{
		Name: "app", From: "ros:humble",
		Entrypoint: &spec.Entrypoint{Source: "/opt/ros/humble/setup.bash", Exec: []string{"ros2", "run", "demo", "talker"}},
	}, nil)
	want := `ENTRYPOINT ["/bin/bash", "-c", "source '/opt/ros/humble/setup.bash' && exec \"$@\"", "bash", "ros2", "run", "demo", "talker"]`
	if !strings.Contains(out, want) {
		t.Fatalf("missing %q in:\n%s", want, out)
	}
}

func TestGeneratePipIndexFlags(t *testing.T) {
	out := genOne(t, spec.Stage{
		Name: "app", From: "python:3.11-slim",
		Install: &spec.Install{Pip: []spec.PipInstall{{
			Packages: []string{"torch"},
			Index:    "https://pypi.jetson-ai-lab.io/jp6/cu126",
			ExtraIndex: []string{
				"https://pypi.org/simple",
			},
		}}},
	}, nil)
	want := "pip install --root '/opt/stagefile/pip/root' --index-url 'https://pypi.jetson-ai-lab.io/jp6/cu126' --extra-index-url 'https://pypi.org/simple' 'torch'"
	if !strings.Contains(out, want) {
		t.Fatalf("missing %q in:\n%s", want, out)
	}
}

func TestGenerateNpmProductionPerManager(t *testing.T) {
	cases := map[string]string{
		"npm":  "npm ci --omit=dev",
		"yarn": "yarn install --frozen-lockfile --production",
		"pnpm": "pnpm install --frozen-lockfile --prod",
	}
	for mgr, want := range cases {
		out := genOne(t, spec.Stage{
			Name: "app", From: "node:22-alpine",
			Install: &spec.Install{Npm: &spec.NpmInstall{Manager: mgr, Production: true}},
		}, nil)
		if !strings.Contains(out, want) {
			t.Fatalf("manager %s: missing %q in:\n%s", mgr, want, out)
		}
	}
}

func TestGenerateUvSync(t *testing.T) {
	out := genOne(t, spec.Stage{
		Name: "app", From: "python:3.12-slim",
		Install: &spec.Install{Uv: &spec.UvInstall{Extras: []string{"proxy"}}},
	}, nil)
	for _, want := range []string{
		"COPY pyproject.toml uv.lock ./",
		"RUN --mount=type=cache,sharing=locked,id=stagefile-uv-6e340b9cffb37a98,target=/root/.cache/uv uv sync --frozen --no-dev --extra 'proxy'",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	dev := genOne(t, spec.Stage{
		Name: "app", From: "python:3.12-slim",
		Install: &spec.Install{Uv: &spec.UvInstall{Dev: true}},
	}, nil)
	if strings.Contains(dev, "--no-dev") {
		t.Fatalf("dev: true must drop --no-dev:\n%s", dev)
	}
}

func TestGenerateAptRepositories(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	out := genOne(t, spec.Stage{
		Name: "core", From: "ubuntu:jammy",
		Install: &spec.Install{Apt: &spec.AptInstall{
			Packages: []string{"ros-humble-ros-core"},
			Repositories: []spec.AptRepository{{
				Name: "ros2", URL: "http://packages.ros.org/ros2/ubuntu",
				Suites: []string{"jammy"}, Components: []string{"main"},
				Key: spec.AptRepositoryKey{URL: "https://raw.githubusercontent.com/ros/rosdistro/master/ros.key", SHA256: sha},
			}},
		}},
	}, nil)
	for _, want := range []string{
		"install -y --no-install-recommends ca-certificates",
		"ADD --chmod=0644 --checksum=sha256:" + sha + " https://raw.githubusercontent.com/ros/rosdistro/master/ros.key /etc/apt/keyrings/ros2.gpg",
		`RUN printf '%s\n' 'deb [signed-by=/etc/apt/keyrings/ros2.gpg] http://packages.ros.org/ros2/ubuntu jammy main' > /etc/apt/sources.list.d/ros2.list`,
		"apt-get install -y --no-install-recommends 'ros-humble-ros-core'",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestGenerateApkRepositories(t *testing.T) {
	out := genOne(t, spec.Stage{
		Name: "app", From: "alpine:3.20",
		Install: &spec.Install{Apk: &spec.ApkInstall{
			Packages:     []string{"python3"},
			Repositories: []string{"https://dl-cdn.alpinelinux.org/alpine/edge/testing"},
		}},
	}, nil)
	want := `RUN printf '%s\n' 'https://dl-cdn.alpinelinux.org/alpine/edge/testing' >> /etc/apk/repositories`
	if !strings.Contains(out, want) {
		t.Fatalf("missing %q in:\n%s", want, out)
	}
}

func TestGenerateCopyOwnerAndMode(t *testing.T) {
	out := genOne(t, spec.Stage{
		Name: "app", From: "debian:12",
		Copy: []spec.CopyEntry{{From: "local", Paths: []string{"entry.sh"}, Dest: "/entry.sh", Owner: "1000:1000", Mode: "0755"}},
	}, nil)
	want := "COPY --chown=1000:1000 --chmod=0755 entry.sh /entry.sh"
	if !strings.Contains(out, want) {
		t.Fatalf("missing %q in:\n%s", want, out)
	}
}

func TestGenerateBuildProducts(t *testing.T) {
	cases := []struct {
		build *spec.Build
		want  string
	}{
		{&spec.Build{Lang: "swift", Product: "camserver"}, "-c release --product 'camserver'"},
		{&spec.Build{Lang: "rust", Product: "serve"}, "cargo build --release --bin 'serve'"},
		{&spec.Build{Lang: "go", Product: "./cmd/serve"}, "go build -o /usr/local/bin/ './cmd/serve'"},
		{&spec.Build{Lang: "npm"}, "RUN --mount=type=cache,sharing=locked,target=/root/.npm npm run 'build'"},
		{&spec.Build{Lang: "yarn", Script: "bundle"}, "RUN --mount=type=cache,sharing=locked,target=/root/.cache/yarn yarn run 'bundle'"},
	}
	for _, c := range cases {
		out := genOne(t, spec.Stage{Name: "app", From: "debian:12", Build: c.build}, nil)
		if !strings.Contains(out, c.want) {
			t.Fatalf("build %+v: missing %q in:\n%s", c.build, c.want, out)
		}
	}
}

func TestGenerateUnpinnedStage(t *testing.T) {
	no := false
	out, err := Generate(&spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "mlx-server:0.1", Pin: &no},
	}}, map[string]string{}, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(out, "FROM mlx-server:0.1 AS app") {
		t.Fatalf("unpinned FROM must have no digest:\n%s", out)
	}
}

func TestGenerateBuildPlatformStage(t *testing.T) {
	out := genOne(t, spec.Stage{Name: "ui", From: "node:22-alpine", Platform: "build"}, nil)
	if !strings.Contains(out, "FROM --platform=$BUILDPLATFORM node:22-alpine@sha256:abc123 AS ui") {
		t.Fatalf("missing $BUILDPLATFORM pin:\n%s", out)
	}
}
