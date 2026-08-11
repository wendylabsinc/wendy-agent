package data

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEpisodeLifecycleUsesBoottimeAndSealsFiles(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	started, err := m.Start(StartOptions{Name: "trial", Sources: []string{"applications"}})
	if err != nil {
		t.Fatal(err)
	}
	if started.CanonicalClock != "CLOCK_BOOTTIME" || started.BootID == "" {
		t.Fatalf("bad clock identity: %+v", started)
	}
	if len(started.UTCObservations) != 1 || started.UTCObservations[0].UncertaintyNanos < 0 {
		t.Fatalf("bad UTC interval: %+v", started.UTCObservations)
	}
	stopped, err := m.Stop()
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != "complete" || stopped.StoppedEpisodeNS < 0 {
		t.Fatalf("bad stopped manifest: %+v", stopped)
	}
	if stopped.Device.ID == "" || stopped.Trigger.Reason != "manual" || stopped.Upload.State != "local" || stopped.Labeling.State != "unlabeled" {
		t.Fatalf("missing episode identity/workflow metadata: %+v", stopped)
	}
	if len(stopped.Files) != 1 || stopped.Files[0].Format != "jsonl" || stopped.Files[0].MediaType != "application/x-ndjson" || stopped.Files[0].SourceID != "applications" {
		t.Fatalf("payload format metadata missing: %+v", stopped.Files)
	}
	_, failures, err := m.Inspect(stopped.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 0 {
		t.Fatalf("verification failures: %v", failures)
	}
}

func TestApplicationPreRollHasNegativeCanonicalOffsets(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now, err := readBootTime()
	if err != nil {
		t.Fatal(err)
	}
	state, err := m.RecordApplication("com.example.app", ApplicationRecord{Version: 1, Type: "event", Name: "ready", ClientBootNanos: now, ClientBootID: bootID()})
	if err != nil {
		t.Fatal(err)
	}
	if state != "buffered" {
		t.Fatalf("state=%s", state)
	}
	started, err := m.Start(StartOptions{Sources: []string{"applications"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Stop(); err != nil {
		t.Fatal(err)
	}
	dir, err := m.episodeDir(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var stored storedApplicationRecord
	if err = json.Unmarshal(bytes.TrimSpace(b), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.EpisodeNanos >= 0 {
		t.Fatalf("pre-roll offset=%d, want negative", stored.EpisodeNanos)
	}
}

func TestRecoveryFinalizesPartialAsInterrupted(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	started, err := m.Start(StartOptions{Sources: []string{"applications"}})
	if err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(root, started.ID+".partial", "events.jsonl")
	if err = os.WriteFile(events, []byte("{\"ok\":true}\n{broken"), 0o640); err != nil {
		t.Fatal(err)
	}
	m2, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	mf, failures, err := m2.Inspect(started.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if mf.State != "interrupted" || mf.Interruption != "agent_restart" {
		t.Fatalf("bad recovery: %+v", mf)
	}
	if len(failures) != 0 {
		t.Fatalf("failures: %v", failures)
	}
	b, err := os.ReadFile(filepath.Join(root, started.ID, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "{\"ok\":true}\n" {
		t.Fatalf("tail not truncated: %q", b)
	}
}

func TestInspectDetectsChecksumCorruption(t *testing.T) {
	m, e := NewManager(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	start, e := m.Start(StartOptions{Sources: []string{"applications"}})
	if e != nil {
		t.Fatal(e)
	}
	done, e := m.Stop()
	if e != nil {
		t.Fatal(e)
	}
	if start.ID != done.ID {
		t.Fatal("id changed")
	}
	dir, _ := m.episodeDir(done.ID)
	if e = os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte("tampered"), 0o640); e != nil {
		t.Fatal(e)
	}
	_, fail, e := m.Inspect(done.ID, true)
	if e != nil {
		t.Fatal(e)
	}
	if len(fail) == 0 {
		t.Fatal("expected corruption failure")
	}
}

func TestSafeJoinRejectsTraversal(t *testing.T) {
	for _, p := range []string{"../x", "/tmp/x", "a/../../x", ""} {
		if _, e := safeJoin("/safe", p); e == nil {
			t.Errorf("accepted %q", p)
		}
	}
}
