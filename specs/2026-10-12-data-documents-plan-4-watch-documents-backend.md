# Data documents PR 5: camera watch tools on data documents (#2207 rebased), implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The `watch_*` MCP tools from #2207 run each watch as a leased watch document and an inference document it owns, instead of a leased campaign, and #2207's review findings are fixed.

**Architecture:** #2207 (branch `ed/chat-watches-mcp`) moves from closed #2206 onto PR 4 (`ed/data-documents-agent`). Its manager, tools, notifications and backend interface stay. `watch_campaign.go` is replaced by `watch_documents.go`, a backend that applies the two documents, renews the watch, follows its state through `Documents`, reads only its own watches' journal entries through `Events.watches`, and removes the watch (and so the inference) on stop. The single pinned detector becomes a vetted list with one entry.

**Tech Stack:** Go 1.27, mcp-go, gRPC client (`agentpbv2.DataServiceClient`).

**Spec:** `specs/2026-10-12-data-documents-design.md` §6.5, §11.1, §11.2, §14 row 5. The watch manager, tools and notifications are chat-watches design §6.1, §6.2, §6.5, §6.6 (on branch `ed/chat-watches-leased-campaigns`, `specs/2026-10-05-chat-watches-design.md`).

## Global Constraints

- Prerequisite: PR 4 (`ed/data-documents-agent`) is complete. This branch is rebased onto it.
- Worktree `/Users/ethan/Documents/WendyAgent-watch-documents` on branch `ed/chat-watches-mcp` (the branch #2207 already uses). Commits authored as `24462281+EBro912@users.noreply.github.com`.
- Per watch: a watch document `{kind: watch, version: 1, name: <name>, lease: 60s, on: {inference: <name>}}` applied first, then `{kind: inference, version: 1, name: <name>, owner: watch/<name>, sources: [{camera: <id>}], model, revision, labels, threshold, rate: 2, clear_after: 5s, cooldown: 30s}`. No `notify`, no `event` (it defaults to the name). Names `chat-<server id>-<n>`, server id 8 random bytes in hex (16 hex digits).
- Renew every 20 s; status every 5 s; poll every 1 s; each device call 10 s; each apply 30 s (busy device).
- The poller sets `watches` to this server's active and starting watches; it never reads without names. Each response: at most 4 MiB of JSON and 512 entries; past either, the watches get a gap.
- A gap stays journal-wide (spec §6.5): every active watch hears "some detections may have been missed".
- Stop: `DocumentRemove{kind: watch, name}`; NOT_FOUND counts as removed.
- An agent without data documents (UNIMPLEMENTED, or an `Events` response not marked `notifications`) refuses with "This device's agent is too old for watches; update it with `wendy device update`." and leaves nothing on the device.
- Detector list entries: Hugging Face repository pinned to a commit; licence that allows commercial use; safetensors weights loadable by the Transformers backend without remote code; labels from that commit's `id2label`; an on-device result of at least 1.5 frames per second per watch with two watches on the Orin Nano CPU at 2 threads. v1: `ustc-community/dfine-nano-coco` at `066438d3d8f0da137a37b38fdf3368fd4afceced` only.
- Run `gofmt -l .` from `go/` before every push. Never use `git stash`.

## Review Focus

1. **The inference apply fails after the watch applied.** The watch is removed (or, if the device is unreachable, left to its lease); `watch_start` reports the error, and no slot stays taken. Test: Task 1, `TestDocumentWatchRemovesTheWatchWhenTheInferenceFails`.
2. **Another session's watch fires on the same device.** This session never receives it, because the poller names only its own watches. Test: Task 1, `TestDocumentPollerReadsOnlyItsOwnWatches` (the other watch's entry uses a different label, so a leak is caught).
3. **A journal response larger than 4 MiB or with more than 512 entries.** Never parsed whole; the watches get a gap and the cursor moves on. Test: Task 1, `TestDocumentPollerCapsAResponse`.
4. **Two `mcp serve` processes start watches at once.** Their names never collide. Test: Task 2, `TestWatchManagerServerIDsAreSixteenHexDigits`.
5. **Classes no detector has.** `watch_start` refuses before anything reaches the device. Test: Task 3, `TestWatchStartRefusesClassesNoDetectorHas`.

---

## File map

| File | Change |
|---|---|
| `go/internal/cli/mcp/watch_backend.go` | `watchSpec.Detector` |
| `go/internal/cli/mcp/watch_campaign.go`, `watch_campaign_test.go` | deleted |
| `go/internal/cli/mcp/watch_documents.go` (new) | the document backend and its poller |
| `go/internal/cli/mcp/watch_documents_test.go` (new) | its tests |
| `go/internal/cli/mcp/watch_fake_test.go` | the fake device speaks data documents |
| `go/internal/cli/mcp/watch_manager.go` | 8-byte server id |
| `go/internal/cli/mcp/watch_detector.go` | vetted list |
| `go/internal/cli/mcp/watch_tools.go` | detector chosen per watch; backend construction |
| `go/internal/cli/mcp/watch_tools_test.go`, `watch_manager_test.go`, `watch_detector_test.go` | follow the changes |
| `go/internal/cli/assets/docs/integrations/mcp.mdx`, `go/internal/cli/mcp/tools_guide.go` | "campaign" wording |
| `specs/2026-10-07-chat-watches-plan-b1-mcp-watches.md`, `specs/2026-10-07-chat-watches-plan-b2-chat-watches.md` | removed (superseded; they stay on the old branches) |

---

### Task 0: Rebase onto PR 4

Controller-run. The rebase rewrites a pushed PR branch; Ethan approved keeping #2207 and rebasing it (2026-10-12).

- [ ] **Step 1: Rebase**

```bash
cd /Users/ethan/Documents/WendyAgent
git fetch origin
git worktree add ../WendyAgent-watch-documents ed/chat-watches-mcp
cd ../WendyAgent-watch-documents
git log --oneline ed/chat-watches-leased-campaigns..HEAD | cat   # B1's own commits
git rebase --onto ed/data-documents-agent ed/chat-watches-leased-campaigns
```

B1's commits touch only `go/internal/cli/mcp/`, its docs and `specs/`, none of which PR 4 changes, so the rebase should apply cleanly. If a conflict appears, resolve it toward PR 4's version for files outside `go/internal/cli/mcp/watch_*`.

- [ ] **Step 2: Drop the superseded plans**

```bash
git rm specs/2026-10-07-chat-watches-plan-b1-mcp-watches.md specs/2026-10-07-chat-watches-plan-b2-chat-watches.md 2>/dev/null || true
git commit -m "docs(spec): drop the chat-watches plans that data documents supersede

They remain on ed/chat-watches-leased-campaigns for history; the current
design is specs/2026-10-12-data-documents-design.md."
```

(Run `git rm` only for the files that exist after the rebase.)

- [ ] **Step 3: Confirm what is broken**

```bash
cd go && go build ./internal/cli/mcp/ 2>&1 | head -20
```

Expected: errors only in `watch_campaign.go` (and, under `go vet`/`go test`, `watch_campaign_test.go` and `watch_fake_test.go`), naming `CampaignRenew`, `CampaignRemove`, `DataCampaignRenewRequest` or `DataCampaignRemoveRequest`, which no longer exist. Anything else is a rebase problem to fix before Task 1.

---

### Task 1: The document backend

**Files:**
- Delete: `go/internal/cli/mcp/watch_campaign.go`, `go/internal/cli/mcp/watch_campaign_test.go`
- Create: `go/internal/cli/mcp/watch_documents.go`, `go/internal/cli/mcp/watch_documents_test.go`
- Rewrite: `go/internal/cli/mcp/watch_fake_test.go`
- Modify: `go/internal/cli/mcp/watch_backend.go` (`watchSpec`), `go/internal/cli/mcp/watch_tools.go` (backend construction ~line 261; set `spec.Detector`)

