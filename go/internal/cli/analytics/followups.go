package analytics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

const activationStateFile = "activation-state.json"

var activationStateMu sync.Mutex

// Only timestamps are persisted. No project, device, command arguments, or
// identity beyond the existing installation's config directory is recorded.
type activationState struct {
	InstalledAt time.Time `json:"installed_at"`
	Observed24h time.Time `json:"observed_24h,omitempty"`
	Observed7d  time.Time `json:"observed_7d,omitempty"`
}

func trackActivationFollowups(ctx context.Context, event, command, outcome string) {
	if !enabled || client == nil || !activationStateEvent(event) {
		return
	}
	dir, err := config.ConfigDir()
	if err != nil {
		return
	}
	for _, followup := range activationFollowups(dir, event, outcome, time.Now().UTC(), true) {
		TrackStage(ctx, followup, command, "completed", "", map[string]string{"stage": "install"})
	}
}

func activationStateEvent(event string) bool {
	return event == "cli_seen" || event == "install_finished" || event == "os_install_finished" || event == "run_started"
}

// Follow-ups are once per waiting window, best-effort like the event transport.
// A process-local lock and atomic replacement protect against partial writes;
// concurrent CLI processes can still observe the same threshold. These are
// next-launch observations, never evidence that an absent user abandoned setup.
func activationFollowups(dir, event, outcome string, now time.Time, trackingEnabled bool) []string {
	if !trackingEnabled || !activationStateEvent(event) {
		return nil
	}
	activationStateMu.Lock()
	defer activationStateMu.Unlock()
	path := filepath.Join(dir, activationStateFile)
	if event == "run_started" {
		_ = os.Remove(path)
		return nil
	}
	var state activationState
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &state)
	}
	if event == "install_finished" || event == "os_install_finished" {
		if outcome == "completed" {
			writeActivationState(dir, activationState{InstalledAt: now})
		}
		return nil
	}
	if state.InstalledAt.IsZero() || now.Before(state.InstalledAt) {
		return nil
	}
	var events []string
	if now.Sub(state.InstalledAt) >= 24*time.Hour && state.Observed24h.IsZero() {
		state.Observed24h = now
		events = append(events, "next_launch_no_run_24h")
	}
	if now.Sub(state.InstalledAt) >= 7*24*time.Hour && state.Observed7d.IsZero() {
		state.Observed7d = now
		events = append(events, "next_launch_no_run_7d")
	}
	if len(events) > 0 {
		writeActivationState(dir, state)
	}
	return events
}

func writeActivationState(dir string, state activationState) {
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	f, err := os.CreateTemp(dir, ".activation-state-*") // mode 0600
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	closeErr := f.Close()
	if err == nil && closeErr == nil {
		_ = os.Rename(f.Name(), filepath.Join(dir, activationStateFile))
	}
}
