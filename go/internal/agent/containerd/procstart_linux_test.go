package containerd

import "testing"

func TestParseStarttimeTicks(t *testing.T) {
	tests := []struct {
		name string
		stat string
		want int64
		ok   bool
	}{
		{
			// Real-ish line; field 22 (starttime) is 8675309. comm is simple.
			name: "simple comm",
			stat: "1234 (myproc) S 1 1234 1234 0 -1 4194560 100 0 0 0 5 2 0 0 20 0 1 0 8675309 1000 200 18446744073709551615",
			want: 8675309,
			ok:   true,
		},
		{
			// comm containing spaces and parentheses must not confuse the parser:
			// we anchor on the LAST ')'.
			name: "comm with spaces and parens",
			stat: "42 (weird ) name) R 1 42 42 0 -1 0 0 0 0 0 1 1 0 0 20 0 1 0 555 0 0 0",
			want: 555,
			ok:   true,
		},
		{
			name: "too few fields",
			stat: "1 (init) S 1 1 1",
			ok:   false,
		},
		{
			name: "no close paren",
			stat: "garbage without paren",
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseStarttimeTicks(tt.stat)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("ticks = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseBtime(t *testing.T) {
	stat := "cpu  1 2 3 4\nbtime 1700000000\nprocesses 99\n"
	got, ok := parseBtime(stat)
	if !ok {
		t.Fatal("expected btime to parse")
	}
	if got != 1700000000 {
		t.Errorf("btime = %d, want 1700000000", got)
	}

	if _, ok := parseBtime("cpu 1 2 3\nprocesses 5\n"); ok {
		t.Error("expected ok=false when btime line absent")
	}
}
