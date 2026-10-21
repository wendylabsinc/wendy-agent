package commands

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadArtifactToTempNoninteractive(t *testing.T) {
	for _, tc := range []struct {
		name        string
		interactive bool
		json        bool
		chunked     bool
	}{
		{name: "no terminal"},
		{name: "unknown content length", chunked: true},
		{name: "JSON on terminal", interactive: true, json: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateArtifactDownload(t, tc.interactive, tc.json)
			const artifact = "test artifact bytes"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.chunked {
					w.(http.Flusher).Flush()
				}
				fmt.Fprint(w, artifact)
			}))
			defer server.Close()

			path, err := downloadArtifactToTemp(server.URL + "/image.wendy?token=test")
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(path)
			if filepath.Ext(path) != ".wendy" {
				t.Fatalf("artifact suffix lost: %s", path)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != artifact {
				t.Fatalf("artifact = %q, err = %v", data, err)
			}
		})
	}
}

func TestDownloadArtifactToTempFailureCleanup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		short  bool
		want   string
	}{
		{name: "HTTP failure", status: http.StatusNotFound, want: "status 404"},
		{name: "truncated download", status: http.StatusOK, short: true, want: "unexpected EOF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateArtifactDownload(t, false, false)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.short {
					w.Header().Set("Content-Length", "100")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, "short")
			}))
			defer server.Close()

			path, err := downloadArtifactToTemp(server.URL + "/image.wendy")
			if err == nil || !strings.Contains(err.Error(), tc.want) || path != "" {
				t.Fatalf("path = %q, err = %v; want %q", path, err, tc.want)
			}
			cacheDir, err := osCacheDir()
			if err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(cacheDir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("partial artifacts left: %v, err = %v", entries, err)
			}
		})
	}
}

func isolateArtifactDownload(t *testing.T, interactive, json bool) {
	t.Helper()
	// Isolate UserCacheDir on macOS, Linux and Windows. Never use the
	// developer's cache for either success or failure-cleanup checks.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("LocalAppData", t.TempDir())
	originalTerminal, originalJSON := isInteractiveTerminalFn, jsonOutput
	t.Cleanup(func() {
		isInteractiveTerminalFn, jsonOutput = originalTerminal, originalJSON
	})
	isInteractiveTerminalFn = func() bool { return interactive }
	jsonOutput = json
}
