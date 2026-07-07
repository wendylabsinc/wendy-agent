//go:build darwin || linux || windows

package commands

import (
	"strings"
	"testing"
)

func TestManifestStorage(t *testing.T) {
	// Variant shapes mirroring what each device publishes today.
	rpi5 := deviceVersion{ // dual-variant; legacy path = the nvme image
		Path: "img/nvme.img.gz", NVMEPath: "img/nvme.img.gz", SDPath: "img/sd.img.gz",
	}
	jetson := deviceVersion{ // nvme-only; legacy path = the nvme image
		Path: "img/nvme.img.zip", NVMEPath: "img/nvme.img.zip",
	}
	legacyOnly := deviceVersion{ // RPi 3/4: only the legacy (SD) image
		Path: "img/sd.img.gz", BmapPath: "img/sd.bmap",
	}

	tests := []struct {
		name       string
		v          deviceVersion
		st         StorageType
		mediaFixed bool
		override   string
		want       string
	}{
		{
			name:     "override nvme wins regardless of storage type",
			v:        rpi5,
			st:       StorageUnknown,
			override: "nvme",
			want:     "nvme",
		},
		{
			name:     "override sd wins regardless of storage type",
			v:        jetson,
			st:       StorageNVMe,
			override: "sd",
			want:     "sd",
		},
		{
			name: "real NVMe controller returns nvme without consulting manifest",
			v:    deviceVersion{}, // empty: proves StorageNVMe short-circuits
			st:   StorageNVMe,
			want: "nvme",
		},
		{
			name: "unknown storage type (built-in SD reader) returns sd",
			v:    rpi5,
			st:   StorageUnknown,
			want: "sd",
		},
		{
			// The regression: an RPi 5 SD card in a USB reader enumerates as USB
			// and reports removable media. It must resolve to the SD image.
			name: "USB drive + removable media + dual variant returns sd (RPi 5 SD reader)",
			v:    rpi5,
			st:   StorageUSB,
			want: "sd",
		},
		{
			// An RPi 5 NVMe SSD in a USB enclosure enumerates as USB but reports
			// fixed, solid-state media — not an SD card. It is bound for the NVMe
			// slot, so it must resolve to the NVMe image.
			name:       "USB drive + fixed SSD media + dual variant returns nvme (RPi 5 NVMe enclosure)",
			v:          rpi5,
			st:         StorageUSB,
			mediaFixed: true,
			want:       "nvme",
		},
		{
			// Fixed media but a legacy-only device (RPi 3/4) ships no NVMe variant,
			// so there is nothing to upgrade to — stays sd.
			name:       "USB drive + fixed media + legacy-only device returns sd",
			v:          legacyOnly,
			st:         StorageUSB,
			mediaFixed: true,
			want:       "sd",
		},
		{
			// Preserves commit 95d63153: a Jetson NVMe SSD in a USB enclosure has
			// no SD variant, so the ambiguous USB case falls through to nvme.
			name: "USB drive + device publishes only an NVMe variant returns nvme (Jetson)",
			v:    jetson,
			st:   StorageUSB,
			want: "nvme",
		},
		{
			name: "USB drive + legacy-only device returns sd (RPi 3/4)",
			v:    legacyOnly,
			st:   StorageUSB,
			want: "sd",
		},
		{
			name:     "override beats the ambiguous-USB default",
			v:        rpi5,
			st:       StorageUSB,
			override: "nvme",
			want:     "nvme",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := manifestStorage(tc.v, tc.st, tc.mediaFixed, tc.override)
			if got != tc.want {
				t.Errorf("manifestStorage(%+v, %v, fixed=%v, %q) = %q; want %q", tc.v, tc.st, tc.mediaFixed, tc.override, got, tc.want)
			}
		})
	}
}

// End-to-end guard for the boot bug: an RPi 5 (dual-variant) target on a USB
// reader must resolve to the SD image URL, never the NVMe one.
func TestManifestStorage_RPi5USBResolvesSDImage(t *testing.T) {
	dm := &deviceManifest{
		DeviceID: "raspberry-pi-5",
		Versions: map[string]deviceVersion{
			"nightly": {
				Path:     "images/rpi5/nightly/wendyos-nvme.sdimg.gz",
				NVMEPath: "images/rpi5/nightly/wendyos-nvme.sdimg.gz",
				SDPath:   "images/rpi5/nightly/wendyos-sd.sdimg.gz",
			},
		},
	}
	v := dm.Versions["nightly"]

	storage := manifestStorage(v, StorageUSB, false, "")
	if storage != "sd" {
		t.Fatalf("manifestStorage for RPi5 USB = %q; want sd", storage)
	}

	info, err := getImageInfo(dm, "nightly", storage)
	if err != nil {
		t.Fatalf("getImageInfo: %v", err)
	}
	if !strings.HasSuffix(info.DownloadURL, "wendyos-sd.sdimg.gz") {
		t.Errorf("resolved image = %q; want the sd image", info.DownloadURL)
	}
	if info.Storage != "sd" {
		t.Errorf("imageInfo.Storage = %q; want sd", info.Storage)
	}
}

// storageChoiceAmbiguous decides when to ask the user which slot a USB drive
// is for, rather than silently guessing the image variant.
func TestStorageChoiceAmbiguous(t *testing.T) {
	rpi5 := deviceVersion{Path: "n", NVMEPath: "n", SDPath: "s"} // dual-variant
	jetson := deviceVersion{Path: "n", NVMEPath: "n"}            // nvme-only
	legacyOnly := deviceVersion{Path: "s"}                       // sd-only

	cases := []struct {
		name       string
		v          deviceVersion
		st         StorageType
		mediaFixed bool
		want       bool
	}{
		{"USB + unsure media + dual variant is ambiguous", rpi5, StorageUSB, false, true},
		{"USB + detected fixed SSD is not ambiguous (auto nvme)", rpi5, StorageUSB, true, false},
		{"USB + nvme-only device is not ambiguous (no sd alternative)", jetson, StorageUSB, false, false},
		{"USB + legacy-only device is not ambiguous (no nvme alternative)", legacyOnly, StorageUSB, false, false},
		{"real NVMe controller is not ambiguous", rpi5, StorageNVMe, false, false},
		{"unknown bus is not ambiguous", rpi5, StorageUnknown, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := storageChoiceAmbiguous(tc.v, tc.st, tc.mediaFixed); got != tc.want {
				t.Errorf("storageChoiceAmbiguous(%+v, %v, %v) = %v; want %v", tc.v, tc.st, tc.mediaFixed, got, tc.want)
			}
		})
	}
}

// defaultManifestStorage drives `wendy os download`, which has no target drive.
func TestDefaultManifestStorage(t *testing.T) {
	cases := []struct {
		name string
		v    deviceVersion
		want string
	}{
		{"prefers sd when an sd variant exists", deviceVersion{SDPath: "s", NVMEPath: "n"}, "sd"},
		{"nvme when only an nvme variant exists", deviceVersion{NVMEPath: "n"}, "nvme"},
		{"sd for legacy-only devices", deviceVersion{Path: "p"}, "sd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultManifestStorage(tc.v); got != tc.want {
				t.Errorf("defaultManifestStorage(%+v) = %q; want %q", tc.v, got, tc.want)
			}
		})
	}
}