**Interfaces:**
- Consumes: PR 4's `DocumentApply`, `DocumentRenew`, `DocumentRemove`, `Documents`, `Events` with `Watches`; `data.ParseDocument`; `data.KindWatch`, `data.KindInference`, `data.WatchVersion`, `data.InferenceDocumentVersion`; `data.CampaignNotification.Watch`.
- Produces: `watchSpec.Detector watchDetector`; `func newDocumentWatchBackend() *documentWatchBackend`; `func watchDocuments(spec watchSpec) (watch, inference []byte, err error)`; `func watchStateFromDocument(doc *agentpbv2.DataDocument) (watchState, string)`. Kept from #2207 unchanged: `watchStateFromInference`, `errWatchAgentTooOld`, `errWatchDeviceBusy`, `watchUnconfirmedError`, `watchGoneReason`, the timing constants.

- [ ] **Step 1: Give the spec its detector**

In `watch_backend.go`, add to `watchSpec`:

```go
	// Detector is the vetted detector chosen for the classes (watch_detector.go).
	Detector watchDetector
```

and change the `Name` field's comment to "Name is the device-side identity the manager assigns: the name of the watch document and of the inference document it owns."

- [ ] **Step 2: Rewrite the fake device**

Replace `watch_fake_test.go` with:

```go
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// watchDataClient is a small in-memory device that speaks data documents:
// stored documents, each inference's state, a notification journal and camera
// sources. calls records device operations in order, so tests can check, for
// example, journal-before-apply.
type watchDataClient struct {
	agentpbv2.DataServiceClient
	mu         sync.Mutex
	calls      []string
	documents  map[string]data.StoredDocument // by "<kind>/<name>"
	raw        map[string][]byte              // the YAML each document was sent as
	inference  map[string]data.InferenceStatus // by inference name; default running
	renews     map[string]int
	inspects   int // Documents calls, counted after the state is read
	applyErr   map[string]error // by kind
	applyWait  time.Duration    // DocumentApply waits this long, or until its ctx ends
	sourcesErr error
	renewErr   error
	inspectErr error
	removeErr  error
	removeWait time.Duration
	journal    []data.CampaignNotification
	gapOnce    bool
	oldJournal bool   // Events does not mark the response as the notification journal
	oversize   []byte // when set, Events returns this as events_json once
	lastWatches []string // the watches the last Events call named
	sources    []*agentpbv2.DataSource
}

func newWatchDataClient() *watchDataClient {
	return &watchDataClient{
		documents: map[string]data.StoredDocument{}, raw: map[string][]byte{}, inference: map[string]data.InferenceStatus{}, renews: map[string]int{}, applyErr: map[string]error{},
		sources: []*agentpbv2.DataSource{
			{Id: "v4l2:/dev/video0", Kind: "camera", Healthy: true, Detail: "Brio 101 VIDEO_TRANSPORT_USB"},
			{Id: "v4l2:/dev/video2", Kind: "camera", Healthy: false, Detail: "Unplugged VIDEO_TRANSPORT_USB"},
			{Id: "audio:55", Kind: "audio", Healthy: true, Detail: "Built-in Audio"},
		},
	}
}

func (f *watchDataClient) record(call string) { f.calls = append(f.calls, call) }

func (f *watchDataClient) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *watchDataClient) DocumentApply(ctx context.Context, r *agentpbv2.DataDocumentApplyRequest, _ ...grpc.CallOption) (*agentpbv2.DataDocumentApplyResponse, error) {
	f.mu.Lock()
	wait := f.applyWait
	f.mu.Unlock()
	if wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	}
	doc, err := data.ParseDocument(r.GetDocumentYaml())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	ref := doc.Header.Kind + "/" + doc.Header.Name
	if err := f.applyErr[doc.Header.Kind]; err != nil {
		f.record("apply-error:" + ref)
		return nil, err
	}
	if doc.Header.Owner != "" {
		if _, ok := f.documents[doc.Header.Owner]; !ok {
			return nil, status.Errorf(codes.FailedPrecondition, "owner %s does not exist; apply it first", doc.Header.Owner)
		}
	}
	body, _ := json.Marshal(doc.Body)
	stored := data.StoredDocument{Kind: doc.Header.Kind, Version: doc.Header.Version, Name: doc.Header.Name, Owner: doc.Header.Owner, Revision: doc.Revision, Body: body}
	f.record("apply:" + ref)
	f.documents[ref] = stored
	f.raw[ref] = append([]byte(nil), r.GetDocumentYaml()...)
	return &agentpbv2.DataDocumentApplyResponse{Document: &agentpbv2.DataDocument{Kind: stored.Kind, Name: stored.Name, Owner: stored.Owner, Revision: stored.Revision, State: "waiting"}}, nil
}

func (f *watchDataClient) DocumentRenew(_ context.Context, r *agentpbv2.DataDocumentRenewRequest, _ ...grpc.CallOption) (*agentpbv2.DataDocumentRenewResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.renewErr != nil {
		return nil, f.renewErr
	}
	if _, ok := f.documents[r.GetKind()+"/"+r.GetName()]; !ok {
		return nil, status.Error(codes.NotFound, "document not found")
	}
	f.renews[r.GetName()]++
	return &agentpbv2.DataDocumentRenewResponse{ExpiresUnixNanos: time.Now().Add(time.Minute).UnixNano()}, nil
}

// DocumentRemove removes owned documents first, then the document, like the
// agent.
func (f *watchDataClient) DocumentRemove(ctx context.Context, r *agentpbv2.DataDocumentRemoveRequest, _ ...grpc.CallOption) (*agentpbv2.DataDocumentRemoveResponse, error) {
	f.mu.Lock()
	wait := f.removeWait
	f.mu.Unlock()
	if wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	ref := r.GetKind() + "/" + r.GetName()
	f.record("remove:" + ref)
	if f.removeErr != nil {
		return nil, f.removeErr
	}
	if _, ok := f.documents[ref]; !ok {
		return nil, status.Error(codes.NotFound, "document not found")
	}
	var removed []string
	for key, doc := range f.documents {
		if doc.Owner == ref {
			delete(f.documents, key)
			removed = append(removed, key)
		}
	}
	delete(f.documents, ref)
	return &agentpbv2.DataDocumentRemoveResponse{Removed: append(removed, ref)}, nil
}

// Documents reports a watch's state from its inference's, like the agent.
func (f *watchDataClient) Documents(_ context.Context, r *agentpbv2.DataDocumentsRequest, _ ...grpc.CallOption) (*agentpbv2.DataDocumentsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inspectErr != nil {
		return nil, f.inspectErr
	}
	ref := r.GetKind() + "/" + r.GetName()
	stored, ok := f.documents[ref]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "no document named %q", r.GetName())
	}
	document := &agentpbv2.DataDocument{Kind: stored.Kind, Name: stored.Name, Owner: stored.Owner, Revision: stored.Revision, DocumentJson: stored.Body}
	if stored.Kind == data.KindWatch {
		var watch data.Watch
		_ = json.Unmarshal(stored.Body, &watch)
		inference, ok := f.inference[watch.On.Inference]
		if !ok {
			inference = data.InferenceStatus{State: "running"}
		}
		if _, deployed := f.documents["inference/"+watch.On.Inference]; !deployed {
			inference = data.InferenceStatus{}
		}
		switch inference.State {
		case "running":
			document.State = "active"
		case "error":
			document.State, document.StateDetail = "error", inference.Error
		default:
			document.State = "waiting"
		}
		document.StatusJson, _ = json.Marshal(map[string]string{"inference_state": inference.State, "inference_error": inference.Error})
	}
	f.inspects++
	return &agentpbv2.DataDocumentsResponse{Documents: []*agentpbv2.DataDocument{document}}, nil
}

// Events serves the notification journal like the agent: an empty cursor
// without replay returns only the tail; a cursor returns entries after it,
// only of the named watches when watches is set.
func (f *watchDataClient) Events(_ context.Context, r *agentpbv2.DataEventsRequest, _ ...grpc.CallOption) (*agentpbv2.DataEventsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("events:" + r.GetCursor())
	f.lastWatches = append([]string(nil), r.GetWatches()...)
	var after uint64
	if _, number, ok := strings.Cut(r.GetCursor(), ":"); ok {
		after, _ = strconv.ParseUint(number, 10, 64)
	}
	out := []data.CampaignNotification{}
	var tail uint64
	for _, entry := range f.journal {
		tail = max(tail, entry.Sequence)
		if r.GetCursor() == "" || entry.Sequence <= after {
			continue
		}
		if len(r.GetWatches()) > 0 && !slices.Contains(r.GetWatches(), entry.Watch) {
			continue
		}
		out = append(out, entry)
	}
	raw, _ := json.Marshal(out)
	if f.oversize != nil && r.GetCursor() != "" {
		raw, f.oversize = f.oversize, nil
	}
	gap := f.gapOnce
	f.gapOnce = false
	return &agentpbv2.DataEventsResponse{EventsJson: raw, Cursor: fmt.Sprintf("epoch:%d", tail), Gap: gap, Notifications: !f.oldJournal}, nil
}

func (f *watchDataClient) Sources(context.Context, *agentpbv2.DataSourcesRequest, ...grpc.CallOption) (*agentpbv2.DataSourcesResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sourcesErr != nil {
		return nil, f.sourcesErr
	}
	return &agentpbv2.DataSourcesResponse{Sources: f.sources}, nil
}

// publish appends a leased watch's notification to the journal.
func (f *watchDataClient) publish(watch string, detections ...data.NotificationDetection) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sequence := uint64(len(f.journal) + 1)
	f.journal = append(f.journal, data.CampaignNotification{
		ID: strconv.FormatUint(sequence, 10), Event: watch, Watch: watch, Inference: watch, Leased: true, Count: len(detections),
		Detections: detections, OccurredAt: time.Now().UTC().Format(time.RFC3339Nano), Sequence: sequence,
	})
}

func (f *watchDataClient) setInference(name string, state data.InferenceStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inference[name] = state
}

// setInferenceSeen sets an inference's state and waits until a Documents call
// has read it, so a watch polling the device cannot skip it.
func (f *watchDataClient) setInferenceSeen(t *testing.T, name string, state data.InferenceStatus) {
	t.Helper()
	f.mu.Lock()
	f.inference[name] = state
	seen := f.inspects
	f.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.mu.Lock()
		n := f.inspects
		f.mu.Unlock()
		if n > seen {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no Documents call read inference state %+v", state)
		}
		time.Sleep(time.Millisecond)
	}
}

func (f *watchDataClient) document(ref string) (data.StoredDocument, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	doc, ok := f.documents[ref]
	return doc, ok
}

func (f *watchDataClient) documentRefs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	refs := make([]string, 0, len(f.documents))
	for ref := range f.documents {
		refs = append(refs, ref)
	}
	slices.Sort(refs)
	return refs
}

func (f *watchDataClient) namedWatches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.lastWatches...)
}
```

