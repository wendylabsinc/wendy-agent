package services

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/agent/oshealth"
)

// fakeUpdater is a test double for the osUpdater interface.
type fakeUpdater struct {
	nameVal      string
	detectVal    bool
	availableVal bool
	delegatesVal bool
	commandVal   string
}

func (f fakeUpdater) name() string    { return f.nameVal }
func (f fakeUpdater) detect() bool    { return f.detectVal }
func (f fakeUpdater) available() bool { return f.availableVal }
func (f fakeUpdater) install(context.Context, string, func(string, int32)) error {
	return nil
}
func (f fakeUpdater) commit() oshealth.UpdaterResult   { return oshealth.UpdaterResult{} }
func (f fakeUpdater) rollback() oshealth.UpdaterResult { return oshealth.UpdaterResult{} }
func (f fakeUpdater) delegatesHealthcheck() bool       { return f.delegatesVal }
func (f fakeUpdater) commitCommand() string            { return f.commandVal }

func TestChooseUpdaterForCommit(t *testing.T) {
	// The gate must select the commit backend by binary presence (available),
	// NOT the connector probe (detect): an update was already installed, so a
	// transient detect() failure must not strand a healthy slot uncommitted.
	wendyos := func(detect, available bool) osUpdater {
		return fakeUpdater{nameVal: updaterNameWendyOS, detectVal: detect, availableVal: available}
	}
	// other is a generic second backend used only to exercise "auto"
	// ordering/fallback; it is not a real registered backend.
	other := func(detect, available bool) osUpdater {
		return fakeUpdater{nameVal: "other-backend", detectVal: detect, availableVal: available}
	}

	tests := []struct {
		name       string
		requested  string
		candidates []osUpdater
		wantName   string // "" => expect nil
	}{
		{
			name:       "named wendyos is returned even when detect fails (binary present)",
			requested:  updaterNameWendyOS,
			candidates: []osUpdater{wendyos(false, true), other(true, true)},
			wantName:   updaterNameWendyOS,
		},
		{
			name:       "auto prefers an available wendyos without probing detect",
			requested:  "",
			candidates: []osUpdater{wendyos(false, true), other(false, true)},
			wantName:   updaterNameWendyOS,
		},
		{
			name:       "auto falls back to the next available backend when wendyos binary is absent",
			requested:  "auto",
			candidates: []osUpdater{wendyos(false, false), other(false, true)},
			wantName:   "other-backend",
		},
		{
			name:       "auto returns nil when no backend binary is present",
			requested:  "auto",
			candidates: []osUpdater{wendyos(false, false), other(false, false)},
			wantName:   "",
		},
		{
			name:       "unknown backend value returns nil",
			requested:  "bogus",
			candidates: []osUpdater{wendyos(true, true), other(true, true)},
			wantName:   "",
		},
		{
			// Regression: a pending-update marker written by an old agent can
			// still carry Backend: "mender" (see requestedBackendFromMarker).
			// It must resolve to nil, not a real backend, so the gate keeps the
			// marker and retries rather than committing/rolling back nothing.
			name:       "stale mender request returns nil (regression: mender is no longer a valid backend)",
			requested:  "mender",
			candidates: []osUpdater{wendyos(true, true), other(true, true)},
			wantName:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chooseUpdaterForCommit(tt.requested, tt.candidates)
			if tt.wantName == "" {
				if got != nil {
					t.Fatalf("chooseUpdaterForCommit(%q) = %q, want nil", tt.requested, got.name())
				}
				return
			}
			if got == nil {
				t.Fatalf("chooseUpdaterForCommit(%q) = nil, want %q", tt.requested, tt.wantName)
			}
			if got.name() != tt.wantName {
				t.Fatalf("chooseUpdaterForCommit(%q) = %q, want %q", tt.requested, got.name(), tt.wantName)
			}
		})
	}
}

func TestWendyOSBackendPolicy(t *testing.T) {
	wendyos := newWendyOSUpdater(zap.NewNop())

	if !wendyos.delegatesHealthcheck() {
		t.Error("wendyos-update must delegate healthchecking to its own commit (health.d)")
	}
	if wendyos.commitCommand() != "wendyos-update" {
		t.Errorf("wendyos commitCommand = %q, want wendyos-update", wendyos.commitCommand())
	}
}

