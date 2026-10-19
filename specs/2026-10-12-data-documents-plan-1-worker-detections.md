# Data documents PR 1: worker resilience and detections, implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Campaign inference keeps running when its detector fails on a frame, and every detection notification says what was detected.

**Architecture:** Two independent changes taken from closed PR #2206, both useful on `main` without anything else in the data documents spec. The Python worker turns a detector exception into a `source_error` for that frame and exits only when 10 of the last 20 frames failed (Martien's rolling-window note on #2206). Detection notifications carry the top five `{label, score}` detections; the journal trims that list rather than drop an entry that would exceed 4096 bytes.

**Tech Stack:** Python 3.12 worker (`go/internal/agent/inference/worker.py`, `unittest`, run through `uv`); Go 1.27 (`go/internal/agent/data`, `go/internal/agent/services`).

**Spec:** `specs/2026-10-12-data-documents-design.md` §6.4 (journal entries) and §14 row 1. The code it reuses was reviewed and verified on device in #2206 (branch `ed/chat-watches-leased-campaigns`, kept for reference).

## Global Constraints

- Branch `ed/inference-worker-detections` from `origin/main`, in worktree `/Users/ethan/Documents/WendyAgent-worker-detections`. Commits authored as `24462281+EBro912@users.noreply.github.com`.
- A detector failure message is `inference: <ExceptionType>` or `inference: <ExceptionType>: <text>`, cut to 512 UTF-8 bytes, and producing it never raises.
- The worker exits with `{"type": "error", ...}` when 10 of the last 20 frames failed. Fewer failures cost only their frames.
- `detections`: up to five `{label, score}` pairs that passed the label and threshold filter, highest score first. Never boxes or images. JSON key `detections`, omitted when empty.
- `count` stays the number of accepted detections, not the length of `detections`.
- A journal entry stays within 4096 bytes. Oversized `detections` are trimmed from the lowest score up; the entry itself is never dropped for them.
- No proto change, no new RPC, no change to campaign YAML.
- Run `gofmt -l .` from `go/` before every push.

## Review Focus

