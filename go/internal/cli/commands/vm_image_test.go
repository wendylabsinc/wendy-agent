package commands

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
)

func TestVMImageCacheTracksBytesNotMutableVersion(t *testing.T) {
	dir := t.TempDir()
	old := downloadVMImage
	t.Cleanup(func() { downloadVMImage = old })
	content, downloads := "first build", 0
	downloadVMImage = func(info *imageInfo) (string, error) {
		if info.DownloadURL != "https://example.test/pr/image.zst" {
			t.Fatal(info.DownloadURL)
		}
		downloads++
		f, err := os.CreateTemp(dir, "download-")
		if err != nil {
			return "", err
		}
		_, err = f.WriteString(content)
		_ = f.Close()
		return f.Name(), err
	}
	digest := func(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
	info := &imageInfo{Version: "pr-1834", ZstURL: "https://example.test/pr/image.zst", ZstChecksum: digest(content)}
	first, done, err := resolveVMImageIn(dir, info)
	if err != nil {
		t.Fatal(err)
	}
	done()
	if _, done, err := resolveVMImageIn(dir, info); err != nil {
		t.Fatal(err)
	} else {
		done()
	}
	if downloads != 1 {
		t.Fatal("verified cache hit downloaded again")
	}
	content = "second build"
	info.ZstChecksum = digest(content)
	second, done, err := resolveVMImageIn(dir, info)
	if err != nil {
		t.Fatal(err)
	}
	done()
	if first == second || downloads != 2 {
		t.Fatal("mutable PR reused old bytes")
	}
	if err := os.WriteFile(second, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, done, err := resolveVMImageIn(dir, info); err != nil {
		t.Fatal(err)
	} else {
		done()
	}
	if downloads != 3 {
		t.Fatal("corrupt cache not redownloaded")
	}
	info.ZstChecksum = digest("manifest ahead of artifact")
	if _, _, err := resolveVMImageIn(dir, info); err == nil {
		t.Fatal("accepted mismatched download")
	}
	info.ZstChecksum = ""
	for range 2 {
		path, done, err := resolveVMImageIn(dir, info)
		if err != nil {
			t.Fatal(err)
		}
		done()
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("temporary image survived cleanup")
		}
	}
	if downloads != 6 {
		t.Fatal("checksum-free image was cached")
	}
	info.ZstChecksum = "../../escape"
	if _, _, err := resolveVMImageIn(dir, info); err == nil {
		t.Fatal("accepted invalid digest")
	}
}

func TestVMImageChecksumFollowsArtifactStorage(t *testing.T) {
	dm := &deviceManifest{Versions: map[string]deviceVersion{"v": {
		Path: "legacy.zip", Checksum: "legacy", ZstPath: "legacy.zst", ZstChecksum: "legacy-zst",
		NVMEPath: "nvme.zip", NVMEChecksum: "nvme", NVMEZstPath: "nvme.zst", NVMEZstChecksum: "nvme-zst",
	}}}
	for _, tc := range []struct{ storage, checksum string }{{"nvme", "nvme"}, {"sd", "legacy"}, {"", "legacy"}} {
		info, err := getImageInfo(dm, "v", tc.storage)
		if err != nil {
			t.Fatal(err)
		}
		if info.Checksum != tc.checksum || info.ZstChecksum != tc.checksum+"-zst" {
			t.Fatalf("wrong triple: %+v", info)
		}
	}
}
