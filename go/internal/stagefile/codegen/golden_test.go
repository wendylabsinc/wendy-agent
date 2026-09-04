package codegen

import (
	"os"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

// TestGenerateGoldenExampleFixture exercises install and copy together on
// the same stage — the combination no single-primitive task test ever
// covered, which is exactly how the install-before-copy ordering defect
// (fixed in this plan's addendum) survived ten individually-clean task
// reviews. It reads the real shipped fixture so a regression here means
// the project's own worked example stops building.
func TestGenerateGoldenExampleFixture(t *testing.T) {
	data, err := os.ReadFile("../testdata/example.stagefile.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f, err := spec.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	images := map[string]string{"python:3.12-slim": "sha256:abc123"}

	out, err := Generate(f, images, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "FROM python:3.12-slim@sha256:abc123 AS stagefile-pip-deps-0\n" +
		"RUN --mount=type=cache,sharing=locked,id=stagefile-apt-lists-c27f20c14e43eb07,target=/var/lib/apt/lists \\\n" +
		"    --mount=type=cache,sharing=locked,id=stagefile-apt-archives-c27f20c14e43eb07,target=/var/cache/apt \\\n" +
		"    rm -f /etc/apt/apt.conf.d/docker-clean && if command -v pip >/dev/null 2>&1; then apt-get update && apt-get install -y --no-install-recommends 'build-essential'; else apt-get update && apt-get install -y --no-install-recommends 'python3-pip' 'build-essential'; fi\n" +
		"COPY requirements.txt requirements.txt\n" +
		"RUN --mount=type=cache,sharing=locked,id=stagefile-pip-7cf413641a7e1ab6,target=/root/.cache/pip pip install --root '/opt/stagefile/pip/root' -r 'requirements.txt'\n" +
		"\n" +
		"FROM python:3.12-slim@sha256:abc123 AS deps\n" +
		"RUN --mount=type=cache,sharing=locked,id=stagefile-apt-lists-c27f20c14e43eb07,target=/var/lib/apt/lists \\\n" +
		"    --mount=type=cache,sharing=locked,id=stagefile-apt-archives-c27f20c14e43eb07,target=/var/cache/apt \\\n" +
		"    rm -f /etc/apt/apt.conf.d/docker-clean && apt-get update && apt-get install -y --no-install-recommends 'libgomp1'\n" +
		"COPY --link --from=stagefile-pip-deps-0 /opt/stagefile/pip/root/ /\n" +
		"\n" +
		"FROM python:3.12-slim@sha256:abc123 AS app\n" +
		"COPY --from=deps /usr/local/lib/python3.12/site-packages /usr/local/lib/python3.12/site-packages\n" +
		"COPY app.py app.py\n" +
		"ENTRYPOINT [\"python3\", \"app.py\"]\n" +
		"USER 65532\n"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}
