package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

func TestAgentVersionFromManifest(t *testing.T) {
	m := &agentManifest{Latest: "2026.07.01-120000", LatestNightly: "2026.07.03-093000"}

	v, err := agentVersionFromManifest(m, false)
	if err != nil || v != "2026.07.01-120000" {
		t.Fatalf("stable: v=%q err=%v", v, err)
	}
	v, err = agentVersionFromManifest(m, true)
	if err != nil || v != "2026.07.03-093000" {
		t.Fatalf("nightly: v=%q err=%v", v, err)
	}
}

func TestAgentVersionFromManifestEmptyIsError(t *testing.T) {
	if _, err := agentVersionFromManifest(&agentManifest{}, false); err == nil {
		t.Fatal("expected error when no stable version present")
	}
	if _, err := agentVersionFromManifest(&agentManifest{Latest: "x"}, true); err == nil {
		t.Fatal("expected error when no nightly version present")
	}
}

func TestDownloadAgentFromGCS(t *testing.T) {
	payload := []byte("agent-binary-bytes")
	tgz := makeAgentTarGz(t, "wendy-agent-linux-amd64/wendy-agent", payload)
	sum := sha256.Sum256(tgz)
	checksum := hex.EncodeToString(sum[:])

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/manifest.json":
			fmt.Fprintf(w, `{"latest":"v1","versions":{"v1":{"is_nightly":false,"artifacts":{"amd64":{"path":"agent/v1/a.tar.gz","checksum":%q,"size_bytes":%d}}}}}`, checksum, len(tgz))
		case "/agent/v1/a.tar.gz":
			w.Write(tgz)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	m, err := fetchAgentManifestFrom(srv.URL)
	if err != nil {
		t.Fatalf("fetch manifest: %v", err)
	}
	bin, ver, err := downloadAgentFromGCS(srv.URL, m, "amd64", false)
	if err != nil {
		t.Fatalf("downloadAgentFromGCS: %v", err)
	}
	if ver != "v1" || !bytes.Equal(bin, payload) {
		t.Fatalf("ver=%q bin=%q", ver, bin)
	}
}

func TestDownloadAgentFromGCSMissingArch(t *testing.T) {
	m := &agentManifest{Latest: "v1", Versions: map[string]agentManifestVersion{
		"v1": {Artifacts: map[string]agentManifestArtifact{"amd64": {Path: "p"}}},
	}}
	if _, _, err := downloadAgentFromGCS("http://unused", m, "arm64", false); err == nil {
		t.Fatal("expected error for missing arch")
	}
}

func TestDownloadAgentFromGCSChecksumMismatch(t *testing.T) {
	tgz := makeAgentTarGz(t, "wendy-agent-linux-amd64/wendy-agent", []byte("x"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agent/v1/a.tar.gz" {
			w.Write(tgz)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	m := &agentManifest{Latest: "v1", Versions: map[string]agentManifestVersion{
		"v1": {Artifacts: map[string]agentManifestArtifact{"amd64": {Path: "agent/v1/a.tar.gz", Checksum: "deadbeef"}}},
	}}
	if _, _, err := downloadAgentFromGCS(srv.URL, m, "amd64", false); err == nil {
		t.Fatal("expected checksum mismatch error")
	}
}
