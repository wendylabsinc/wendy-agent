package tui

import "testing"

func TestSniffProgressDetail(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string // "" means: no sniffer should match
	}{
		{"swiftpm", "[525/1027] Compiling WendyKit Foo.swift", "[525/1027] 51%  Compiling WendyKit Foo.swift"},
		{"swiftpm start", "[0/1027] Planning build", "[0/1027] 0%  Planning build"},
		{"ninja", "[12/40] Building CXX object foo.o", "[12/40] 30%  Building CXX object foo.o"},
		{"cmake percent", "[ 42%] Building CXX object src/a.cpp.o", "42%  Building CXX object src/a.cpp.o"},
		{"pip bar", "  ━━━━━━━ 128.0/797.3 MB 9.4 MB/s eta 0:01:11", "16%  128.0/797.3 MB  9.4 MB/s"},
		{"pip collecting", "Collecting debugpy", "collecting debugpy"},
		{"pip downloading verb", "  Downloading numpy-2.5.2.whl.metadata (6.6 kB)", "downloading numpy-2.5.2.whl.metadata (6.6 kB)"},
		{"pip downloading", "Downloading torch-2.4.0.whl (797.3 MB)", "downloading torch-2.4.0.whl (797.3 MB)"},
		{"apt get", "Get:5 http://deb.debian.org/debian bookworm/main arm64 libfoo 1.2-3 [1,234 kB]", "fetching bookworm/main arm64 libfoo 1.2-3 [1,234 kB]"},
		{"apt fetched", "Fetched 45.6 MB in 3s (15.2 MB/s)", "fetched 45.6 MB (15.2 MB/s)"},
		{"wget", " 50%[=====>      ] 1,234,567   1.23MB/s", "50%  1,234,567  1.23MB/s"},
		{"ansi stripped", "\x1b[32m[3/4] Compiling\x1b[0m", "[3/4] 75%  Compiling"},
		{"plain chatter", "note: this is just a warning", ""},
		{"counter out of range", "[9/4] weird", ""},
		{"empty", "   ", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := sniffProgressDetail(tc.line)
			if tc.want == "" {
				if ok {
					t.Fatalf("want no match, got %q", got)
				}
				return
			}
			if !ok {
				t.Fatalf("want %q, got no match", tc.want)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSniffProgressDetailTruncatesOnRuneBoundaries(t *testing.T) {
	// A long multi-byte tail must not be cut mid-rune: a broken UTF-8 sequence
	// corrupts the live terminal redraw.
	line := "[1/2] Compiling " + repeat("é", 200)
	got, ok := sniffProgressDetail(line)
	if !ok {
		t.Fatal("want a match")
	}
	if !utf8Valid(got) {
		t.Fatalf("truncated detail is not valid UTF-8: %q", got)
	}
	if n := runeLen(got); n > maxDetailLen {
		t.Fatalf("detail is %d runes, want <= %d", n, maxDetailLen)
	}
}

func TestByteProgressString(t *testing.T) {
	tests := []struct {
		name string
		b    ByteProgress
		want string
	}{
		{"full", ByteProgress{Current: 5_240_000, Total: 27_090_000, Rate: 3_100_000}, "19%  5.2MB/27.1MB  3.1MB/s"},
		{"no total", ByteProgress{Current: 1500}, "1.5kB"},
		{"no rate", ByteProgress{Current: 500, Total: 1000}, "50%  500B/1.0kB"},
		{"over 100 percent clamps", ByteProgress{Current: 20, Total: 10}, "100%  20B/10B"},
		{"empty", ByteProgress{}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.b.String(); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatProgressBytes(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0B"},
		{999, "999B"},
		{1000, "1.0kB"},
		{1_500_000, "1.5MB"},
		{2_700_000_000, "2.7GB"},
		{5_000_000_000_000, "5.0TB"},
		{-1, "0B"},
	}
	for _, tc := range tests {
		if got := formatProgressBytes(tc.n); got != tc.want {
			t.Errorf("formatProgressBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestSniffTransferBytes(t *testing.T) {
	got, ok := sniffTransferBytes("sha256:abc 5.24MB / 27.09MB 1.2s")
	if !ok {
		t.Fatal("want a match")
	}
	if got.Current != 5_240_000 || got.Total != 27_090_000 {
		t.Fatalf("got %+v", got)
	}
	if _, ok := sniffTransferBytes("resolve docker.io/library/alpine done"); ok {
		t.Fatal("want no match for a non-transfer sub-status")
	}
}

func TestShortImageRef(t *testing.T) {
	tests := map[string]string{
		"nvcr.io/nvidia/l4t-base:r36.2@sha256:abcdef": "nvidia/l4t-base:r36.2",
		"docker.io/library/alpine:3.20":               "library/alpine:3.20",
		"alpine:3.20":                                 "alpine:3.20",
	}
	for in, want := range tests {
		if got := shortImageRef(in); got != want {
			t.Errorf("shortImageRef(%q) = %q, want %q", in, got, want)
		}
	}
}

// Small helpers kept local so the test file has no extra imports.
func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for range n {
		out = append(out, s...)
	}
	return string(out)
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}
