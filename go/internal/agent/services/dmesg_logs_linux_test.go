//go:build linux

package services

import (
	"strings"
	"testing"
	"time"

	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

func TestParseKmsgLine(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		wantLevel   int
		wantMessage string
		wantTsUS    int64
		wantOK      bool
	}{
		{
			name:        "valid debug line",
			line:        "6,1234,5000000,-;hello world",
			wantLevel:   6,
			wantMessage: "hello world",
			wantTsUS:    5000000,
			wantOK:      true,
		},
		{
			name:        "level extracted via facility|level masking",
			line:        "14,99,1000,-;net message", // facility=1 (user), level=6
			wantLevel:   6,
			wantMessage: "net message",
			wantTsUS:    1000,
			wantOK:      true,
		},
		{
			name:        "zero timestamp (first boot message)",
			line:        "6,0,0,-;boot start",
			wantLevel:   6,
			wantMessage: "boot start",
			wantTsUS:    0,
			wantOK:      true,
		},
		{
			name:   "no semicolon separator",
			line:   "6,1234,5000000,-no-semicolon",
			wantOK: false,
		},
		{
			name:   "too few comma fields",
			line:   "6,1234;message",
			wantOK: false,
		},
		{
			name:   "non-numeric priority",
			line:   "x,1234,5000000,-;message",
			wantOK: false,
		},
		{
			name:   "priority out of byte range",
			line:   "300,1234,5000000,-;message",
			wantOK: false,
		},
		{
			name:   "negative priority",
			line:   "-1,1234,5000000,-;message",
			wantOK: false,
		},
		{
			name:   "non-numeric timestamp",
			line:   "6,1234,abc,-;message",
			wantOK: false,
		},
		{
			name:   "negative timestamp",
			line:   "6,1234,-500,-;message",
			wantOK: false,
		},
		{
			name:        "control characters stripped",
			line:        "6,0,1000,-;msg\x01with\x1bcontrols",
			wantLevel:   6,
			wantMessage: "msgwithcontrols",
			wantTsUS:    1000,
			wantOK:      true,
		},
		{
			name:        "tab preserved",
			line:        "6,0,1000,-;col1\tcol2",
			wantLevel:   6,
			wantMessage: "col1\tcol2",
			wantTsUS:    1000,
			wantOK:      true,
		},
		{
			name:        "CSI remnant stripped after ESC removal",
			line:        "6,0,1000,-;msg[31mnormal",
			wantLevel:   6,
			wantMessage: "msgnormal",
			wantTsUS:    1000,
			wantOK:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			level, message, tsUS, ok := parseKmsgLine(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if level != tc.wantLevel {
				t.Errorf("level=%d want %d", level, tc.wantLevel)
			}
			if tsUS != tc.wantTsUS {
				t.Errorf("tsUS=%d want %d", tsUS, tc.wantTsUS)
			}
			if message != tc.wantMessage {
				t.Errorf("message=%q want %q", message, tc.wantMessage)
			}
		})
	}
}

func TestKernelLevelToOTEL(t *testing.T) {
	tests := []struct {
		level          int
		wantNumber     otelpb.SeverityNumber
		wantText       string
		wantTextPrefix string // SeverityText must start with the base name
	}{
		{7, otelpb.SeverityNumber_SEVERITY_NUMBER_TRACE, "TRACE", "TRACE"},
		{6, otelpb.SeverityNumber_SEVERITY_NUMBER_TRACE4, "TRACE4", "TRACE"},
		{5, otelpb.SeverityNumber_SEVERITY_NUMBER_DEBUG, "DEBUG", "DEBUG"},
		{4, otelpb.SeverityNumber_SEVERITY_NUMBER_DEBUG2, "DEBUG2", "DEBUG"},
		{3, otelpb.SeverityNumber_SEVERITY_NUMBER_DEBUG3, "DEBUG3", "DEBUG"},
		{2, otelpb.SeverityNumber_SEVERITY_NUMBER_WARN, "WARN", "WARN"},
		{1, otelpb.SeverityNumber_SEVERITY_NUMBER_ERROR, "ERROR", "ERROR"},
		{0, otelpb.SeverityNumber_SEVERITY_NUMBER_FATAL, "FATAL", "FATAL"},
	}

	for _, tc := range tests {
		t.Run(tc.wantText, func(t *testing.T) {
			num, text := kernelLevelToOTEL(tc.level)
			if num != tc.wantNumber {
				t.Errorf("level %d: SeverityNumber=%v want %v", tc.level, num, tc.wantNumber)
			}
			if text != tc.wantText {
				t.Errorf("level %d: SeverityText=%q want %q", tc.level, text, tc.wantText)
			}
			if !strings.HasPrefix(text, tc.wantTextPrefix) {
				t.Errorf("level %d: SeverityText=%q does not start with %q", tc.level, text, tc.wantTextPrefix)
			}
		})
	}
}

func TestKmsgTimestampToWall(t *testing.T) {
	// Use a recent boot epoch for tests.
	bootEpoch := time.Now().UnixNano() - int64(10*time.Second) // booted 10s ago

	t.Run("valid timestamp", func(t *testing.T) {
		tsUS := int64(5 * 1e6) // 5 seconds after boot
		result := kmsgTimestampToWall(tsUS, bootEpoch)
		expected := uint64(bootEpoch + tsUS*1000)
		if result != expected {
			t.Errorf("result=%d want %d", result, expected)
		}
	})

	t.Run("zero timestamp maps to boot epoch", func(t *testing.T) {
		result := kmsgTimestampToWall(0, bootEpoch)
		expected := uint64(bootEpoch)
		if result != expected {
			t.Errorf("zero tsUS: result=%d want boot epoch %d", result, expected)
		}
	})

	t.Run("zero boot epoch falls back to now", func(t *testing.T) {
		before := uint64(time.Now().UnixNano())
		result := kmsgTimestampToWall(1000, 0)
		after := uint64(time.Now().UnixNano())
		if result < before || result > after {
			t.Errorf("expected fallback to time.Now(), got %d (window [%d, %d])", result, before, after)
		}
	})

	t.Run("oversized timestamp falls back to now", func(t *testing.T) {
		before := uint64(time.Now().UnixNano())
		result := kmsgTimestampToWall(maxReasonableTsUS+1, bootEpoch)
		after := uint64(time.Now().UnixNano())
		if result < before || result > after {
			t.Errorf("expected fallback to time.Now(), got %d (window [%d, %d])", result, before, after)
		}
	})

	t.Run("far-future computed timestamp falls back to now", func(t *testing.T) {
		// tsUS that would produce a timestamp 48h in the future
		futureUS := (time.Now().UnixNano() - bootEpoch + int64(48*time.Hour)) / 1000
		before := uint64(time.Now().UnixNano())
		result := kmsgTimestampToWall(futureUS, bootEpoch)
		after := uint64(time.Now().UnixNano())
		if result < before || result > after {
			t.Errorf("expected fallback to time.Now() for far-future ts, got %d", result)
		}
	})
}
