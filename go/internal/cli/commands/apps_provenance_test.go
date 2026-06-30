package commands

import (
	"testing"
	"time"
)

func TestFormatDeployedAt(t *testing.T) {
	if got := formatDeployedAt(""); got != emDash {
		t.Errorf("empty = %q; want em-dash", got)
	}
	if got := formatDeployedAt("not-a-time"); got != emDash {
		t.Errorf("unparseable = %q; want em-dash", got)
	}
	// A real timestamp renders in local time as "2006-01-02 15:04".
	ts := time.Date(2026, 6, 28, 20, 42, 0, 0, time.UTC).Format(time.RFC3339)
	want := time.Date(2026, 6, 28, 20, 42, 0, 0, time.UTC).Local().Format("2006-01-02 15:04")
	if got := formatDeployedAt(ts); got != want {
		t.Errorf("formatDeployedAt(%q) = %q; want %q", ts, got, want)
	}
}

func TestFormatDeployedBy(t *testing.T) {
	if got := formatDeployedBy(""); got != emDash {
		t.Errorf("empty = %q; want em-dash", got)
	}
	if got := formatDeployedBy("wendy/user/42"); got != "wendy/user/42" {
		t.Errorf("short principal was altered: %q", got)
	}
	long := "wendy/user/1234567890123456789012345678901234567890"
	got := formatDeployedBy(long)
	if len([]rune(got)) != 28 {
		t.Errorf("elided length = %d runes; want 28", len([]rune(got)))
	}
}

func TestFormatUptime(t *testing.T) {
	if got := formatUptime(""); got != emDash {
		t.Errorf("empty = %q; want em-dash", got)
	}
	if got := formatUptime("garbage"); got != emDash {
		t.Errorf("unparseable = %q; want em-dash", got)
	}
	// Future start time → em-dash (clock skew guard).
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	if got := formatUptime(future); got != emDash {
		t.Errorf("future start = %q; want em-dash", got)
	}
}

func TestCompactDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m30s"},
		{7*time.Hour + 10*time.Minute, "7h10m"},
		{50*time.Hour + 30*time.Minute, "2d2h"},
	}
	for _, tt := range tests {
		if got := compactDuration(tt.d); got != tt.want {
			t.Errorf("compactDuration(%s) = %q; want %q", tt.d, got, tt.want)
		}
	}
}