In `watch_tools_test.go`'s `watchToolServer`, change `fastCampaignBackend()` to `fastDocumentBackend()`. Then `grep -rn 'deployedNames\|\.campaign(\|deployedYAML\|deployErr\|deployWait\|CampaignDeploy\|CampaignInspect\|fastCampaignBackend' go/internal/cli/mcp/*_test.go` and update the manager and tool tests that used the old fake: `deployedNames()` → `documentRefs()` (a watch shows as `watch/<name>` and `inference/<name>`), `campaign(name)` → `document("watch/"+name)` or `document("inference/"+name)`, `deployErr` → `applyErr[data.KindWatch]`, `deployWait` → `applyWait`, `publish(campaign, …)` keeps its call shape (the argument is now the watch name, which equals the campaign name the manager assigned before).

- [ ] **Step 3: Write the backend tests**

Create `watch_documents_test.go`:

```go
package mcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"
)

func fastDocumentBackend() *documentWatchBackend {
	b := newDocumentWatchBackend()
	b.renewEvery, b.statusEvery, b.pollEvery = 20*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond
	return b
}

func testWatchSpec(name string) watchSpec {
	return watchSpec{Name: name, CameraID: "v4l2:/dev/video0", CameraName: "Brio 101", Classes: []string{"person"}, MinConfidence: 0.5, Label: "door", Detector: watchDetectors[0]}
}

func startTestWatch(t *testing.T, b *documentWatchBackend, f *watchDataClient, name string) watchHandle {
	t.Helper()
	h, err := b.Start(context.Background(), &grpcclient.AgentConnection{DataService: f}, testWatchSpec(name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Stop(context.Background()) })
	return h
}

func nextUpdate(t *testing.T, h watchHandle) watchUpdate {
	t.Helper()
	select {
	case u, ok := <-h.Updates():
		if !ok {
			t.Fatal("updates closed")
		}
		return u
	case <-time.After(3 * time.Second):
		t.Fatal("no update")
	}
	return watchUpdate{}
}

func nextStatus(t *testing.T, h watchHandle) watchStatusUpdate {
	t.Helper()
	for {
		if u := nextUpdate(t, h); u.Status != nil {
			return *u.Status
		}
	}
}

func nextEvent(t *testing.T, h watchHandle) watchUpdate {
	t.Helper()
	for {
		if u := nextUpdate(t, h); u.Event != nil || u.Gap != "" {
			return u
		}
	}
}

func TestDocumentWatchAppliesLeasedWatchAndOwnedInference(t *testing.T) {
	f := newWatchDataClient()
	startTestWatch(t, fastDocumentBackend(), f, "chat-a-1")
	calls := f.callLog()
	applies := slices.DeleteFunc(slices.Clone(calls), func(c string) bool { return !strings.HasPrefix(c, "apply:") })
	if strings.Join(applies, ",") != "apply:watch/chat-a-1,apply:inference/chat-a-1" {
		t.Fatalf("applies = %v; the watch must come first", applies)
	}
	if !strings.HasPrefix(calls[0], "events:") {
		t.Fatalf("calls = %v; the journal tail must be read before applying", calls)
	}
	watchDoc, _ := f.document("watch/chat-a-1")
	inferenceDoc, _ := f.document("inference/chat-a-1")
	watch, err := watchDoc.Watch()
	if err != nil || watch.Lease != "60s" || watch.On.Inference != "chat-a-1" || len(watch.Notify) != 0 {
		t.Fatalf("watch = %+v, %v", watch, err)
	}
	inference, err := inferenceDoc.Inference()
	if err != nil || inferenceDoc.Owner != "watch/chat-a-1" || inference.Sources[0].Camera != "v4l2:/dev/video0" ||
		inference.Model != "ustc-community/dfine-nano-coco" || inference.Revision != "066438d3d8f0da137a37b38fdf3368fd4afceced" ||
		!slices.Equal(inference.Labels, []string{"person"}) || inference.Threshold != 0.5 || inference.Rate != 2 ||
		inference.ClearAfter != "5s" || inference.Cooldown != "30s" || inference.Event != "chat-a-1" {
		t.Fatalf("inference = %+v (owner %q), %v", inference, inferenceDoc.Owner, err)
	}
}

func TestDocumentWatchSendsOnlyTheDocumentKeys(t *testing.T) {
	watchYAML, inferenceYAML, err := watchDocuments(testWatchSpec("chat-a-1"))
	if err != nil {
		t.Fatal(err)
	}
	keys := func(raw []byte) string {
		var m map[string]any
		if err := yaml.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		var out []string
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return strings.Join(out, ",")
	}
	if got := keys(watchYAML); got != "kind,lease,name,on,version" {
		t.Fatalf("watch keys = %s", got)
	}
	if got := keys(inferenceYAML); got != "clear_after,cooldown,kind,labels,model,name,owner,rate,revision,sources,threshold,version" {
		t.Fatalf("inference keys = %s", got)
	}
}

func TestDocumentWatchApplyHasADeadline(t *testing.T) {
	f := newWatchDataClient()
	f.applyWait = time.Hour
	b := fastDocumentBackend()
	b.deployTimeout = 50 * time.Millisecond
	_, err := b.Start(context.Background(), &grpcclient.AgentConnection{DataService: f}, testWatchSpec("chat-a-1"))
	var unconfirmed watchUnconfirmedError
	if !errors.As(err, &unconfirmed) || !errors.Is(err, errWatchDeviceBusy) {
		t.Fatalf("error = %v; want an unconfirmed busy device", err)
	}
}

func TestDocumentWatchApplyCancelledByTheCallerIsNotBusy(t *testing.T) {
	f := newWatchDataClient()
	f.applyWait = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	_, err := fastDocumentBackend().Start(ctx, &grpcclient.AgentConnection{DataService: f}, testWatchSpec("chat-a-1"))
	if errors.Is(err, errWatchDeviceBusy) || !errors.As(err, new(watchUnconfirmedError)) {
		t.Fatalf("error = %v; want unconfirmed but not busy", err)
	}
}

func TestDocumentWatchRemovesTheWatchWhenTheInferenceFails(t *testing.T) {
	f := newWatchDataClient()
	f.applyErr[data.KindInference] = status.Error(codes.InvalidArgument, "inference.labels must contain 1..32 labels")
	b := fastDocumentBackend()
	_, err := b.Start(context.Background(), &grpcclient.AgentConnection{DataService: f}, testWatchSpec("chat-a-1"))
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("error = %v", err)
	}
	if refs := f.documentRefs(); len(refs) != 0 {
		t.Fatalf("left on the device: %v", refs)
	}
	if !slices.Contains(f.callLog(), "remove:watch/chat-a-1") {
		t.Fatal("the watch was not removed")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.poller != nil || len(b.pending) != 0 {
		t.Fatal("the failed start kept the poller or its pending name")
	}
}

func TestDocumentWatchOldAgentRefusesCleanly(t *testing.T) {
	for name, set := range map[string]func(*watchDataClient){
		"no DocumentApply": func(f *watchDataClient) { f.applyErr[data.KindWatch] = status.Error(codes.Unimplemented, "unknown method DocumentApply") },
		"old journal":      func(f *watchDataClient) { f.oldJournal = true },
	} {
		t.Run(name, func(t *testing.T) {
			f := newWatchDataClient()
			set(f)
			_, err := fastDocumentBackend().Start(context.Background(), &grpcclient.AgentConnection{DataService: f}, testWatchSpec("chat-a-1"))
			if !errors.Is(err, errWatchAgentTooOld) {
				t.Fatalf("error = %v", err)
			}
			if refs := f.documentRefs(); len(refs) != 0 {
				t.Fatalf("left on the device: %v", refs)
			}
		})
	}
}

func TestDocumentWatchMapsTheWatchState(t *testing.T) {
	f := newWatchDataClient()
	f.setInference("chat-a-1", data.InferenceStatus{State: "loading"})
	h := startTestWatch(t, fastDocumentBackend(), f, "chat-a-1")
	if s := nextStatus(t, h); s.State != watchPreparing || !strings.Contains(s.Reason, "loading the detector") {
		t.Fatalf("loading = %+v", s)
	}
	f.setInference("chat-a-1", data.InferenceStatus{State: "waiting_for_cameras", Error: "camera unplugged"})
	if s := nextStatus(t, h); s.State != watchError || !strings.Contains(s.Reason, "camera unplugged") {
		t.Fatalf("camera gone = %+v", s)
	}
	f.setInference("chat-a-1", data.InferenceStatus{State: "running"})
	if s := nextStatus(t, h); s.State != watchReady {
		t.Fatalf("running = %+v", s)
	}
	f.setInference("chat-a-1", data.InferenceStatus{State: "error", Error: "inference: ValueError: bad"})
	if s := nextStatus(t, h); s.State != watchError || s.Reason != "inference: ValueError: bad" {
		t.Fatalf("error = %+v", s)
	}
}

func TestDocumentWatchStaysInErrorWhileTheAgentRetries(t *testing.T) {
	f := newWatchDataClient()
	f.setInference("chat-a-1", data.InferenceStatus{State: "error", Error: "boom"})
	h := startTestWatch(t, fastDocumentBackend(), f, "chat-a-1")
	if s := nextStatus(t, h); s.State != watchError {
		t.Fatalf("first = %+v", s)
	}
	f.setInferenceSeen(t, "chat-a-1", data.InferenceStatus{State: "loading"})
	f.setInferenceSeen(t, "chat-a-1", data.InferenceStatus{State: "loading"})
	f.setInference("chat-a-1", data.InferenceStatus{State: "running"})
	if s := nextStatus(t, h); s.State != watchReady {
		t.Fatalf("after retry = %+v; loading between errors must not show as PREPARING", s)
	}
}

func TestDocumentWatchRenewsAndEndsWhenTheLeaseIsGone(t *testing.T) {
	f := newWatchDataClient()
	h := startTestWatch(t, fastDocumentBackend(), f, "chat-a-1")
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.mu.Lock()
		renews := f.renews["chat-a-1"]
		f.mu.Unlock()
		if renews >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the watch was not renewed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.mu.Lock()
	delete(f.documents, "watch/chat-a-1")
	f.mu.Unlock()
	for {
		s := nextStatus(t, h)
		if s.State == watchEnded {
			if s.Reason != watchGoneReason {
				t.Fatalf("reason = %q", s.Reason)
			}
			break
		}
	}
}

func TestDocumentWatchStopRemovesTheWatchAndItsInference(t *testing.T) {
	f := newWatchDataClient()
	h, err := fastDocumentBackend().Start(context.Background(), &grpcclient.AgentConnection{DataService: f}, testWatchSpec("chat-a-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if refs := f.documentRefs(); len(refs) != 0 {
		t.Fatalf("left on the device: %v", refs)
	}
	if err := h.Stop(context.Background()); err != nil {
		t.Fatalf("a second stop (NOT_FOUND) = %v", err)
	}
}

func TestDocumentPollerReadsOnlyItsOwnWatches(t *testing.T) {
	f := newWatchDataClient()
	b := fastDocumentBackend()
	h := startTestWatch(t, b, f, "chat-a-1")
	f.publish("chat-b-1", data.NotificationDetection{Label: "dog", Score: 0.8})
	f.publish("chat-a-1", data.NotificationDetection{Label: "person", Score: 0.9})
	u := nextEvent(t, h)
	if u.Event == nil || len(u.Event.Classes) != 1 || u.Event.Classes[0].Label != "person" {
		t.Fatalf("event = %+v; another session's watch leaked in", u)
	}
	if named := f.namedWatches(); !slices.Equal(named, []string{"chat-a-1"}) {
		t.Fatalf("the poller named %v", named)
	}
}

func TestDocumentPollerReportsGapsToEveryWatch(t *testing.T) {
	f := newWatchDataClient()
	b := fastDocumentBackend()
	first := startTestWatch(t, b, f, "chat-a-1")
	second := startTestWatch(t, b, f, "chat-a-2")
	f.mu.Lock()
	f.gapOnce = true
	f.mu.Unlock()
	for _, h := range []watchHandle{first, second} {
		if u := nextEvent(t, h); u.Gap == "" {
			t.Fatalf("update = %+v; want a gap", u)
		}
	}
}

func TestDocumentPollerCapsAResponse(t *testing.T) {
	f := newWatchDataClient()
	b := fastDocumentBackend()
	h := startTestWatch(t, b, f, "chat-a-1")
	f.mu.Lock()
	f.oversize = []byte(`[` + strings.Repeat(`{"watch":"chat-a-1"},`, 220_000) + `{}]`)
	f.mu.Unlock()
	if u := nextEvent(t, h); u.Gap == "" {
		t.Fatalf("update = %+v; an oversized response must become a gap", u)
	}
	f.publish("chat-a-1", data.NotificationDetection{Label: "person", Score: 0.9})
	if u := nextEvent(t, h); u.Event == nil {
		t.Fatalf("update = %+v; the poller must move on after the cap", u)
	}
}

func TestDocumentPollerStopsWithTheLastWatch(t *testing.T) {
	f := newWatchDataClient()
	b := fastDocumentBackend()
	h, err := b.Start(context.Background(), &grpcclient.AgentConnection{DataService: f}, testWatchSpec("chat-a-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.poller != nil {
		t.Fatal("the poller outlived the last watch")
	}
}

func TestDocumentPollerConcurrentStartsShareOnePoller(t *testing.T) {
	f := newWatchDataClient()
	b := fastDocumentBackend()
	var wg sync.WaitGroup
	for i := 1; i <= 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			startTestWatch(t, b, f, "chat-a-"+string(rune('0'+i)))
		}(i)
	}
	wg.Wait()
	tails := 0
	for _, call := range f.callLog() {
		if call == "events:" {
			tails++
		}
	}
	if tails != 1 {
		t.Fatalf("%d tail reads; concurrent starts must share one poller", tails)
	}
}
```

(`t.Cleanup` from `startTestWatch` runs on the test goroutine, so calling it from the goroutines in the concurrent test is safe: `t.Cleanup` is goroutine-safe.)

- [ ] **Step 4: Run the tests to verify they fail**

Run: `cd go && go test ./internal/cli/mcp/ -run 'DocumentWatch|DocumentPoller' -v`
Expected: build failure, `undefined: newDocumentWatchBackend`.

- [ ] **Step 5: Write the backend**

`git rm go/internal/cli/mcp/watch_campaign.go go/internal/cli/mcp/watch_campaign_test.go`, then create `watch_documents.go`:

```go
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"
)

const (
	// A 60 s lease renewed every 20 s survives two missed renewals.
	watchLease         = "60s"
	watchRenewEvery    = 20 * time.Second
	watchStatusEvery   = 5 * time.Second
	watchPollEvery     = time.Second
	watchInferenceRate = 2
	watchCallTimeout   = 10 * time.Second
	// The agent applies documents under the lock a recording campaign holds
	// while it starts a capture, which can take over 20 s.
	watchDeployTimeout = 30 * time.Second
	watchGoneReason    = "the device restarted or the watch expired"
	// The gateway reader's bounds on one journal response (spec §11.1).
	maxWatchJournalBytes   = 4 << 20
	maxWatchJournalEntries = 512
	watchGapReason         = "some detections may have been missed"
)

// errWatchAgentTooOld is returned for agents without data documents. They
// answer DocumentApply with UNIMPLEMENTED, so a refused watch leaves nothing
// on the device.
var errWatchAgentTooOld = errors.New("This device's agent is too old for watches; update it with `wendy device update`.")

// errWatchDeviceBusy is an apply the device did not take within
// watchDeployTimeout. The agent may still apply it once it is free; the lease
// then removes the watch, which nothing renews.
var errWatchDeviceBusy = errors.New("The device did not accept the watch within 30 s; it is busy, for example starting a recording. Try again in a minute. If the device accepts this watch late, it removes itself within a minute.")

// documentWatchBackend runs each watch as a leased watch document and an
// inference document the watch owns (data documents spec §11.1). Removing the
// watch, or its lease lapsing, removes both.
type documentWatchBackend struct {
	renewEvery    time.Duration
	statusEvery   time.Duration
	pollEvery     time.Duration
	callTimeout   time.Duration // each device call's deadline
	deployTimeout time.Duration // each apply's deadline

	mu      sync.Mutex
	watches map[string]*documentWatch // by watch name
	pending map[string]bool           // watches being started; the poller names them too
	poller  *documentPoller           // while any watch runs or starts

	// startMu serializes Start, so two concurrent starts never both create a
	// poller.
	startMu sync.Mutex
}

func newDocumentWatchBackend() *documentWatchBackend {
	return &documentWatchBackend{renewEvery: watchRenewEvery, statusEvery: watchStatusEvery, pollEvery: watchPollEvery, callTimeout: watchCallTimeout,
		deployTimeout: watchDeployTimeout, watches: map[string]*documentWatch{}, pending: map[string]bool{}}
}

func (b *documentWatchBackend) Start(ctx context.Context, conn *grpcclient.AgentConnection, spec watchSpec) (watchHandle, error) {
	if conn == nil || conn.DataService == nil {
		return nil, errWatchAgentTooOld
	}
	watchDoc, inferenceDoc, err := watchDocuments(spec)
	if err != nil {
		return nil, err
	}
	client := conn.DataService
	b.startMu.Lock()
	defer b.startMu.Unlock()
	b.mu.Lock()
	b.pending[spec.Name] = true
	b.mu.Unlock()
	fail := func(err error) (watchHandle, error) {
		b.mu.Lock()
		delete(b.pending, spec.Name)
		b.mu.Unlock()
		b.detachIfIdle()
		return nil, err
	}
	if err := b.attach(ctx, client); err != nil {
		return fail(err)
	}
	if err := b.apply(ctx, client, watchDoc); err != nil {
		return fail(err)
	}
	if err := b.apply(ctx, client, inferenceDoc); err != nil {
		// Removing the watch removes an inference the device applies late. If
		// the device cannot be reached, the watch's lease removes both.
		removeCtx, cancel := context.WithTimeout(context.Background(), b.callTimeout)
		_, _ = client.DocumentRemove(removeCtx, &agentpbv2.DataDocumentRemoveRequest{Kind: data.KindWatch, Name: spec.Name})
		cancel()
		return fail(err)
	}
	watchCtx, cancel := context.WithCancel(context.Background())
	w := &documentWatch{backend: b, client: client, name: spec.Name, ctx: watchCtx, cancel: cancel, updates: make(chan watchUpdate, 32), done: make(chan struct{})}
	b.mu.Lock()
	b.watches[spec.Name] = w
	delete(b.pending, spec.Name)
	b.mu.Unlock()
	go w.run()
	return w, nil
}

// apply sends one document. A call that ended without the device's answer
// may still land, which watchUnconfirmedError says.
func (b *documentWatchBackend) apply(ctx context.Context, client agentpbv2.DataServiceClient, document []byte) error {
	applyCtx, cancel := context.WithTimeout(ctx, b.deployTimeout)
	_, err := client.DocumentApply(applyCtx, &agentpbv2.DataDocumentApplyRequest{DocumentYaml: document})
	cancel()
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.DeadlineExceeded, codes.Canceled, codes.Unavailable:
		if status.Code(err) == codes.DeadlineExceeded && ctx.Err() == nil {
			err = errWatchDeviceBusy
		}
		return watchUnconfirmedError{err}
	}
	return watchDeviceError(err)
}

// watchDocuments builds the watch and the inference it owns (spec §11.1) and
// validates both locally, so an invalid document never reaches the device.
func watchDocuments(spec watchSpec) (watch, inference []byte, err error) {
	watch, err = yaml.Marshal(watchDocumentWire{Kind: data.KindWatch, Version: data.WatchVersion, Name: spec.Name, Lease: watchLease, On: watchTargetWire{Inference: spec.Name}})
	if err != nil {
		return nil, nil, err
	}
	inference, err = yaml.Marshal(inferenceDocumentWire{
		Kind: data.KindInference, Version: data.InferenceDocumentVersion, Name: spec.Name, Owner: data.KindWatch + "/" + spec.Name,
		Sources: []watchSourceWire{{Camera: spec.CameraID}}, Model: spec.Detector.Model, Revision: spec.Detector.Revision,
		Labels: spec.Classes, Threshold: spec.MinConfidence, Rate: watchInferenceRate, ClearAfter: "5s", Cooldown: "30s",
	})
	if err != nil {
		return nil, nil, err
	}
	for _, document := range [][]byte{watch, inference} {
		if _, err := data.ParseDocument(document); err != nil {
			return nil, nil, fmt.Errorf("building the watch documents: %w", err)
		}
	}
	return watch, inference, nil
}

// The wire structs carry exactly the keys spec §11.1 gives a chat watch's
// documents, with the agent parser's YAML names. They are not data.Watch and
// data.InferenceDocument, so a field later added there is never sent to agents
// that predate it.
type watchDocumentWire struct {
	Kind    string          `yaml:"kind"`
	Version int             `yaml:"version"`
	Name    string          `yaml:"name"`
	Lease   string          `yaml:"lease"`
	On      watchTargetWire `yaml:"on"`
}

type watchTargetWire struct {
	Inference string `yaml:"inference"`
}

type inferenceDocumentWire struct {
	Kind       string            `yaml:"kind"`
	Version    int               `yaml:"version"`
	Name       string            `yaml:"name"`
	Owner      string            `yaml:"owner"`
	Sources    []watchSourceWire `yaml:"sources"`
	Model      string            `yaml:"model"`
	Revision   string            `yaml:"revision"`
	Labels     []string          `yaml:"labels"`
	Threshold  float64           `yaml:"threshold"`
	Rate       float64           `yaml:"rate"`
	ClearAfter string            `yaml:"clear_after"`
	Cooldown   string            `yaml:"cooldown"`
}

type watchSourceWire struct {
	Camera string `yaml:"camera"`
}

// watchDeviceError maps a device refusal. An agent without data documents
// answers UNIMPLEMENTED.
func watchDeviceError(err error) error {
	if status.Code(err) == codes.Unimplemented {
		return errWatchAgentTooOld
	}
	return err
}

func (b *documentWatchBackend) forget(w *documentWatch) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.watches[w.name] == w {
		delete(b.watches, w.name)
	}
}

// names lists the watches the poller asks for: running and starting ones.
func (b *documentWatchBackend) names() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	names := slices.Collect(maps.Keys(b.watches))
	for name := range b.pending {
		names = append(names, name)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// documentPoller reads this server's watches' entries from one device's
// notification journal (spec §6.5, §11.1).
type documentPoller struct {
	client agentpbv2.DataServiceClient
	cursor string
	cancel context.CancelFunc
	done   chan struct{}
}

// attach starts the poller before a watch is applied. Its first read, with an
// empty cursor, returns the journal's tail, so a detection right after the
// apply is not missed. A poller for another client belongs to a connection the
// manager has already ended; it is replaced.
func (b *documentWatchBackend) attach(ctx context.Context, client agentpbv2.DataServiceClient) error {
	b.mu.Lock()
	if b.poller != nil && b.poller.client == client {
		b.mu.Unlock()
		return nil
	}
	old := b.poller
	b.poller = nil
	b.mu.Unlock()
	if old != nil {
		old.cancel()
		<-old.done
	}
	callCtx, cancel := context.WithTimeout(ctx, b.callTimeout)
	defer cancel()
	response, err := client.Events(callCtx, &agentpbv2.DataEventsRequest{NotificationsOnly: true})
	if err != nil {
		return watchDeviceError(err)
	}
	// An older agent ignores notifications_only and answers from its ordinary
	// event journal; it does not mark the response.
	if !response.GetNotifications() {
		return errWatchAgentTooOld
	}
	pollCtx, stop := context.WithCancel(context.Background())
	p := &documentPoller{client: client, cursor: response.GetCursor(), cancel: stop, done: make(chan struct{})}
	b.mu.Lock()
	b.poller = p
	b.mu.Unlock()
	go b.poll(pollCtx, p)
	return nil
}

// detachIfIdle stops the poller once no watch runs or starts.
func (b *documentWatchBackend) detachIfIdle() {
	b.mu.Lock()
	p := b.poller
	if len(b.watches) > 0 || len(b.pending) > 0 || p == nil {
		b.mu.Unlock()
		return
	}
	b.poller = nil
	b.mu.Unlock()
	p.cancel()
	<-p.done
}

func (b *documentWatchBackend) poll(ctx context.Context, p *documentPoller) {
	defer close(p.done)
	ticker := time.NewTicker(b.pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		names := b.names()
		if len(names) == 0 {
			continue // never read without names: that would be every unleased entry
		}
		callCtx, cancel := context.WithTimeout(ctx, b.callTimeout)
		response, err := p.client.Events(callCtx, &agentpbv2.DataEventsRequest{Cursor: p.cursor, Replay: true, NotificationsOnly: true, Watches: names})
		cancel()
		if err != nil || !response.GetNotifications() {
			continue // transient; the next read resumes from the same cursor
		}
		raw := response.GetEventsJson()
		var entries []data.CampaignNotification
		gap := response.GetGap()
		if len(raw) > maxWatchJournalBytes || json.Unmarshal(raw, &entries) != nil {
			entries, gap = nil, true
		}
		if len(entries) > maxWatchJournalEntries {
			entries, gap = entries[len(entries)-maxWatchJournalEntries:], true
		}
		p.cursor = response.GetCursor()
		b.route(entries, gap)
	}
}

// route turns journal entries into events for the watches they belong to.
func (b *documentWatchBackend) route(entries []data.CampaignNotification, gap bool) {
	b.mu.Lock()
	watches := maps.Clone(b.watches)
	b.mu.Unlock()
	if gap {
		for _, w := range watches {
			w.send(watchUpdate{Gap: watchGapReason})
		}
	}
	for _, entry := range entries {
		w := watches[entry.Watch]
		if w == nil {
			continue
		}
		occurred, err := time.Parse(time.RFC3339Nano, entry.OccurredAt)
		if err != nil {
			occurred = time.Now()
		}
		classes := make([]watchClass, 0, len(entry.Detections))
		for _, detection := range entry.Detections {
			classes = append(classes, watchClass{Label: detection.Label, Score: detection.Score})
		}
		w.send(watchUpdate{Event: &watchEventUpdate{Kind: "entered", Classes: classes, OccurredAt: occurred}})
	}
}

// documentWatch renews one watch's lease and follows its state.
type documentWatch struct {
	backend *documentWatchBackend
	client  agentpbv2.DataServiceClient
	name    string
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}

	sendMu  sync.Mutex
	closed  bool
	updates chan watchUpdate

	lastState  watchState
	lastReason string
}

func (w *documentWatch) Updates() <-chan watchUpdate { return w.updates }

// send delivers u unless the watch has stopped. The manager drains updates
// until they close, so a send blocks only briefly.
func (w *documentWatch) send(u watchUpdate) bool {
	w.sendMu.Lock()
	defer w.sendMu.Unlock()
	if w.closed {
		return false
	}
	select {
	case w.updates <- u:
		return true
	case <-w.ctx.Done():
		return false
	}
}

func (w *documentWatch) closeUpdates() {
	w.sendMu.Lock()
	defer w.sendMu.Unlock()
	if !w.closed {
		w.closed = true
		close(w.updates)
	}
}

func (w *documentWatch) run() {
	defer close(w.done)
	defer w.closeUpdates()
	defer w.backend.detachIfIdle()
	defer w.backend.forget(w)
	renew := time.NewTicker(w.backend.renewEvery)
	defer renew.Stop()
	inspect := time.NewTicker(w.backend.statusEvery)
	defer inspect.Stop()
	if !w.refreshStatus() {
		return
	}
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-renew.C:
			if !w.renew() {
				return
			}
		case <-inspect.C:
			if !w.refreshStatus() {
				return
			}
		}
	}
}

// renew pushes the lease forward. A transient failure is retried on the next
// tick. NOT_FOUND means the device restarted or the lease lapsed.
func (w *documentWatch) renew() bool {
	ctx, cancel := context.WithTimeout(w.ctx, w.backend.callTimeout)
	defer cancel()
	_, err := w.client.DocumentRenew(ctx, &agentpbv2.DataDocumentRenewRequest{Kind: data.KindWatch, Name: w.name})
	switch status.Code(err) {
	case codes.OK:
		return true
	case codes.NotFound:
		w.end(watchGoneReason)
		return false
	case codes.Unimplemented:
		w.end(errWatchAgentTooOld.Error())
		return false
	}
	return w.ctx.Err() == nil
}

// refreshStatus reports a change in the watch's state.
//
// After a failure the agent loads the detector again every 5 s, so it reports
// loading between errors. That retry belongs to ERROR: an ERROR watch stays
// ERROR, with its last reason, until the detector runs or fails differently.
func (w *documentWatch) refreshStatus() bool {
	ctx, cancel := context.WithTimeout(w.ctx, w.backend.callTimeout)
	defer cancel()
	response, err := w.client.Documents(ctx, &agentpbv2.DataDocumentsRequest{Kind: data.KindWatch, Name: w.name})
	if status.Code(err) == codes.NotFound {
		w.end(watchGoneReason)
		return false
	}
	if err != nil || len(response.GetDocuments()) != 1 {
		return w.ctx.Err() == nil
	}
	state, reason := watchStateFromDocument(response.GetDocuments()[0])
	if w.lastState == watchError && state == watchPreparing {
		return true
	}
	if state != w.lastState || reason != w.lastReason {
		w.lastState, w.lastReason = state, reason
		return w.send(watchUpdate{Status: &watchStatusUpdate{State: state, Reason: reason}})
	}
	return true
}

func (w *documentWatch) end(reason string) {
	w.send(watchUpdate{Status: &watchStatusUpdate{State: watchEnded, Reason: reason}})
}

// watchStateFromDocument maps the device's watch state and its inference's
// status (spec §6.6) to a watch state.
func watchStateFromDocument(document *agentpbv2.DataDocument) (watchState, string) {
	switch document.GetState() {
	case "active":
		return watchReady, ""
	case "error":
		return watchError, document.GetStateDetail()
	}
	var inference struct {
		State string `json:"inference_state"`
		Error string `json:"inference_error"`
	}
	_ = json.Unmarshal(document.GetStatusJson(), &inference)
	return watchStateFromInference(data.InferenceStatus{State: inference.State, Error: inference.Error})
}

// watchStateFromInference maps the agent's inference state.
func watchStateFromInference(s data.InferenceStatus) (watchState, string) {
	switch s.State {
	case "running":
		return watchReady, ""
	case "error":
		return watchError, s.Error
	case "waiting_for_cameras":
		if s.Error != "" {
			return watchError, "the camera is unavailable: " + s.Error
		}
		return watchError, "the camera is unavailable"
	case "loading":
		return watchPreparing, "loading the detector; a first watch on a device also installs it, which takes a few minutes"
	}
	return watchPreparing, ""
}

// Stop ends renewals, then removes the watch, which removes its inference.
// NOT_FOUND means it is already gone, which is the goal.
func (w *documentWatch) Stop(ctx context.Context) error {
	w.cancel()
	<-w.done
	_, err := w.client.DocumentRemove(ctx, &agentpbv2.DataDocumentRemoveRequest{Kind: data.KindWatch, Name: w.name})
	switch status.Code(err) {
	case codes.OK, codes.NotFound:
		return nil
	case codes.Unimplemented:
		return errWatchAgentTooOld
	}
	return err
}
```

Before deleting `watch_campaign.go`, compare its `watchStateFromInference` and `refreshStatus` with the code above; if #2207's review rounds changed them (for example the loading reason text), carry #2207's version over, since its tests (`TestWatchStateFromInferenceLoadingReasonFitsAWarmDevice`, now in this file's test) pin it.

- [ ] **Step 6: Construct the new backend**

In `watch_tools.go` (~line 261), replace `newCampaignWatchBackend(defaultWatchDetector)` with `newDocumentWatchBackend()`. In the `watch_start` handler, set `spec.Detector = defaultWatchDetector` for now (Task 3 replaces it with the list choice). Move `TestWatchStateFromInferenceLoadingReasonFitsAWarmDevice` from the deleted campaign test file into `watch_documents_test.go` unchanged.

- [ ] **Step 7: Run the tests to verify they pass**

Run: `cd go && go test ./internal/cli/mcp/ -run 'DocumentWatch|DocumentPoller|WatchState|Watch' -v && go test -race ./internal/cli/mcp/ -run 'DocumentWatch|DocumentPoller|WatchManager|WatchTools'`
Expected: PASS. Then `cd go && go vet ./internal/cli/mcp/ && go test ./internal/cli/mcp/` pass.

- [ ] **Step 8: Commit**

```bash
git add -A go/internal/cli/mcp/
git commit -m "feat(mcp): run camera watches as data documents

Each watch applies a leased watch document and then an inference it
owns, renews the watch, follows its state through Documents, and
removes the watch on stop, which removes the inference. If the
inference is refused, the watch is removed. The journal poller names
only this server's watches and bounds each response at 4 MiB and 512
entries, so another session's detections never reach it."
```

---

### Task 2: Server ids that cannot collide

**Files:**
- Modify: `go/internal/cli/mcp/watch_manager.go` (~line 108-111)
- Test: `go/internal/cli/mcp/watch_manager_test.go`

- [ ] **Step 1: Write the failing test**

Append to `watch_manager_test.go`:

```go
func TestWatchManagerServerIDsAreSixteenHexDigits(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		m := newWatchManager(newDocumentWatchBackend(), func(string, any) {}, func() uint64 { return 0 })
		if len(m.serverID) != 16 || strings.Trim(m.serverID, "0123456789abcdef") != "" {
			t.Fatalf("server id %q", m.serverID)
		}
		if seen[m.serverID] {
			t.Fatalf("server id %q repeated", m.serverID)
		}
		seen[m.serverID] = true
	}
}
```

Adjust the `newWatchManager` arguments to its real signature (`grep -n 'func newWatchManager' go/internal/cli/mcp/watch_manager.go`).

Run: `cd go && go test ./internal/cli/mcp/ -run TestWatchManagerServerIDsAreSixteenHexDigits -v`
Expected: FAIL, the id has 8 hex digits.

- [ ] **Step 2: Widen the id**

In `newWatchManager`, change the id array from 4 to 8 bytes (`var id [8]byte`), and update the comment that describes it ("8 random bytes, so two servers on one device do not pick the same name (#2207 review)"). Then `grep -rn 'chat-[0-9a-f]\{8\}-\|8-hex\|eight hex' go/internal/cli/mcp/ go/internal/cli/assets/docs/` and update any test or doc that assumes 8 hex digits.

- [ ] **Step 3: Run the tests and commit**

Run: `cd go && go test ./internal/cli/mcp/ -run 'WatchManager' -v` — PASS.

```bash
git add go/internal/cli/mcp/
git commit -m "fix(mcp): watch server ids are 8 random bytes

Two mcp serve processes could pick the same 4-byte id and the same
counter, and cross-route each other's watches (#2207 review)."
```

---

### Task 3: A vetted list of detectors

**Files:**
- Modify: `go/internal/cli/mcp/watch_detector.go`, `go/internal/cli/mcp/watch_tools.go` (`watch_sources` ~line 124; `watch_start` class check ~line 143-148)
- Test: `go/internal/cli/mcp/watch_detector_test.go`, `go/internal/cli/mcp/watch_tools_test.go`

**Interfaces:**
- Produces: `var watchDetectors []watchDetector` (v1: one entry, the former `defaultWatchDetector`); `watchDetector` gains `License string` and `Benchmark string`; `func detectorFor(classes []string) (watchDetector, bool)`. `defaultWatchDetector` is removed.

- [ ] **Step 1: Write the failing tests**

Replace the body of `watch_detector_test.go`'s tests with (keep its existing check that the labels match the checkpoint's `id2label` fixture, pointing it at `watchDetectors[0]`):

