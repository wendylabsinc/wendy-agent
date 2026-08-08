package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

// readLayerEntries un-gzips a native layer blob and returns name → header/data
// for every tar entry, plus the raw uncompressed bytes.
func readLayerEntries(t *testing.T, blob []byte) (map[string]*tar.Header, map[string][]byte, []byte) {
	t.Helper()
	gr, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(gr)
	if err != nil {
		t.Fatal(err)
	}
	hdrs := map[string]*tar.Header{}
	data := map[string][]byte{}
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		hdrs[hdr.Name] = hdr
		data[hdr.Name] = b
	}
	return hdrs, data, raw
}

func TestBuildNativeCopyLayerFileToFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.py", "print('hello')\n")

	l, err := buildNativeCopyLayer(dir, spec.CopyEntry{From: "local", Paths: []string{"main.py"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	hdrs, data, raw := readLayerEntries(t, l.Blob)

	h, ok := hdrs["main.py"]
	if !ok {
		t.Fatalf("missing main.py entry; have %v", l.FileNames)
	}
	if string(data["main.py"]) != "print('hello')\n" {
		t.Fatal("content mismatch")
	}
	if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
		t.Fatalf("ownership must be numeric root:root, got %d:%d %q:%q", h.Uid, h.Gid, h.Uname, h.Gname)
	}
	if !h.ModTime.Equal(nativeLayerEpoch) {
		t.Fatalf("mtime must be the fixed epoch, got %v", h.ModTime)
	}
	if want := "sha256:" + sha256Hex(raw); l.DiffID != want {
		t.Fatalf("DiffID = %s, want %s", l.DiffID, want)
	}
	if want := "sha256:" + sha256Hex(l.Blob); l.Digest != want {
		t.Fatalf("Digest = %s, want %s", l.Digest, want)
	}
}

func TestBuildNativeCopyLayerDestForms(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.py", "m\n")
	writeFile(t, dir, "util.py", "u\n")

	t.Run("file into dir dest", func(t *testing.T) {
		l, err := buildNativeCopyLayer(dir, spec.CopyEntry{From: "local", Paths: []string{"main.py"}, Dest: "app/"}, "")
		if err != nil {
			t.Fatal(err)
		}
		hdrs, _, _ := readLayerEntries(t, l.Blob)
		if _, ok := hdrs["app/main.py"]; !ok {
			t.Fatalf("want app/main.py, have %v", l.FileNames)
		}
		if d, ok := hdrs["app/"]; !ok || d.Typeflag != tar.TypeDir {
			t.Fatal("missing synthesized parent dir entry app/")
		}
	})

	t.Run("multi-source forces dir dest", func(t *testing.T) {
		l, err := buildNativeCopyLayer(dir, spec.CopyEntry{From: "local", Paths: []string{"main.py", "util.py"}, Dest: "app"}, "")
		if err != nil {
			t.Fatal(err)
		}
		hdrs, _, _ := readLayerEntries(t, l.Blob)
		if _, ok := hdrs["app/main.py"]; !ok {
			t.Fatalf("want app/main.py, have %v", l.FileNames)
		}
		if _, ok := hdrs["app/util.py"]; !ok {
			t.Fatal("want app/util.py")
		}
	})

	t.Run("relative dest resolves against workdir", func(t *testing.T) {
		l, err := buildNativeCopyLayer(dir, spec.CopyEntry{From: "local", Paths: []string{"main.py"}}, "/srv")
		if err != nil {
			t.Fatal(err)
		}
		hdrs, _, _ := readLayerEntries(t, l.Blob)
		if _, ok := hdrs["srv/main.py"]; !ok {
			t.Fatalf("want srv/main.py, have %v", l.FileNames)
		}
	})
}

func TestBuildNativeCopyLayerDirSourceCopiesContents(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "srcpkg/mod.py", "mod\n")
	writeFile(t, dir, "srcpkg/sub/deep.py", "deep\n")

	l, err := buildNativeCopyLayer(dir, spec.CopyEntry{From: "local", Paths: []string{"srcpkg"}, Dest: "/app"}, "")
	if err != nil {
		t.Fatal(err)
	}
	hdrs, _, _ := readLayerEntries(t, l.Blob)
	for _, want := range []string{"app/", "app/mod.py", "app/sub/", "app/sub/deep.py"} {
		if _, ok := hdrs[want]; !ok {
			t.Fatalf("missing %q; have %v", want, l.FileNames)
		}
	}
	if _, ok := hdrs["app/srcpkg/mod.py"]; ok {
		t.Fatal("dir source must copy CONTENTS, not the dir itself")
	}
}

