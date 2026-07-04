//go:build darwin || linux

package rcm

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/gousb"
)

func TestIsUSBAccessErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "raw gousb access error",
			err:  gousb.ErrorAccess,
			want: true,
		},
		{
			name: "wrapped gousb access error",
			err:  fmt.Errorf("claiming config: %w", gousb.ErrorAccess),
			want: true,
		},
		{
			// The exact shape gousb's macOS auto-detach path produces: a formatted
			// string that errors.Is cannot see through, so the text fallback matters.
			name: "libusb bad-access string",
			err:  errors.New("Can't detach kernel driver of the device vid=0955,pid=7026,bus=2,addr=1 and interface 0: libusb: bad access [code -3]"),
			want: true,
		},
		{
			name: "unrelated gousb error",
			err:  gousb.ErrorTimeout,
			want: false,
		},
		{
			name: "unrelated plain error",
			err:  errors.New("device missing bulk IN or OUT endpoints"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUSBAccessErr(tt.err); got != tt.want {
				t.Errorf("isUSBAccessErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// utf16Desc builds a USB string descriptor (bLength, 0x03, then UTF-16LE of s).
func utf16Desc(s string) []byte {
	b := []byte{0, 0x03}
	for i := 0; i < len(s); i++ {
		b = append(b, s[i], 0x00)
	}
	b[0] = byte(len(b))
	return b
}

func TestParseChipIDDescriptor(t *testing.T) {
	// Read from a live T264 (Thor) over macOS IOKit. The descriptor payload is the
	// BR_CID hex string reversed; un-reversed it equals the chip's BR_CID
	// (0x80012641783DE2442400000016FF80C0).
	liveT264 := utf16Desc("0C08FF6100000042442ED38714621008")

	tests := []struct {
		name    string
		buf     []byte
		n       int
		want    string
		wantErr bool
	}{
		{
			name: "live T264 BR_CID",
			buf:  liveT264,
			n:    len(liveT264),
			want: "80012641783DE2442400000016FF80C0",
		},
		{
			// reversal is the whole point: "12" stored → "21" returned
			name: "reversal",
			buf:  utf16Desc("12"),
			n:    6,
			want: "21",
		},
		{
			name:    "non-hex byte returns error",
			buf:     []byte{0x06, 0x03, 0x05, 0x00, 0x00, 0x00},
			n:       6,
			wantErr: true,
		},
		{
			name:    "n=2 too short",
			buf:     []byte{0x04, 0x03},
			n:       2,
			wantErr: true,
		},
		{
			name:    "n=0 empty read",
			buf:     make([]byte, 96),
			n:       0,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseChipIDDescriptor(tt.buf, tt.n)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseChipIDDescriptor() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseChipIDDescriptor() = %q, want %q", got, tt.want)
			}
		})
	}
}