```go
func TestEveryDetectorMeetsTheEntryRules(t *testing.T) {
	revision := regexp.MustCompile(`^[0-9a-f]{40}$`)
	for _, d := range watchDetectors {
		if d.ID == "" || !strings.Contains(d.Model, "/") || !revision.MatchString(d.Revision) || len(d.Labels) == 0 || d.License == "" || d.Benchmark == "" {
			t.Errorf("detector %+v lacks an entry rule (spec §11.2)", d)
		}
	}
}

func TestDetectorForPicksTheFirstThatCoversTheClasses(t *testing.T) {
	d, ok := detectorFor([]string{"person", "dog"})
	if !ok || d.ID != "dfine-nano-coco" {
		t.Fatalf("detectorFor = %+v, %v", d, ok)
	}
	if _, ok := detectorFor([]string{"person", "forklift"}); ok {
		t.Fatal("a class no detector has was accepted")
	}
}
```

Append to `watch_tools_test.go` (using its existing helpers for calling a tool; `grep -n 'func callWatchTool\|func callTool' go/internal/cli/mcp/watch_tools_test.go`):

```go
func TestWatchStartRefusesClassesNoDetectorHas(t *testing.T) {
	s, client, _ := watchToolServer(t)
	out, isErr := callWatchTool(t, s, "watch_start", map[string]any{"camera": "v4l2:/dev/video0", "classes": []any{"person", "forklift"}})
	if !isErr || out["error_code"] != "INVALID_ARGUMENT" || !strings.Contains(out["message"].(string), `"forklift"`) || !strings.Contains(out["message"].(string), "watch_sources") {
		t.Fatalf("got %v %v", isErr, out)
	}
	if refs := client.documentRefs(); len(refs) != 0 {
		t.Fatalf("refused classes reached the device: %v", refs)
	}
}
```

