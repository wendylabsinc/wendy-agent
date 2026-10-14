package analytics

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestActivationFollowupsMatureAtThresholdOnce(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	activationFollowups(dir, "install_finished", "completed", start, true)
	for _, tc := range []struct {
		elapsed time.Duration
		want    []string
	}{
		{24*time.Hour - time.Nanosecond, nil},
		{24 * time.Hour, []string{"next_launch_no_run_24h"}},
		{25 * time.Hour, nil},
		{7*24*time.Hour - time.Nanosecond, nil},
		{7 * 24 * time.Hour, []string{"next_launch_no_run_7d"}},
		{8 * 24 * time.Hour, nil},
	} {
		if got := activationFollowups(dir, "cli_seen", "completed", start.Add(tc.elapsed), true); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("at %v: got %v, want %v", tc.elapsed, got, tc.want)
		}
	}
	info, err := os.Stat(filepath.Join(dir, activationStateFile))
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("state must be private: %v, %v", info, err)
	}
}

func TestActivationFollowupsFirstReturnAfterWeekEmitsBoth(t *testing.T) {
	dir, now := t.TempDir(), time.Now().UTC()
	activationFollowups(dir, "install_finished", "completed", now, true)
	want := []string{"next_launch_no_run_24h", "next_launch_no_run_7d"}
	if got := activationFollowups(dir, "cli_seen", "completed", now.Add(8*24*time.Hour), true); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestActivationFollowupsRunClearsAndFailedInstallDoesNotStartWindow(t *testing.T) {
	dir, now := t.TempDir(), time.Now().UTC()
	activationFollowups(dir, "install_finished", "failed", now, true)
	if _, err := os.Stat(filepath.Join(dir, activationStateFile)); !os.IsNotExist(err) {
		t.Fatal("failed install created a waiting window")
	}
	activationFollowups(dir, "os_install_finished", "completed", now, true)
	activationFollowups(dir, "run_started", "started", now.Add(time.Hour), true)
	if got := activationFollowups(dir, "cli_seen", "completed", now.Add(8*24*time.Hour), true); len(got) != 0 {
		t.Fatalf("observed a run but emitted %v", got)
	}
}

func TestActivationFollowupsDisabledDoesNotTouchState(t *testing.T) {
	dir, now := t.TempDir(), time.Now().UTC()
	activationFollowups(dir, "install_finished", "completed", now, false)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("disabled analytics created state: %v, %v", entries, err)
	}
	activationFollowups(dir, "install_finished", "completed", now, true)
	before, _ := os.ReadFile(filepath.Join(dir, activationStateFile))
	activationFollowups(dir, "run_started", "started", now, false)
	if got := activationFollowups(dir, "cli_seen", "completed", now.Add(8*24*time.Hour), false); len(got) != 0 {
		t.Fatalf("disabled analytics emitted %v", got)
	}
	after, _ := os.ReadFile(filepath.Join(dir, activationStateFile))
	if string(before) != string(after) {
		t.Fatal("disabled analytics changed existing state")
	}
}
