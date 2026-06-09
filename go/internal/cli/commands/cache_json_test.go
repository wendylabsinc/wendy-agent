//go:build darwin || linux || windows

package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestCacheListJSONEmptyArray(t *testing.T) {
	setupCacheListTestEnv(t)

	output, err := executeCacheListCommand(t, []string{"--json", "cache", "list"})
	if err != nil {
		t.Fatalf("cache list --json: %v", err)
	}

	var items []map[string]any
	if err := json.Unmarshal([]byte(output), &items); err != nil {
		t.Fatalf("unmarshal output: %v\nraw=%s", err, output)
	}
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(items))
	}
	if strings.TrimSpace(output) != "[]" {
		t.Fatalf("empty JSON output = %q, want []", output)
	}
}

func TestCacheListJSONIncludesEntries(t *testing.T) {
	setupCacheListTestEnv(t)
	cacheDir, err := config.CacheDir()
	if err != nil {
		t.Fatalf("cache dir: %v", err)
	}
	writeSizedFile(t, filepath.Join(cacheDir, "template.tar"), 1536)
	writeSizedFile(t, filepath.Join(cacheDir, "os-images", "wendyos-test.img"), 2*1024*1024)

	output, err := executeCacheListCommand(t, []string{"--json", "cache", "list"})
	if err != nil {
		t.Fatalf("cache list --json: %v", err)
	}

	var items []struct {
		Name      string `json:"name"`
		Path      string `json:"path"`
		SizeBytes int64  `json:"sizeBytes"`
		Size      string `json:"size"`
	}
	if err := json.Unmarshal([]byte(output), &items); err != nil {
		t.Fatalf("unmarshal output: %v\nraw=%s", err, output)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2; raw=%s", len(items), output)
	}

	byName := make(map[string]struct {
		Path      string
		SizeBytes int64
		Size      string
	}, len(items))
	for _, item := range items {
		byName[item.Name] = struct {
			Path      string
			SizeBytes int64
			Size      string
		}{Path: item.Path, SizeBytes: item.SizeBytes, Size: item.Size}
	}

	assertCacheItem(t, byName, "template.tar", filepath.Join(cacheDir, "template.tar"), 1536, "1.5 KB")
	assertCacheItem(t, byName, "os-images/wendyos-test.img", filepath.Join(cacheDir, "os-images", "wendyos-test.img"), 2*1024*1024, "2.0 MB")
}

func TestCacheListNonInteractiveDefaultStaysPlainText(t *testing.T) {
	setupCacheListTestEnv(t)
	cacheDir, err := config.CacheDir()
	if err != nil {
		t.Fatalf("cache dir: %v", err)
	}
	writeSizedFile(t, filepath.Join(cacheDir, "template.tar"), 1536)

	output, err := executeCacheListCommand(t, []string{"cache", "list"}, func() {
		// Root command execution auto-enables jsonOutput in non-interactive
		// contexts. The cache list command should still require explicit --json.
		jsonOutput = true
	})
	if err != nil {
		t.Fatalf("cache list: %v", err)
	}
	if !strings.Contains(output, "  template.tar  (1.5 KB)") {
		t.Fatalf("plain-text output missing cache entry: %q", output)
	}
	if json.Valid([]byte(output)) {
		t.Fatalf("cache list without explicit --json produced JSON: %q", output)
	}
}

func TestOSCacheListJSONEmptyArray(t *testing.T) {
	setupCacheListTestEnv(t)

	output, err := executeCacheListCommand(t, []string{"--json", "os", "cache", "list"})
	if err != nil {
		t.Fatalf("os cache list --json: %v", err)
	}

	var items []map[string]any
	if err := json.Unmarshal([]byte(output), &items); err != nil {
		t.Fatalf("unmarshal output: %v\nraw=%s", err, output)
	}
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(items))
	}
}

func TestOSCacheListJSONUsesSharedSizeShape(t *testing.T) {
	setupCacheListTestEnv(t)
	cacheDir, err := osCacheDir()
	if err != nil {
		t.Fatalf("os cache dir: %v", err)
	}
	writeSizedFile(t, filepath.Join(cacheDir, "wendyos-test.img"), 2*1024*1024+512*1024)
	writeSizedFile(t, filepath.Join(cacheDir, "ignored-dir", "nested.img"), 1024)

	output, err := executeCacheListCommand(t, []string{"--json", "os", "cache", "list"})
	if err != nil {
		t.Fatalf("os cache list --json: %v", err)
	}

	var items []map[string]any
	if err := json.Unmarshal([]byte(output), &items); err != nil {
		t.Fatalf("unmarshal output: %v\nraw=%s", err, output)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1; raw=%s", len(items), output)
	}
	item := items[0]
	if item["name"] != "wendyos-test.img" {
		t.Fatalf("name = %v, want wendyos-test.img", item["name"])
	}
	if item["sizeBytes"] != float64(2*1024*1024+512*1024) {
		t.Fatalf("sizeBytes = %v, want %d", item["sizeBytes"], 2*1024*1024+512*1024)
	}
	if item["size"] != "2.5 MB" {
		t.Fatalf("size = %v, want 2.5 MB", item["size"])
	}
	if _, ok := item["sizeMB"]; ok {
		t.Fatalf("JSON output includes deprecated sizeMB field: %s", output)
	}
}

func setupCacheListTestEnv(t *testing.T) {
	t.Helper()

	origJSON := jsonOutput
	origDevice := deviceFlag
	t.Cleanup(func() {
		jsonOutput = origJSON
		deviceFlag = origDevice
	})
	jsonOutput = false
	deviceFlag = ""

	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "xdg-cache"))
	t.Setenv("LOCALAPPDATA", filepath.Join(base, "local-app-data"))
	t.Setenv("WENDY_ANALYTICS", "false")

	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", base)
	}
}

func executeCacheListCommand(t *testing.T, args []string, beforeExecute ...func()) (string, error) {
	t.Helper()

	root := &cobra.Command{
		Use:           "wendy",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	root.AddCommand(newCacheCmd())

	osCmd := &cobra.Command{Use: "os"}
	addOSCacheCmd(osCmd)
	root.AddCommand(osCmd)

	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)

	for _, fn := range beforeExecute {
		fn()
	}

	return captureCommandStdout(t, root.Execute)
}

func captureCommandStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	oldStdout := os.Stdout
	os.Stdout = writer
	restored := false
	restoreStdout := func() {
		if restored {
			return
		}
		_ = writer.Close()
		os.Stdout = oldStdout
		restored = true
	}
	defer restoreStdout()

	execErr := fn()
	restoreStdout()
	defer reader.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, reader); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return buf.String(), execErr
}

func writeSizedFile(t *testing.T, path string, size int) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := bytes.Repeat([]byte("x"), size)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func assertCacheItem(t *testing.T, items map[string]struct {
	Path      string
	SizeBytes int64
	Size      string
}, name, path string, sizeBytes int64, size string) {
	t.Helper()

	item, ok := items[name]
	if !ok {
		t.Fatalf("missing cache item %q", name)
	}
	if item.Path != path {
		t.Fatalf("%s path = %q, want %q", name, item.Path, path)
	}
	if item.SizeBytes != sizeBytes {
		t.Fatalf("%s sizeBytes = %d, want %d", name, item.SizeBytes, sizeBytes)
	}
	if item.Size != size {
		t.Fatalf("%s size = %q, want %q", name, item.Size, size)
	}
}