`watchToolServer` builds the manager with `fastCampaignBackend()`; Task 1 changes that line to `fastDocumentBackend()`.

Also extend the existing `watch_sources` test to assert the result has `detectors` (a list with one entry whose `id` is `dfine-nano-coco` and which carries `labels`) in addition to the existing `detector` object.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd go && go test ./internal/cli/mcp/ -run 'Detector|WatchStart|WatchSources' -v`
Expected: build failure, `undefined: watchDetectors`.

- [ ] **Step 3: Make the list**

In `watch_detector.go`, replace the type's comment and `defaultWatchDetector` with:

```go
// watchDetector is one detector a chat watch may run: a model run as its
// publisher released it, pinned to a commit. Neither the LLM nor the user can
// name another model or a URL through the watch tools.
//
// An entry needs, recorded here (spec §11.2): a Hugging Face repository
// pinned to a commit; a licence that allows commercial use; safetensors
// weights the Transformers backend loads without remote code; its labels as
// that commit's id2label gives them, in class order; and an on-device result
// of at least 1.5 frames per second per watch with two watches on the Orin
// Nano CPU at the worker's default 2 threads.
type watchDetector struct {
	ID        string
	Model     string
	Revision  string
	License   string
	Benchmark string
	Labels    []string
}

// watchDetectors is the vetted list, in order of preference: a watch runs the
// first detector whose labels cover all its classes. Open-vocabulary
// detectors are too heavy for the CPU budget and wait for the GPU backend.
var watchDetectors = []watchDetector{{
	ID:        "dfine-nano-coco",
	Model:     "ustc-community/dfine-nano-coco",
	Revision:  "066438d3d8f0da137a37b38fdf3368fd4afceced",
	License:   "Apache-2.0",
	Benchmark: "Orin Nano CPU, 2 threads: 1.90 fps alone, 1.65-1.74 fps each with two watches (2026-10-07)",
	Labels:    /* the existing label list, unchanged */,
}}

