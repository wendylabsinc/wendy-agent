package services

import (
	"testing"
	"unsafe"
)

// The VIDIOC_ENUM_FRAMESIZES request code encodes the struct's size, so the
// Go struct and the constant must agree with the kernel's layout or the ioctl
// silently misbehaves (ENOTTY, or worse, a partial read into the wrong fields).
// Both numbers are hand-derived, so pin them.
//
//	struct v4l2_frmsizeenum {
//	    __u32 index;                  // 4
//	    __u32 pixel_format;           // 4
//	    __u32 type;                   // 4
//	    union { discrete(8); stepwise(24); };  // 24 — the larger arm sizes it
//	    __u32 reserved[2];            // 8
//	};                                // 44
func TestV4L2FrmSizeEnumLayout(t *testing.T) {
	if got := unsafe.Sizeof(v4l2FrmSizeEnum{}); got != 44 {
		t.Fatalf("v4l2FrmSizeEnum is %d bytes, kernel expects 44", got)
	}

	// Discrete width/height are the first two members of the union, i.e. at
	// offset 12 and 16 — right after index/pixel_format/type.
	var fse v4l2FrmSizeEnum
	if off := unsafe.Offsetof(fse.Width); off != 12 {
		t.Errorf("discrete.width at offset %d, want 12", off)
	}
	if off := unsafe.Offsetof(fse.Height); off != 16 {
		t.Errorf("discrete.height at offset %d, want 16", off)
	}
}

// _IOWR('V', 74, struct v4l2_frmsizeenum):
//
//	dir  = _IOC_READ|_IOC_WRITE = 3 -> bits 30..31
//	size = 44                       -> bits 16..29
//	type = 'V' = 0x56               -> bits 8..15
//	nr   = 74  = 0x4a               -> bits 0..7
func TestEnumFramesizesRequestCode(t *testing.T) {
	const (
		dirReadWrite = 3
		size         = 44
		typ          = 'V'
		nr           = 74
	)
	want := uint32(dirReadWrite)<<30 | uint32(size)<<16 | uint32(typ)<<8 | uint32(nr)
	if vidiocEnumFramesizes != want {
		t.Fatalf("vidiocEnumFramesizes = %#x, want %#x", uint32(vidiocEnumFramesizes), want)
	}
	if want != 0xC02C564A {
		t.Fatalf("derivation drifted: %#x", want)
	}
}

// The cap exists so a 4K webcam does not become the default for every
// subscriber that asked for "no preference".
func TestDefaultFrameSizeCapIs1080p(t *testing.T) {
	if defaultMaxDefaultWidth != 1920 || defaultMaxDefaultHeight != 1080 {
		t.Fatalf("default cap is %dx%d, want 1920x1080",
			defaultMaxDefaultWidth, defaultMaxDefaultHeight)
	}
}
