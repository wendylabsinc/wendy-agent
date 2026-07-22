package flashengine

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFTF(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "FileToFlash.txt")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseFileToFlash(t *testing.T) {
	// Real 12-column rows (device, name, file, start, size, _, resize, _, _, _, md5, _),
	// plus a comment and a blank line that must be skipped.
	const content = `# header comment

/dev/block/810c5b0000.spi bad-page badpage.bin 65863680 524288 2 0 0 bad-page 4 c83c0716bf44fd36d9b20da87d05254f 0
/dev/nvme0n1 APP wendyos-image.ext4.simg 2469429248 6442450944 x 1 x x d41d8cd98f00b204e9800998ecf8427e x x
`
	parts, err := parseFileToFlash(writeFTF(t, content))
	if err != nil {
		t.Fatalf("parseFileToFlash: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("got %d partitions, want 2", len(parts))
	}

	bp := parts[0]
	if bp.Device != "/dev/block/810c5b0000.spi" || bp.Name != "bad-page" || bp.FileName != "badpage.bin" {
		t.Fatalf("bad-page fields wrong: %+v", bp)
	}
	if bp.Start != 65863680 || bp.Size != 524288 || bp.Resize != 0 {
		t.Fatalf("bad-page numbers wrong: %+v", bp)
	}
	if bp.MD5 != "c83c0716bf44fd36d9b20da87d05254f" {
		t.Fatalf("bad-page md5 wrong: %q", bp.MD5)
	}

	app := parts[1]
	if app.Device != "/dev/nvme0n1" || app.Name != "APP" || app.Start != 2469429248 || app.Size != 6442450944 || app.Resize != 1 {
		t.Fatalf("APP fields wrong: %+v", app)
	}
}

func TestParseFileToFlashRejectsShortRow(t *testing.T) {
	// A row with fewer than 12 columns must be rejected, not silently misassigned
	// to Device/Name/Start/... (which would target the wrong device or offset).
	const content = "/dev/nvme0n1 APP app.img 512 1024 x 1\n"
	if _, err := parseFileToFlash(writeFTF(t, content)); err == nil {
		t.Fatal("expected an error for a short (<12 column) row")
	}
}

func TestParseFileToFlashMissingFile(t *testing.T) {
	if _, err := parseFileToFlash(filepath.Join(t.TempDir(), "nope.txt")); err == nil {
		t.Fatal("expected an error for a missing FileToFlash.txt")
	}
}