func TestBuildNativeCopyLayerPreservesModeAndSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes/symlinks not applicable on windows")
	}
	dir := t.TempDir()
	writeFile(t, dir, "tool/run.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(dir, "tool", "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("run.sh", filepath.Join(dir, "tool", "alias")); err != nil {
		t.Fatal(err)
	}

	l, err := buildNativeCopyLayer(dir, spec.CopyEntry{From: "local", Paths: []string{"tool"}, Dest: "/tool"}, "")
	if err != nil {
		t.Fatal(err)
	}
	hdrs, _, _ := readLayerEntries(t, l.Blob)
	if h := hdrs["tool/run.sh"]; h == nil || h.Mode&0o777 != 0o755 {
		t.Fatalf("mode not preserved: %+v", h)
	}
	if h := hdrs["tool/alias"]; h == nil || h.Typeflag != tar.TypeSymlink || h.Linkname != "run.sh" {
		t.Fatalf("symlink not preserved: %+v", h)
	}
}

func TestBuildNativeCopyLayerDeterministic(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a/x.py", "x\n")
	writeFile(t, dir, "a/y.py", "y\n")
	writeFile(t, dir, "main.py", "m\n")

	entry := spec.CopyEntry{From: "local", Paths: []string{"main.py", "a"}, Dest: "app/"}
	l1, err := buildNativeCopyLayer(dir, entry, "")
	if err != nil {
		t.Fatal(err)
	}
	l2, err := buildNativeCopyLayer(dir, entry, "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(l1.Blob, l2.Blob) {
		t.Fatal("two builds of identical inputs must be byte-identical")
	}
	if !sort.StringsAreSorted(l1.FileNames) {
		t.Fatalf("FileNames must be sorted: %v", l1.FileNames)
	}
}

// nativeDepsHash must cover every build input EXCEPT the final stage's local
// copy files: those are the app inputs whose changes the native path handles
// without buildx.
func TestNativeDepsHash(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Dockerfile.generated", "FROM python@sha256:abc\nCOPY requirements.txt requirements.txt\nRUN pip install -r requirements.txt\nCOPY main.py main.py\n")
	writeFile(t, dir, "build.stagefile.lock.yaml", "version: 1\nimages:\n  python:3.11-slim: sha256:abc\n")
	writeFile(t, dir, "requirements.txt", "mcp\n")
	writeFile(t, dir, "main.py", "print('v1')\n")
	sf := &spec.File{Version: 1, Stages: []spec.Stage{{
		Name:    "app",
		From:    "python:3.11-slim",
		Install: &spec.Install{Pip: &spec.PipInstall{Requirements: "requirements.txt"}},
		Copy:    []spec.CopyEntry{{From: "local", Paths: []string{"main.py"}}},
	}}}

	hash := func(args map[string]string, platform string) string {
		t.Helper()
		h, err := nativeDepsHash(dir, "Dockerfile.generated", platform, args, sf)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}

	base := hash(nil, "linux/arm64")
	if got := hash(nil, "linux/arm64"); got != base {
		t.Fatal("deps hash not stable")
	}
	writeFile(t, dir, "main.py", "print('v2')\n")
	if got := hash(nil, "linux/arm64"); got != base {
		t.Fatal("deps hash must ignore final-stage copy files (app inputs)")
	}
	writeFile(t, dir, "requirements.txt", "mcp\nuvicorn\n")
	afterReqs := hash(nil, "linux/arm64")
	if afterReqs == base {
		t.Fatal("deps hash unchanged after requirements.txt edit")
	}
	writeFile(t, dir, "Dockerfile.generated", "FROM python@sha256:def\n")
	afterDF := hash(nil, "linux/arm64")
	if afterDF == afterReqs {
		t.Fatal("deps hash unchanged after Dockerfile.generated edit")
	}
	if got := hash(map[string]string{"K": "V"}, "linux/arm64"); got == afterDF {
		t.Fatal("deps hash unchanged after build-arg change")
	}
	if got := hash(nil, "linux/amd64"); got == afterDF {
		t.Fatal("deps hash unchanged after platform change")
	}
}

