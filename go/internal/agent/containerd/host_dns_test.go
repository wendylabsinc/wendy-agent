package containerd

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

func TestResolvedStubAvailable(t *testing.T) {
	statOK := func(string) (os.FileInfo, error) { return nil, nil }
	dialOK := func() (io.Closer, error) { return io.NopCloser(strings.NewReader("")), nil }

	if !resolvedStubAvailable(statOK, dialOK) {
		t.Fatal("existing stub config plus accepting listener should be available")
	}

	dialled := false
	if resolvedStubAvailable(
		func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		func() (io.Closer, error) {
			dialled = true
			return nil, nil
		},
	) {
		t.Fatal("missing stub config must be unavailable")
	}
	if dialled {
		t.Fatal("missing stub config should short-circuit before dialing loopback")
	}

	if resolvedStubAvailable(statOK, func() (io.Closer, error) {
		return nil, errors.New("connection refused")
	}) {
		t.Fatal("non-listening stub must be unavailable")
	}
}

func TestWriteHostResolvConfIn(t *testing.T) {
	dir := t.TempDir()
	path, err := writeHostResolvConfIn(dir)
	if err != nil {
		t.Fatalf("writeHostResolvConfIn: %v", err)
	}
	if want := filepath.Join(dir, hostResolvConfName); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != hostResolvConfContent {
		t.Fatalf("resolv.conf = %q, want %q", data, hostResolvConfContent)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("mode = %o, want 644", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != hostResolvConfName {
		t.Fatalf("unexpected files after atomic write: %v", entries)
	}
}

func TestRecreateHostResolvConfIn(t *testing.T) {
	t.Run("recreates the exact managed mount source", func(t *testing.T) {
		dir := t.TempDir()
		path, err := writeHostResolvConfIn(dir)
		if err != nil {
			t.Fatalf("writeHostResolvConfIn: %v", err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		mounts := []specs.Mount{{
			Destination: "/etc/resolv.conf",
			Source:      path,
			Type:        "bind",
		}}
		ok, err := recreateHostResolvConfIn(dir, mounts)
		if err != nil {
			t.Fatalf("recreateHostResolvConfIn: %v", err)
		}
		if !ok {
			t.Fatal("managed mount was not recognized")
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != hostResolvConfContent {
			t.Fatalf("recreated resolv.conf = %q, err %v", data, err)
		}
	})

	t.Run("ignores unrelated resolv mounts", func(t *testing.T) {
		dir := t.TempDir()
		mounts := []specs.Mount{{
			Destination: "/etc/resolv.conf",
			Source:      filepath.Join(dir+"-other", hostResolvConfName),
			Type:        "bind",
		}}
		ok, err := recreateHostResolvConfIn(dir, mounts)
		if err != nil {
			t.Fatalf("recreateHostResolvConfIn: %v", err)
		}
		if ok {
			t.Fatal("unrelated resolver mount was treated as managed")
		}
		if _, err := os.Stat(filepath.Join(dir, hostResolvConfName)); !os.IsNotExist(err) {
			t.Fatalf("managed path should not be written, stat err = %v", err)
		}
	})
}
