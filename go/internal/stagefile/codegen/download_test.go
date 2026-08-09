package codegen

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

var (
	testDigest  = "sha256:" + strings.Repeat("ab", 32)
	testDigest2 = "sha256:" + strings.Repeat("cd", 32)
	baseImages  = map[string]string{"debian:12": "sha256:abc123"}
)

func generateStage(t *testing.T, s spec.Stage, downloads map[string]string) string {
	t.Helper()
	out, err := Generate(&spec.File{Version: 1, Stages: []spec.Stage{s}}, baseImages, downloads, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return out
}

func TestDownloadEmitsPinnedAdd(t *testing.T) {
	out := generateStage(t, spec.Stage{
		Name: "app", From: "debian:12",
		Download: []spec.Download{{
			URL:    "https://example.com/model.onnx",
			SHA256: strings.TrimPrefix(testDigest, "sha256:"),
			Dest:   "/app/model.onnx",
			Mode:   "0644",
			Owner:  "1000:1000",
		}},
	}, nil)

	want := "ADD --chown=1000:1000 --chmod=0644 --checksum=" + testDigest +
		" https://example.com/model.onnx /app/model.onnx"
	if !strings.Contains(out, want) {
		t.Fatalf("want line:\n%s\ngot:\n%s", want, out)
	}
}

func TestDownloadUsesLockedDigestWhenSourceHasNone(t *testing.T) {
	out := generateStage(t, spec.Stage{
		Name: "app", From: "debian:12",
		Download: []spec.Download{{URL: "https://example.com/model.onnx", Dest: "/app/model.onnx"}},
	}, map[string]string{"https://example.com/model.onnx": testDigest})

	if !strings.Contains(out, "--checksum="+testDigest+" https://example.com/model.onnx /app/model.onnx") {
		t.Fatalf("locked digest not used:\n%s", out)
	}
}

// An unpinned download must be unrepresentable in the output: silently
// emitting a plain ADD would produce an image whose contents nothing verified.
func TestDownloadWithoutAnyDigestIsAnError(t *testing.T) {
	_, err := Generate(&spec.File{Version: 1, Stages: []spec.Stage{{
		Name: "app", From: "debian:12",
		Download: []spec.Download{{URL: "https://example.com/model.onnx", Dest: "/app/model.onnx"}},
	}}}, baseImages, nil, "", nil)
	if err == nil {
		t.Fatal("expected an error for a download with no sha256 anywhere")
	}
	if !strings.Contains(err.Error(), "https://example.com/model.onnx") {
		t.Fatalf("error must name the url, got: %v", err)
	}
}

func TestDownloadDigestAcceptsEitherPrefixForm(t *testing.T) {
	withPrefix := generateStage(t, spec.Stage{
		Name: "app", From: "debian:12",
		Download: []spec.Download{{URL: "https://example.com/a", SHA256: testDigest, Dest: "/a"}},
	}, nil)
	without := generateStage(t, spec.Stage{
		Name: "app", From: "debian:12",
		Download: []spec.Download{{URL: "https://example.com/a", SHA256: strings.TrimPrefix(testDigest, "sha256:"), Dest: "/a"}},
	}, nil)
	if withPrefix != without {
		t.Fatalf("a sha256: prefix must not change the output:\n%s\n---\n%s", withPrefix, without)
	}
	if strings.Contains(withPrefix, "sha256:sha256:") {
		t.Fatalf("prefix doubled:\n%s", withPrefix)
	}
}

// The ordering property the whole feature is arranged around: fetching is the
// most stable step and goes first, unpacking needs installed tools and goes
// after them.
func TestDownloadFetchesBeforeInstallAndExtractsAfter(t *testing.T) {
	out := generateStage(t, spec.Stage{
		Name: "app", From: "debian:12",
		Install: &spec.Install{Apt: &spec.AptInstall{Packages: []string{"unzip"}}},
		Download: []spec.Download{{
			URL: "https://example.com/tool.zip", SHA256: testDigest,
			Dest: "/usr/local", Extract: "zip",
		}},
		Copy: []spec.CopyEntry{{From: "local", Paths: []string{"app.py"}}},
	}, nil)

	addAt := strings.Index(out, "ADD ")
	aptAt := strings.Index(out, "apt-get update")
	unzipAt := strings.Index(out, "unzip -q")
	copyAt := strings.Index(out, "COPY app.py")
	if addAt < 0 || aptAt < 0 || unzipAt < 0 || copyAt < 0 {
		t.Fatalf("missing an expected line:\n%s", out)
	}
	if !(addAt < aptAt && aptAt < unzipAt && unzipAt < copyAt) {
		t.Fatalf("want ADD < apt < unzip < COPY, got %d %d %d %d:\n%s", addAt, aptAt, unzipAt, copyAt, out)
	}
}

func TestDownloadExtractStagesThenUnpacksAndRemoves(t *testing.T) {
	out := generateStage(t, spec.Stage{
		Name: "app", From: "debian:12",
		Download: []spec.Download{
			{URL: "https://example.com/model.onnx", SHA256: testDigest, Dest: "/app/model.onnx"},
			{URL: "https://example.com/tool.tar.gz", SHA256: testDigest2, Dest: "/usr/local", Extract: "tar.gz"},
		},
	}, nil)

	staged := "/tmp/stagefile-download-1.tar.gz"
	if !strings.Contains(out, "--checksum="+testDigest2+" https://example.com/tool.tar.gz "+staged) {
		t.Fatalf("archive must land on its indexed staging path:\n%s", out)
	}
	want := "RUN mkdir -p '/usr/local' && tar -xzf '" + staged + "' -C '/usr/local' && rm '" + staged + "'"
	if !strings.Contains(out, want) {
		t.Fatalf("want:\n%s\ngot:\n%s", want, out)
	}
	// The non-archive entry keeps its real destination and gets no RUN.
	if !strings.Contains(out, "https://example.com/model.onnx /app/model.onnx") {
		t.Fatalf("plain download must go straight to its dest:\n%s", out)
	}
	if strings.Count(out, "RUN mkdir -p") != 1 {
		t.Fatalf("only the archive should produce an unpack step:\n%s", out)
	}
}

func TestDownloadZipUsesUnzip(t *testing.T) {
	out := generateStage(t, spec.Stage{
		Name: "app", From: "debian:12",
		Download: []spec.Download{{URL: "https://example.com/t.zip", SHA256: testDigest, Dest: "/opt/t", Extract: "zip"}},
	}, nil)
	staged := "/tmp/stagefile-download-0.zip"
	want := "RUN mkdir -p '/opt/t' && unzip -q '" + staged + "' -d '/opt/t' && rm '" + staged + "'"
	if !strings.Contains(out, want) {
		t.Fatalf("want:\n%s\ngot:\n%s", want, out)
	}
}

// Staging paths are indexed by position, never generated: the same source has
// to compile to the same bytes on every run.
func TestDownloadStagingPathIsDeterministic(t *testing.T) {
	s := spec.Stage{
		Name: "app", From: "debian:12",
		Download: []spec.Download{{URL: "https://example.com/t.zip", SHA256: testDigest, Dest: "/opt/t", Extract: "zip"}},
	}
	if a, b := generateStage(t, s, nil), generateStage(t, s, nil); a != b {
		t.Fatalf("two compiles of one source differ:\n%s\n---\n%s", a, b)
	}
}

func TestDownloadWorksOnANonFinalStage(t *testing.T) {
	out, err := Generate(&spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "models", From: "debian:12", Download: []spec.Download{
			{URL: "https://example.com/model.onnx", SHA256: testDigest, Dest: "/models/model.onnx"},
		}},
		{Name: "app", From: "debian:12", Copy: []spec.CopyEntry{
			{From: "models", Paths: []string{"/models"}},
		}},
	}}, baseImages, nil, "", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(out, "ADD --checksum="+testDigest) {
		t.Fatalf("download missing from the builder stage:\n%s", out)
	}
	if !strings.Contains(out, "COPY --from=models /models /models") {
		t.Fatalf("cross-stage copy missing:\n%s", out)
	}
}