1. **A detector that fails nine frames out of ten.** It must end the worker with an error, not emit `source_error` forever (Martien, #2206). Test: Task 1, `test_mostly_failing_detector_ends_the_worker`.
2. **A detector that fails now and then.** One failure in four frames must never end the worker. Test: Task 1, `test_occasional_failures_keep_the_worker_running`.
3. **An exception whose `str()` raises, or holds unencodable text.** The message must still be built, within 512 bytes. Test: Task 1, `test_detection_error_never_raises`.
4. **Long labels push five detections past 4096 bytes.** The entry survives with fewer detections, keeping the highest score. Test: Task 2, `TestNotificationDropsDetectionsToFitLimit`.
5. **Seven detections in one frame.** `count` is 7 and `detections` holds the top five by score. Test: Task 2, `TestDetectionNotificationKeepsTopFiveByScore`.

---

## File map

| File | Change |
|---|---|
| `go/internal/agent/inference/worker.py` | `detection_error`, rolling failure window in `run` |
| `go/internal/agent/inference/test_worker.py` | `DetectorFailureTests` |
| `go/internal/agent/data/notifications.go` | `NotificationDetection`, `CampaignNotification.Detections`, trim in `RecordNotification` |
| `go/internal/agent/data/notifications_test.go` | round-trip and trim tests |
| `go/internal/agent/services/data_inference.go` | `detectionNotification` takes detections; `notificationDetections` |
| `go/internal/agent/services/data_inference_test.go` | top-five test; end-to-end assertion |
| `go/internal/cli/assets/docs/clients/wendy-cli/commands/data.md` | document `detections` |

---

### Task 0: Worktree

- [ ] **Step 1: Create the worktree**

```bash
cd /Users/ethan/Documents/WendyAgent
git fetch origin
git worktree add -b ed/inference-worker-detections ../WendyAgent-worker-detections origin/main
cd ../WendyAgent-worker-detections
git config user.email
```

Expected: the last line prints `24462281+EBro912@users.noreply.github.com`.

---

### Task 1: The worker survives failing frames

**Files:**
- Modify: `go/internal/agent/inference/worker.py` (constants after `output_lock`; the frame loop in `run`, ~line 330)
- Test: `go/internal/agent/inference/test_worker.py`

**Interfaces:**
- Produces: `worker.detection_error(exc) -> str`; constants `FAILURE_WINDOW = 20`, `MAX_FAILURES_IN_WINDOW = 10`. Worker output gains `{"type": "source_error", "source_id", "generation", "error"}` per failed frame and `{"type": "error", "error"}` when the window trips. The agent already handles both (`data_inference.go`: `error` ends the job, which `supervise` restarts; a `source_error` sets that source's state).

- [ ] **Step 1: Write the failing tests**

In `go/internal/agent/inference/test_worker.py`, add `import time` after `import threading`, and add this class before `if __name__ == "__main__":`:

```python
class DetectorFailureTests(unittest.TestCase):
    def run_with_detector(self, detector, frame_count=None, wait=30):
        """Runs worker.run over one camera that always has a frame ready and
        returns what it emitted and how long run took."""
        import json
        import types
        from unittest import mock
        from worker import run

        emitted = []
        finished = threading.Event()
        taken = []

        class FakeDecoder:
            def __init__(self, source_id, generation, encoding, initialization=b""):
                self.source_id, self.generation, self.encoding = source_id, generation, encoding
                self.stream = types.SimpleNamespace(stopped=False, feed=lambda payload: True)

            def take(self, now, interval):
                if frame_count is not None and len(taken) >= frame_count:
                    return None
                taken.append(1)
                return len(taken)

            def stop(self):
                self.stream.stopped = True

        class Input:
            lines = [json.dumps({"source_id": "camera", "generation": 1, "encoding": "h264",
                                 "payload": "eA=="}).encode() + b"\n"]

            def readline(self, limit):
                if self.lines:
                    return self.lines.pop(0)
                finished.wait(wait)
                return b""

        def emit(result):
            emitted.append(result)

        started = time.monotonic()
        with mock.patch("worker.Decoder", FakeDecoder), \
                mock.patch("worker.emit", emit), \
                mock.patch("worker.sys.stdin", types.SimpleNamespace(buffer=Input())):
            run({"rate": 1000}, detector)
        finished.set()
        return emitted, time.monotonic() - started

    def test_failing_frame_reports_source_error_and_next_frame_is_scored(self):
        def detector(frame):
            if frame == 1:
                raise ValueError("bad frame")
            return []

        emitted, _ = self.run_with_detector(detector, frame_count=2, wait=1)
        self.assertEqual(emitted, [
            {"type": "source_error", "source_id": "camera", "generation": 1,
             "error": "inference: ValueError: bad frame"},
            {"type": "prediction", "source_id": "camera", "generation": 1, "detections": []},
        ])

    def test_detector_that_always_fails_ends_the_worker_with_an_error(self):
        def detector(frame):
            raise ValueError("broken")

        emitted, elapsed = self.run_with_detector(detector)
        self.assertLess(elapsed, 10)
        self.assertEqual([item["type"] for item in emitted], ["source_error"] * 9 + ["error"])
        self.assertEqual(emitted[-1], {"type": "error", "error": "inference: ValueError: broken"})

    def test_mostly_failing_detector_ends_the_worker(self):
        # Nine failures, one success, then failures: a straight count of
        # consecutive failures would reset at the success and never trip.
        def detector(frame):
            if frame == 10:
                return []
            raise ValueError("broken")

        emitted, elapsed = self.run_with_detector(detector)
        self.assertLess(elapsed, 10)
        self.assertEqual([item["type"] for item in emitted],
                         ["source_error"] * 9 + ["prediction", "error"])

    def test_occasional_failures_keep_the_worker_running(self):
        # One frame in four fails, over 40 frames: 10 failures in all, but
        # never 10 within any 20 frames.
        def detector(frame):
            if frame % 4 == 0:
                raise ValueError("flaky")
            return []

        # The loop sleeps 20 ms per pass, so 40 frames need ~0.8 s before
        # stdin closes.
        emitted, _ = self.run_with_detector(detector, frame_count=40, wait=5)
        types_seen = [item["type"] for item in emitted]
        self.assertNotIn("error", types_seen)
        self.assertEqual(types_seen.count("source_error"), 10)
        self.assertEqual(types_seen.count("prediction"), 30)

    def test_detection_error_never_raises(self):
        from worker import detection_error

        class Unprintable(Exception):
            def __str__(self):
                raise RuntimeError("no")

        self.assertEqual(detection_error(Unprintable()), "inference: Unprintable")
        message = detection_error(ValueError("\udc80" * 400))
        self.assertLessEqual(len(message.encode()), 512)
        self.assertTrue(message.startswith("inference: ValueError: "))

    def test_detection_error_is_cut_to_512_utf8_bytes(self):
        from worker import detection_error
        message = detection_error(RuntimeError("é" * 600))
        self.assertLessEqual(len(message.encode()), 512)
        self.assertTrue(message.startswith("inference: RuntimeError: é"))
        self.assertEqual(detection_error(KeyError()), "inference: KeyError")
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd go/internal/agent/inference && uv run python -m unittest test_worker.DetectorFailureTests -v`
Expected: FAIL. `test_detection_error_*` fail with `ImportError: cannot import name 'detection_error'`; the frame tests fail because the first `ValueError` escapes `run`. The first `uv run` installs the worker's environment (torch, transformers) and can take several minutes.

- [ ] **Step 3: Implement**

In `worker.py`, after `output_lock = threading.Lock()` add:

```python
# The worker exits, so the agent restarts it with a fresh process, once this
# many of the last FAILURE_WINDOW frames failed. Fewer failures cost only their
# frames. A window rather than a run of consecutive failures, so a detector
# that fails nine frames in ten still counts as broken.
FAILURE_WINDOW = 20
MAX_FAILURES_IN_WINDOW = 10


def detection_error(exc):
    """A detector failure for one frame, cut to 512 UTF-8 bytes."""
    message = "inference: " + type(exc).__name__
    try:
        text = str(exc)
    except Exception:
        text = ""
    if text:
        message += ": " + text
    return message.encode(errors="replace")[:512].decode(errors="ignore")
```

In `run`, replace

```python
    threading.Thread(target=receive, daemon=True).start()
    try:
```

with

```python
    threading.Thread(target=receive, daemon=True).start()
    recent = deque(maxlen=FAILURE_WINDOW)
    try:
```

and replace

```python
                detections = detector(frame)
```

with

```python
                try:
                    detections = detector(frame)
                except Exception as exc:
                    # One bad frame costs that frame, not the worker. Before,
                    # the exception ended the process, and the agent lost the
                    # stderr tail once the model had loaded.
                    recent.append(False)
                    if recent.count(False) >= MAX_FAILURES_IN_WINDOW:
                        emit({"type": "error", "error": detection_error(exc)})
                        return
                    emit({"type": "source_error", "source_id": decoder.source_id,
                          "generation": decoder.generation, "error": detection_error(exc)})
                    continue
                recent.append(True)
```

(`deque` is already imported from `collections`.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd go/internal/agent/inference && uv run python -m unittest test_worker -v`
Expected: PASS for every test in the file, including the existing stream, YOLO and WebM tests. If `test_real_onnx_session_download_and_frame` is skipped or needs network, note it in the commit message; it is unrelated to this change.

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/inference/worker.py go/internal/agent/inference/test_worker.py
git commit -m "fix(inference): survive a failing frame, exit when 10 of the last 20 fail

A detector exception ended the worker, losing the stderr tail once the
model had loaded. Each failure now costs its frame and reports a
source_error. A rolling window, not a run of consecutive failures, decides
when the detector counts as broken, so one that fails nine frames in ten
still ends the worker (review note on #2206).

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Detections in notifications

**Files:**
- Modify: `go/internal/agent/data/notifications.go` (above `type CampaignNotification struct`; the struct; `RecordNotification`)
- Test: `go/internal/agent/data/notifications_test.go`
- Modify: `go/internal/agent/services/data_inference.go` (`detectionNotification` ~line 457 and its one call in `run` ~line 348)
- Test: `go/internal/agent/services/data_inference_test.go`
- Modify: `go/internal/cli/assets/docs/clients/wendy-cli/commands/data.md` (Notifications section, after the paragraph ending "…the same event UUID.", ~line 340)

**Interfaces:**
- Produces: `data.NotificationDetection{Label string; Score float64}` (`json:"label"`, `json:"score"`); `CampaignNotification.Detections []NotificationDetection` (`json:"detections,omitempty"`); `func detectionNotification(campaign data.Campaign, source string, detections []inference.Detection) DetectionNotification` (was `count int`); `func notificationDetections([]inference.Detection) []data.NotificationDetection`. PR 4 (watch runner) builds its journal entries from these.

- [ ] **Step 1: Write the failing data tests**

In `go/internal/agent/data/notifications_test.go`, add `"encoding/json"` and `"strings"` to the imports, then append:

```go
func TestNotificationDetectionsRoundTrip(t *testing.T) {
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	notification := CampaignNotification{ID: uuid.NewString(), Event: "person_detected", Campaign: "people", SourceID: "camera", Count: 1,
		Detections: []NotificationDetection{{Label: "person", Score: 0.91}}, OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := manager.RecordNotification(notification); err != nil {
		t.Fatal(err)
	}
	items, _, _, err := manager.Notifications("", "", "", true)
	if err != nil || len(items) != 1 {
		t.Fatalf("notification not retained: %v %v", items, err)
	}
	if len(items[0].Detections) != 1 || items[0].Detections[0] != (NotificationDetection{Label: "person", Score: 0.91}) {
		t.Fatalf("detections did not round-trip: %+v", items[0].Detections)
	}
}

// Labels may be up to 128 bytes and encoding/json escapes '<' as six bytes, so
// five detections can exceed the entry limit. The entry must survive with fewer.
func TestNotificationDropsDetectionsToFitLimit(t *testing.T) {
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	label := strings.Repeat("<", 128)
	notification := CampaignNotification{ID: uuid.NewString(), Event: "e", Campaign: "c", SourceID: "camera", Count: 5, OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)}
	for i := 0; i < 5; i++ {
		notification.Detections = append(notification.Detections, NotificationDetection{Label: label, Score: 0.9 - float64(i)/10})
	}
	if err := manager.RecordNotification(notification); err != nil {
		t.Fatalf("an oversized detection list cost the whole notification: %v", err)
	}
	items, _, _, err := manager.Notifications("", "", "", true)
	if err != nil || len(items) != 1 {
		t.Fatalf("notification not retained: %v %v", items, err)
	}
	got := items[0].Detections
	if len(got) == 0 || len(got) >= 5 {
		t.Fatalf("detections were not trimmed to fit: kept %d", len(got))
	}
	if got[0].Score != 0.9 {
		t.Fatal("trimming dropped the highest-scored detection")
	}
	if raw, _ := json.Marshal(items[0]); len(raw) > 4096 {
		t.Fatalf("stored entry is %d bytes", len(raw))
	}
}
```

- [ ] **Step 2: Write the failing services tests**

Append to `go/internal/agent/services/data_inference_test.go`:

```go
func TestDetectionNotificationKeepsTopFiveByScore(t *testing.T) {
	campaign, err := data.ParseCampaign(inferenceTestYAML(t))
	if err != nil {
		t.Fatal(err)
	}
	var detections []inference.Detection
	for _, score := range []float64{.5, .9, .7, .95, .6, .8, .55} {
		detections = append(detections, inference.Detection{Label: "person", Score: score, Box: [4]float64{1, 2, 3, 4}})
	}
	notification := detectionNotification(campaign, "v4l2:/dev/video0", detections)
	if notification.Count != 7 {
		t.Fatalf("count = %d, want every accepted detection", notification.Count)
	}
	want := []float64{.95, .9, .8, .7, .6}
	if len(notification.Detections) != len(want) {
		t.Fatalf("kept %d detections, want 5", len(notification.Detections))
	}
	for i, detection := range notification.Detections {
		if detection.Label != "person" || detection.Score != want[i] {
			t.Fatalf("detections[%d] = %+v, want person %.2f", i, detection, want[i])
		}
	}
}
```

In `TestAgentInferenceAllCamerasRecordAndNotify` (~line 165), directly after `notification1 := receiveInference(t, sender.requests)` add:

```go
	if len(notification1.Detections) != 1 || notification1.Detections[0] != (data.NotificationDetection{Label: "person", Score: .99}) {
		t.Fatalf("detection notification lacks its detections: %+v", notification1.Detections)
	}
```

(The test's fake detector reports one `person` at `.99`, line ~173.)

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd go && go test ./internal/agent/data/ -run 'NotificationDetections|NotificationDrops' -v`
Expected: build failure, `undefined: NotificationDetection`.

- [ ] **Step 4: Implement the data side**

In `notifications.go`, above `type CampaignNotification struct` add:

```go
// NotificationDetection summarizes one detection for a notification: its label
// and score, never a box or an image.
type NotificationDetection struct {
	Label string  `json:"label"`
	Score float64 `json:"score"`
}
```

In `CampaignNotification`, between `OccurredAt` and `Sequence`, add:

```go
	// Detections holds up to five of the detections behind a detection
	// notification, highest score first. Older clients ignore it.
	Detections []NotificationDetection `json:"detections,omitempty"`
```

In `RecordNotification`, replace

```go
	raw, err := json.Marshal(notification)
	if err != nil || len(raw) > 4096 {
		return fmt.Errorf("notification exceeds limit")
	}
```

with

```go
	raw, err := json.Marshal(notification)
	// Detections are a summary: drop the lowest-scored ones, not the
	// notification, when long labels push an entry past the limit. Cloud and
	// webhook delivery send the request as it was built.
	for err == nil && len(raw) > 4096 && len(notification.Detections) > 0 {
		notification.Detections = notification.Detections[:len(notification.Detections)-1]
		raw, err = json.Marshal(notification)
	}
	if err != nil || len(raw) > 4096 {
		return fmt.Errorf("notification exceeds limit")
	}
```

- [ ] **Step 5: Implement the services side**

In `data_inference.go`, add `"cmp"` and `"slices"` to the imports, and replace `detectionNotification` with:

```go
func detectionNotification(campaign data.Campaign, source string, detections []inference.Detection) DetectionNotification {
	return DetectionNotification{ID: uuid.NewString(), Event: campaign.Inference.Event, Campaign: campaign.Name, SourceID: source, Model: campaign.Inference.Model, Revision: campaign.Inference.Revision, Count: len(detections), Detections: notificationDetections(detections), OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)}
}

// notificationDetections keeps the five highest-scored detections, which keeps
// a journal entry well under its 4096-byte limit.
func notificationDetections(detections []inference.Detection) []data.NotificationDetection {
	sorted := slices.Clone(detections)
	slices.SortStableFunc(sorted, func(a, b inference.Detection) int { return cmp.Compare(b.Score, a.Score) })
	sorted = sorted[:min(len(sorted), 5)]
	out := make([]data.NotificationDetection, 0, len(sorted))
	for _, detection := range sorted {
		out = append(out, data.NotificationDetection{Label: detection.Label, Score: detection.Score})
	}
	return out
}
```

In `run`, change `request := detectionNotification(j.campaign, result.SourceID, len(detections))` to `request := detectionNotification(j.campaign, result.SourceID, detections)`. Then `grep -rn 'detectionNotification(' go/internal/agent/services/` and update any other caller (including tests) the same way.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd go && go test ./internal/agent/data/ ./internal/agent/services/ -run 'Notification|AllCamerasRecordAndNotify' -v`
Expected: PASS.

- [ ] **Step 7: Document `detections`**

In `data.md`, after the paragraph that ends `…three attempts, each with a 10-second timeout and the same event UUID.`, add:

```markdown
A `detection` notification carries `detections`: up to five `{label, score}`
pairs that passed the label and threshold filter, highest score first. It never
carries boxes or images. Readers that predate the field ignore it.
```

- [ ] **Step 8: Commit**

```bash
git add go/internal/agent/data/notifications.go go/internal/agent/data/notifications_test.go go/internal/agent/services/data_inference.go go/internal/agent/services/data_inference_test.go go/internal/cli/assets/docs/clients/wendy-cli/commands/data.md
git commit -m "feat(data): detection notifications carry their top five detections

Each detection notification now says what was seen: up to five
{label, score} pairs, highest score first. The journal trims the list,
lowest score first, rather than drop an entry that would pass 4096 bytes.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Verify and open the PR

**Files:** none changed unless a check fails.

- [ ] **Step 1: Run the affected packages in full**

Run: `cd go && go test ./internal/agent/data/ ./internal/agent/services/ ./internal/agent/inference/`
Expected: PASS. Compare any failure against `origin/main` before blaming this branch (`git stash` is not allowed across worktrees; check out `origin/main` in a scratch worktree instead).

- [ ] **Step 2: Vet and format**

Run: `cd go && go vet ./internal/agent/data/ ./internal/agent/services/ && gofmt -l .`
Expected: no output from `gofmt -l`.

- [ ] **Step 3: Push and open the PR**

```bash
git push -u origin ed/inference-worker-detections
gh pr create --base main --head ed/inference-worker-detections \
  --title "fix(inference): worker survives failing frames; notifications carry detections" \
  --body "$(cat <<'EOF'
Two changes from closed #2206 that stand on their own (data documents spec #2257, §14 row 1).

- **Worker resilience.** A detector exception costs its frame and reports a `source_error` instead of ending the worker. The worker exits, so the agent restarts it, once 10 of the last 20 frames failed: a rolling window instead of a run of consecutive failures, per Martien's note on #2206.
- **Detections in notifications.** Each detection notification carries up to five `{label, score}` pairs, highest score first. The journal trims that list rather than drop an entry that would pass 4096 bytes. Older readers ignore the field.

No proto or campaign format change. Both were verified on an Orin Nano in #2206; the rolling window is covered by unit tests.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Expected: a PR URL. Leave it ready for review (not draft); CODEOWNERS requests the reviewers.