// detectorFor is the first vetted detector whose labels include every class.
func detectorFor(classes []string) (watchDetector, bool) {
	for _, d := range watchDetectors {
		if !slices.ContainsFunc(classes, func(class string) bool { return !d.hasLabel(class) }) {
			return d, true
		}
	}
	return watchDetector{}, false
}
```

(Copy the existing `Labels` slice literal in place of the comment.)

In `watch_tools.go`:

1. In `watch_sources`, build `detectors := make([]map[string]any, 0, len(watchDetectors))` with `{"id", "model", "labels"}` per entry, and return both `"detector": detectors[0]` (unchanged for clients that read it) and `"detectors": detectors`. Update the tool's description to "…the detectors watches can run (id, model, class labels)…".
2. In `watch_start`, replace the per-class `defaultWatchDetector.hasLabel` loop with:

```go
	detector, ok := detectorFor(classes)
	if !ok {
		for _, class := range classes {
			if _, covered := detectorFor([]string{class}); !covered {
				return errResultf(errCodeInvalidArgument, "%q is not one of the detectors' labels; call watch_sources for them", class), nil
			}
		}
		return errResult(errCodeInvalidArgument, "no single detector covers all of these classes; start one watch per detector"), nil
	}
```

and set `spec.Detector = detector` where Task 1 set `defaultWatchDetector`. Update the `classes` parameter description to "1-10 class labels from one detector's labels in watch_sources".

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd go && go test ./internal/cli/mcp/ -run 'Detector|WatchStart|WatchSources|Watch' -v` — PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/mcp/
git commit -m "feat(mcp): watch detectors come from a vetted list

