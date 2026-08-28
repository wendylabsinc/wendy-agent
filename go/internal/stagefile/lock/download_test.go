package lock

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestResolveDownloadsPinsEachURLOnce(t *testing.T) {
	f := &File{Version: 1, Images: map[string]string{}}
	var calls atomic.Int32
	hasher := func(url string) (string, error) {
		calls.Add(1)
		return "sha256:" + strings.Repeat("ab", 32), nil
	}

	pending, err := f.ResolveDownloads([]string{"https://example.com/a", "https://example.com/a"}, nil, hasher)
	if err != nil {
		t.Fatalf("ResolveDownloads: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("a duplicate url is one lookup, got %+v", pending)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("hasher called %d times, want 1", got)
	}
	if f.Downloads["https://example.com/a"] == "" {
		t.Fatal("pin not recorded")
	}
}

func TestResolveDownloadsKeepsAnExistingPin(t *testing.T) {
	pinned := "sha256:" + strings.Repeat("ab", 32)
	f := &File{Version: 1, Downloads: map[string]string{"https://example.com/a": pinned}}

	pending, err := f.ResolveDownloads([]string{"https://example.com/a"}, nil,
		func(string) (string, error) { return "", fmt.Errorf("must not be called") })
	if err != nil {
		t.Fatalf("ResolveDownloads: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("an already-pinned url needs no lookup, got %+v", pending)
	}
	if f.Downloads["https://example.com/a"] != pinned {
		t.Fatal("existing pin was overwritten")
	}
}

func TestResolveDownloadsForceUpdateRepins(t *testing.T) {
	old := "sha256:" + strings.Repeat("ab", 32)
	fresh := "sha256:" + strings.Repeat("cd", 32)
	f := &File{Version: 1, Downloads: map[string]string{"https://example.com/a": old}}

	_, err := f.ResolveDownloads([]string{"https://example.com/a"},
		map[string]bool{"https://example.com/a": true},
		func(string) (string, error) { return fresh, nil })
	if err != nil {
		t.Fatalf("ResolveDownloads: %v", err)
	}
	if f.Downloads["https://example.com/a"] != fresh {
		t.Fatalf("forced update did not repin, got %q", f.Downloads["https://example.com/a"])
	}
}

// The same property image resolution has: whichever lookup fails first in
// wall-clock time, the error reported is the first one in file order, so a
// broken Stagefile produces the same message every run.
func TestResolveDownloadsReportsFirstFailureInDeclarationOrder(t *testing.T) {
	f := &File{Version: 1}
	_, err := f.ResolveDownloads(
		[]string{"https://example.com/a", "https://example.com/b"}, nil,
		func(url string) (string, error) { return "", fmt.Errorf("boom %s", url) })
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "https://example.com/a") {
		t.Fatalf("want the first url in declaration order, got: %v", err)
	}
}

func TestResolveCarriesExistingDownloadPinsForward(t *testing.T) {
	pinned := "sha256:" + strings.Repeat("ab", 32)
	existing := &File{Version: 1,
		Images:    map[string]string{"debian:12": "sha256:abc"},
		Downloads: map[string]string{"https://example.com/a": pinned}}

	updated, _, err := Resolve(existing, "hash", []string{"debian:12"}, nil,
		func(string) (string, error) { return "", fmt.Errorf("must not be called") })
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if updated.Downloads["https://example.com/a"] != pinned {
		t.Fatalf("image resolution dropped the download pins: %+v", updated.Downloads)
	}
}

func TestResolveLeavesDownloadsNilWhenThereAreNone(t *testing.T) {
	updated, _, err := Resolve(nil, "hash", []string{"debian:12"}, nil,
		func(string) (string, error) { return "sha256:abc", nil })
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// omitempty plus a nil map is what keeps every existing lockfile in the
	// repo from growing an empty `downloads:` key on its next build.
	if updated.Downloads != nil {
		t.Fatalf("want nil, got %+v", updated.Downloads)
	}
}

func TestHTTPHasherHashesTheBody(t *testing.T) {
	body := []byte("the bytes a model would be")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	got, err := HTTPHasher(srv.URL + "/model.onnx")
	if err != nil {
		t.Fatalf("HTTPHasher: %v", err)
	}
	sum := sha256.Sum256(body)
	want := "sha256:" + hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A 404 page hashes as cleanly as a model does. Without the status check the
// build would pin successfully and fail later, somewhere else.
func TestHTTPHasherRejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := HTTPHasher(srv.URL + "/missing.onnx")
	if err == nil {
		t.Fatal("expected an error for 404")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "missing.onnx") {
		t.Fatalf("error must name the status and the url, got: %v", err)
	}
}

func TestHTTPHasherFailsOnATruncatedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.Write([]byte("short"))
	}))
	defer srv.Close()

	if _, err := HTTPHasher(srv.URL + "/model.onnx"); err == nil {
		t.Fatal("a body that ends early must not produce a digest")
	}
}

func TestHTTPHasherFollowsRedirects(t *testing.T) {
	body := []byte("released asset bytes")
	var origin *httptest.Server
	origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			w.Write(body)
			return
		}
		http.Redirect(w, r, origin.URL+"/final", http.StatusFound)
	}))
	defer origin.Close()

	got, err := HTTPHasher(origin.URL + "/download")
	if err != nil {
		t.Fatalf("HTTPHasher: %v", err)
	}
	sum := sha256.Sum256(body)
	if want := "sha256:" + hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("got %q, want %q — release URLs redirect to a CDN", got, want)
	}
}