func TestClosuresForUpdater(t *testing.T) {
	tests := []struct {
		name          string
		updater       osUpdater
		wantDelegated bool
		wantLabel     string
	}{
		{
			name:          "a backend that delegates health labels with its binary",
			updater:       fakeUpdater{nameVal: updaterNameWendyOS, delegatesVal: true, commandVal: "wendyos-update"},
			wantDelegated: true,
			wantLabel:     "wendyos-update",
		},
		{
			name:          "a backend without a health gate keeps the agent healthcheck path",
			updater:       fakeUpdater{nameVal: "other-backend", delegatesVal: false, commandVal: "other-backend"},
			wantDelegated: false,
			wantLabel:     "other-backend",
		},
		{
			name:          "no backend degrades to the non-delegated wendyos-update-labelled path",
			updater:       nil,
			wantDelegated: false,
			wantLabel:     "wendyos-update",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commit, rollback, delegated, label := closuresForUpdater(tt.updater)
			if commit == nil || rollback == nil {
				t.Fatal("commit/rollback closures must never be nil")
			}
			if delegated != tt.wantDelegated {
				t.Errorf("delegated = %v, want %v", delegated, tt.wantDelegated)
			}
			if label != tt.wantLabel {
				t.Errorf("label = %q, want %q", label, tt.wantLabel)
			}
			if tt.updater == nil {
				// The degraded no-op must report "unavailable" so the gate keeps
				// the marker rather than committing/rolling back a real slot.
				if got := commit().Status; got != oshealth.UpdaterUnavailable {
					t.Errorf("degraded commit status = %v, want UpdaterUnavailable", got)
				}
			}
		})
	}
}

func TestRequestedBackendFromMarker(t *testing.T) {
	tests := []struct {
		name   string
		marker oshealth.PendingMarker
		found  bool
		want   string
	}{
		{name: "no marker selects auto", found: false, want: ""},
		{name: "legacy marker without backend selects auto", marker: oshealth.PendingMarker{}, found: true, want: ""},
		{name: "marker backend is honored", marker: oshealth.PendingMarker{Backend: updaterNameWendyOS}, found: true, want: updaterNameWendyOS},
		{
			// Transitional case: a marker written by an old agent mid-update can
			// still carry Backend: "mender". requestedBackendFromMarker just
			// echoes it back verbatim; chooseUpdaterForCommit is what resolves
			// it to nil (see TestChooseUpdaterForCommit).
			name:   "mender marker is echoed back unresolved",
			marker: oshealth.PendingMarker{Backend: "mender"},
			found:  true,
			want:   "mender",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requestedBackendFromMarker(tt.marker, tt.found); got != tt.want {
				t.Fatalf("requestedBackendFromMarker = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestChooseUpdater(t *testing.T) {
	wendyosUp := func(detect bool) osUpdater { return fakeUpdater{nameVal: updaterNameWendyOS, detectVal: detect} }
	// otherUp is a generic second backend used only to exercise "auto"
	// ordering/fallback; it is not a real registered backend.
	otherUp := func(detect bool) osUpdater { return fakeUpdater{nameVal: "other-backend", detectVal: detect} }

	tests := []struct {
		name       string
		requested  string
		candidates []osUpdater
		wantName   string
		wantErr    bool
	}{
		{
			name:       "auto prefers wendyos when it detects",
			requested:  "auto",
			candidates: []osUpdater{wendyosUp(true), otherUp(true)},
			wantName:   updaterNameWendyOS,
		},
		{
			name:       "auto falls back to the next candidate when wendyos does not detect",
			requested:  "auto",
			candidates: []osUpdater{wendyosUp(false), otherUp(true)},
			wantName:   "other-backend",
		},
		{
			name:       "empty string behaves like auto",
			requested:  "",
			candidates: []osUpdater{wendyosUp(false), otherUp(true)},
			wantName:   "other-backend",
		},
		{
			name:       "auto errors when nothing detects",
			requested:  "auto",
			candidates: []osUpdater{wendyosUp(false), otherUp(false)},
			wantErr:    true,
		},
		{
			name:       "explicit wendyos is honored even when another candidate detects",
			requested:  "wendyos",
			candidates: []osUpdater{wendyosUp(true), otherUp(true)},
			wantName:   updaterNameWendyOS,
		},
		{
			name:       "wendyos-update alias selects wendyos",
			requested:  "wendyos-update",
			candidates: []osUpdater{wendyosUp(true), otherUp(true)},
			wantName:   updaterNameWendyOS,
		},
		{
			name:       "explicit wendyos errors when wendyos undetected (no silent fallback)",
			requested:  "wendyos",
			candidates: []osUpdater{wendyosUp(false), otherUp(true)},
			wantErr:    true,
		},
		{
			name:       "unknown backend value errors",
			requested:  "bogus",
			candidates: []osUpdater{wendyosUp(true), otherUp(true)},
			wantErr:    true,
		},
		{
			// Regression: mender was a valid backend id before its removal. A
			// stale CLI sending "mender" must be rejected like any other
			// unknown value, not silently routed anywhere.
			name:       "mender is rejected (regression: mender is no longer a valid backend)",
			requested:  "mender",
			candidates: []osUpdater{wendyosUp(true), otherUp(true)},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chooseUpdater(tt.requested, tt.candidates)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("chooseUpdater(%q) = %v, want error", tt.requested, got.name())
				}
				return
			}
			if err != nil {
				t.Fatalf("chooseUpdater(%q) returned error: %v", tt.requested, err)
			}
			if got.name() != tt.wantName {
				t.Fatalf("chooseUpdater(%q) = %q, want %q", tt.requested, got.name(), tt.wantName)
			}
		})
	}
}
