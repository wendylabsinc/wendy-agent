package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"testing"
)

const sampleAgentManifest = `{
  "latest": "2026.07.01-120000",
  "latest_nightly": "2026.07.03-093000",
  "versions": {
    "2026.07.03-093000": {
      "is_nightly": true,
      "artifacts": {
        "amd64": {"path": "agent/2026.07.03-093000/wendy-agent-linux-amd64-2026.07.03-093000.tar.gz", "checksum": "abc123", "size_bytes": 42},
        "arm64": {"path": "agent/2026.07.03-093000/wendy-agent-linux-arm64-2026.07.03-093000.tar.gz", "checksum": "def456", "size_bytes": 43}
      }
    },
    "2026.07.01-120000": {
      "is_nightly": false,
      "artifacts": {
        "arm64": {"path": "agent/2026.07.01-120000/wendy-agent-linux-arm64-2026.07.01-120000.tar.gz", "checksum": "aaa", "size_bytes": 10}
      }
    }
  }
}`

func TestFetchAgentManifestDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/manifest.json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(sampleAgentManifest))
	}))
	defer srv.Close()

	m, err := fetchAgentManifestFrom(srv.URL)
	if err != nil {
		t.Fatalf("fetchAgentManifestFrom: %v", err)
	}
	if m.Latest != "2026.07.01-120000" || m.LatestNightly != "2026.07.03-093000" {
		t.Fatalf("pointers: latest=%q nightly=%q", m.Latest, m.LatestNightly)
	}
	v, ok := m.Versions["2026.07.03-093000"]
	if !ok || !v.IsNightly {
		t.Fatalf("nightly version entry missing or not flagged nightly: %+v", v)
	}
	if v.Artifacts["amd64"].Path == "" || v.Artifacts["amd64"].Checksum != "abc123" || v.Artifacts["amd64"].SizeBytes != 42 {
		t.Fatalf("amd64 artifact wrong: %+v", v.Artifacts["amd64"])
	}
}

func TestFetchAgentManifest404IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := fetchAgentManifestFrom(srv.URL); err == nil {
		t.Fatal("expected error on 404, got nil")
	}
}

func makeAgentTarGz(t *testing.T, innerName string, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: innerName, Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractAgentFromTarGz(t *testing.T) {
	payload := []byte("ELF-ish-binary")
	tgz := makeAgentTarGz(t, "wendy-agent-linux-amd64/wendy-agent", payload)
	got, err := extractAgentFromTarGz(bytes.NewReader(tgz))
	if err != nil {
		t.Fatalf("extractAgentFromTarGz: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: got %q", got)
	}
}

func TestExtractAgentFromTarGzMissing(t *testing.T) {
	tgz := makeAgentTarGz(t, "some-dir/not-the-agent", []byte("x"))
	if _, err := extractAgentFromTarGz(bytes.NewReader(tgz)); err == nil {
		t.Fatal("expected error when wendy-agent absent")
	}
}
