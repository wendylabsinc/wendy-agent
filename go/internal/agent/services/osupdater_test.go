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
func (f fakeUpdater) commit() oshealth.MenderResult   { return oshealth.MenderResult{} }
func (f fakeUpdater) rollback() oshealth.MenderResult { return oshealth.MenderResult{} }
func (f fakeUpdater) delegatesHealthcheck() bool      { return f.delegatesVal }
func (f fakeUpdater) commitCommand() string           { return f.commandVal }

func TestChooseUpdaterForCommit(t *testing.T) {
	// The gate must select the commit backend by binary presence (available),
	// NOT the connector probe (detect): an update was already installed, so a
	// transient detect() failure must not strand a healthy slot uncommitted.
	wendyos := func(detect, available bool) osUpdater {
		return fakeUpdater{nameVal: updaterNameWendyOS, detectVal: detect, availableVal: available}
	}
	mender := func(detect, available bool) osUpdater {
		return fakeUpdater{nameVal: updaterNameMender, detectVal: detect, availableVal: available}
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
			candidates: []osUpdater{wendyos(false, true), mender(true, true)},
			wantName:   updaterNameWendyOS,
		},
		{
			name:       "named mender is returned",
			requested:  updaterNameMender,
			candidates: []osUpdater{wendyos(true, true), mender(false, true)},
			wantName:   updaterNameMender,
		},
		{
			name:       "auto prefers an available wendyos without probing detect",
			requested:  "",
			candidates: []osUpdater{wendyos(false, true), mender(false, true)},
			wantName:   updaterNameWendyOS,
		},
		{
			name:       "auto falls back to mender when wendyos binary is absent",
			requested:  "auto",
			candidates: []osUpdater{wendyos(false, false), mender(false, true)},
			wantName:   updaterNameMender,
		},
		{
			name:       "auto returns nil when no backend binary is present",
			requested:  "auto",
			candidates: []osUpdater{wendyos(false, false), mender(false, false)},
			wantName:   "",
		},
		{
			name:       "unknown backend returns nil",
			requested:  "bogus",
			candidates: []osUpdater{wendyos(true, true), mender(true, true)},
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

func TestBackendHealthcheckDelegationPolicy(t *testing.T) {
	logger := zap.NewNop()
	wendyos := newWendyOSUpdater(logger)
	mender := newMenderUpdater(logger)

	if !wendyos.delegatesHealthcheck() {
		t.Error("wendyos-update must delegate healthchecking to its own commit (health.d)")
	}
	if wendyos.commitCommand() != "wendyos-update" {
		t.Errorf("wendyos commitCommand = %q, want wendyos-update", wendyos.commitCommand())
	}
	if mender.delegatesHealthcheck() {
		t.Error("mender has no health.d; the agent gate must run its own healthchecks")
	}
	if mender.commitCommand() != "mender-update" {
		t.Errorf("mender commitCommand = %q, want mender-update", mender.commitCommand())
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
			name:          "wendyos delegates health and labels with its binary",
			updater:       fakeUpdater{nameVal: updaterNameWendyOS, delegatesVal: true, commandVal: "wendyos-update"},
			wantDelegated: true,
			wantLabel:     "wendyos-update",
		},
		{
			name:          "mender keeps the agent healthcheck path",
			updater:       fakeUpdater{nameVal: updaterNameMender, delegatesVal: false, commandVal: "mender-update"},
			wantDelegated: false,
			wantLabel:     "mender-update",
		},
		{
			name:          "no backend degrades to the non-delegated mender-labelled path",
			updater:       nil,
			wantDelegated: false,
			wantLabel:     "mender-update",
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
				if got := commit().Status; got != oshealth.MenderUnavailable {
					t.Errorf("degraded commit status = %v, want MenderUnavailable", got)
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
		{name: "mender marker is honored", marker: oshealth.PendingMarker{Backend: updaterNameMender}, found: true, want: updaterNameMender},
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
	menderUp := func(detect bool) osUpdater { return fakeUpdater{nameVal: updaterNameMender, detectVal: detect} }

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
			candidates: []osUpdater{wendyosUp(true), menderUp(true)},
			wantName:   updaterNameWendyOS,
		},
		{
			name:       "auto falls back to mender when wendyos does not detect",
			requested:  "auto",
			candidates: []osUpdater{wendyosUp(false), menderUp(true)},
			wantName:   updaterNameMender,
		},
		{
			name:       "empty string behaves like auto",
			requested:  "",
			candidates: []osUpdater{wendyosUp(false), menderUp(true)},
			wantName:   updaterNameMender,
		},
		{
			name:       "auto errors when nothing detects",
			requested:  "auto",
			candidates: []osUpdater{wendyosUp(false), menderUp(false)},
			wantErr:    true,
		},
		{
			name:       "explicit mender is honored even when wendyos detects",
			requested:  "mender",
			candidates: []osUpdater{wendyosUp(true), menderUp(true)},
			wantName:   updaterNameMender,
		},
		{
			name:       "explicit wendyos is honored even when mender detects",
			requested:  "wendyos",
			candidates: []osUpdater{wendyosUp(true), menderUp(true)},
			wantName:   updaterNameWendyOS,
		},
		{
			name:       "wendyos-update alias selects wendyos",
			requested:  "wendyos-update",
			candidates: []osUpdater{wendyosUp(true), menderUp(true)},
			wantName:   updaterNameWendyOS,
		},
		{
			name:       "explicit mender errors when mender unavailable (no silent fallback)",
			requested:  "mender",
			candidates: []osUpdater{wendyosUp(true), menderUp(false)},
			wantErr:    true,
		},
		{
			name:       "explicit wendyos errors when wendyos undetected (no silent fallback)",
			requested:  "wendyos",
			candidates: []osUpdater{wendyosUp(false), menderUp(true)},
			wantErr:    true,
		},
		{
			name:       "unknown backend value errors",
			requested:  "bogus",
			candidates: []osUpdater{wendyosUp(true), menderUp(true)},
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