// Local copies in NON-final stages feed deps layers and must be part of the
// deps hash (only the final stage's local copies are app inputs).
func TestNativeDepsHashIncludesNonFinalCopies(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Dockerfile.generated", "FROM base\n")
	writeFile(t, dir, "settings.conf", "cfg v1\n")
	writeFile(t, dir, "main.py", "print('v1')\n")
	sf := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "cfg", From: "python:3.11-slim", Copy: []spec.CopyEntry{{From: "local", Paths: []string{"settings.conf"}, Dest: "/etc/app/"}}},
		{Name: "app", From: "python:3.11-slim", Copy: []spec.CopyEntry{
			{From: "cfg", Paths: []string{"/etc/app"}, Dest: "/etc/app"},
			{From: "local", Paths: []string{"main.py"}},
		}},
	}}

	hash := func() string {
		t.Helper()
		h, err := nativeDepsHash(dir, "Dockerfile.generated", "linux/arm64", nil, sf)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}

	base := hash()
	writeFile(t, dir, "main.py", "print('v2')\n")
	if got := hash(); got != base {
		t.Fatal("final-stage copy file must not affect the deps hash")
	}
	writeFile(t, dir, "settings.conf", "cfg v2\n")
	if got := hash(); got == base {
		t.Fatal("non-final-stage copy file must affect the deps hash")
	}
}

func TestNativeStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, ok := loadNativeState(dir); ok {
		t.Fatal("missing state must load as not-ok")
	}
	want := nativeState{
		DepsHash:        "sha256:aaa",
		ManifestDigest:  "sha256:bbb",
		AppLayerDigests: []string{"sha256:ccc"},
	}
	if err := saveNativeState(dir, want); err != nil {
		t.Fatal(err)
	}
	got, ok := loadNativeState(dir)
	if !ok || got.DepsHash != want.DepsHash || got.ManifestDigest != want.ManifestDigest || len(got.AppLayerDigests) != 1 || got.AppLayerDigests[0] != "sha256:ccc" {
		t.Fatalf("round-trip mismatch: %+v ok=%v", got, ok)
	}

	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadNativeState(dir); ok {
		t.Fatal("corrupt state must load as not-ok")
	}
}

func TestBuildNativeCopyLayerRejectsEscapes(t *testing.T) {
	dir := t.TempDir()
	if _, err := buildNativeCopyLayer(dir, spec.CopyEntry{From: "local", Paths: []string{"../outside"}}, ""); err == nil {
		t.Fatal("want error for source escaping the context")
	}
	if _, err := buildNativeCopyLayer(dir, spec.CopyEntry{From: "local", Paths: []string{"/etc/passwd"}}, ""); err == nil {
		t.Fatal("want error for absolute source path")
	}
	if _, err := buildNativeCopyLayer(dir, spec.CopyEntry{From: "local", Paths: []string{"missing.py"}}, ""); err == nil {
		t.Fatal("want error for missing source")
	}
}
