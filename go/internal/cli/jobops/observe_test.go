package jobops_test

import (
	"context"
	"encoding/json"
	"github.com/wendylabsinc/wendy/go/internal/cli/jobops"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSubmitWaitReturnsTerminalExitsAndRetryDoesNotRerun(t *testing.T) {
	c := connection(t, &host{})
	spec := jobops.Spec{Name: "observe", WorkingDirectory: t.TempDir(), Steps: []jobops.Step{{Name: "check", Argv: []string{"python3", "-c", "print('ok')"}, TimeoutSeconds: 2}}}
	r, err := jobops.SubmitObserved(context.Background(), c, spec, 3)
	if err != nil {
		t.Fatal(err)
	}
	obs := r["observation"].(map[string]any)
	if r["reported_state"] != "completed" || obs["reported_state"] != "completed" || obs["last_stage"].(map[string]any)["returncode"] != float64(0) {
		t.Fatalf("missing terminal evidence: %+v", r)
	}
	again, err := jobops.SubmitObserved(context.Background(), c, spec, 3)
	if err != nil || again["reused"] != true || again["runner_pid"] != r["runner_pid"] {
		t.Fatalf("retry changed job: %v %+v", err, again)
	}
	spec.Name = "failed"
	spec.Steps[0].Argv = []string{"python3", "-c", "raise SystemExit(7)"}
	r, err = jobops.SubmitObserved(context.Background(), c, spec, 3)
	if err != nil {
		t.Fatal(err)
	}
	if r["reported_state"] != "failed" || r["observation"].(map[string]any)["reported_state"] != "failed" {
		t.Fatalf("false successful outcome: %+v", r)
	}
}

func TestTerminalWaitIgnoresIntermediateChangesAndNeverCancelsOnExpiry(t *testing.T) {
	c := connection(t, &host{})
	root := fixture(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, state := range []string{"running", "completed"} {
			time.Sleep(100 * time.Millisecond)
			data, _ := json.Marshal(map[string]any{"state": state, "stage": "next", "runner_pid": 999999999})
			os.WriteFile(filepath.Join(root, "next.json"), data, 0600)
			os.Rename(filepath.Join(root, "next.json"), filepath.Join(root, "status.json"))
		}
	}()
	r, err := jobops.Inspect(context.Background(), c, root, jobops.InspectOptions{UntilTerminal: true, WaitSeconds: 2})
	<-done
	if err != nil || r["reported_state"] != "completed" {
		t.Fatalf("intermediate state returned: %v %+v", err, r)
	}
	root = fixture(t)
	before, _ := os.ReadFile(filepath.Join(root, "status.json"))
	start := time.Now()
	r, err = jobops.Inspect(context.Background(), c, root, jobops.InspectOptions{UntilTerminal: true, WaitSeconds: 1})
	after, _ := os.ReadFile(filepath.Join(root, "status.json"))
	if err != nil || r["reported_state"] != "running" || time.Since(start) < time.Second || string(before) != string(after) {
		t.Fatalf("expiry changed job or claimed completion: %v %+v", err, r)
	}
	if _, err = jobops.SubmitObserved(context.Background(), nil, jobops.Spec{}, 3601); err == nil {
		t.Fatal("invalid wait reached submit")
	}
}

func TestSubmitWaitPreservesReceiptWhenObservationFails(t *testing.T) {
	c := connection(t, &host{failInspect: true})
	spec := jobops.Spec{Name: "observation-failed", WorkingDirectory: t.TempDir(), Steps: []jobops.Step{{Name: "check", Argv: []string{"python3", "-c", "print('still runs')"}, TimeoutSeconds: 2}}}
	r, err := jobops.SubmitObserved(context.Background(), c, spec, 2)
	if err != nil || r["job_path"] == nil || r["runner_pid"] == nil || r["observation_error"] == nil || r["completion"] != "unknown" {
		t.Fatalf("lost receipt or false completion: %v %+v", err, r)
	}
	if state := waitForJob(t, r["job_path"].(string)); state["state"] != "completed" {
		t.Fatalf("observation failure interrupted job: %+v", state)
	}
}

func TestSubmitWaitDeadlineLeavesOwnedJobRunningUntilCompletion(t *testing.T) {
	c := connection(t, &host{})
	spec := jobops.Spec{Name: "wait-expired", WorkingDirectory: t.TempDir(), Steps: []jobops.Step{{Name: "check", Argv: []string{"python3", "-c", "import time;time.sleep(2)"}, TimeoutSeconds: 5}}}
	r, err := jobops.SubmitObserved(context.Background(), c, spec, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r["observation"].(map[string]any)["reported_state"] != "running" {
		t.Fatalf("claimed terminal outcome at wait expiry: %+v", r)
	}
	if state := waitForJob(t, r["job_path"].(string)); state["state"] != "completed" {
		t.Fatalf("wait expiry cancelled job: %+v", state)
	}
}

func TestLongTerminalWaitUsesShortHostRPCAndReturnsAlreadyCompletedJobImmediately(t *testing.T) {
	h := &host{}
	c := connection(t, h)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "status.json"), []byte(`{"state":"completed","stages":[]}`), 0600)
	start := time.Now()
	r, err := jobops.WaitTerminal(context.Background(), c, root, 120)
	if err != nil || r["reported_state"] != "completed" || h.lastWait.Load() != 30 || time.Since(start) > 2*time.Second {
		t.Fatalf("long unbounded RPC or delayed terminal response: %v %+v wait=%d", err, r, h.lastWait.Load())
	}
	query, err := jobops.Inspect(context.Background(), c, root, jobops.InspectOptions{Since: r["cursor"].(string)})
	if err != nil || query["changed"] != false {
		t.Fatalf("view-only metadata invalidated semantic cursor: %v %+v", err, query)
	}
}
