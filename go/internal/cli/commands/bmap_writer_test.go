package commands

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateBmapSourceRejectsOutsideCache(t *testing.T) {
	if err := validateBmapSource("/etc/passwd"); err == nil {
		t.Fatal("expected rejection of path outside cache root")
	}
}

func TestValidateBmapSourceAcceptsInsideCache(t *testing.T) {
	dir, err := osCacheDir()
	if err != nil {
		t.Skipf("no cache dir: %v", err)
	}
	p := filepath.Join(dir, "jetson", "1.0.0", "image.img.zst")
	if err := validateBmapSource(p); err != nil {
		t.Fatalf("expected acceptance inside cache root, got %v", err)
	}
}

func TestDownloadBmap(t *testing.T) {
	const body = `<bmap version="2.0"></bmap>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	dst := t.TempDir() + "/x.bmap"
	if err := downloadBmap(srv.URL, dst); err != nil {
		t.Fatalf("downloadBmap: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

func TestDownloadBmapHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if err := downloadBmap(srv.URL, t.TempDir()+"/x.bmap"); err == nil {
		t.Fatal("expected error on 404")
	}
}

func TestRunBmapWriteToFile(t *testing.T) {
	img, b := buildSparseImage() // from bmap_test.go (same package)
	dir := t.TempDir()
	bmapPath := dir + "/img.bmap"
	if err := os.WriteFile(bmapPath, []byte(bmapXML(b)), 0o644); err != nil {
		t.Fatalf("write bmap: %v", err)
	}
	devPath := dir + "/device.img"
	if err := os.WriteFile(devPath, make([]byte, b.ImageSize), 0o644); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if err := runBmapWrite(devPath, bmapPath, bytes.NewReader(img)); err != nil {
		t.Fatalf("runBmapWrite: %v", err)
	}
	got, _ := os.ReadFile(devPath)
	if !bytes.Equal(got, img) {
		t.Fatal("device content does not match image")
	}
}

func TestBmapWriteCommandIsHidden(t *testing.T) {
	root := NewRootCmd()
	c, _, err := root.Find([]string{"__bmap-write"})
	if err != nil {
		t.Fatalf("find __bmap-write: %v", err)
	}
	if c.Name() != "__bmap-write" || !c.Hidden {
		t.Fatalf("__bmap-write should be a hidden command; got name=%q hidden=%v", c.Name(), c.Hidden)
	}
}

// bmapXML renders a minimal valid bmap document for b (sha256, blocks in order).
func bmapXML(b *Bmap) string {
	var sb bytes.Buffer
	fmt.Fprintf(&sb, `<bmap version="2.0"><ImageSize>%d</ImageSize><BlockSize>%d</BlockSize>`,
		b.ImageSize, b.BlockSize)
	sb.WriteString(`<ChecksumType>sha256</ChecksumType><BlockMap>`)
	for _, r := range b.Ranges {
		fmt.Fprintf(&sb, `<Range chksum="%s">%d-%d</Range>`, r.Checksum, r.First, r.Last)
	}
	sb.WriteString(`</BlockMap></bmap>`)
	return sb.String()
}
