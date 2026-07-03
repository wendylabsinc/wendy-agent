package flasher

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSummarizeFlashPlan_TotalBytes(t *testing.T) {
	dir := t.TempDir()
	// Two entries pushing the same image (A/B slots) plus one distinct one;
	// a missing file and the header comment must not break the total.
	if err := os.WriteFile(filepath.Join(dir, "rootfs.ext4.simg"), make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "esp.img"), make([]byte, 300), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := "# LinuxPartitionName, PartitionName, FileName, Start, Size\n" +
		"/dev/nvme0n1 A_rootfs rootfs.ext4.simg 0 1000 4 0 0 A_rootfs 4 md5 0\n" +
		"/dev/nvme0n1 B_rootfs rootfs.ext4.simg 0 1000 4 0 0 B_rootfs 4 md5 0\n" +
		"/dev/nvme0n1 esp esp.img 0 300 1 0 0 esp 4 md5 0\n" +
		"/dev/nvme0n1 gone missing.img 0 5 1 0 0 gone 4 md5 0\n"
	ftf := filepath.Join(dir, "FileToFlash.txt")
	if err := os.WriteFile(ftf, []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}

	p := summarizeFlashPlan(ftf)
	if p.count != 4 {
		t.Errorf("count = %d, want 4", p.count)
	}
	// A/B rootfs counted twice (pushed twice), esp once, missing.img skipped.
	if p.totalBytes != 2300 {
		t.Errorf("totalBytes = %d, want 2300", p.totalBytes)
	}
	if p.summary != "ESP, rootfs (A/B)" {
		t.Errorf("summary = %q", p.summary)
	}
}
