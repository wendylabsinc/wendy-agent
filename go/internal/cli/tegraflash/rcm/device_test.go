//go:build darwin || linux

package rcm

import "testing"

func TestParseStateDescriptor(t *testing.T) {
	tests := []struct {
		name    string
		buf     []byte
		n       int
		want    byte
		wantErr bool
	}{
		{
			// Confirmed on live T264: state 0 (initial) is encoded as ASCII '0' = 0x30.
			name: "state 0 initial",
			buf:  []byte{0x06, 0x03, 0x30, 0x00, 0x00, 0x00},
			n:    6,
			want: 0,
		},
		{
			name: "state 5 MB2 applet running",
			buf:  []byte{0x06, 0x03, 0x35, 0x00, 0x00, 0x00},
			n:    6,
			want: 5,
		},
		{
			name: "state 8 MB2 running",
			buf:  []byte{0x06, 0x03, 0x38, 0x00, 0x00, 0x00},
			n:    6,
			want: 8,
		},
		{
			name:    "non-digit byte returns error",
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
			got, err := parseStateDescriptor(tt.buf, tt.n)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseStateDescriptor() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseStateDescriptor() = %d, want %d", got, tt.want)
			}
		})
	}
}
