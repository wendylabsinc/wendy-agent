package commands

import (
	"encoding/json"
	"testing"
)

func TestGetImageInfoResolvesBmapURL(t *testing.T) {
	const body = `{
		"device_id": "raspberry-pi-5",
		"versions": {
			"0.11.0": {
				"path": "images/rpi5/0.11.0/wendyos.img.gz",
				"size_bytes": 123,
				"checksum": "abc",
				"bmap_path": "images/rpi5/0.11.0/wendyos.img.bmap"
			}
		}
	}`
	var dm deviceManifest
	if err := json.Unmarshal([]byte(body), &dm); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	info, err := getImageInfo(&dm, "0.11.0", "")
	if err != nil {
		t.Fatalf("getImageInfo: %v", err)
	}
	want := gcsBaseURL + "/images/rpi5/0.11.0/wendyos.img.bmap"
	if info.BmapURL != want {
		t.Fatalf("BmapURL = %q, want %q", info.BmapURL, want)
	}
}

func TestGetImageInfoNoBmap(t *testing.T) {
	dm := deviceManifest{Versions: map[string]deviceVersion{
		"1.0.0": {Path: "p/x.img", SizeBytes: 1},
	}}
	info, err := getImageInfo(&dm, "1.0.0", "")
	if err != nil {
		t.Fatalf("getImageInfo: %v", err)
	}
	if info.BmapURL != "" {
		t.Fatalf("BmapURL = %q, want empty", info.BmapURL)
	}
}