Each entry records its pinned revision, licence and on-device benchmark
(spec §11.2). watch_start runs the first detector whose labels cover
the requested classes; watch_sources lists every detector. The list
holds D-FINE nano for now."
```

---

### Task 4: Wording, verification, and the PR

**Files:**
- Modify: `go/internal/cli/assets/docs/integrations/mcp.mdx`, `go/internal/cli/mcp/tools_guide.go`, and any other text found below

- [ ] **Step 1: Replace campaign wording**

Run `grep -rn -i 'campaign' go/internal/cli/mcp/watch_*.go go/internal/cli/mcp/tools_guide.go go/internal/cli/assets/docs/integrations/mcp.mdx`. In user-facing text about watches, say what runs now: a watch runs a detector on the device as a leased watch and an inference document it owns; removing the watch, or the session ending, removes both. Keep `tools_guide.go`'s line within its byte budget (`go test ./internal/cli/mcp/ -run Guide` checks it). Commit:

```bash
git add go/internal/cli/
git commit -m "docs(mcp): camera watches run as data documents"
```

- [ ] **Step 2: Local verification**

```bash
cd go
gofmt -l .
go vet ./internal/cli/mcp/
go test -race ./internal/cli/mcp/ -run 'Watch|Document|Detector'
go test ./internal/cli/mcp/
go build ./...
```

Expected: `gofmt -l` prints nothing; the rest pass.

- [ ] **Step 3: On the Orin Nano** (controller-run; needs Ethan in front of the camera)

The device runs PR 4's agent (side-load it as PR 4's Task 10 did, if it was restored since). Build this branch's CLI to the scratchpad and drive `"$SCRATCH/wendy" mcp serve --tool-groups all --device <dev>` with an MCP stdio client script, as #2207's on-device check did (mcp-go client, `OnNotification` printing notifications):

1. `watch_sources` lists the Brio as `Brio 101` and one detector.
2. `watch_start` on the Brio with `["person"]`; READY within 30 s warm.
3. Ethan steps into view: a `notifications/wendy/watch_event` arrives with `kind: entered` and a `person` class; `wendy data documents` shows `watch/chat-…` (active, lease expiry) and `inference/chat-…` (owner `watch/chat-…`).
4. `watch_stop`: both documents gone (`wendy data documents`).
5. Start again, close the client's stdin: both gone within 5 s.
6. Start again, `kill -9` the script: both gone within 65 s.
7. A second `mcp serve` with its own watch on the same camera: neither session receives the other's event.

Restore the device as PR 4's Task 10 Step 5 does.

- [ ] **Step 4: Push and retarget #2207**

```bash
git push --force-with-lease origin ed/chat-watches-mcp
gh pr edit 2207 --base ed/data-documents-agent
gh pr comment 2207 --body "<what changed: rebased onto the data documents agent PR; the campaign backend is replaced by data documents (spec #2257 §11.1); review fixes: 8-byte server ids, poll bounds, the gateway leak closed on the device side (§6.5), the isolation test's distinct label; the gap stays journal-wide by decision (§6.5); the vetted detector list (§11.2); on-device results>"
```

Reply on each of Martien's #2207 review threads with what changed and the commit, in the thread (`gh api repos/wendylabsinc/WendyOS/pulls/2207/comments/<id>/replies -f body=…`): 4231144530 (server id → Task 2; gap routing → kept journal-wide, spec §6.5) and 4231197231 (poll caps → Task 1; renew timing → unchanged, 60 s lease with 20 s renewals; isolation test → Task 1). Reply to the issue comment about the gateway read with the device-side fix (PR 4, spec §6.5).
