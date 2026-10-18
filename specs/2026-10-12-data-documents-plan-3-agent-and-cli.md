# Data documents PR 4: inference and watch documents in the agent and CLI, implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A person or a client can apply `inference` and `watch` documents to a device. The inference runs a detector on cameras and emits arrivals; a watch turns one inference's arrivals into notifications, and a leased watch, together with what it owns, disappears when its client stops renewing it.

**Architecture:** The `data` package gains the two kinds (parsing, validation, revisions), a document store in `data/documents/<kind>/<name>.json`, notification journal fields for watches, and a way to write an inference document's records only into the episodes of collections that trigger on its event. The services package widens today's campaign inference loop to run inference documents too; a new watch runner holds lease deadlines, writes journal entries and sends Cloud or webhook deliveries; four new `DataService` calls apply, remove, renew and list documents. The CLI gains `wendy data apply`, `wendy data documents` and `wendy data remove`.

**Tech Stack:** Go 1.27, protobuf/gRPC (`Proto/wendy/agent/services/v2/data_service.proto`, Go generation only), `gopkg.in/yaml.v3`, cobra.

**Spec:** `specs/2026-10-12-data-documents-design.md` §4, §6, §7, §9, §10, §12, §13, §14 row 4. Read §6, §7 and §9 before starting any task.

## Global Constraints

- Branch `ed/data-documents-agent`, stacked on `ed/datadoc` (PR 3) with `ed/inference-worker-detections` (PR 1) merged in; worktree `/Users/ethan/Documents/WendyAgent-data-documents-agent`. Commits authored as `24462281+EBro912@users.noreply.github.com`.
- Kinds and versions: `inference` version 1, `watch` version 1; both ownable. `collection` is unchanged and never ownable.
- `lease`: a Go duration from `15s` through `10m`. A watch with `lease` is leased. Adding or dropping `lease`, or changing `owner`, on a redeploy is FAILED_PRECONDITION.
- `on.inference`: the name of an inference document; it need not exist yet.
- `notify`: a list of `cloud` (at most once) and `webhook: <url>` (at most once), with today's campaign webhook URL rule. The journal is always written.
- Inference document sources: cameras only, no per-source `capture` or `calibration_revision`. Its model fields, validation and Go type are `data.CampaignInference`. `event` defaults to the document's `name`.
- Stored at `filepath.Dir(<episode root>)/documents/<kind>/<name>.json`; collections stay in `campaigns/`.
- Owner rules: the owner must exist when the owned document is applied; it must have no owner itself; removal removes owned documents first, then the owner.
- Lease deadlines live in memory. At boot, before any inference job starts, every leased watch, everything those watches own, and every document whose owner is missing are deleted.
- `DocumentRenew` and the lease check before each watch notification take only the lease table's lock, never `deploymentMu`.
- `DocumentRemove` returns once the removed inference jobs have stopped, waiting at most 10 s; past that it returns DEADLINE_EXCEEDED with the documents already deleted.
- A watch's journal entry: `watch`, `inference`, `leased`, `detections`, no `campaign`. Leased entries are returned by `Events` only to a request that names the watch in `watches`; a request with `watches` gets only those watches' entries.
- An inference document's prediction and event records go only into episodes (open, or later through the pre-roll ring) of armed collections whose `capture.triggers` name its event. With no such collection, no prediction record is written. Watches are notified before records are written.
- Cloud identity of a watch notification: `watch:<name>`; the CLI registers it only for a watch with `cloud`.
- Generated code: commit only `go/proto/gen/agentpb/v2/data_service.pb.go` and `go/proto/gen/agentpb/v2/data_service_grpc.pb.go`; restore every other regenerated file.
- Run `gofmt -l .` from `go/` before every push. Never use `git stash` (worktrees share it).

## Review Focus

1. **A leased watch's lease lapses while its inference keeps finding people.** No journal entry may be written after the deadline, even before the 5 s pass removes the watch. Test: Task 6, `TestLapsedLeaseWritesNothingBeforeRemoval`.
2. **The agent restarts with a leased watch and its owned inference on disk.** Both are gone before any inference job starts, so the camera never opens. Test: Task 6, `TestBootSweepRemovesLeasedWatchesWhatTheyOwnAndOrphans`.
3. **`DocumentRemove` of a watch whose inference is mid-frame.** Once the call returns, the job has stopped and no later reconcile pass restarts it. Test: Task 7, `TestDocumentRemoveStopsOwnedInferenceBeforeReturning`.
4. **A person names an inference document like an existing campaign.** Both run as separate jobs; neither retires the other. Test: Task 5, `TestInferenceDocumentAndCampaignWithOneNameBothRun`.
5. **An unfiltered journal reader, like the robot gateway.** It never sees a leased watch's entries; a reader naming the watch does. Test: Task 3, `TestNotificationsHideLeasedWatchesFromOtherReaders`.

---

## File map

| File | Responsibility |
|---|---|
| `go/internal/shared/datadoc/datadoc.go` | export `ValidName` |
| `go/internal/agent/data/inference_document.go` (new) | `InferenceDocument`: schema, validation, revision, `Campaign()` |
| `go/internal/agent/data/watch.go` (new) | `Watch`, `WatchNotify`: schema, validation, revision, helpers |
| `go/internal/agent/data/documents.go` | register `inference` and `watch`; `KindInference`, `KindWatch`; strict decode helper |
| `go/internal/agent/data/document_store.go` (new) | `StoredDocument`, `SaveDocument`, `Document`, `Documents`, `RemoveDocument` |
| `go/internal/agent/data/campaign.go` | `validateWebhookURL` extracted from campaign validation |
| `go/internal/agent/data/notifications.go` | watch fields; identity rule; `NotificationsFor` |
| `go/internal/agent/data/manager.go` | `documentMu`; record audience; `RecordApplicationFor`; pre-roll audience |
| `Proto/wendy/agent/services/v2/data_service.proto` + generated Go | four calls, `DataDocument`, `DataEventsRequest.watches` |
| `go/internal/agent/services/data_events.go` | `watches` filter |
| `go/internal/agent/services/data_inference.go` | job keys, inference document jobs, `documentResult`, `reconcileNow`, `wakeNow` |
| `go/internal/agent/services/data_inference_notification.go` | shared `deliverNotification`; watch Cloud identity |
| `go/internal/agent/services/data_watch.go` (new) | lease table, watch runner, expiry, boot sweep, tree removal |
| `go/internal/agent/services/data_documents.go` (new) | the four RPC handlers and status messages |
| `go/internal/cli/commands/data_documents.go` (new) | `wendy data apply`, `documents`, `remove` |
| `go/internal/cli/commands/data.go` | shared file picker; campaign deploy points other kinds at `apply` |
| `go/internal/cli/assets/docs/clients/wendy-cli/commands/data.md` | data documents section |
| `Examples/WendyDataWatch/` (new) | inference, watch and collection examples |

---

### Task 0: Worktree

PR 1 (`ed/inference-worker-detections`, #2258) and PR 3 (`ed/datadoc`, #2259) are not on `main` yet. This branch starts from PR 3 and merges PR 1, because the watch runner uses PR 1's `detections` and `detectionNotification`.

- [ ] **Step 1: Create the worktree**

```bash
cd /Users/ethan/Documents/WendyAgent
git worktree add -b ed/data-documents-agent ../WendyAgent-data-documents-agent ed/datadoc
cd ../WendyAgent-data-documents-agent
git merge --no-edit ed/inference-worker-detections
git config user.email
cd go && go build ./... && go test ./internal/agent/data/ ./internal/shared/datadoc/
```

Expected: the merge is clean (the branches touch different files), the email is `24462281+EBro912@users.noreply.github.com`, and the build and tests pass.

---

### Task 1: The `inference` and `watch` kinds

**Files:**
- Modify: `go/internal/shared/datadoc/datadoc.go` (add `ValidName`)
- Create: `go/internal/agent/data/inference_document.go`
- Create: `go/internal/agent/data/watch.go`
- Modify: `go/internal/agent/data/documents.go`
- Modify: `go/internal/agent/data/campaign.go` (webhook check ~line 531)
- Test: `go/internal/agent/data/document_kinds_test.go` (new)

**Interfaces:**
- Produces:
  - `func datadoc.ValidName(name string) bool`
  - `const data.KindInference = "inference"`, `const data.KindWatch = "watch"`, `const data.InferenceDocumentVersion = 1`, `const data.WatchVersion = 1`, `const data.MinWatchLease = 15 * time.Second`, `const data.MaxWatchLease = 10 * time.Minute`
  - `type data.InferenceDocument struct { Kind, Version, Name, Owner; Sources []CampaignSource; CampaignInference (embedded, yaml inline) }` with `func (d InferenceDocument) Campaign(revision string) Campaign`
  - `type data.WatchTarget struct { Inference string }`, `type data.WatchNotify struct { Cloud bool; Webhook string }`, `type data.Watch struct { Kind, Version, Name, Owner, Lease string; On WatchTarget; Notify []WatchNotify }` with `Leased() bool`, `LeaseDuration() time.Duration`, `NotifiesCloud() bool`, `WebhookURL() string`
  - `func validateWebhookURL(field, raw string) error` (package-private)
  - For these kinds, `ParseDocument` returns `Body` as an `InferenceDocument` or `Watch` value.

- [ ] **Step 1: Write the failing tests**

Create `go/internal/agent/data/document_kinds_test.go`:

```go
package data

import (
	"strings"
	"testing"
	"time"
)

const testInferenceDocument = `kind: inference
version: 1
name: front-door
sources:
  - camera: v4l2:/dev/video0
model: ustc-community/dfine-nano-coco
revision: 066438d3d8f0da137a37b38fdf3368fd4afceced
labels: [person]
threshold: 0.5
rate: 2
clear_after: 5s
cooldown: 30s
`

const testWatchDocument = `kind: watch
version: 1
name: front-door
lease: 60s
on:
  inference: front-door
notify:
  - cloud
  - webhook: https://example.com/hooks/front-door
`

func TestInferenceDocumentParses(t *testing.T) {
	doc, err := ParseDocument([]byte(testInferenceDocument))
	if err != nil {
		t.Fatal(err)
	}
	inference, ok := doc.Body.(InferenceDocument)
	if !ok {
		t.Fatalf("body is %T", doc.Body)
	}
	if inference.Event != "front-door" {
		t.Fatalf("event = %q, want the document name", inference.Event)
	}
	if inference.Model != "ustc-community/dfine-nano-coco" || inference.Sources[0].Camera != "v4l2:/dev/video0" {
		t.Fatalf("inference = %+v", inference)
	}
	if doc.Revision == "" {
		t.Fatal("no revision")
	}
	campaign := inference.Campaign(doc.Revision)
	if campaign.Name != "front-door" || campaign.State != "armed" || campaign.Revision != doc.Revision || !campaign.Inference.IsEnabled() || campaign.Inference.Event != "front-door" {
		t.Fatalf("job campaign = %+v", campaign)
	}
}

func TestInferenceDocumentRevisionFollowsTheAuthor(t *testing.T) {
	first, err := ParseDocument([]byte(testInferenceDocument))
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseDocument([]byte("# a comment changes nothing\n" + testInferenceDocument))
	if err != nil {
		t.Fatal(err)
	}
	changed, err := ParseDocument([]byte(strings.Replace(testInferenceDocument, "threshold: 0.5", "threshold: 0.6", 1)))
	if err != nil {
		t.Fatal(err)
	}
	owned, err := ParseDocument([]byte(testInferenceDocument + "owner: watch/front-door\n"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != again.Revision {
		t.Fatal("a comment changed the revision")
	}
	if first.Revision == changed.Revision || first.Revision == owned.Revision {
		t.Fatal("an author change kept the revision")
	}
}

func TestInferenceDocumentErrors(t *testing.T) {
	cases := map[string]struct{ from, to, want string }{
		"no sources":        {"sources:\n  - camera: v4l2:/dev/video0\n", "sources: []\n", "at least one camera"},
		"audio source":      {"  - camera: v4l2:/dev/video0\n", "  - audio: cabin-mic\n", "cameras only"},
		"capture on source": {"  - camera: v4l2:/dev/video0\n", "  - camera: v4l2:/dev/video0\n    capture: {mode: snapshot, interval: 2s}\n", "cameras only"},
		"unpinned model":    {"revision: 066438d3d8f0da137a37b38fdf3368fd4afceced", "revision: main", "40-character"},
		"unknown field":     {"rate: 2\n", "rate: 2\nupload: {when: wifi}\n", "field upload not found"},
		"bad event":         {"rate: 2\n", "rate: 2\nevent: front door\n", "inference.event"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(testInferenceDocument, tc.from) {
				t.Fatalf("fixture lacks %q", tc.from)
			}
			_, err := ParseDocument([]byte(strings.Replace(testInferenceDocument, tc.from, tc.to, 1)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestWatchParses(t *testing.T) {
	doc, err := ParseDocument([]byte(testWatchDocument))
	if err != nil {
		t.Fatal(err)
	}
	watch, ok := doc.Body.(Watch)
	if !ok {
		t.Fatalf("body is %T", doc.Body)
	}
	if !watch.Leased() || watch.LeaseDuration() != time.Minute || watch.On.Inference != "front-door" {
		t.Fatalf("watch = %+v", watch)
	}
	if !watch.NotifiesCloud() || watch.WebhookURL() != "https://example.com/hooks/front-door" {
		t.Fatalf("notify = %+v", watch.Notify)
	}
	plain, err := ParseDocument([]byte("kind: watch\nversion: 1\nname: porch\non: {inference: porch}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if w := plain.Body.(Watch); w.Leased() || w.NotifiesCloud() || w.WebhookURL() != "" {
		t.Fatalf("plain watch = %+v", w)
	}
}

func TestWatchErrors(t *testing.T) {
	cases := map[string]struct{ from, to, want string }{
		"lease too short":   {"lease: 60s", "lease: 5s", "lease must be a duration from 15s to 10m"},
		"lease too long":    {"lease: 60s", "lease: 11m", "lease must be a duration from 15s to 10m"},
		"lease not a time":  {"lease: 60s", "lease: soon", "lease must be a duration from 15s to 10m"},
		"no inference":      {"  inference: front-door\n", "  inference: \"\"\n", "on.inference must name an inference document"},
		"bad inference":     {"  inference: front-door\n", "  inference: front door\n", "on.inference must name an inference document"},
		"filter on watch":   {"  inference: front-door\n", "  inference: front-door\n  labels: [person]\n", "field labels not found"},
		"unknown notify":    {"  - cloud\n", "  - email\n", "a notify entry is `cloud` or `webhook: <url>`"},
		"two clouds":        {"  - cloud\n", "  - cloud\n  - cloud\n", "at most once"},
		"bad webhook":       {"https://example.com/hooks/front-door", "ftp://example.com/x", "notify webhook must be an absolute HTTP(S) URL"},
		"webhook userinfo":  {"https://example.com/hooks/front-door", "https://user:pw@example.com/x", "notify webhook must be an absolute HTTP(S) URL"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(testWatchDocument, tc.from) {
				t.Fatalf("fixture lacks %q", tc.from)
			}
			_, err := ParseDocument([]byte(strings.Replace(testWatchDocument, tc.from, tc.to, 1)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCampaignWebhookRuleIsUnchanged(t *testing.T) {
	raw, err := ParseCampaign(inferenceCampaignWithWebhook("ftp://example.com/x"))
	if err == nil || !strings.Contains(err.Error(), "notify.webhook must be an absolute HTTP(S) URL without userinfo or fragment, at most 2048 bytes") {
		t.Fatalf("campaign webhook error = %v (%+v)", err, raw)
	}
}

func inferenceCampaignWithWebhook(webhook string) []byte {
	return []byte(`version: 1
name: people
sources: [{camera: "*"}]
inference:
  model: facebook/detr-resnet-50
  revision: 1d5f47bd3bdd2c4bbfa585418ffe6da5028b4c0b
  labels: [person]
  threshold: 0.9
  rate: 1
  event: person_detected
  clear_after: 5s
  cooldown: 30s
capture: {buffer: 0s, after_trigger: 1s, triggers: [{event: person_detected}]}
upload: {when: manual}
export: {annotation: cvat}
notify: {on: detection, webhook: "` + webhook + `"}
`)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd go && go test ./internal/agent/data/ -run 'InferenceDocument|Watch|CampaignWebhookRule' -v`
Expected: build failure, `undefined: InferenceDocument`.

- [ ] **Step 3: Tighten the header (carried over from PR 3's review)**

PR 3's final review deferred two header items to this PR: a header field of the wrong YAML type should be named in the error (spec §5.2), and `NewRegistry` should refuse kind names a document could never name as an owner.

First add these tests to `go/internal/shared/datadoc/datadoc_test.go`:

```go
func TestHeaderTypeErrorsNameTheField(t *testing.T) {
	registry := testRegistry(nil)
	for doc, want := range map[string]string{
		"kind: [a]\nversion: 1\nname: x\n":                 "kind must be a string",
		"version: 1\nname: {a: b}\n":                       "name must be a string",
		"kind: inference\nversion: 1\nname: x\nowner: [w]\n": "owner must be a string",
		"version: [1]\nname: x\n":                          "version must be a whole number",
		"- a\n- b\n":                                       "a data document is a YAML mapping",
	} {
		if _, err := registry.ParseHeader([]byte(doc)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error = %v, want %q", doc, err, want)
		}
	}
}

func TestNewRegistryRefusesUnnameableKinds(t *testing.T) {
	decode := func(Header, []byte) (any, string, []string, error) { return nil, "", nil, nil }
	for _, name := range []string{"a/b", "has space", strings.Repeat("k", 129)} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("NewRegistry accepted kind %q", name)
				}
			}()
			NewRegistry(Kind{Name: name, Versions: []int{1}, Decode: decode})
		}()
	}
}
```

and change the `"not a mapping"` case in `TestParseHeaderErrors` to expect `"a data document is a YAML mapping"`.

Then, in `go/internal/shared/datadoc/datadoc.go`:

1. Below `var nameRE = ...` add:

```go
// ValidName reports whether name may name a document: 1 to 128 letters,
// numbers, '.', '-' or '_'. Kinds use it for names they reference.
func ValidName(name string) bool { return nameRE.MatchString(name) }
```

2. In `NewRegistry`, extend the first check to `if kind.Name == "" || !nameRE.MatchString(kind.Name) || len(kind.Versions) == 0 || kind.Decode == nil` (update the panic text to "a kind needs a document-name-shaped name, versions and a decoder"), and store `kind.Versions = slices.Clone(kind.Versions)` before `r.kinds[kind.Name] = kind`.

3. In `ParseHeader`, replace the first decode

```go
	var raw rawHeader
	if err := decoder.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return Header{}, errors.New("document is empty")
		}
		return Header{}, fmt.Errorf("parsing document header: %w", err)
	}
```

with

```go
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		if errors.Is(err, io.EOF) {
			return Header{}, errors.New("document is empty")
		}
		return Header{}, fmt.Errorf("parsing document header: %w", err)
	}
	if err := checkHeaderTypes(&root); err != nil {
		return Header{}, err
	}
	var raw rawHeader
	if err := root.Decode(&raw); err != nil {
		return Header{}, fmt.Errorf("parsing document header: %w", err)
	}
```

and add:

```go
// checkHeaderTypes names the header field that holds a list or mapping where
// a scalar belongs, instead of yaml's generic type error (spec §5.2).
func checkHeaderTypes(document *yaml.Node) error {
	if document.Kind != yaml.DocumentNode || len(document.Content) == 0 {
		return errors.New("document is empty")
	}
	if document.Content[0].Kind != yaml.MappingNode {
		return errors.New("a data document is a YAML mapping")
	}
	fields := document.Content[0].Content
	for i := 0; i+1 < len(fields); i += 2 {
		key, value := fields[i].Value, fields[i+1]
		if value.Kind == yaml.ScalarNode || value.Kind == yaml.AliasNode {
			continue
		}
		switch key {
		case "kind", "name", "owner":
			return fmt.Errorf("%s must be a string", key)
		case "version":
			return errors.New("version must be a whole number")
		}
	}
	return nil
}
```

Run `cd go && go test ./internal/shared/datadoc/ -v`; every case passes, including PR 3's.

- [ ] **Step 4: Extract the webhook URL rule**

In `campaign.go`, replace

```go
		if c.Notify.Webhook != "" {
			endpoint, err := url.Parse(c.Notify.Webhook)
			if err != nil || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Fragment != "" || len(c.Notify.Webhook) > 2048 {
				return errors.New("notify.webhook must be an absolute HTTP(S) URL without userinfo or fragment, at most 2048 bytes")
			}
		}
```

with

```go
		if c.Notify.Webhook != "" {
			if err := validateWebhookURL("notify.webhook", c.Notify.Webhook); err != nil {
				return err
			}
		}
```

and add at the end of `campaign.go`:

```go
// validateWebhookURL is the rule every notification webhook follows: an
// absolute http or https URL with a host, no userinfo or fragment, at most
// 2048 bytes. field names the setting in the error.
func validateWebhookURL(field, raw string) error {
	endpoint, err := url.Parse(raw)
	if err != nil || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Fragment != "" || len(raw) > 2048 {
		return fmt.Errorf("%s must be an absolute HTTP(S) URL without userinfo or fragment, at most 2048 bytes", field)
	}
	return nil
}
```

- [ ] **Step 5: Add the inference kind**

Create `go/internal/agent/data/inference_document.go`:

```go
package data

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/datadoc"
)

// InferenceDocumentVersion is the inference document schema this agent reads.
const InferenceDocumentVersion = 1

// inferenceRevisionSchema versions the field list digestInput covers. Adding a
// field there is a schema change: bump it with the field.
const inferenceRevisionSchema = 1

// InferenceDocument runs one detector on cameras and emits one event per
// arrival (spec §7). Its model fields are a campaign's inline inference block,
// with the same type and validation.
type InferenceDocument struct {
	Kind              string           `json:"kind" yaml:"kind"`
	Version           int              `json:"version" yaml:"version"`
	Name              string           `json:"name" yaml:"name"`
	Owner             string           `json:"owner,omitempty" yaml:"owner,omitempty"`
	Sources           []CampaignSource `json:"sources" yaml:"sources"`
	CampaignInference `yaml:",inline"`
}

func decodeInferenceDocument(_ datadoc.Header, contents []byte) (any, string, []string, error) {
	var document InferenceDocument
	if err := decodeDocumentStrict(contents, &document); err != nil {
		return nil, "", nil, err
	}
	if err := document.validate(); err != nil {
		return nil, "", nil, err
	}
	return document, document.revision(), nil, nil
}

func (d *InferenceDocument) validate() error {
	if len(d.Sources) == 0 {
		return errors.New("an inference must define at least one camera source")
	}
	for i, source := range d.Sources {
		if source.Camera == "" || strings.TrimSpace(source.Camera) != source.Camera || source.Audio != "" || source.ROS2 != "" || source.Telemetry || source.Calibration != "" || source.Capture != nil {
			return fmt.Errorf("sources[%d]: an inference reads cameras only: use `camera: <selector>`, without capture or calibration_revision", i)
		}
	}
	if d.Event == "" {
		d.Event = d.Name
	}
	return d.CampaignInference.validate()
}

func (d InferenceDocument) revision() string {
	cameras := make([]string, len(d.Sources))
	for i, source := range d.Sources {
		cameras[i] = source.Camera
	}
	canonical, _ := json.Marshal(map[string]any{
		"revision_schema": inferenceRevisionSchema,
		"version":         d.Version,
		"name":            d.Name,
		"owner":           d.Owner,
		"cameras":         cameras,
		"backend":         d.Backend,
		"model_file":      d.ModelFile,
		"model":           d.Model,
		"model_revision":  d.Revision,
		"labels":          d.Labels,
		"threshold":       d.Threshold,
		"rate":            d.Rate,
		"event":           d.Event,
		"clear_after":     d.ClearAfter,
		"cooldown":        d.Cooldown,
		"enabled":         d.IsEnabled(),
	})
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:])
}

// Campaign is the inference as the inference runner's job sees it: an armed
// plan with these cameras and this model, and nothing to capture.
func (d InferenceDocument) Campaign(revision string) Campaign {
	inference := d.CampaignInference
	return Campaign{Version: CampaignVersion, Name: d.Name, Sources: d.Sources, Inference: &inference, State: "armed", Revision: revision}
}
```

(`CampaignInference.validate` is the existing pointer method in `inference.go`; it reports "inference.revision must pin a 40-character Hugging Face commit SHA" and "inference.event must use 1..128 letters…", which the tests match.)

- [ ] **Step 6: Add the watch kind**

Create `go/internal/agent/data/watch.go`:

```go
package data

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/datadoc"
	"gopkg.in/yaml.v3"
)

// WatchVersion is the watch document schema this agent reads.
const WatchVersion = 1

// watchRevisionSchema versions the field list revision covers.
const watchRevisionSchema = 1

// The lease range a watch may ask for (spec §6.1).
const (
	MinWatchLease = 15 * time.Second
	MaxWatchLease = 10 * time.Minute
)

// WatchTarget names the inference whose arrivals a watch receives.
type WatchTarget struct {
	Inference string `json:"inference" yaml:"inference"`
}

// WatchNotify is one destination beyond the journal: `cloud`, or
// `webhook: <url>`.
type WatchNotify struct {
	Cloud   bool   `json:"cloud,omitempty" yaml:"-"`
	Webhook string `json:"webhook,omitempty" yaml:"-"`
}

func (n *WatchNotify) UnmarshalYAML(node *yaml.Node) error {
	switch {
	case node.Kind == yaml.ScalarNode && node.Value == "cloud":
		n.Cloud = true
		return nil
	case node.Kind == yaml.MappingNode && len(node.Content) == 2 && node.Content[0].Value == "webhook":
		return node.Content[1].Decode(&n.Webhook)
	}
	return fmt.Errorf("line %d: a notify entry is `cloud` or `webhook: <url>`", node.Line)
}

// Watch delivers one inference's arrivals as notifications (spec §6). A watch
// with a lease is removed, with what it owns, when its lease lapses.
type Watch struct {
	Kind    string        `json:"kind" yaml:"kind"`
	Version int           `json:"version" yaml:"version"`
	Name    string        `json:"name" yaml:"name"`
	Owner   string        `json:"owner,omitempty" yaml:"owner,omitempty"`
	Lease   string        `json:"lease,omitempty" yaml:"lease,omitempty"`
	On      WatchTarget   `json:"on" yaml:"on"`
	Notify  []WatchNotify `json:"notify,omitempty" yaml:"notify,omitempty"`
}

func decodeWatch(_ datadoc.Header, contents []byte) (any, string, []string, error) {
	var watch Watch
	if err := decodeDocumentStrict(contents, &watch); err != nil {
		return nil, "", nil, err
	}
	if err := watch.validate(); err != nil {
		return nil, "", nil, err
	}
	return watch, watch.revision(), nil, nil
}

func (w Watch) validate() error {
	if w.Lease != "" {
		lease, err := time.ParseDuration(w.Lease)
		if err != nil || lease < MinWatchLease || lease > MaxWatchLease {
			return errors.New("lease must be a duration from 15s to 10m")
		}
	}
	if !datadoc.ValidName(w.On.Inference) {
		return errors.New("on.inference must name an inference document")
	}
	clouds, webhooks := 0, 0
	for _, notify := range w.Notify {
		if notify.Cloud {
			clouds++
		}
		if notify.Webhook != "" {
			webhooks++
			if err := validateWebhookURL("notify webhook", notify.Webhook); err != nil {
				return err
			}
		}
	}
	if clouds > 1 || webhooks > 1 {
		return errors.New("notify lists cloud and webhook at most once each")
	}
	return nil
}

func (w Watch) revision() string {
	canonical, _ := json.Marshal(map[string]any{
		"revision_schema": watchRevisionSchema,
		"version":         w.Version,
		"name":            w.Name,
		"owner":           w.Owner,
		"lease":           w.Lease,
		"on_inference":    w.On.Inference,
		"notify":          w.Notify,
	})
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:])
}

// Leased reports whether the device removes the watch when it is not renewed.
func (w Watch) Leased() bool { return w.Lease != "" }

// LeaseDuration is the lease, or zero for a watch without one.
func (w Watch) LeaseDuration() time.Duration {
	lease, _ := time.ParseDuration(w.Lease)
	return lease
}

// NotifiesCloud reports whether the watch delivers through Wendy Cloud.
func (w Watch) NotifiesCloud() bool {
	for _, notify := range w.Notify {
		if notify.Cloud {
			return true
		}
	}
	return false
}

// WebhookURL is the watch's webhook, or empty.
func (w Watch) WebhookURL() string {
	for _, notify := range w.Notify {
		if notify.Webhook != "" {
			return notify.Webhook
		}
	}
	return ""
}
```

- [ ] **Step 7: Register both kinds**

Replace `go/internal/agent/data/documents.go` with:

```go
package data

import (
	"bytes"
	"fmt"

	"github.com/wendylabsinc/wendy/go/internal/shared/datadoc"
	"gopkg.in/yaml.v3"
)

// The document kinds this agent stores and runs itself. A collection is a
// campaign and keeps its own storage.
const (
	KindInference = "inference"
	KindWatch     = "watch"
)

// documentKinds are the data document kinds this build understands. A
// collection is a campaign: its decoder is ParseCampaign, so collections parse,
// validate and hash exactly as campaigns always have.
var documentKinds = datadoc.NewRegistry(
	datadoc.Kind{
		Name:     datadoc.DefaultKind,
		Versions: []int{CampaignVersion},
		Decode: func(_ datadoc.Header, contents []byte) (any, string, []string, error) {
			campaign, err := ParseCampaign(contents)
			if err != nil {
				return nil, "", nil, err
			}
			return campaign, campaign.Revision, nil, nil
		},
	},
	datadoc.Kind{Name: KindInference, Versions: []int{InferenceDocumentVersion}, Ownable: true, Decode: decodeInferenceDocument},
	datadoc.Kind{Name: KindWatch, Versions: []int{WatchVersion}, Ownable: true, Decode: decodeWatch},
)

// ParseDocument parses any data document this build knows. A file without
// kind is a collection.
func ParseDocument(contents []byte) (datadoc.Document, error) {
	return documentKinds.Parse(contents)
}

// DocumentHeader checks only a document's header, for callers that sort files
// by kind without validating them.
func DocumentHeader(contents []byte) (datadoc.Header, error) {
	return documentKinds.ParseHeader(contents)
}

// decodeDocumentStrict decodes a document with unknown fields rejected. The
// registry has already checked that the file holds one document.
func decodeDocumentStrict(contents []byte, into any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("parsing document: %w", err)
	}
	return nil
}
```

(Keep `ParseDocument` and `DocumentHeader` exactly as PR 3 wrote them if their doc comments differ; only the registry and the new helper change.)

- [ ] **Step 8: Run the tests to verify they pass**

Run: `cd go && go test ./internal/shared/datadoc/ ./internal/agent/data/ -v -run 'InferenceDocument|Watch|CampaignWebhookRule|ParseDocument|ParseCampaign'`
Expected: PASS. Then `cd go && go test ./internal/agent/data/` passes in full (the golden campaign revisions from PR 3 must hold).

- [ ] **Step 9: Commit**

```bash
git add go/internal/shared/datadoc/datadoc.go go/internal/agent/data/
git commit -m "feat(data): inference and watch data documents

An inference document runs a campaign's inference block on cameras, with
its event defaulting to its name; a watch names the inference whose
arrivals it delivers, an optional lease, and cloud or webhook
destinations. Both are ownable kinds in the shared registry. The webhook
URL rule moves into one function both campaigns and watches use."
```

(Add the co-author trailer your session's attribution instructions give you.)

---

### Task 2: The document store

**Files:**
- Create: `go/internal/agent/data/document_store.go`
- Modify: `go/internal/agent/data/manager.go` (Manager struct: add `documentMu` beside `campaignMu`, ~line 87)
- Test: `go/internal/agent/data/document_store_test.go` (new)

**Interfaces:**
- Consumes: Task 1's kinds; `datadoc.Document`, `datadoc.ValidName`.
- Produces:
  - `type StoredDocument struct { Kind string; Version int; Name, Owner, Revision string; DeployedUnixNanos int64; Warnings []string; Body json.RawMessage }` (JSON keys `kind`, `version`, `name`, `owner,omitempty`, `revision`, `deployed_unix_nanos`, `warnings,omitempty`, `body`)
  - `func (d StoredDocument) Ref() string` → `"<kind>/<name>"`
  - `func (d StoredDocument) Inference() (InferenceDocument, error)`, `func (d StoredDocument) Watch() (Watch, error)`
  - `var ErrInvalidDocumentName`, `var ErrNotStoredKind`
  - `func (m *Manager) SaveDocument(doc datadoc.Document) (StoredDocument, error)`
  - `func (m *Manager) Document(kind, name string) (StoredDocument, error)` (wraps `os.ErrNotExist` when absent)
  - `func (m *Manager) Documents(kind string) ([]StoredDocument, error)` (sorted by name; unreadable files warned about and skipped)
  - `func (m *Manager) RemoveDocument(kind, name string) error` (wraps `os.ErrNotExist` when absent)

- [ ] **Step 1: Write the failing tests**

Create `go/internal/agent/data/document_store_test.go`:

```go
package data

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func saveTestDocument(t *testing.T, m *Manager, contents string) StoredDocument {
	t.Helper()
	doc, err := ParseDocument([]byte(contents))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := m.SaveDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func TestDocumentStoreRoundTrip(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stored := saveTestDocument(t, m, testWatchDocument)
	if stored.Ref() != "watch/front-door" || stored.Revision == "" || stored.DeployedUnixNanos == 0 {
		t.Fatalf("stored = %+v", stored)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(m.root), "documents", "watch", "front-door.json")); err != nil {
		t.Fatalf("document file: %v", err)
	}
	loaded, err := m.Document(KindWatch, "front-door")
	if err != nil {
		t.Fatal(err)
	}
	watch, err := loaded.Watch()
	if err != nil || !watch.Leased() || watch.On.Inference != "front-door" || !watch.NotifiesCloud() {
		t.Fatalf("loaded watch = %+v, %v", watch, err)
	}
	if loaded.Revision != stored.Revision {
		t.Fatal("revision changed in storage")
	}
	inference := saveTestDocument(t, m, testInferenceDocument)
	document, err := inference.Inference()
	if err != nil || document.Event != "front-door" || document.Model != "ustc-community/dfine-nano-coco" {
		t.Fatalf("loaded inference = %+v, %v", document, err)
	}
	if _, err := inference.Watch(); err == nil {
		t.Fatal("an inference document decoded as a watch")
	}
}

func TestDocumentsListsByKindSkippingUnreadable(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var warnings []string
	m.SetWarnLogger(func(msg string) { warnings = append(warnings, msg) })
	saveTestDocument(t, m, strings.Replace(testWatchDocument, "name: front-door", "name: zz", 1))
	saveTestDocument(t, m, testWatchDocument)
	saveTestDocument(t, m, testInferenceDocument)
	if err := os.WriteFile(filepath.Join(filepath.Dir(m.root), "documents", "watch", "broken.json"), []byte("{"), 0o640); err != nil {
		t.Fatal(err)
	}
	watches, err := m.Documents(KindWatch)
	if err != nil {
		t.Fatal(err)
	}
	if len(watches) != 2 || watches[0].Name != "front-door" || watches[1].Name != "zz" {
		t.Fatalf("watches = %+v", watches)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "broken") {
		t.Fatalf("warnings = %v", warnings)
	}
	none, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if empty, err := none.Documents(KindInference); err != nil || len(empty) != 0 {
		t.Fatalf("empty store = %v, %v", empty, err)
	}
}

func TestDocumentStoreErrors(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Document(KindWatch, "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing document error = %v", err)
	}
	if err := m.RemoveDocument(KindWatch, "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing removal error = %v", err)
	}
	if _, err := m.Document(KindWatch, "../escape"); !errors.Is(err, ErrInvalidDocumentName) {
		t.Fatalf("escaping name error = %v", err)
	}
	if _, err := m.Documents("collection"); !errors.Is(err, ErrNotStoredKind) {
		t.Fatalf("collection listing error = %v", err)
	}
	collection, err := ParseDocument(minimalCampaign(1, "telemetry: true", "wifi"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SaveDocument(collection); !errors.Is(err, ErrNotStoredKind) {
		t.Fatalf("saving a collection error = %v", err)
	}
	saveTestDocument(t, m, testWatchDocument)
	if err := m.RemoveDocument(KindWatch, "front-door"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Document(KindWatch, "front-door"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed document error = %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd go && go test ./internal/agent/data/ -run 'DocumentStore|DocumentsLists' -v`
Expected: build failure, `m.SaveDocument undefined`.

- [ ] **Step 3: Add the lock**

In `manager.go`'s `Manager` struct, directly below the `campaignMu sync.Mutex` field and its comment, add:

```go
	// documentMu serializes inference and watch document writes and removals.
	documentMu sync.Mutex
```

- [ ] **Step 4: Implement the store**

Create `go/internal/agent/data/document_store.go`:

```go
package data

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/atomicfile"
	"github.com/wendylabsinc/wendy/go/internal/shared/datadoc"
)

var (
	ErrInvalidDocumentName = errors.New("invalid document name")
	ErrNotStoredKind       = errors.New("only inference and watch documents are stored as documents; a collection is a campaign")
)

// StoredDocument is an inference or watch document as the agent keeps it, in
// data/documents/<kind>/<name>.json beside data/campaigns (spec §5.6).
type StoredDocument struct {
	Kind              string          `json:"kind"`
	Version           int             `json:"version"`
	Name              string          `json:"name"`
	Owner             string          `json:"owner,omitempty"`
	Revision          string          `json:"revision"`
	DeployedUnixNanos int64           `json:"deployed_unix_nanos"`
	Warnings          []string        `json:"warnings,omitempty"`
	Body              json.RawMessage `json:"body"`
}

// Ref is the document's "<kind>/<name>", the form owners are named in.
func (d StoredDocument) Ref() string { return d.Kind + "/" + d.Name }

// Inference decodes an inference document's body.
func (d StoredDocument) Inference() (InferenceDocument, error) {
	var document InferenceDocument
	if d.Kind != KindInference {
		return document, fmt.Errorf("%s is not an inference document", d.Ref())
	}
	err := json.Unmarshal(d.Body, &document)
	return document, err
}

// Watch decodes a watch document's body.
func (d StoredDocument) Watch() (Watch, error) {
	var watch Watch
	if d.Kind != KindWatch {
		return watch, fmt.Errorf("%s is not a watch document", d.Ref())
	}
	err := json.Unmarshal(d.Body, &watch)
	return watch, err
}

func storedKind(kind string) bool { return kind == KindInference || kind == KindWatch }

func (m *Manager) documentsDir(kind string) string {
	return filepath.Join(filepath.Dir(m.root), "documents", kind)
}

func (m *Manager) documentPath(kind, name string) (string, error) {
	if !storedKind(kind) {
		return "", ErrNotStoredKind
	}
	if !datadoc.ValidName(name) {
		return "", ErrInvalidDocumentName
	}
	return filepath.Join(m.documentsDir(kind), name+".json"), nil
}

// SaveDocument stores a parsed inference or watch document, replacing any
// earlier one of the same kind and name. Owner and lease rules are the
// caller's: this is storage only.
func (m *Manager) SaveDocument(doc datadoc.Document) (StoredDocument, error) {
	path, err := m.documentPath(doc.Header.Kind, doc.Header.Name)
	if err != nil {
		return StoredDocument{}, err
	}
	body, err := json.Marshal(doc.Body)
	if err != nil {
		return StoredDocument{}, err
	}
	stored := StoredDocument{Kind: doc.Header.Kind, Version: doc.Header.Version, Name: doc.Header.Name, Owner: doc.Header.Owner,
		Revision: doc.Revision, DeployedUnixNanos: time.Now().UnixNano(), Warnings: doc.Warnings, Body: body}
	encoded, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return StoredDocument{}, err
	}
	m.documentMu.Lock()
	defer m.documentMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return StoredDocument{}, err
	}
	if err := atomicfile.Write(path, append(encoded, '\n'), 0o640); err != nil {
		return StoredDocument{}, err
	}
	return stored, nil
}

// Document reads one stored document. A missing one is os.ErrNotExist.
func (m *Manager) Document(kind, name string) (StoredDocument, error) {
	path, err := m.documentPath(kind, name)
	if err != nil {
		return StoredDocument{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return StoredDocument{}, err
	}
	var stored StoredDocument
	if err := json.Unmarshal(raw, &stored); err != nil {
		return StoredDocument{}, fmt.Errorf("reading %s/%s: %w", kind, name, err)
	}
	return stored, nil
}

// Documents lists one kind's documents by name. A file that cannot be read is
// reported through the warning logger and skipped, as Campaigns does.
func (m *Manager) Documents(kind string) ([]StoredDocument, error) {
	if !storedKind(kind) {
		return nil, ErrNotStoredKind
	}
	entries, err := os.ReadDir(m.documentsDir(kind))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var documents []StoredDocument
	for _, entry := range entries {
		name, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || entry.IsDir() {
			continue
		}
		stored, err := m.Document(kind, name)
		if err != nil {
			m.Warnf("skipping unreadable %s document %q: %v", kind, entry.Name(), err)
			continue
		}
		documents = append(documents, stored)
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].Name < documents[j].Name })
	return documents, nil
}

// RemoveDocument deletes one stored document. A missing one is os.ErrNotExist.
func (m *Manager) RemoveDocument(kind, name string) error {
	path, err := m.documentPath(kind, name)
	if err != nil {
		return err
	}
	m.documentMu.Lock()
	defer m.documentMu.Unlock()
	return os.Remove(path)
}
```

If `m.Warnf` does not exist under that name, use the method `Campaigns()` uses to warn (`grep -n 'func (m \*Manager) Warnf' go/internal/agent/data/`), and if `SetWarnLogger` is named differently, adjust the test the same way.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd go && go test ./internal/agent/data/ -run 'DocumentStore|DocumentsLists' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go/internal/agent/data/document_store.go go/internal/agent/data/document_store_test.go go/internal/agent/data/manager.go
git commit -m "feat(data): store inference and watch documents

Documents live in data/documents/<kind>/<name>.json beside campaigns,
written atomically under their own lock. Listing skips and reports an
unreadable file, as campaign listing does; a missing document is
os.ErrNotExist."
```

---

### Task 3: Journal entries for watches, and records for chosen collections only

**Files:**
- Modify: `go/internal/agent/data/notifications.go` (`CampaignNotification`; `RecordNotification`; `Notifications`)
- Modify: `go/internal/agent/data/manager.go` (`bufferedRecord` ~line 235; `RecordApplication`, `RecordCampaignApplication`, `recordApplication` ~line 1130; `flushPreRoll` ~line 1293 and its call ~line 606)
- Test: `go/internal/agent/data/notifications_test.go`, `go/internal/agent/data/record_audience_test.go` (new)

**Interfaces:**
- Produces:
  - `CampaignNotification` gains `Watch string` (`json:"watch,omitempty"`), `Inference string` (`json:"inference,omitempty"`), `Leased bool` (`json:"leased,omitempty"`); `Campaign`'s tag becomes `json:"campaign,omitempty"`.
  - `RecordNotification` accepts an entry with `Campaign` or `Watch`.
  - `func (m *Manager) NotificationsFor(appID, event, cursor string, replay bool, watches []string) ([]CampaignNotification, string, bool, error)`; `Notifications(...)` keeps its signature and calls it with `nil`.
  - `func (m *Manager) RecordApplicationFor(appID string, record ApplicationRecord, audience map[string]bool, observe bool) (string, error)`

- [ ] **Step 1: Write the failing journal tests**

Append to `notifications_test.go` (it already imports `uuid` and `time`):

```go
func recordTestNotification(t *testing.T, m *Manager, n CampaignNotification) {
	t.Helper()
	n.ID = uuid.NewString()
	n.Event = "front-door"
	n.OccurredAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := m.RecordNotification(n); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationsHideLeasedWatchesFromOtherReaders(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recordTestNotification(t, m, CampaignNotification{Campaign: "people"})
	recordTestNotification(t, m, CampaignNotification{Watch: "porch", Inference: "front-door"})
	recordTestNotification(t, m, CampaignNotification{Watch: "chat-1", Inference: "chat-1", Leased: true})
	recordTestNotification(t, m, CampaignNotification{Watch: "chat-2", Inference: "chat-2", Leased: true})

	names := func(items []CampaignNotification) string {
		var out []string
		for _, item := range items {
			out = append(out, item.Campaign+item.Watch)
		}
		return strings.Join(out, ",")
	}
	all, _, _, err := m.Notifications("", "", "", true)
	if err != nil || names(all) != "people,porch" {
		t.Fatalf("unfiltered read = %s, %v", names(all), err)
	}
	mine, _, _, err := m.NotificationsFor("", "", "", true, []string{"chat-1"})
	if err != nil || names(mine) != "chat-1" {
		t.Fatalf("read naming chat-1 = %s, %v", names(mine), err)
	}
	both, _, _, err := m.NotificationsFor("", "", "", true, []string{"porch", "chat-2"})
	if err != nil || names(both) != "porch,chat-2" {
		t.Fatalf("read naming porch and chat-2 = %s, %v", names(both), err)
	}
	byApp, _, _, err := m.Notifications("sh.wendy.watch.porch", "", "", true)
	if err != nil || names(byApp) != "porch" {
		t.Fatalf("read by watch app id = %s, %v", names(byApp), err)
	}
}

func TestRecordNotificationNeedsACampaignOrAWatch(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = m.RecordNotification(CampaignNotification{ID: uuid.NewString(), Event: "e", OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)})
	if err == nil || !strings.Contains(err.Error(), "invalid notification identity") {
		t.Fatalf("anonymous notification error = %v", err)
	}
	raw, _ := json.Marshal(CampaignNotification{Watch: "porch"})
	if strings.Contains(string(raw), `"campaign"`) {
		t.Fatalf("a watch entry carries an empty campaign: %s", raw)
	}
}
```

- [ ] **Step 2: Write the failing audience tests**

Create `go/internal/agent/data/record_audience_test.go`:

```go
package data

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func episodeEvents(t *testing.T, m *Manager, id string) string {
	t.Helper()
	dir, err := m.episodeDir(id)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRecordApplicationForReachesOnlyItsCollections(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"clips", "other"} {
		if _, err := m.Start(StartOptions{Sources: []string{"applications"}, Trigger: EpisodeTrigger{Reason: "event:x", CampaignName: key}}); err != nil {
			t.Fatal(err)
		}
	}
	record := ApplicationRecord{Version: 1, Type: "prediction", Model: "m", Attributes: map[string]any{"marker": "for-clips"}}
	if _, err := m.RecordApplicationFor("sh.wendy.inference.front-door", record, map[string]bool{"clips": true}, false); err != nil {
		t.Fatal(err)
	}
	clips, err := m.Stop("clips")
	if err != nil {
		t.Fatal(err)
	}
	other, err := m.Stop("other")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(episodeEvents(t, m, clips.ID), "for-clips") {
		t.Fatal("the named collection's episode lacks the record")
	}
	if strings.Contains(episodeEvents(t, m, other.ID), "for-clips") {
		t.Fatal("another collection's episode received the record")
	}
}

func TestRecordApplicationForPreRollReachesOnlyItsCollections(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record := ApplicationRecord{Version: 1, Type: "event", Name: "front-door", Attributes: map[string]any{"marker": "pre-roll"}}
	if _, err := m.RecordApplicationFor("sh.wendy.inference.front-door", record, map[string]bool{"clips": true}, false); err != nil {
		t.Fatal(err)
	}
	opts := func(key string) StartOptions {
		return StartOptions{Sources: []string{"applications"}, PreRollDuration: 10 * time.Second, Trigger: EpisodeTrigger{Reason: "event:front-door", CampaignName: key}}
	}
	clips := recordEpisode(t, m, opts("clips"))
	other := recordEpisode(t, m, opts("other"))
	if !strings.Contains(episodeEvents(t, m, clips.ID), "pre-roll") {
		t.Fatal("the named collection's later episode lacks the pre-roll record")
	}
	if strings.Contains(episodeEvents(t, m, other.ID), "pre-roll") {
		t.Fatal("another collection's later episode took the pre-roll record")
	}
}

func TestRecordApplicationForWithoutAudienceStoresNoPrediction(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var observed []string
	m.SetApplicationObserver(func(_ string, record ApplicationRecord) { observed = append(observed, record.Type+":"+record.Name) })
	prediction := ApplicationRecord{Version: 1, Type: "prediction", Model: "m", Attributes: map[string]any{"marker": "nobody"}}
	if _, err := m.RecordApplicationFor("sh.wendy.inference.chat-1", prediction, nil, true); err != nil {
		t.Fatal(err)
	}
	event := ApplicationRecord{Version: 1, Type: "event", Name: "chat-1", Attributes: map[string]any{"marker": "nobody"}}
	if _, err := m.RecordApplicationFor("sh.wendy.inference.chat-1", event, map[string]bool{}, true); err != nil {
		t.Fatal(err)
	}
	later := recordEpisode(t, m, StartOptions{Sources: []string{"applications"}, PreRollDuration: 10 * time.Second, Trigger: EpisodeTrigger{Reason: "event:x", CampaignName: "later"}})
	if strings.Contains(episodeEvents(t, m, later.ID), "nobody") {
		t.Fatal("a record with no audience reached a later episode")
	}
	if strings.Join(observed, ",") != "event:chat-1" {
		t.Fatalf("observer saw %v; want only the event", observed)
	}
	events, _, _, err := m.DeviceEvents("", "chat-1", "", true)
	if err != nil || len(events) != 1 {
		t.Fatalf("device events = %v, %v; the event must still be journaled", events, err)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd go && go test ./internal/agent/data/ -run 'NotificationsHide|NeedsACampaignOrAWatch|RecordApplicationFor' -v`
Expected: build failure, `unknown field Watch` / `m.NotificationsFor undefined` / `m.RecordApplicationFor undefined`.

- [ ] **Step 4: Extend the journal**

In `notifications.go`, change the `Campaign` field of `CampaignNotification` to

```go
	Campaign   string `json:"campaign,omitempty"`
	// Watch and Inference name the watch that delivered the entry and the
	// inference document behind it. Leased marks a leased watch's entry, which
	// only readers naming the watch receive (spec §6.5).
	Watch     string `json:"watch,omitempty"`
	Inference string `json:"inference,omitempty"`
	Leased    bool   `json:"leased,omitempty"`
```

In `RecordNotification`, change `notification.Campaign == ""` in the identity check to `notification.Campaign == "" && notification.Watch == ""`.

Rename `func (m *Manager) Notifications(appID, event, cursor string, replay bool)` to `NotificationsFor` with an added `watches []string` parameter, update its doc comment, and add the old name back as a wrapper:

```go
// Notifications has the same explicit gap/replay semantics as DeviceEvents.
// appID selects a campaign's or a watch's managed app identity,
// sh.wendy.campaign.NAME or sh.wendy.watch.NAME. Leased watches' entries are
// left out; see NotificationsFor.
func (m *Manager) Notifications(appID, event, cursor string, replay bool) ([]CampaignNotification, string, bool, error) {
	return m.NotificationsFor(appID, event, cursor, replay, nil)
}

// NotificationsFor is Notifications for a reader that names watches. With
// watches set it returns only those watches' entries, leased or not; without,
// it returns every entry except leased watches'.
func (m *Manager) NotificationsFor(appID, event, cursor string, replay bool, watches []string) ([]CampaignNotification, string, bool, error) {
```

and in its loop replace the condition

```go
		if notification.Sequence > after && (appID == "" || appID == "sh.wendy.campaign."+notification.Campaign) && (event == "" || event == notification.Event) {
```

with

```go
		if notification.Sequence > after && notificationMatches(notification, appID, event, watches) {
```

adding below the function:

```go
func notificationMatches(n CampaignNotification, appID, event string, watches []string) bool {
	if event != "" && event != n.Event {
		return false
	}
	if appID != "" && !(n.Campaign != "" && appID == "sh.wendy.campaign."+n.Campaign) && !(n.Watch != "" && appID == "sh.wendy.watch."+n.Watch) {
		return false
	}
	if len(watches) > 0 {
		return n.Watch != "" && slices.Contains(watches, n.Watch)
	}
	return !n.Leased
}
```

(add `"slices"` to the imports).

- [ ] **Step 5: Give records an audience**

In `manager.go`:

1. In `type bufferedRecord struct`, add as the last field:

```go
	// audience names the collections whose episodes may take this record from
	// the ring. nil means every episode, as for application records.
	audience map[string]bool
```

2. Replace `RecordApplication` and `RecordCampaignApplication` with:

```go
// RecordApplication validates and stamps an entitled application's record.
// It returns buffered or recorded; protocol-level validation happens before it.
func (m *Manager) RecordApplication(appID string, record ApplicationRecord) (string, error) {
	return m.recordApplication(appID, record, true, nil)
}

// RecordCampaignApplication uses the same journal and timestamping as app
// records, but the agent evaluates its campaign trigger synchronously against
// the current revision rather than broadcasting it to every campaign.
func (m *Manager) RecordCampaignApplication(appID string, record ApplicationRecord) (string, error) {
	return m.recordApplication(appID, record, false, nil)
}

// RecordApplicationFor records an inference document's record into the
// episodes of the named collections only: open ones now, later ones through
// the pre-roll ring (spec §7.3). An empty audience keeps a prediction out of
// the store entirely; an event still reaches the device-event journal and,
// with observe, the trigger observer. The audience map must not change after
// the call.
func (m *Manager) RecordApplicationFor(appID string, record ApplicationRecord, audience map[string]bool, observe bool) (string, error) {
	if audience == nil {
		audience = map[string]bool{}
	}
	if len(audience) == 0 && record.Type != "event" {
		return "buffered", nil
	}
	return m.recordApplication(appID, record, observe, audience)
}
```

3. Change `func (m *Manager) recordApplication(appID string, record ApplicationRecord, observe bool) (string, error)` to take `audience map[string]bool` as a fourth parameter. In its loop over `m.openEpisodesLocked()`, directly after `if !a.capturesApplications { continue }`, add:

```go
		if audience != nil && !audience[a.key] {
			continue
		}
```

Replace the pre-roll append block

```go
	stored.EpisodeNanos = 0
	b, err := json.Marshal(stored)
	if err != nil {
		m.mu.Unlock()
		return "rejected", err
	}
	m.preRoll = append(m.preRoll, bufferedRecord{bootNanos: stamp, receiptNanos: receipt, encoded: b})
	m.preRollBytes += len(b)
	m.evictPreRoll(receipt)
```

with

```go
	// A record with an empty audience can reach no later episode, so it does
	// not take ring space from records that can.
	if audience == nil || len(audience) > 0 {
		stored.EpisodeNanos = 0
		b, err := json.Marshal(stored)
		if err != nil {
			m.mu.Unlock()
			return "rejected", err
		}
		m.preRoll = append(m.preRoll, bufferedRecord{bootNanos: stamp, receiptNanos: receipt, encoded: b, audience: audience})
		m.preRollBytes += len(b)
		m.evictPreRoll(receipt)
	}
```

4. Change `func (m *Manager) flushPreRoll(dir string, origin int64, requested time.Duration)` to `flushPreRoll(dir, key string, origin int64, requested time.Duration)`; in its loop, directly after the window check `continue`, add:

```go
		if r.audience != nil && !r.audience[key] {
			continue
		}
```

and update the one call in `Start` to `m.flushPreRoll(dir, key, origin, opts.PreRollDuration)` (`key` is `opts.Trigger.CampaignName`, defined at the top of `Start`). Update any test that calls `flushPreRoll` directly (`grep -rn 'flushPreRoll(' go/internal/agent/data/`) to pass a key; a test that predates audiences passes `""`, which every nil-audience record still reaches.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd go && go test ./internal/agent/data/ -run 'NotificationsHide|NeedsACampaignOrAWatch|RecordApplicationFor|Notification|PreRoll' -v`
Expected: PASS. Then `cd go && go test ./internal/agent/data/ ./internal/agent/services/` passes in full.

- [ ] **Step 7: Commit**

```bash
git add go/internal/agent/data/
git commit -m "feat(data): watch journal entries and records for chosen collections

Journal entries can belong to a watch, and a leased watch's entries
reach only readers that name it. RecordApplicationFor writes an
inference document's records into the episodes of the named collections
only, now and through the pre-roll ring; with no such collection a
prediction is not stored at all."
```

---

### Task 4: The proto

**Files:**
- Modify: `Proto/wendy/agent/services/v2/data_service.proto`
- Regenerate: `go/proto/gen/agentpb/v2/data_service.pb.go`, `go/proto/gen/agentpb/v2/data_service_grpc.pb.go`
- Modify: `go/internal/agent/services/data_events.go`
- Test: `go/internal/agent/services/data_events_test.go` (new, or append if it exists)

**Interfaces:**
- Produces (generated, Go names): `DataDocumentApplyRequest{DocumentYaml []byte}`, `DataDocumentApplyResponse{Document *DataDocument}`, `DataDocumentRemoveRequest{Kind, Name string}`, `DataDocumentRemoveResponse{Removed []string}`, `DataDocumentRenewRequest{Kind, Name string}`, `DataDocumentRenewResponse{ExpiresUnixNanos int64}`, `DataDocumentsRequest{Kind, Name string}`, `DataDocumentsResponse{Documents []*DataDocument}`, `DataDocument{Kind string; Version uint32; Name, Owner, Revision, State, StateDetail string; DeployedUnixNanos, ExpiresUnixNanos int64; DocumentJson []byte; Warnings []string; StatusJson []byte}`; `DataEventsRequest.Watches []string`; service methods `DocumentApply`, `DocumentRemove`, `DocumentRenew`, `Documents` (UNIMPLEMENTED on `DataService` until Task 7).

- [ ] **Step 1: Edit the proto**

In the `service DataService` block, after the `rpc CampaignTrigger(...)` line, add:

```proto
  // Data documents (specs/2026-10-12-data-documents-design.md §9.2).
  // DocumentApply validates and stores a document of any kind and hands it to
  // its runner. A collection takes the CampaignDeploy path.
  rpc DocumentApply(DataDocumentApplyRequest) returns (DataDocumentApplyResponse);
  // DocumentRemove removes an inference or watch document and everything it
  // owns, owned documents first, and returns once their inference has stopped
  // (at most 10 s, then DEADLINE_EXCEEDED). NOT_FOUND if absent;
  // FAILED_PRECONDITION for a collection.
  rpc DocumentRemove(DataDocumentRemoveRequest) returns (DataDocumentRemoveResponse);
  // DocumentRenew pushes a leased watch's deadline to now + lease. NOT_FOUND
  // once removed or expired; FAILED_PRECONDITION for a document without a lease.
  rpc DocumentRenew(DataDocumentRenewRequest) returns (DataDocumentRenewResponse);
  // Documents lists documents of every kind with their status; kind and name
  // narrow it. NOT_FOUND when a named document does not exist.
  rpc Documents(DataDocumentsRequest) returns (DataDocumentsResponse);
```

In `message DataEventsRequest`, after `bool notifications_only = 5;`, add:

```proto
  // With notifications_only: only these watches' notifications. A leased
  // watch's notifications are returned only to a request that names it.
  repeated string watches = 6;
```

After `message DataCampaignsResponse { ... }`, add:

```proto
message DataDocumentApplyRequest { bytes document_yaml = 1; }
message DataDocumentApplyResponse { DataDocument document = 1; }
message DataDocumentRemoveRequest { string kind = 1; string name = 2; }
message DataDocumentRemoveResponse {
  repeated string removed = 1;        // "<kind>/<name>", in the order removed
}
message DataDocumentRenewRequest { string kind = 1; string name = 2; }
message DataDocumentRenewResponse { int64 expires_unix_nanos = 1; }
message DataDocumentsRequest { string kind = 1; string name = 2; }
message DataDocumentsResponse { repeated DataDocument documents = 1; }

message DataDocument {
  string kind = 1;
  uint32 version = 2;
  string name = 3;
  string owner = 4;
  string revision = 5;
  string state = 6;                   // collection: campaign state; inference: job state; watch: waiting | active | error
  string state_detail = 7;
  int64 deployed_unix_nanos = 8;
  int64 expires_unix_nanos = 9;       // leased watches only
  bytes document_json = 10;           // the stored document, like DataCampaign.plan_json
  repeated string warnings = 11;
  bytes status_json = 12;             // kind-specific status
}
```

- [ ] **Step 2: Regenerate, keeping only the data service files**

```bash
cd go && export PATH="$PATH:$(go env GOPATH)/bin" && make proto && cd ..
git status --short go/proto/gen/ | head -60
git diff --name-only -- go/proto/gen/ | grep -v -e 'agentpb/v2/data_service.pb.go' -e 'agentpb/v2/data_service_grpc.pb.go' | xargs -r git checkout --
git status --short go/proto/gen/
```

Run the `git diff`/`git checkout` lines from the repository root: `git diff --name-only` prints root-relative paths. Expected at the end: only the two data service files modified, nothing untracked. If `make proto` fails for a missing plugin, the needed versions are protoc-gen-go v1.36.11 and protoc-gen-go-grpc v1.6.2 in `$(go env GOPATH)/bin`.

- [ ] **Step 3: Write the failing Events test**

Append to (or create) `go/internal/agent/services/data_events_test.go`:

```go
package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestEventsWatchFilter(t *testing.T) {
	manager, err := data.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewDataService(manager)
	for _, n := range []data.CampaignNotification{{Campaign: "people"}, {Watch: "chat-1", Inference: "chat-1", Leased: true}} {
		n.ID, n.Event, n.OccurredAt = uuid.NewString(), "front-door", time.Now().UTC().Format(time.RFC3339Nano)
		if err := manager.RecordNotification(n); err != nil {
			t.Fatal(err)
		}
	}
	read := func(watches ...string) []data.CampaignNotification {
		t.Helper()
		response, err := service.Events(context.Background(), &agentpbv2.DataEventsRequest{NotificationsOnly: true, Replay: true, Watches: watches})
		if err != nil {
			t.Fatal(err)
		}
		var items []data.CampaignNotification
		if err := json.Unmarshal(response.GetEventsJson(), &items); err != nil {
			t.Fatal(err)
		}
		return items
	}
	if items := read(); len(items) != 1 || items[0].Campaign != "people" {
		t.Fatalf("unfiltered = %+v", items)
	}
	if items := read("chat-1"); len(items) != 1 || items[0].Watch != "chat-1" {
		t.Fatalf("named = %+v", items)
	}
	tooMany := make([]string, 17)
	for i := range tooMany {
		tooMany[i] = "w"
	}
	if _, err := service.Events(context.Background(), &agentpbv2.DataEventsRequest{NotificationsOnly: true, Watches: tooMany}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("17 watches error = %v", err)
	}
	if _, err := service.Events(context.Background(), &agentpbv2.DataEventsRequest{NotificationsOnly: true, Watches: []string{"../x"}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad watch name error = %v", err)
	}
}
```

Run: `cd go && go test ./internal/agent/services/ -run TestEventsWatchFilter -v`
Expected: FAIL: the named read returns nothing (the handler ignores `Watches`).

- [ ] **Step 4: Honour the filter**

In `data_events.go`, add `"github.com/wendylabsinc/wendy/go/internal/shared/datadoc"` to the imports, extend the first check:

```go
	if len(r.AppId) > 256 || len(r.Event) > 128 || len(r.Cursor) > 100 || len(r.Watches) > 16 {
		return nil, status.Error(codes.InvalidArgument, "event filter exceeds limit")
	}
	for _, watch := range r.Watches {
		if !datadoc.ValidName(watch) {
			return nil, status.Errorf(codes.InvalidArgument, "invalid watch name %q", watch)
		}
	}
```

and in the `NotificationsOnly` branch call `s.manager.NotificationsFor(r.AppId, r.Event, r.Cursor, r.Replay, r.Watches)`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd go && go build ./... && go test ./internal/agent/services/ -run 'Events' -v && GOOS=linux GOARCH=arm64 go build -o /dev/null ./cmd/wendy-agent/`
Expected: PASS and both builds succeed.

- [ ] **Step 6: Commit**

```bash
git add Proto/wendy/agent/services/v2/data_service.proto go/proto/gen/agentpb/v2/data_service.pb.go go/proto/gen/agentpb/v2/data_service_grpc.pb.go go/internal/agent/services/data_events.go go/internal/agent/services/data_events_test.go
git commit -m "feat(proto): data document calls and a watch filter on Events

DocumentApply, DocumentRemove, DocumentRenew and Documents join
DataService (unimplemented until the handlers land). Events takes up to
16 watch names; a request naming watches gets only their entries."
```

---

### Task 5: The inference runner runs inference documents

**Files:**
- Modify: `go/internal/agent/services/data_inference.go`
- Test: `go/internal/agent/services/data_inference_documents_test.go` (new)

**Interfaces:**
- Consumes: `data.StoredDocument.Inference`, `data.InferenceDocument.Campaign`, `manager.Documents(data.KindInference)`, `manager.RecordApplicationFor`, PR 1's `detectionNotification(campaign, source, detections)`.
- Produces:
  - `func campaignJobKey(name string) string` (`"campaign/"+name`), `func inferenceJobKey(name string) string` (`"inference/"+name`)
  - `campaignInferenceJob` gains `key string` and `document bool`; `campaignInferenceManager` gains `ctx context.Context` and `reconcileMu sync.Mutex`
  - `func (m *campaignInferenceManager) reconcileNow()`, `func (m *campaignInferenceManager) wakeNow()`
  - `inferenceActive(key)`, `snapshot(key, revision)` now take a job key
  - `const inferenceAppPrefix = "sh.wendy.inference."`
  - `func (s *DataService) collectionsTriggeredBy(event string) map[string]bool`
  - `func inferenceNotification(campaign data.Campaign, source string, detections []inference.Detection) DetectionNotification`
  - A seam for Task 6: `DataService` gains `watches *watchRunner` (declared in Task 6). Until Task 6 lands, declare in this task, in `data_inference.go`:

    ```go
    // watchRunner is completed in data_watch.go.
    type watchRunner struct{}

    func (*watchRunner) deliver(string, DetectionNotification) {}
    ```

    and add the field `watches *watchRunner` to `DataService` beside `inference`. Task 6 deletes this stub and supplies the real type in `data_watch.go`.

- [ ] **Step 1: Write the failing tests**

Create `go/internal/agent/services/data_inference_documents_test.go`:

```go
package services

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	"github.com/wendylabsinc/wendy/go/internal/agent/inference"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

const testInferenceDocumentYAML = `kind: inference
version: 1
name: front-door
sources:
  - camera: v4l2:/dev/video0
model: ustc-community/dfine-nano-coco
revision: 066438d3d8f0da137a37b38fdf3368fd4afceced
labels: [person]
threshold: 0.5
rate: 2
clear_after: 5s
cooldown: 30s
`

// newDocumentTestService is newInferenceTestService without the campaign: one
// healthy camera, a fake model runtime and a recording sender. The caller
// starts it, so a test can arrange the store first.
func newDocumentTestService(t *testing.T) (*DataService, *inferenceTestFactory, *inferenceTestSender) {
	t.Helper()
	manager, err := data.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewDataService(manager)
	service.addAdapter(&inferenceTestAdapter{sources: []data.Source{{ID: "v4l2:/dev/video0", Kind: "camera", Healthy: true}}})
	service.video = inferenceTestVideo{}
	return service, &inferenceTestFactory{sessions: make(chan *inferenceTestSession, 8)}, &inferenceTestSender{requests: make(chan DetectionNotification, 8)}
}

func startDocumentTestService(t *testing.T, service *DataService, factory *inferenceTestFactory, sender CampaignNotificationSender) {
	t.Helper()
	stop := service.StartCampaignInference(context.Background(), factory, sender)
	t.Cleanup(func() {
		stop()
		for _, key := range service.manager.ActiveEpisodeKeys() {
			_, _ = service.stopCapture(context.Background(), key)
		}
	})
}

func saveDocument(t *testing.T, service *DataService, contents string) data.StoredDocument {
	t.Helper()
	doc, err := data.ParseDocument([]byte(contents))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := service.manager.SaveDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	if service.inference != nil {
		service.inference.wakeNow()
	}
	return stored
}

func personResult(input inference.Input) inference.Result {
	return inference.Result{Type: "prediction", SourceID: input.SourceID, Generation: input.Generation, Detections: []inference.Detection{{Label: "person", Score: .99, Box: [4]float64{1, 2, 3, 4}}}}
}

func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

const testClipsCollection = `kind: collection
version: 1
name: clips
sources:
  - camera: "*"
capture:
  buffer: 0s
  drain: 0s
  after_trigger: 1s
  triggers: [{event: front-door}]
upload: {when: manual}
export: {annotation: cvat}
`

func TestInferenceDocumentRunsAndTriggersItsCollection(t *testing.T) {
	service, factory, sender := newDocumentTestService(t)
	startDocumentTestService(t, service, factory, sender)
	if _, err := service.CampaignDeploy(context.Background(), &agentpbv2.DataCampaignDeployRequest{CampaignYaml: []byte(testClipsCollection)}); err != nil {
		t.Fatal(err)
	}
	other := strings.ReplaceAll(strings.Replace(testClipsCollection, "name: clips", "name: other", 1), "event: front-door", "event: something-else")
	if _, err := service.CampaignDeploy(context.Background(), &agentpbv2.DataCampaignDeployRequest{CampaignYaml: []byte(other)}); err != nil {
		t.Fatal(err)
	}
	stored := saveDocument(t, service, testInferenceDocumentYAML)
	session := receiveInference(t, factory.sessions)
	input := receiveInference(t, session.inputs)
	eventually(t, "the inference job to run", func() bool {
		return service.inference.snapshot(inferenceJobKey("front-door"), stored.Revision).State == "running"
	})
	session.results <- personResult(input)
	eventually(t, "the clips collection to trigger", func() bool {
		return slices.Contains(service.manager.ActiveEpisodeKeys(), "clips")
	})
	if slices.Contains(service.manager.ActiveEpisodeKeys(), "other") {
		t.Fatal("a collection triggered by another event started")
	}
	if got := service.collectionsTriggeredBy("front-door"); len(got) != 1 || !got["clips"] {
		t.Fatalf("audience = %v", got)
	}
}

func TestInferenceDocumentAndCampaignWithOneNameBothRun(t *testing.T) {
	service, factory, sender := newDocumentTestService(t)
	startDocumentTestService(t, service, factory, sender)
	if _, err := service.CampaignDeploy(context.Background(), &agentpbv2.DataCampaignDeployRequest{CampaignYaml: inferenceTestYAML(t)}); err != nil {
		t.Fatal(err)
	}
	first := receiveInference(t, factory.sessions)
	saveDocument(t, service, strings.Replace(testInferenceDocumentYAML, "name: front-door", "name: people-all-cameras", 1))
	second := receiveInference(t, factory.sessions)
	time.Sleep(300 * time.Millisecond)
	for _, session := range []*inferenceTestSession{first, second} {
		select {
		case <-session.closed:
			t.Fatal("one job retired the other")
		default:
		}
	}
	if !service.inference.inferenceActive(campaignJobKey("people-all-cameras")) {
		t.Fatal("the campaign's job is not active")
	}
}

func TestDisabledInferenceDocumentDoesNotRun(t *testing.T) {
	service, factory, sender := newDocumentTestService(t)
	startDocumentTestService(t, service, factory, sender)
	saveDocument(t, service, testInferenceDocumentYAML+"enabled: false\n")
	select {
	case <-factory.sessions:
		t.Fatal("a disabled inference document started a model")
	case <-time.After(500 * time.Millisecond):
	}
}

func TestRemovedInferenceDocumentStopsOnReconcileNow(t *testing.T) {
	service, factory, sender := newDocumentTestService(t)
	startDocumentTestService(t, service, factory, sender)
	saveDocument(t, service, testInferenceDocumentYAML)
	session := receiveInference(t, factory.sessions)
	if err := service.manager.RemoveDocument(data.KindInference, "front-door"); err != nil {
		t.Fatal(err)
	}
	service.inference.reconcileNow()
	select {
	case <-session.closed:
	default:
		t.Fatal("reconcileNow returned before the job stopped")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd go && go test ./internal/agent/services/ -run 'InferenceDocument|RemovedInferenceDocument' -v`
Expected: build failure, `undefined: inferenceJobKey`.

- [ ] **Step 3: Widen the manager**

In `data_inference.go`:

1. Below `const campaignAppPrefix = "sh.wendy.campaign."` add:

```go
const inferenceAppPrefix = "sh.wendy.inference."

// Job keys keep a campaign's inline inference and an inference document
// apart even when they share a name.
func campaignJobKey(name string) string  { return "campaign/" + name }
func inferenceJobKey(name string) string { return "inference/" + name }

// wantedInference is one job a reconcile pass wants running.
type wantedInference struct {
	campaign data.Campaign
	document bool
}
```

2. In `campaignInferenceManager`, add fields:

```go
	ctx context.Context
	// reconcileMu runs one pass at a time: a pass that read the plans before
	// a removal must not restart a job that a later pass, run for that
	// removal, has just retired.
	reconcileMu sync.Mutex
```

3. In `campaignInferenceJob`, add fields `key string` (the job's key in `jobs`) and `document bool` (an inference document's job: no campaign capture, no campaign notifications).

4. In `StartCampaignInference`, set `ctx: ctx` in the manager literal.

5. Replace `reconcile` with:

```go
func (m *campaignInferenceManager) reconcile(ctx context.Context) {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	campaigns, err := m.service.manager.Campaigns()
	if err != nil {
		m.service.manager.Warnf("reading inference campaigns: %v", err)
		return
	}
	wanted := map[string]wantedInference{}
	for _, campaign := range campaigns {
		if campaign.State == "armed" && (campaign.Inference.IsEnabled() || campaign.Notify != nil && campaign.Notify.On == data.NotifyOnEvent) {
			wanted[campaignJobKey(campaign.Name)] = wantedInference{campaign: campaign}
		}
	}
	documents, err := m.service.manager.Documents(data.KindInference)
	documentsKnown := err == nil
	if err != nil {
		m.service.manager.Warnf("reading inference documents: %v", err)
	}
	for _, stored := range documents {
		document, err := stored.Inference()
		if err != nil {
			m.service.manager.Warnf("inference document %q: %v", stored.Name, err)
			continue
		}
		if document.IsEnabled() {
			wanted[inferenceJobKey(stored.Name)] = wantedInference{campaign: document.Campaign(stored.Revision), document: true}
		}
	}
	m.mu.Lock()
	var retired []*campaignInferenceJob
	for key, job := range m.jobs {
		want, ok := wanted[key]
		if ok && want.campaign.Revision == job.campaign.Revision {
			continue
		}
		// An unreadable document directory is not a removal.
		if !ok && job.document && !documentsKnown {
			continue
		}
		job.cancel()
		retired = append(retired, job)
	}
	m.mu.Unlock()
	for _, job := range retired {
		<-job.done
		m.mu.Lock()
		if m.jobs[job.key] == job {
			delete(m.jobs, job.key)
		}
		m.mu.Unlock()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, want := range wanted {
		if m.jobs[key] != nil || ctx.Err() != nil {
			continue
		}
		child, cancel := context.WithCancel(ctx)
		job := &campaignInferenceJob{owner: m, key: key, document: want.document, campaign: want.campaign, cancel: cancel, done: make(chan struct{}), queue: make(chan DetectionNotification, 16), generations: map[string]uint64{}, status: data.InferenceStatus{State: "loading", Sources: map[string]string{}}}
		m.jobs[key] = job
		go job.supervise(child)
	}
}

// reconcileNow runs one pass in the caller's goroutine; it returns once the
// jobs it retired have stopped.
func (m *campaignInferenceManager) reconcileNow() { m.reconcile(m.ctx) }

// wakeNow asks the loop for a pass without waiting for it.
func (m *campaignInferenceManager) wakeNow() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}
```

6. In `stopAll`, change `if m.jobs[job.campaign.Name] == job` / `delete(m.jobs, job.campaign.Name)` to use `job.key`.

7. Rename the `name` parameter of `inferenceActive` and `snapshot` to `key` (bodies unchanged: they index `m.jobs`). In `campaignMessage`, change `s.inference.inferenceActive(campaign.Name)` and `s.inference.snapshot(campaign.Name, campaign.Revision)` to pass `campaignJobKey(campaign.Name)`. Then `grep -rn 'inferenceActive(\|\.snapshot(' go/internal/agent/services/` and update every other caller, tests included, the same way.

- [ ] **Step 4: Handle a document job's results**

In `run`:

1. Directly after `presence := map[string]*inferencePresence{}`, add:

```go
	// The collections whose episodes an inference document's records may
	// reach, refreshed with the cameras on every pass (spec §7.3).
	var audience map[string]bool
```

and as the first statement inside the `reconcile := func() {` closure:

```go
		if j.document {
			audience = j.owner.service.collectionsTriggeredBy(j.campaign.Inference.Event)
		}
```

2. Change `if changed {` (the block that calls `armCampaign`) to `if changed && !j.document {`.

3. Directly after `j.sourceState(result.SourceID, "detecting")`, add:

```go
			if j.document {
				j.documentResult(presence[result.SourceID], result.SourceID, detections, result.DroppedResults, audience)
				continue
			}
```

4. Add, below `detectionNotification`:

```go
// inferenceNotification is an inference document's arrival as a watch
// receives it: no campaign, the inference named instead.
func inferenceNotification(campaign data.Campaign, source string, detections []inference.Detection) DetectionNotification {
	notification := detectionNotification(campaign, source, detections)
	notification.Campaign = ""
	notification.Inference = campaign.Name
	return notification
}

// documentResult handles one scored frame of an inference document (spec
// §7.3). An arrival reaches watches first; the records go only to the
// collections that trigger on its event, and storing them never holds up a
// notification.
func (j *campaignInferenceJob) documentResult(state *inferencePresence, sourceID string, detections []inference.Detection, dropped uint64, audience map[string]bool) {
	service := j.owner.service
	appID := inferenceAppPrefix + j.campaign.Name
	attributes := map[string]any{"inference": j.campaign.Name, "source_id": sourceID, "model_version": j.campaign.Inference.Revision, "detections": detections,
		"input_reference_status": "encoded_stream_decode_does_not_preserve_sample_ids", "runtime_results_dropped": dropped}
	record := data.ApplicationRecord{Version: 1, Type: "prediction", Model: j.campaign.Inference.Model, Attributes: attributes}
	if _, err := service.manager.RecordApplicationFor(appID, record, audience, false); err != nil {
		service.manager.Warnf("inference %q prediction: %v", j.campaign.Name, err)
	}
	if state == nil || !state.observe(len(detections) > 0, time.Now(), j.campaign.Inference) {
		return
	}
	if service.watches != nil {
		service.watches.deliver(j.campaign.Name, inferenceNotification(j.campaign, sourceID, detections))
	}
	record.Type, record.Name = "event", j.campaign.Inference.Event
	if _, err := service.manager.RecordApplicationFor(appID, record, audience, true); err != nil {
		service.manager.Warnf("inference %q event: %v", j.campaign.Name, err)
	}
}

// collectionsTriggeredBy names the armed collections whose capture triggers
// include event: the only episodes an inference document's records reach.
func (s *DataService) collectionsTriggeredBy(event string) map[string]bool {
	audience := map[string]bool{}
	campaigns, err := s.manager.Campaigns()
	if err != nil {
		s.manager.Warnf("reading campaigns for inference records: %v", err)
		return audience
	}
	for _, campaign := range campaigns {
		if campaign.State != "armed" {
			continue
		}
		for _, trigger := range campaign.Capture.Triggers {
			if trigger.Event == event {
				audience[campaign.Name] = true
			}
		}
	}
	return audience
}
```

5. Add the Task 6 seam described under Interfaces (the empty `watchRunner` with `deliver`, and the `watches *watchRunner` field on `DataService`).

In `supervise`, the warning `"campaign %q inference: %v; retrying in 5s"` should name a document as such: replace it with

```go
		what := "campaign"
		if j.document {
			what = "inference document"
		}
		j.owner.service.manager.Warnf("%s %q inference: %v; retrying in 5s", what, j.campaign.Name, err)
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd go && go test ./internal/agent/services/ -run 'InferenceDocument|RemovedInferenceDocument|AgentInference|Campaign' -v`
Expected: PASS. Then `cd go && go test -race ./internal/agent/services/ -run 'InferenceDocument|RemovedInferenceDocument'` passes, and `cd go && go test ./internal/agent/services/` passes in full.

- [ ] **Step 6: Commit**

```bash
git add go/internal/agent/services/
git commit -m "feat(agent): run inference documents beside campaign inference

The inference loop keys jobs by kind, so an inference document and a
campaign may share a name. A document's arrivals go to watches first;
its records reach only collections triggered by its event, and the
event reaches the trigger observer so those collections record.
reconcileNow serializes with the loop so a removal's pass cannot be
undone by a stale one."
```

---

### Task 6: The watch runner

**Files:**
- Create: `go/internal/agent/services/data_watch.go`
- Modify: `go/internal/agent/services/data_inference.go` (delete Task 5's `watchRunner` stub; `StartCampaignInference`; `notifications`)
- Modify: `go/internal/agent/services/data_inference_notification.go` (`deliverNotification`; watch identity in `CampaignCloudSender.Send`)
- Modify: `go/internal/agent/services/data_service.go` (field `leases documentLeases` on `DataService`)
- Test: `go/internal/agent/services/data_watch_test.go` (new), `go/internal/agent/services/data_inference_notification_test.go`

**Interfaces:**
- Consumes: Task 5's `watches` seam, `inferenceJobKey`, `wakeNow`; Task 2's store; Task 3's journal fields.
- Produces:
  - `type documentLeases struct` with `extend(name string, lease time.Duration) time.Time`, `renew(name string, lease time.Duration) (time.Time, bool)`, `forget(name string)`, `expired(name string) bool`, `deadline(name string) (time.Time, bool)`; field `now func() time.Time` (nil = `time.Now`). `DataService.leases documentLeases`.
  - `type watchRunner struct` with `deliver(inference string, base DetectionNotification)` and `deliveryError(watch string) string`
  - `func deliverNotification(ctx context.Context, sender CampaignNotificationSender, endpoint string, request DetectionNotification) error`
  - `func (s *DataService) removeDocumentTree(kind, name string) ([]string, error)` and `removeDocumentTreeLocked` (caller holds `deploymentMu`)
  - `func (s *DataService) expireLeases()`, `func (s *DataService) sweepLeasedDocuments()`
  - `StartCampaignInference` sweeps, then starts the watch runner, before the inference loop.

- [ ] **Step 1: Write the failing tests**

Create `go/internal/agent/services/data_watch_test.go`:

```go
package services

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time         { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Add(d time.Duration)    { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }
func newFakeClock() *fakeClock              { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

type sentNotification struct {
	endpoint     string
	notification DetectionNotification
}

type endpointSender struct{ sent chan sentNotification }

func (s *endpointSender) Send(_ context.Context, endpoint string, n DetectionNotification) error {
	s.sent <- sentNotification{endpoint, n}
	return nil
}

func watchYAML(name, inference, extra string) string {
	return "kind: watch\nversion: 1\nname: " + name + "\non: {inference: " + inference + "}\n" + extra
}

func journal(t *testing.T, service *DataService, watches ...string) []data.CampaignNotification {
	t.Helper()
	items, _, _, err := service.manager.NotificationsFor("", "", "", true, watches)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestDocumentLeases(t *testing.T) {
	clock := newFakeClock()
	leases := documentLeases{now: clock.Now}
	if !leases.expired("w") {
		t.Fatal("a watch without a deadline must count as expired")
	}
	deadline := leases.extend("w", time.Minute)
	if !deadline.Equal(clock.Now().Add(time.Minute)) || leases.expired("w") {
		t.Fatalf("extend = %v", deadline)
	}
	clock.Add(30 * time.Second)
	renewed, ok := leases.renew("w", time.Minute)
	if !ok || !renewed.Equal(clock.Now().Add(time.Minute)) {
		t.Fatalf("renew = %v, %v", renewed, ok)
	}
	clock.Add(time.Minute)
	if !leases.expired("w") {
		t.Fatal("not expired at the deadline")
	}
	if _, ok := leases.renew("w", time.Minute); ok {
		t.Fatal("renew revived a lapsed lease")
	}
	leases.extend("x", time.Minute)
	leases.forget("x")
	if _, ok := leases.deadline("x"); ok {
		t.Fatal("forget kept the deadline")
	}
}

func TestWatchDeliverWritesOneEntryPerWatch(t *testing.T) {
	service, factory, _ := newDocumentTestService(t)
	sender := &endpointSender{sent: make(chan sentNotification, 8)}
	startDocumentTestService(t, service, factory, sender)
	saveDocument(t, service, watchYAML("porch", "front-door", "notify:\n  - cloud\n  - webhook: https://example.com/hook\n"))
	saveDocument(t, service, watchYAML("chat-1", "front-door", "lease: 60s\n"))
	service.leases.extend("chat-1", time.Minute)
	saveDocument(t, service, watchYAML("elsewhere", "back-door", ""))

	base := inferenceNotification(data.InferenceDocument{}.Campaign(""), "v4l2:/dev/video0", nil)
	base.Inference, base.Event = "front-door", "front-door"
	service.watches.deliver("front-door", base)

	entries := journal(t, service, "porch", "chat-1", "elsewhere")
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	byWatch := map[string]data.CampaignNotification{}
	for _, entry := range entries {
		byWatch[entry.Watch] = entry
	}
	if byWatch["porch"].Leased || !byWatch["chat-1"].Leased || byWatch["porch"].ID == byWatch["chat-1"].ID {
		t.Fatalf("entries = %+v", byWatch)
	}
	if byWatch["porch"].Campaign != "" || byWatch["porch"].Inference != "front-door" {
		t.Fatalf("porch entry = %+v", byWatch["porch"])
	}
	endpoints := map[string]bool{}
	for i := 0; i < 2; i++ {
		sent := receiveInference(t, sender.sent)
		if sent.notification.Watch != "porch" {
			t.Fatalf("delivered for %q", sent.notification.Watch)
		}
		endpoints[sent.endpoint] = true
	}
	if !endpoints[""] || !endpoints["https://example.com/hook"] {
		t.Fatalf("deliveries went to %v; want Cloud and the webhook", endpoints)
	}
}

func TestLapsedLeaseWritesNothingBeforeRemoval(t *testing.T) {
	service, factory, sender := newDocumentTestService(t)
	clock := newFakeClock()
	service.leases.now = clock.Now
	startDocumentTestService(t, service, factory, sender)
	saveDocument(t, service, watchYAML("chat-1", "front-door", "lease: 15s\n"))
	service.leases.extend("chat-1", 15*time.Second)
	clock.Add(16 * time.Second)
	base := DetectionNotification{Event: "front-door", Inference: "front-door", SourceID: "v4l2:/dev/video0", Count: 1}
	base.OccurredAt = time.Now().UTC().Format(time.RFC3339Nano)
	service.watches.deliver("front-door", base)
	if entries := journal(t, service, "chat-1"); len(entries) != 0 {
		t.Fatalf("a lapsed lease wrote %+v", entries)
	}
	if _, err := service.manager.Document(data.KindWatch, "chat-1"); err != nil {
		t.Fatal("the watch is gone before the expiry pass; this test needs it present")
	}
}

func TestExpireLeasesRemovesTheWatchAndWhatItOwns(t *testing.T) {
	service, factory, sender := newDocumentTestService(t)
	clock := newFakeClock()
	service.leases.now = clock.Now
	startDocumentTestService(t, service, factory, sender)
	saveDocument(t, service, watchYAML("chat-1", "chat-1", "lease: 15s\n"))
	service.leases.extend("chat-1", 15*time.Second)
	saveDocument(t, service, strings.Replace(testInferenceDocumentYAML, "name: front-door", "name: chat-1\nowner: watch/chat-1", 1))
	session := receiveInference(t, factory.sessions)
	clock.Add(16 * time.Second)
	service.expireLeases()
	for _, ref := range [][2]string{{data.KindWatch, "chat-1"}, {data.KindInference, "chat-1"}} {
		if _, err := service.manager.Document(ref[0], ref[1]); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s/%s after expiry: %v", ref[0], ref[1], err)
		}
	}
	select {
	case <-session.closed:
	case <-time.After(8 * time.Second):
		t.Fatal("the owned inference kept running after its watch expired")
	}
}

func TestBootSweepRemovesLeasedWatchesWhatTheyOwnAndOrphans(t *testing.T) {
	service, factory, sender := newDocumentTestService(t)
	saveDocument(t, service, watchYAML("chat-1", "chat-1", "lease: 60s\n"))
	saveDocument(t, service, strings.Replace(testInferenceDocumentYAML, "name: front-door", "name: chat-1\nowner: watch/chat-1", 1))
	saveDocument(t, service, watchYAML("porch", "front-door", ""))
	saveDocument(t, service, strings.Replace(testInferenceDocumentYAML, "name: front-door", "name: orphan\nowner: watch/gone", 1))
	startDocumentTestService(t, service, factory, sender)
	for _, ref := range [][2]string{{data.KindWatch, "chat-1"}, {data.KindInference, "chat-1"}, {data.KindInference, "orphan"}} {
		if _, err := service.manager.Document(ref[0], ref[1]); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s/%s survived the boot sweep: %v", ref[0], ref[1], err)
		}
	}
	if _, err := service.manager.Document(data.KindWatch, "porch"); err != nil {
		t.Fatalf("an unleased watch was swept: %v", err)
	}
	select {
	case <-factory.sessions:
		t.Fatal("an inference started for a swept document")
	case <-time.After(300 * time.Millisecond):
	}
}
```

Append to `data_inference_notification_test.go`:

```go
func TestCloudSenderUsesTheWatchIdentity(t *testing.T) {
	cloud := &capturingCloud{}
	notification := DetectionNotification{ID: uuid.NewString(), Event: "front-door", Watch: "porch", Inference: "front-door", Count: 1, OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := (&CampaignCloudSender{Cloud: cloud}).Send(context.Background(), "", notification); err != nil {
		t.Fatal(err)
	}
	request := cloud.last()
	if request.GetAppId() != "watch:porch" || request.GetTitle() != "Watch event" || !strings.Contains(request.GetBody(), "porch") {
		t.Fatalf("request = %+v", request)
	}
	if _, ok := request.GetMetadata().GetFields()["campaign"]; ok {
		t.Fatal("a watch notification carries campaign metadata")
	}
	if request.GetMetadata().GetFields()["watch"].GetStringValue() != "porch" {
		t.Fatal("metadata lacks the watch")
	}
}
```

Before writing it, read the existing Cloud test in that file (~line 40-60). If it already has a fake `NotificationSender` that records the request, use it under its own name instead of `capturingCloud`; otherwise add:

```go
type capturingCloud struct {
	mu       sync.Mutex
	requests []*cloudpb.CreateNotificationV2Request
}

func (c *capturingCloud) CreateNotificationV2(_ context.Context, request *cloudpb.CreateNotificationV2Request) (*cloudpb.CreateNotificationV2Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, request)
	id := request.GetNotificationId()
	return &cloudpb.CreateNotificationV2Response{NotificationId: id}, nil
}

func (c *capturingCloud) last() *cloudpb.CreateNotificationV2Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests[len(c.requests)-1]
}
```

(check how `CreateNotificationV2Response.NotificationId` is typed in `go/proto/gen/cloudpb`; if it is a pointer, use `&id`).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd go && go test ./internal/agent/services/ -run 'DocumentLeases|WatchDeliver|LapsedLease|ExpireLeases|BootSweep|CloudSenderUsesTheWatch' -v`
Expected: build failure, `undefined: documentLeases`.

- [ ] **Step 3: Share the delivery loop**

In `data_inference_notification.go`, add:

```go
// deliverNotification sends one notification to Wendy Cloud (empty endpoint)
// or a webhook: at most three attempts, each with a 10 s timeout, stopping at
// once on errors Cloud reports as permanent.
func deliverNotification(ctx context.Context, sender CampaignNotificationSender, endpoint string, request DetectionNotification) error {
	if sender == nil {
		return errors.New("campaign notification delivery is unavailable")
	}
	var err error
	for attempt := 0; attempt < 3 && ctx.Err() == nil; attempt++ {
		forward, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = sender.Send(forward, endpoint, request)
		cancel()
		if err == nil {
			return nil
		}
		// Cloud rejects unauthorized identities and duplicate UUIDs permanently.
		// Retrying those responses cannot deliver this notification.
		if endpoint == "" {
			switch status.Code(err) {
			case codes.InvalidArgument, codes.Unauthenticated, codes.PermissionDenied,
				codes.FailedPrecondition, codes.AlreadyExists, codes.Unimplemented, codes.DataLoss:
				return err
			}
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return err
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			}
		}
	}
	return err
}
```

(add `"time"` to its imports). In `data_inference.go`, replace the body of `notifications` with:

```go
func (j *campaignInferenceJob) notifications(ctx context.Context, queue <-chan DetectionNotification) {
	for {
		select {
		case <-ctx.Done():
			return
		case request := <-queue:
			err := deliverNotification(ctx, j.owner.sender, j.campaign.Notify.Webhook, request)
			if ctx.Err() != nil {
				return
			}
			j.notificationError(err)
		}
	}
}
```

In `CampaignCloudSender.Send`, replace the metadata, `appID`, `Title` and `Body` lines so that a watch's notification uses its own identity:

```go
	fields := map[string]any{
		"event":          notification.Event,
		"source_id":      notification.SourceID,
		"model":          notification.Model,
		"model_revision": notification.Revision,
		"count":          notification.Count,
		"occurred_at":    notification.OccurredAt,
	}
	appID := "campaign:" + notification.Campaign
	title, body := "Campaign event", fmt.Sprintf("Campaign %s emitted %s.", notification.Campaign, notification.Event)
	if notification.Watch != "" {
		fields["watch"], fields["inference"] = notification.Watch, notification.Inference
		appID = "watch:" + notification.Watch
		title, body = "Watch event", fmt.Sprintf("Watch %s saw %s.", notification.Watch, notification.Event)
	} else {
		fields["campaign"] = notification.Campaign
	}
	metadata, err := structpb.NewStruct(fields)
	if err != nil {
		return err
	}
```

and pass `Title: title, Body: body` in the request. The campaign request is otherwise unchanged; the existing Cloud test must still pass.

- [ ] **Step 4: Write the runner**

Delete Task 5's `watchRunner` stub from `data_inference.go`. In `data_service.go`'s `DataService` struct, below `deploymentMu`, add:

```go
	// leases holds leased watches' deadlines. Apply, expiry and removal change
	// them under deploymentMu with the documents they belong to; renew and the
	// check before each notification take only the table's own lock.
	leases documentLeases
	// watches is the watch runner, set by StartCampaignInference.
	watches *watchRunner
```

(move the `watches` field Task 5 added here if it sits elsewhere). Create `go/internal/agent/services/data_watch.go`:

```go
package services

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wendylabsinc/wendy/go/internal/agent/data"
)

// documentLeases holds leased watches' deadlines. They live in memory only:
// a leased watch without a deadline counts as expired, and the boot sweep
// removes every leased watch of a previous run (spec §6.3). The zero value is
// ready to use.
type documentLeases struct {
	mu sync.Mutex
	// now is the clock; nil means time.Now. Tests set a fake one before
	// StartCampaignInference.
	now       func() time.Time
	deadlines map[string]time.Time
}

func (l *documentLeases) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// extend sets name's deadline to now + lease and returns it.
func (l *documentLeases) extend(name string, lease time.Duration) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.deadlines == nil {
		l.deadlines = map[string]time.Time{}
	}
	deadline := l.clock().Add(lease)
	l.deadlines[name] = deadline
	return deadline
}

// renew extends a lease that has not lapsed. It refuses a lapsed one, even in
// the seconds before the expiry pass removes its watch: renewing would revive
// a watch whose client has already lost it.
func (l *documentLeases) renew(name string, lease time.Duration) (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	if deadline, ok := l.deadlines[name]; !ok || !now.Before(deadline) {
		return time.Time{}, false
	}
	l.deadlines[name] = now.Add(lease)
	return l.deadlines[name], true
}

func (l *documentLeases) forget(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.deadlines, name)
}

// expired reports whether name's lease has lapsed. A name without a deadline
// has lapsed.
func (l *documentLeases) expired(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	deadline, ok := l.deadlines[name]
	return !ok || !l.clock().Before(deadline)
}

func (l *documentLeases) deadline(name string) (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	deadline, ok := l.deadlines[name]
	return deadline, ok
}

// watchDelivery is one Cloud (empty endpoint) or webhook delivery.
type watchDelivery struct {
	watch        string
	endpoint     string
	notification DetectionNotification
}

// watchRunner turns inference arrivals into watch notifications (spec §6).
type watchRunner struct {
	service *DataService
	sender  CampaignNotificationSender
	queue   chan watchDelivery
	mu      sync.Mutex
	errors  map[string]string
}

// startWatches removes what a previous agent run leased, then runs the watch
// runner until ctx ends: deliveries, and lease expiry every 5 s. It returns a
// channel closed once both loops have stopped.
func (s *DataService) startWatches(ctx context.Context, sender CampaignNotificationSender) <-chan struct{} {
	s.sweepLeasedDocuments()
	runner := &watchRunner{service: s, sender: sender, queue: make(chan watchDelivery, 64), errors: map[string]string{}}
	s.watches = runner
	var loops sync.WaitGroup
	loops.Add(2)
	go func() { defer loops.Done(); runner.deliveries(ctx) }()
	go func() {
		defer loops.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.expireLeases()
			}
		}
	}()
	done := make(chan struct{})
	go func() { loops.Wait(); close(done) }()
	return done
}

// deliver writes one journal entry for each watch on inference and queues its
// Cloud and webhook deliveries. The inference runner calls it for every
// arrival; it never waits on Cloud or a webhook.
func (r *watchRunner) deliver(inference string, base DetectionNotification) {
	s := r.service
	watches, err := s.manager.Documents(data.KindWatch)
	if err != nil {
		s.manager.Warnf("reading watches for inference %q: %v", inference, err)
		return
	}
	for _, stored := range watches {
		watch, err := stored.Watch()
		if err != nil || watch.On.Inference != inference {
			continue
		}
		// The deadline decides, not the 5 s expiry pass: a lapsed lease writes
		// nothing even before its watch is removed.
		if watch.Leased() && s.leases.expired(watch.Name) {
			continue
		}
		notification := base
		notification.ID = uuid.NewString()
		notification.Watch = watch.Name
		notification.Leased = watch.Leased()
		if err := s.manager.RecordNotification(notification); err != nil {
			s.manager.Warnf("watch %q notification: %v", watch.Name, err)
			continue
		}
		if watch.NotifiesCloud() {
			r.enqueue(watchDelivery{watch: watch.Name, notification: notification})
		}
		if endpoint := watch.WebhookURL(); endpoint != "" {
			r.enqueue(watchDelivery{watch: watch.Name, endpoint: endpoint, notification: notification})
		}
	}
}

func (r *watchRunner) enqueue(delivery watchDelivery) {
	select {
	case r.queue <- delivery:
	default:
		r.setError(delivery.watch, errors.New("external notification queue full; notification retained in Wendy Data"))
	}
}

func (r *watchRunner) deliveries(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case delivery := <-r.queue:
			err := deliverNotification(ctx, r.sender, delivery.endpoint, delivery.notification)
			if ctx.Err() != nil {
				return
			}
			r.setError(delivery.watch, err)
			if err != nil {
				r.service.manager.Warnf("watch %q notification: %v", delivery.watch, err)
			}
		}
	}
}

func (r *watchRunner) setError(watch string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil {
		delete(r.errors, watch)
		return
	}
	r.errors[watch] = err.Error()
}

// deliveryError is the watch's last Cloud or webhook failure, or empty.
func (r *watchRunner) deliveryError(watch string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.errors[watch]
}

// removeDocumentTree deletes a document and everything it owns, owned
// documents first, and returns their references in the order removed. The
// caller waits for inference jobs afterwards, without deploymentMu.
func (s *DataService) removeDocumentTree(kind, name string) ([]string, error) {
	s.deploymentMu.Lock()
	defer s.deploymentMu.Unlock()
	return s.removeDocumentTreeLocked(kind, name)
}

func (s *DataService) removeDocumentTreeLocked(kind, name string) ([]string, error) {
	if _, err := s.manager.Document(kind, name); err != nil {
		return nil, err
	}
	ref := kind + "/" + name
	var removed []string
	for _, ownedKind := range []string{data.KindInference, data.KindWatch} {
		documents, err := s.manager.Documents(ownedKind)
		if err != nil {
			return removed, err
		}
		for _, document := range documents {
			if document.Owner != ref {
				continue
			}
			if err := s.manager.RemoveDocument(document.Kind, document.Name); err != nil && !errors.Is(err, os.ErrNotExist) {
				return removed, err
			}
			if document.Kind == data.KindWatch {
				s.leases.forget(document.Name)
			}
			removed = append(removed, document.Ref())
		}
	}
	if err := s.manager.RemoveDocument(kind, name); err != nil {
		return removed, err
	}
	if kind == data.KindWatch {
		s.leases.forget(name)
	}
	return append(removed, ref), nil
}

// expireLeases removes every leased watch whose lease lapsed, with what it
// owns, through DocumentRemove's path, then asks the inference loop to retire
// the jobs.
func (s *DataService) expireLeases() {
	watches, err := s.manager.Documents(data.KindWatch)
	if err != nil {
		s.manager.Warnf("reading watches to expire leases: %v", err)
		return
	}
	removedAny := false
	for _, stored := range watches {
		watch, err := stored.Watch()
		if err != nil || !watch.Leased() || !s.leases.expired(watch.Name) {
			continue
		}
		removed, err := s.removeDocumentTree(data.KindWatch, watch.Name)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			s.manager.Warnf("removing watch %q after its lease lapsed: %v", watch.Name, err)
			continue
		}
		if len(removed) > 0 {
			removedAny = true
			s.manager.Warnf("watch %q was removed because its lease lapsed: %s", watch.Name, strings.Join(removed, ", "))
		}
	}
	if removedAny && s.inference != nil {
		s.inference.wakeNow()
	}
}

// sweepLeasedDocuments runs once at boot, before any runner starts. Deadlines
// live in memory, so every leased watch belongs to a previous run: it goes,
// with what it owns, and so does every document whose owner is missing.
func (s *DataService) sweepLeasedDocuments() {
	s.deploymentMu.Lock()
	defer s.deploymentMu.Unlock()
	watches, err := s.manager.Documents(data.KindWatch)
	if err != nil {
		s.manager.Warnf("reading watches for the boot sweep: %v", err)
		return
	}
	for _, stored := range watches {
		if watch, err := stored.Watch(); err == nil && watch.Leased() {
			if _, err := s.removeDocumentTreeLocked(data.KindWatch, watch.Name); err != nil && !errors.Is(err, os.ErrNotExist) {
				s.manager.Warnf("removing leased watch %q at boot: %v", watch.Name, err)
			}
		}
	}
	for _, kind := range []string{data.KindInference, data.KindWatch} {
		documents, err := s.manager.Documents(kind)
		if err != nil {
			s.manager.Warnf("reading %s documents for the boot sweep: %v", kind, err)
			continue
		}
		for _, document := range documents {
			if document.Owner == "" {
				continue
			}
			ownerKind, ownerName, _ := strings.Cut(document.Owner, "/")
			if _, err := s.manager.Document(ownerKind, ownerName); !errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err := s.manager.RemoveDocument(document.Kind, document.Name); err != nil && !errors.Is(err, os.ErrNotExist) {
				s.manager.Warnf("removing %s, whose owner %s is gone: %v", document.Ref(), document.Owner, err)
			}
		}
	}
}
```

- [ ] **Step 5: Start the runner with the inference loop**

In `StartCampaignInference`, directly after `ctx, cancel := context.WithCancel(ctx)`, add:

```go
	// Leased documents of a previous run go before the first inference job
	// can open a camera for them.
	watchesDone := s.startWatches(ctx, sender)
```

and change the returned stop function to `return func() { cancel(); <-done; <-watchesDone }`.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd go && go test ./internal/agent/services/ -run 'DocumentLeases|WatchDeliver|LapsedLease|ExpireLeases|BootSweep|CloudSender|AgentInference|Notification' -v`
Expected: PASS. Then `cd go && go test -race ./internal/agent/services/ -run 'Watch|Lease|BootSweep|InferenceDocument'` and `cd go && go test ./internal/agent/services/` pass.

- [ ] **Step 7: Commit**

```bash
git add go/internal/agent/services/
git commit -m "feat(agent): watch runner with device-enforced leases

Each arrival of an inference becomes one journal entry per watch on it,
plus queued Cloud and webhook deliveries; a lapsed lease writes nothing
even before the 5 s expiry pass removes the watch and what it owns. At
boot, leased watches of the previous run, their documents and orphans
go before any inference starts. Campaign and watch delivery share one
retry loop; a watch notifies Cloud as watch:<name>."
```

---

### Task 7: The four calls

**Files:**
- Create: `go/internal/agent/services/data_documents.go`
- Modify: `go/internal/agent/services/data_service.go` (`dataStatusError`)
- Test: `go/internal/agent/services/data_documents_test.go` (new)

**Interfaces:**
- Consumes: everything above.
- Produces: `DocumentApply`, `DocumentRemove`, `DocumentRenew`, `Documents` on `*DataService`; `func (s *DataService) documentMessage(stored data.StoredDocument) *agentpbv2.DataDocument`; `func collectionMessage(campaign *agentpbv2.DataCampaign) *agentpbv2.DataDocument`.

- [ ] **Step 1: Write the failing tests**

Create `go/internal/agent/services/data_documents_test.go`:

```go
package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func apply(t *testing.T, service *DataService, contents string) (*agentpbv2.DataDocument, error) {
	t.Helper()
	response, err := service.DocumentApply(context.Background(), &agentpbv2.DataDocumentApplyRequest{DocumentYaml: []byte(contents)})
	return response.GetDocument(), err
}

func mustApply(t *testing.T, service *DataService, contents string) *agentpbv2.DataDocument {
	t.Helper()
	document, err := apply(t, service, contents)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func ownedInference(name, owner string) string {
	return strings.Replace(testInferenceDocumentYAML, "name: front-door", "name: "+name+"\nowner: "+owner, 1)
}

func TestDocumentApplyOwnership(t *testing.T) {
	service, factory, sender := newDocumentTestService(t)
	startDocumentTestService(t, service, factory, sender)
	if _, err := apply(t, service, ownedInference("chat-1", "watch/chat-1")); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("owner missing: %v", err)
	}
	watch := mustApply(t, service, watchYAML("chat-1", "chat-1", "lease: 60s\n"))
	if watch.GetKind() != "watch" || watch.GetExpiresUnixNanos() == 0 || watch.GetState() != "waiting" {
		t.Fatalf("watch = %+v", watch)
	}
	inference := mustApply(t, service, ownedInference("chat-1", "watch/chat-1"))
	if inference.GetOwner() != "watch/chat-1" {
		t.Fatalf("inference = %+v", inference)
	}
	if _, err := apply(t, service, watchYAML("nested", "x", "owner: inference/chat-1\n")); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "one level") {
		t.Fatalf("owner with an owner: %v", err)
	}
	mustApply(t, service, watchYAML("other", "x", ""))
	if _, err := apply(t, service, ownedInference("chat-1", "watch/other")); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "cannot change") {
		t.Fatalf("owner change: %v", err)
	}
	if _, err := apply(t, service, watchYAML("chat-1", "chat-1", "")); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "lease") {
		t.Fatalf("lease dropped: %v", err)
	}
	mustApply(t, service, watchYAML("porch", "front-door", ""))
	if _, err := apply(t, service, watchYAML("porch", "front-door", "lease: 60s\n")); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("lease added: %v", err)
	}
	if _, err := apply(t, service, "kind: watch\nversion: 1\nname: x\non: {inference: \"\"}\n"); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid document: %v", err)
	}
}

func TestDocumentApplyCollectionTakesTheCampaignPath(t *testing.T) {
	service, factory, sender := newDocumentTestService(t)
	startDocumentTestService(t, service, factory, sender)
	document := mustApply(t, service, testClipsCollection)
	if document.GetKind() != "collection" || document.GetName() != "clips" || document.GetState() != "armed" || len(document.GetDocumentJson()) == 0 {
		t.Fatalf("collection = %+v", document)
	}
	if _, err := service.manager.Campaign("clips"); err != nil {
		t.Fatalf("the collection is not a campaign: %v", err)
	}
}

func TestDocumentApplyWithoutRuntime(t *testing.T) {
	service, _, _ := newDocumentTestService(t)
	if _, err := apply(t, service, watchYAML("porch", "front-door", "")); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("no runtime: %v", err)
	}
}

func TestDocumentRemoveStopsOwnedInferenceBeforeReturning(t *testing.T) {
	service, factory, sender := newDocumentTestService(t)
	startDocumentTestService(t, service, factory, sender)
	mustApply(t, service, watchYAML("chat-1", "chat-1", "lease: 60s\n"))
	mustApply(t, service, ownedInference("chat-1", "watch/chat-1"))
	session := receiveInference(t, factory.sessions)
	response, err := service.DocumentRemove(context.Background(), &agentpbv2.DataDocumentRemoveRequest{Kind: "watch", Name: "chat-1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(response.GetRemoved(), ",") != "inference/chat-1,watch/chat-1" {
		t.Fatalf("removed = %v", response.GetRemoved())
	}
	select {
	case <-session.closed:
	default:
		t.Fatal("DocumentRemove returned before the inference stopped")
	}
	service.inference.wakeNow()
	select {
	case <-factory.sessions:
		t.Fatal("a later pass restarted the removed inference")
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := service.DocumentRenew(context.Background(), &agentpbv2.DataDocumentRenewRequest{Kind: "watch", Name: "chat-1"}); status.Code(err) != codes.NotFound {
		t.Fatalf("renew after removal: %v", err)
	}
	for kind, want := range map[string]codes.Code{"watch": codes.NotFound, "collection": codes.FailedPrecondition, "robot": codes.InvalidArgument} {
		if _, err := service.DocumentRemove(context.Background(), &agentpbv2.DataDocumentRemoveRequest{Kind: kind, Name: "chat-1"}); status.Code(err) != want {
			t.Fatalf("remove %s: %v, want %v", kind, err, want)
		}
	}
}

func TestDocumentRenew(t *testing.T) {
	service, factory, sender := newDocumentTestService(t)
	clock := newFakeClock()
	service.leases.now = clock.Now
	startDocumentTestService(t, service, factory, sender)
	mustApply(t, service, watchYAML("chat-1", "chat-1", "lease: 60s\n"))
	mustApply(t, service, watchYAML("porch", "front-door", ""))
	mustApply(t, service, testInferenceDocumentYAML)
	clock.Add(30 * time.Second)
	renewed, err := service.DocumentRenew(context.Background(), &agentpbv2.DataDocumentRenewRequest{Kind: "watch", Name: "chat-1"})
	if err != nil || renewed.GetExpiresUnixNanos() != clock.Now().Add(time.Minute).UnixNano() {
		t.Fatalf("renew = %v, %v", renewed, err)
	}
	for _, request := range []*agentpbv2.DataDocumentRenewRequest{{Kind: "watch", Name: "porch"}, {Kind: "inference", Name: "front-door"}} {
		if _, err := service.DocumentRenew(context.Background(), request); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("renew %v: %v", request, err)
		}
	}
	clock.Add(61 * time.Second)
	if _, err := service.DocumentRenew(context.Background(), &agentpbv2.DataDocumentRenewRequest{Kind: "watch", Name: "chat-1"}); status.Code(err) != codes.NotFound {
		t.Fatalf("renew after the deadline: %v", err)
	}
}

func TestDocumentsReportsStatus(t *testing.T) {
	service, factory, sender := newDocumentTestService(t)
	startDocumentTestService(t, service, factory, sender)
	mustApply(t, service, testClipsCollection)
	mustApply(t, service, watchYAML("porch", "front-door", ""))
	list := func(kind, name string) []*agentpbv2.DataDocument {
		t.Helper()
		response, err := service.Documents(context.Background(), &agentpbv2.DataDocumentsRequest{Kind: kind, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return response.GetDocuments()
	}
	if porch := list("watch", "porch"); len(porch) != 1 || porch[0].GetState() != "waiting" || !strings.Contains(porch[0].GetStateDetail(), "front-door") {
		t.Fatalf("waiting watch = %+v", porch)
	}
	mustApply(t, service, testInferenceDocumentYAML)
	session := receiveInference(t, factory.sessions)
	receiveInference(t, session.inputs)
	eventually(t, "the watch to become active", func() bool { return list("watch", "porch")[0].GetState() == "active" })
	all := list("", "")
	var kinds []string
	for _, document := range all {
		kinds = append(kinds, document.GetKind()+"/"+document.GetName())
	}
	if strings.Join(kinds, ",") != "collection/clips,inference/front-door,watch/porch" {
		t.Fatalf("all documents = %v", kinds)
	}
	if _, err := service.Documents(context.Background(), &agentpbv2.DataDocumentsRequest{Kind: "watch", Name: "missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("missing document: %v", err)
	}
	if _, err := service.Documents(context.Background(), &agentpbv2.DataDocumentsRequest{Kind: "robot"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unknown kind: %v", err)
	}
	_ = data.KindWatch
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd go && go test ./internal/agent/services/ -run 'DocumentApply|DocumentRemove|DocumentRenew|DocumentsReports' -v`
Expected: FAIL with `codes.Unimplemented` from every call.

- [ ] **Step 3: Map the new errors**

In `dataStatusError`, add `errors.Is(err, data.ErrInvalidDocumentName), errors.Is(err, data.ErrNotStoredKind)` to the InvalidArgument case.

- [ ] **Step 4: Implement the calls**

Create `go/internal/agent/services/data_documents.go`:

```go
package services

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	"github.com/wendylabsinc/wendy/go/internal/shared/datadoc"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// removalWait bounds how long DocumentRemove waits for removed inference
// jobs to stop (spec §9.3).
const removalWait = 10 * time.Second

// DocumentApply validates and stores a document and hands it to its runner
// (spec §9.2). A collection takes the CampaignDeploy path unchanged.
func (s *DataService) DocumentApply(ctx context.Context, req *agentpbv2.DataDocumentApplyRequest) (*agentpbv2.DataDocumentApplyResponse, error) {
	document, err := data.ParseDocument(req.GetDocumentYaml())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if document.Header.Kind == datadoc.DefaultKind {
		campaign, err := s.CampaignDeploy(ctx, &agentpbv2.DataCampaignDeployRequest{CampaignYaml: req.GetDocumentYaml()})
		if err != nil {
			return nil, err
		}
		return &agentpbv2.DataDocumentApplyResponse{Document: collectionMessage(campaign)}, nil
	}
	if s.inference == nil || s.watches == nil || (document.Header.Kind == data.KindInference && s.video == nil) {
		return nil, status.Error(codes.FailedPrecondition, "agent inference and watch runtime is unavailable")
	}
	stored, err := s.applyDocument(document)
	if err != nil {
		return nil, err
	}
	if stored.Kind == data.KindInference {
		s.inference.wakeNow()
	}
	return &agentpbv2.DataDocumentApplyResponse{Document: s.documentMessage(stored)}, nil
}

func (s *DataService) applyDocument(document datadoc.Document) (data.StoredDocument, error) {
	s.deploymentMu.Lock()
	defer s.deploymentMu.Unlock()
	header := document.Header
	if header.Owner != "" {
		ownerKind, ownerName, _ := strings.Cut(header.Owner, "/")
		owner, err := s.manager.Document(ownerKind, ownerName)
		if errors.Is(err, os.ErrNotExist) {
			return data.StoredDocument{}, status.Errorf(codes.FailedPrecondition, "owner %s does not exist; apply it first", header.Owner)
		}
		if err != nil {
			return data.StoredDocument{}, dataStatusError(err)
		}
		if owner.Owner != "" {
			return data.StoredDocument{}, status.Errorf(codes.FailedPrecondition, "owner %s is itself owned by %s; ownership is one level deep", header.Owner, owner.Owner)
		}
	}
	existing, err := s.manager.Document(header.Kind, header.Name)
	switch {
	case err == nil:
		if existing.Owner != header.Owner {
			return data.StoredDocument{}, status.Errorf(codes.FailedPrecondition, "%s has owner %q; a redeploy cannot change it", existing.Ref(), existing.Owner)
		}
		if watch, ok := document.Body.(data.Watch); ok {
			previous, err := existing.Watch()
			if err != nil {
				return data.StoredDocument{}, status.Error(codes.Internal, err.Error())
			}
			if previous.Leased() != watch.Leased() {
				return data.StoredDocument{}, status.Errorf(codes.FailedPrecondition, "%s: a redeploy cannot add or drop a lease", existing.Ref())
			}
		}
	case !errors.Is(err, os.ErrNotExist):
		return data.StoredDocument{}, dataStatusError(err)
	}
	stored, err := s.manager.SaveDocument(document)
	if err != nil {
		return data.StoredDocument{}, dataStatusError(err)
	}
	// Applying or reapplying a leased watch starts its lease afresh.
	if watch, ok := document.Body.(data.Watch); ok && watch.Leased() {
		s.leases.extend(watch.Name, watch.LeaseDuration())
	}
	return stored, nil
}

// DocumentRemove removes an inference or watch document and what it owns,
// then waits, at most removalWait, for their inference to stop.
func (s *DataService) DocumentRemove(ctx context.Context, req *agentpbv2.DataDocumentRemoveRequest) (*agentpbv2.DataDocumentRemoveResponse, error) {
	switch req.GetKind() {
	case datadoc.DefaultKind:
		return nil, status.Error(codes.FailedPrecondition, "collections cannot be removed yet")
	case data.KindInference, data.KindWatch:
	default:
		return nil, status.Errorf(codes.InvalidArgument, "unknown document kind %q", req.GetKind())
	}
	removed, err := s.removeDocumentTree(req.GetKind(), req.GetName())
	if err != nil {
		return nil, dataStatusError(err)
	}
	if err := s.waitForRetiredJobs(ctx); err != nil {
		return nil, err
	}
	return &agentpbv2.DataDocumentRemoveResponse{Removed: removed}, nil
}

// waitForRetiredJobs runs a reconcile pass and waits for it, which returns
// once every job without a document has stopped.
func (s *DataService) waitForRetiredJobs(ctx context.Context) error {
	if s.inference == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, removalWait)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.inference.reconcileNow() }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return status.Error(codes.DeadlineExceeded, "the documents are removed; their inference is still stopping")
	}
}

// DocumentRenew pushes a leased watch's deadline to now + lease. It takes no
// deploymentMu: a capture start holding it must not make an on-time renewal
// late.
func (s *DataService) DocumentRenew(_ context.Context, req *agentpbv2.DataDocumentRenewRequest) (*agentpbv2.DataDocumentRenewResponse, error) {
	stored, err := s.manager.Document(req.GetKind(), req.GetName())
	if err != nil {
		return nil, dataStatusError(err)
	}
	if stored.Kind != data.KindWatch {
		return nil, status.Errorf(codes.FailedPrecondition, "%s has no lease", stored.Ref())
	}
	watch, err := stored.Watch()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if !watch.Leased() {
		return nil, status.Errorf(codes.FailedPrecondition, "%s has no lease", stored.Ref())
	}
	deadline, ok := s.leases.renew(watch.Name, watch.LeaseDuration())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "%s: its lease expired", stored.Ref())
	}
	return &agentpbv2.DataDocumentRenewResponse{ExpiresUnixNanos: deadline.UnixNano()}, nil
}

// Documents lists documents of every kind, or one, with their status.
func (s *DataService) Documents(_ context.Context, req *agentpbv2.DataDocumentsRequest) (*agentpbv2.DataDocumentsResponse, error) {
	kinds := []string{datadoc.DefaultKind, data.KindInference, data.KindWatch}
	if req.GetKind() != "" {
		switch req.GetKind() {
		case datadoc.DefaultKind, data.KindInference, data.KindWatch:
			kinds = []string{req.GetKind()}
		default:
			return nil, status.Errorf(codes.InvalidArgument, "unknown document kind %q", req.GetKind())
		}
	}
	var documents []*agentpbv2.DataDocument
	for _, kind := range kinds {
		if kind == datadoc.DefaultKind {
			campaigns, err := s.manager.Campaigns()
			if err != nil {
				return nil, dataStatusError(err)
			}
			for _, campaign := range campaigns {
				if req.GetName() != "" && campaign.Name != req.GetName() {
					continue
				}
				message, err := s.campaignMessage(campaign)
				if err != nil {
					return nil, status.Error(codes.Internal, err.Error())
				}
				documents = append(documents, collectionMessage(message))
			}
			continue
		}
		stored, err := s.manager.Documents(kind)
		if err != nil {
			return nil, dataStatusError(err)
		}
		for _, document := range stored {
			if req.GetName() == "" || document.Name == req.GetName() {
				documents = append(documents, s.documentMessage(document))
			}
		}
	}
	if req.GetName() != "" && len(documents) == 0 {
		return nil, status.Errorf(codes.NotFound, "no document named %q", req.GetName())
	}
	return &agentpbv2.DataDocumentsResponse{Documents: documents}, nil
}

// collectionMessage presents a campaign as a collection document.
func collectionMessage(campaign *agentpbv2.DataCampaign) *agentpbv2.DataDocument {
	return &agentpbv2.DataDocument{Kind: datadoc.DefaultKind, Version: data.CampaignVersion, Name: campaign.GetName(), Revision: campaign.GetRevision(),
		State: campaign.GetState(), DeployedUnixNanos: campaign.GetDeployedUnixNanos(), DocumentJson: campaign.GetPlanJson(), Warnings: campaign.GetWarnings()}
}

// documentMessage presents a stored inference or watch document with its
// status (spec §6.6).
func (s *DataService) documentMessage(stored data.StoredDocument) *agentpbv2.DataDocument {
	message := &agentpbv2.DataDocument{Kind: stored.Kind, Version: uint32(stored.Version), Name: stored.Name, Owner: stored.Owner, Revision: stored.Revision,
		DeployedUnixNanos: stored.DeployedUnixNanos, DocumentJson: stored.Body, Warnings: stored.Warnings}
	switch stored.Kind {
	case data.KindInference:
		snapshot := s.inferenceSnapshot(stored.Name, stored.Revision)
		message.State, message.StateDetail = snapshot.State, snapshot.Error
		message.StatusJson, _ = json.Marshal(snapshot)
	case data.KindWatch:
		watch, err := stored.Watch()
		if err != nil {
			message.State, message.StateDetail = "error", err.Error()
			return message
		}
		state, detail, inference := s.watchState(watch)
		message.State, message.StateDetail = state, detail
		if watch.Leased() {
			if deadline, ok := s.leases.deadline(watch.Name); ok {
				message.ExpiresUnixNanos = deadline.UnixNano()
			}
		}
		deliveryError := ""
		if s.watches != nil {
			deliveryError = s.watches.deliveryError(watch.Name)
		}
		message.StatusJson, _ = json.Marshal(map[string]string{"inference_state": inference.State, "inference_error": inference.Error, "delivery_error": deliveryError})
	}
	return message
}

func (s *DataService) inferenceSnapshot(name, revision string) *data.InferenceStatus {
	if s.inference == nil {
		return &data.InferenceStatus{State: "stopped"}
	}
	return s.inference.snapshot(inferenceJobKey(name), revision)
}

// watchState maps a watch's inference to waiting, active or error, and
// returns the inference's own status for the watch's status_json, so a client
// can tell a camera that is unavailable from a detector still loading.
func (s *DataService) watchState(watch data.Watch) (state, detail string, inference data.InferenceStatus) {
	stored, err := s.manager.Document(data.KindInference, watch.On.Inference)
	if err != nil {
		return "waiting", "inference " + watch.On.Inference + " is not deployed", data.InferenceStatus{}
	}
	document, err := stored.Inference()
	if err != nil {
		return "error", err.Error(), data.InferenceStatus{}
	}
	if !document.IsEnabled() {
		return "waiting", "inference " + watch.On.Inference + " is disabled", data.InferenceStatus{State: "disabled"}
	}
	snapshot := *s.inferenceSnapshot(stored.Name, stored.Revision)
	switch snapshot.State {
	case "running":
		return "active", "", snapshot
	case "error":
		return "error", snapshot.Error, snapshot
	}
	return "waiting", "inference " + watch.On.Inference + " is " + snapshot.State, snapshot
}
```

(If `data.CampaignVersion` is an untyped constant it converts to `uint32`; if it is typed `int`, write `uint32(data.CampaignVersion)`. If `DataCampaign.plan_json` is generated as `[]byte`, `GetPlanJson()` passes straight through.)

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd go && go test ./internal/agent/services/ -run 'DocumentApply|DocumentRemove|DocumentRenew|DocumentsReports' -v`
Expected: PASS. Then `cd go && go test -race ./internal/agent/services/ -run 'Document|Watch|Lease'` and `cd go && go test ./internal/agent/...` pass, and `cd go && GOOS=linux GOARCH=arm64 go build -o /dev/null ./cmd/wendy-agent/` succeeds.

- [ ] **Step 6: Commit**

```bash
git add go/internal/agent/services/
git commit -m "feat(agent): apply, remove, renew and list data documents

DocumentApply checks owners (present, one level deep, fixed on redeploy)
and keeps a watch's lease fixed; a collection goes through
CampaignDeploy. DocumentRemove removes owned documents first and
returns once their inference stopped, within 10 s. DocumentRenew takes
no deployment lock. Documents reports each watch as waiting, active or
error from its inference, with its lease deadline."
```

---

### Task 8: The CLI

**Files:**
- Create: `go/internal/cli/commands/data_documents.go`
- Modify: `go/internal/cli/commands/data.go` (`newDataCmd`; `resolveCampaignDeployFile` and `isCampaignFile`; the `!ok` branch in campaign deploy)
- Test: `go/internal/cli/commands/data_documents_test.go` (new); `data_campaign_file_test.go` must keep passing unchanged

**Interfaces:**
- Consumes: the generated client calls; `data.ParseDocument`, `data.DocumentHeader`; existing `connectToAgent`, `withDataClient`, `registerCloudApps`, `encodeProtoJSON`, `campaignDeployError`, `shortRevision`, `pickFromItems`, `isInteractiveTerminal`, `jsonOutput`.
- Produces: `newDataApplyCmd`, `newDataDocumentsCmd`, `newDataRemoveCmd`; helpers `type dataFilePicker`, `parseDocumentFilter(args []string) (kind, name string, err error)`, `parseDocumentRef(arg string) (kind, name string, err error)`, `documentCloudApps(doc datadoc.Document) []string`, `documentCallError(err error) error`.

- [ ] **Step 1: Write the failing tests**

Create `go/internal/cli/commands/data_documents_test.go`:

```go
package commands

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDocumentFilePickerOffersEveryKind(t *testing.T) {
	t.Chdir(t.TempDir())
	writePickerFiles(t, map[string]string{
		"clips.collection.yaml":  "version: 1\nname: clips\n",
		"door.inference.yaml":    "kind: inference\nversion: 1\nname: door\n",
		"door.watch.yaml":        "kind: watch\nversion: 1\nname: door\n",
		"compose.yml":            "services: {}\n",
	})
	path, err := documentFilePicker.resolve(nil, true, func(_ string, items []tui.PickerItem) (string, error) {
		var labels []string
		for _, item := range items {
			labels = append(labels, item.Name)
		}
		want := []string{"clips.collection.yaml (collection clips)", "door.inference.yaml (inference door)", "door.watch.yaml (watch door)"}
		if !reflect.DeepEqual(labels, want) {
			t.Fatalf("labels = %v", labels)
		}
		return items[2].Value.(string), nil
	})
	if err != nil || path != "door.watch.yaml" {
		t.Fatalf("picked %q: %v", path, err)
	}
	_, err = documentFilePicker.resolve(nil, false, nil)
	if err == nil || !strings.Contains(err.Error(), "wendy data apply") || !strings.Contains(err.Error(), "door.watch.yaml") || strings.Contains(err.Error(), "compose.yml") {
		t.Fatalf("non-interactive guidance = %v", err)
	}
}

func TestParseDocumentFilterAndRef(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		kind, name string
		ok         bool
	}{
		{nil, "", "", true},
		{[]string{"watch"}, "watch", "", true},
		{[]string{"watch/front-door"}, "watch", "front-door", true},
		{[]string{"collection/clips"}, "collection", "clips", true},
		{[]string{"robot"}, "", "", false},
		{[]string{"watch/front door"}, "", "", false},
		{[]string{"watch/"}, "", "", false},
	} {
		kind, name, err := parseDocumentFilter(tc.args)
		if (err == nil) != tc.ok || kind != tc.kind || name != tc.name {
			t.Errorf("parseDocumentFilter(%v) = %q, %q, %v", tc.args, kind, name, err)
		}
	}
	if _, _, err := parseDocumentRef("watch"); err == nil {
		t.Error("a bare kind was accepted as a reference")
	}
	if kind, name, err := parseDocumentRef("inference/door"); err != nil || kind != "inference" || name != "door" {
		t.Errorf("parseDocumentRef = %q, %q, %v", kind, name, err)
	}
}

func TestDocumentCloudApps(t *testing.T) {
	cases := map[string][]string{
		"version: 1\nname: clips\nsources: [{telemetry: true}]\ncapture: {buffer: 0s, after_trigger: 1s, triggers: [{event: x}]}\nupload: {when: manual}\nexport: {annotation: cvat}\n": {"campaign:clips"},
		"kind: watch\nversion: 1\nname: porch\non: {inference: door}\nnotify: [cloud]\n":                                                                                                            {"watch:porch"},
		"kind: watch\nversion: 1\nname: chat-1\nlease: 60s\non: {inference: chat-1}\n":                                                                                                            nil,
	}
	for contents, want := range cases {
		document, err := data.ParseDocument([]byte(contents))
		if err != nil {
			t.Fatal(err)
		}
		if got := documentCloudApps(document); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: apps = %v, want %v", document.Header.Name, got, want)
		}
	}
}

func TestDocumentCallErrorOnOldAgents(t *testing.T) {
	err := documentCallError(status.Error(codes.Unimplemented, "unknown method DocumentApply"))
	if err == nil || !strings.Contains(err.Error(), "wendy device update") {
		t.Fatalf("old agent error = %v", err)
	}
	other := status.Error(codes.NotFound, "no document named \"x\"")
	if documentCallError(other) != other {
		t.Fatal("an unrelated error was rewritten")
	}
}

func TestCampaignDeployPointsOtherKindsAtApply(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("door.watch.yaml", []byte("kind: watch\nversion: 1\nname: door\non: {inference: door}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newDataCampaignDeployCmd()
	cmd.SetArgs([]string{"door.watch.yaml"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "wendy data apply door.watch.yaml") {
		t.Fatalf("campaign deploy of a watch = %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd go && go test ./internal/cli/commands/ -run 'DocumentFilePicker|ParseDocumentFilter|DocumentCloudApps|DocumentCallError|CampaignDeployPointsOtherKinds' -v`
Expected: build failure, `undefined: documentFilePicker`.

- [ ] **Step 3: One picker for campaigns and documents**

In `data.go`, replace `resolveCampaignDeployFile`, `maxCampaignFileBytes` and `campaignFileSkipReason` (PR 3's final-review fix, which reports why each skipped file was skipped) with:

```go
// dataFilePicker resolves a path argument or, without one, offers the YAML
// files in the current directory whose data document header it accepts.
type dataFilePicker struct {
	usage  string // how to pass a path, e.g. "pass a file path, for example: …"
	noun   string // "campaign", "data document"
	plural string // "campaigns", "data documents"
	// skip says why a file with this valid header is not offered, or "".
	skip  func(datadoc.Header) string
	label func(file string, header datadoc.Header) string
}

var campaignFilePicker = dataFilePicker{
	usage:  "pass a file path, for example: wendy data campaign deploy campaign.yaml",
	noun:   "campaign",
	plural: "campaigns",
	skip: func(header datadoc.Header) string {
		if header.Kind != datadoc.DefaultKind {
			return "kind " + header.Kind
		}
		return ""
	},
	label: func(file string, _ datadoc.Header) string { return file },
}

var documentFilePicker = dataFilePicker{
	usage:  "pass a file path, for example: wendy data apply front-door.watch.yaml",
	noun:   "data document",
	plural: "data documents",
	skip:   func(datadoc.Header) string { return "" },
	label: func(file string, header datadoc.Header) string {
		return fmt.Sprintf("%s (%s %s)", file, header.Kind, header.Name)
	},
}

func resolveCampaignDeployFile(args []string, interactive bool, pick func(string, []tui.PickerItem) (string, error)) (string, error) {
	return campaignFilePicker.resolve(args, interactive, pick)
}

func (p dataFilePicker) resolve(args []string, interactive bool, pick func(string, []tui.PickerItem) (string, error)) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		return "", fmt.Errorf("listing %s files in the current directory: %w; %s", p.noun, err, p.usage)
	}
	var items []tui.PickerItem
	var names, skipped []string
	sawYAML := false
	for _, entry := range entries {
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		info, err := os.Stat(entry.Name())
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		sawYAML = true
		// Offer only files whose header fits, not every YAML file: a project
		// folder also holds compose files and stagefiles. Say why a file was
		// left out, so a broken document is not silently missing.
		header, reason := readDataHeader(entry.Name(), info.Size())
		if reason == "" {
			reason = p.skip(header)
		}
		if reason != "" {
			skipped = append(skipped, fmt.Sprintf("%s (%s)", entry.Name(), reason))
			continue
		}
		names = append(names, entry.Name())
		items = append(items, tui.PickerItem{Name: p.label(entry.Name(), header), Value: entry.Name()})
	}
	if len(items) == 0 {
		if sawYAML {
			return "", fmt.Errorf("no %s files in the current directory (these YAML files are not %s: %s); %s", p.noun, p.plural, strings.Join(skipped, ", "), p.usage)
		}
		return "", fmt.Errorf("no .yaml or .yml files found in the current directory; %s", p.usage)
	}
	if !interactive {
		return "", fmt.Errorf("a %s YAML path is required in non-interactive or --json mode; %s\n%s files in the current directory: %s", p.noun, p.usage, strings.ToUpper(p.noun[:1])+p.noun[1:], strings.Join(names, ", "))
	}
	return pick("Select a "+p.noun+" YAML file", items)
}

// maxDataFileBytes bounds what the picker reads per file. Documents are a
// few kilobytes; anything far larger is not one.
const maxDataFileBytes = 1 << 20

// readDataHeader reads a file's data document header, or says why it has
// none.
func readDataHeader(name string, size int64) (datadoc.Header, string) {
	if size > maxDataFileBytes {
		return datadoc.Header{}, "file too large"
	}
	contents, err := os.ReadFile(name)
	if err != nil {
		return datadoc.Header{}, err.Error()
	}
	header, err := data.DocumentHeader(contents)
	if err != nil {
		return datadoc.Header{}, err.Error()
	}
	return header, ""
}
```

Then run `go test ./internal/cli/commands/ -run 'CampaignDeployFile' -v`; the existing picker tests must pass unchanged, except that a `kind: watch` file's reason now reads `(kind watch)` instead of PR 3's `(unknown kind "watch"; …)`, since this PR registers the kind; update that one expectation.

In `newDataCampaignDeployCmd`, replace the `!ok` branch's error with:

```go
			return fmt.Errorf("%s is a %s document, not a campaign; deploy it with `wendy data apply %s`", path, document.Header.Kind, path)
```

and update the command's `Long` text to "Deploy a campaign YAML file. Without a path, select a campaign file from the current directory.\nNon-interactive and --json invocations require an explicit path."

- [ ] **Step 4: Write the commands**

Create `go/internal/cli/commands/data_documents.go`:

```go
package commands

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	"github.com/wendylabsinc/wendy/go/internal/shared/datadoc"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var documentKinds = []string{datadoc.DefaultKind, data.KindInference, data.KindWatch}

func newDataApplyCmd() *cobra.Command {
	var skipCloudRegistration bool
	command := &cobra.Command{Use: "apply [document.yaml]", Short: "Validate and apply a data document (collection, inference or watch) on the connected device",
		Long: "Apply a data document. Without a path, select one from the current directory.\nNon-interactive and --json invocations require an explicit path.",
		Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			path, err := documentFilePicker.resolve(args, !jsonOutput && isInteractiveTerminal(), pickFromItems)
			if err != nil {
				return err
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("reading %q: %w", path, err)
			}
			document, err := data.ParseDocument(contents)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			conn, err := connectToAgent(cmd.Context())
			if err != nil {
				return err
			}
			defer conn.Close()
			if apps := documentCloudApps(document); len(apps) > 0 {
				if err := registerCloudApps(cmd.Context(), conn, apps, skipCloudRegistration); err != nil {
					return err
				}
			}
			response, err := conn.DataService.DocumentApply(cmd.Context(), &agentpbv2.DataDocumentApplyRequest{DocumentYaml: contents})
			if status.Code(err) == codes.Unimplemented && document.Header.Kind == datadoc.DefaultKind {
				// An agent from before data documents still deploys campaigns.
				campaign, deployErr := conn.DataService.CampaignDeploy(cmd.Context(), &agentpbv2.DataCampaignDeployRequest{CampaignYaml: contents})
				if deployErr != nil {
					return campaignDeployError(deployErr)
				}
				response, err = &agentpbv2.DataDocumentApplyResponse{Document: &agentpbv2.DataDocument{Kind: datadoc.DefaultKind, Version: uint32(document.Header.Version), Name: campaign.GetName(),
					Revision: campaign.GetRevision(), State: campaign.GetState(), DeployedUnixNanos: campaign.GetDeployedUnixNanos(), DocumentJson: campaign.GetPlanJson(), Warnings: campaign.GetWarnings()}}, nil
			}
			if err != nil {
				return documentCallError(err)
			}
			if jsonOutput {
				return encodeProtoJSON(cmd.OutOrStdout(), response.GetDocument())
			}
			printDocumentSummary(cmd.OutOrStdout(), response.GetDocument())
			return nil
		}}
	command.Flags().BoolVar(&skipCloudRegistration, "skip-cloud-registration", false, "Apply without registering the document's Cloud app (offline use)")
	return command
}

func newDataDocumentsCmd() *cobra.Command {
	return &cobra.Command{Use: "documents [<kind>[/<name>]]", Aliases: []string{"docs"}, Short: "List data documents on the connected device, or inspect one",
		Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			kind, name, err := parseDocumentFilter(args)
			if err != nil {
				return err
			}
			return withDataClient(cmd.Context(), func(client agentpbv2.DataServiceClient) error {
				response, err := client.Documents(cmd.Context(), &agentpbv2.DataDocumentsRequest{Kind: kind, Name: name})
				if err != nil {
					return documentCallError(err)
				}
				if jsonOutput {
					return encodeProtoJSON(cmd.OutOrStdout(), response)
				}
				out := cmd.OutOrStdout()
				if name != "" && len(response.GetDocuments()) == 1 {
					document := response.GetDocuments()[0]
					printDocumentSummary(out, document)
					fmt.Fprintf(out, "%s\n", document.GetDocumentJson())
					if len(document.GetStatusJson()) > 0 {
						fmt.Fprintf(out, "status: %s\n", document.GetStatusJson())
					}
					return nil
				}
				fmt.Fprintln(out, "KIND\tNAME\tSTATE\tOWNER\tEXPIRES\tREVISION")
				for _, document := range response.GetDocuments() {
					fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\t%s\n", document.GetKind(), document.GetName(), document.GetState(), dashIfEmpty(document.GetOwner()),
						expiresIn(document.GetExpiresUnixNanos()), shortRevision(document.GetRevision()))
				}
				return nil
			})
		}}
}

func newDataRemoveCmd() *cobra.Command {
	return &cobra.Command{Use: "remove <kind>/<name>", Aliases: []string{"rm"}, Short: "Remove an inference or watch document, and what it owns, from the connected device",
		Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			kind, name, err := parseDocumentRef(args[0])
			if err != nil {
				return err
			}
			return withDataClient(cmd.Context(), func(client agentpbv2.DataServiceClient) error {
				response, err := client.DocumentRemove(cmd.Context(), &agentpbv2.DataDocumentRemoveRequest{Kind: kind, Name: name})
				if err != nil {
					return documentCallError(err)
				}
				if jsonOutput {
					return encodeProtoJSON(cmd.OutOrStdout(), response)
				}
				for _, removed := range response.GetRemoved() {
					fmt.Fprintf(cmd.OutOrStdout(), "Removed %s\n", removed)
				}
				return nil
			})
		}}
}

// parseDocumentFilter reads `documents`' optional "<kind>" or "<kind>/<name>".
func parseDocumentFilter(args []string) (kind, name string, err error) {
	if len(args) == 0 {
		return "", "", nil
	}
	kind, name, hasName := strings.Cut(args[0], "/")
	if !isDocumentKind(kind) {
		return "", "", fmt.Errorf("unknown document kind %q; use %s", kind, strings.Join(documentKinds, ", "))
	}
	if hasName && !datadoc.ValidName(name) {
		return "", "", fmt.Errorf("invalid document name %q", name)
	}
	return kind, name, nil
}

// parseDocumentRef reads a required "<kind>/<name>".
func parseDocumentRef(arg string) (kind, name string, err error) {
	kind, name, err = parseDocumentFilter([]string{arg})
	if err == nil && name == "" {
		err = errors.New("name a document as <kind>/<name>, for example watch/front-door")
	}
	return kind, name, err
}

func isDocumentKind(kind string) bool {
	for _, known := range documentKinds {
		if kind == known {
			return true
		}
	}
	return false
}

// documentCloudApps names the Cloud apps a document needs before it is
// applied: a collection's campaign app, and a watch's when it notifies Cloud.
func documentCloudApps(document datadoc.Document) []string {
	switch body := document.Body.(type) {
	case data.Campaign:
		return []string{"campaign:" + body.Name}
	case data.Watch:
		if body.NotifiesCloud() {
			return []string{"watch:" + body.Name}
		}
	}
	return nil
}

// documentCallError explains an agent from before data documents.
func documentCallError(err error) error {
	if status.Code(err) == codes.Unimplemented {
		return status.Error(codes.Unimplemented, "This device's agent is too old for data documents; update it with `wendy device update`.")
	}
	return err
}

func printDocumentSummary(out io.Writer, document *agentpbv2.DataDocument) {
	fmt.Fprintf(out, "%s %s: %s (revision %s)", document.GetKind(), document.GetName(), document.GetState(), shortRevision(document.GetRevision()))
	if detail := document.GetStateDetail(); detail != "" {
		fmt.Fprintf(out, " — %s", detail)
	}
	if expires := document.GetExpiresUnixNanos(); expires != 0 {
		fmt.Fprintf(out, ", lease expires %s", expiresIn(expires))
	}
	fmt.Fprintln(out)
	for _, warning := range document.GetWarnings() {
		fmt.Fprintf(out, "Warning: %s\n", warning)
	}
}

func expiresIn(unixNanos int64) string {
	if unixNanos == 0 {
		return "-"
	}
	return "in " + time.Until(time.Unix(0, unixNanos)).Round(time.Second).String()
}

func dashIfEmpty(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
```

Register the three commands in `newDataCmd`'s `cmd.AddCommand(...)` list: `newDataApplyCmd(), newDataDocumentsCmd(), newDataRemoveCmd()`. If `dashIfEmpty` or `expiresIn` already exist in the package (`grep -rn 'func dashIfEmpty\|func expiresIn' go/internal/cli/commands/`), drop the new copy and use the existing one.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd go && go test ./internal/cli/commands/ -run 'DocumentFilePicker|ParseDocumentFilter|DocumentCloudApps|DocumentCallError|CampaignDeploy|DataCampaign' -v && go build ./cmd/wendy`
Expected: PASS and the CLI builds.

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/commands/
git commit -m "feat(cli): wendy data apply, documents and remove

apply validates any data document locally, registers the Cloud app it
needs, and falls back to CampaignDeploy for a collection on an older
agent. documents lists or inspects with state, owner and lease; remove
takes <kind>/<name>. Campaign deploy and apply share one file picker,
and campaign deploy points other kinds at apply."
```

---

### Task 9: Docs and examples

**Files:**
- Create: `Examples/WendyDataWatch/README.md`, `Examples/WendyDataWatch/front-door.inference.yaml`, `Examples/WendyDataWatch/front-door.watch.yaml`, `Examples/WendyDataWatch/front-door-clips.collection.yaml`
- Modify: `go/internal/cli/assets/docs/clients/wendy-cli/commands/data.md`
- Test: `go/internal/agent/data/example_documents_test.go` (new)

- [ ] **Step 1: Write the failing test**

Create `go/internal/agent/data/example_documents_test.go`:

```go
package data

import (
	"os"
	"path/filepath"
	"testing"
)

// The example documents are what docs point people at: each must parse, and
// the collection must trigger on the inference's event.
func TestWatchExamplesParse(t *testing.T) {
	files, err := filepath.Glob("../../../../Examples/WendyDataWatch/*.yaml")
	if err != nil || len(files) != 3 {
		t.Fatalf("example files = %v, %v", files, err)
	}
	kinds := map[string]any{}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := ParseDocument(raw)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		kinds[doc.Header.Kind] = doc.Body
	}
	inference := kinds[KindInference].(InferenceDocument)
	watch := kinds[KindWatch].(Watch)
	collection := kinds["collection"].(Campaign)
	if watch.On.Inference != inference.Name {
		t.Fatalf("the watch listens to %q, the inference is %q", watch.On.Inference, inference.Name)
	}
	triggered := false
	for _, trigger := range collection.Capture.Triggers {
		triggered = triggered || trigger.Event == inference.Event
	}
	if !triggered {
		t.Fatalf("the collection does not trigger on %q", inference.Event)
	}
}
```

Run: `cd go && go test ./internal/agent/data/ -run TestWatchExamplesParse -v`
Expected: FAIL (`example files = []`).

- [ ] **Step 2: Write the examples**

`Examples/WendyDataWatch/front-door.inference.yaml`:

```yaml
# An inference runs one detector on cameras and emits an event when
# something it looks for arrives. Model fields are the same as a campaign's
# inline inference block.
kind: inference
version: 1
name: front-door
sources:
  # Any camera selector a campaign accepts. Run `wendy data sources` for ids.
  - camera: "*"
model: ustc-community/dfine-nano-coco
revision: 066438d3d8f0da137a37b38fdf3368fd4afceced
labels: [person]
threshold: 0.5
rate: 2
# The event defaults to the document's name; collections trigger on it.
clear_after: 5s
cooldown: 30s
```

`Examples/WendyDataWatch/front-door.watch.yaml`:

```yaml
# A watch delivers an inference's arrivals as notifications. Every arrival
# lands in the device's notification journal; these destinations are extra.
kind: watch
version: 1
name: front-door
on:
  inference: front-door
notify:
  # Wendy Cloud push. Enroll the device and enable the watch:front-door
  # app's notification grant in Cloud first.
  - cloud
# A client that wants the watch gone when it stops renewing adds a lease,
# from 15s to 10m:
# lease: 60s
```

`Examples/WendyDataWatch/front-door-clips.collection.yaml`:

```yaml
# Keeping what the inference sees is a collection that triggers on its
# event. A watch never stores anything itself.
kind: collection
version: 1
name: front-door-clips
sources:
  - camera: "*"
    capture:
      mode: continuous
capture:
  buffer: 5s
  after_trigger: 10s
  triggers:
    - event: front-door
upload:
  when: wifi
retention:
  local_quota: 2GiB
export:
  annotation: cvat
```

`Examples/WendyDataWatch/README.md`:

```markdown
# Watch a camera with data documents

Three single-purpose documents:

- `front-door.inference.yaml` runs a person detector on every camera and
  emits `front-door` when someone arrives.
- `front-door.watch.yaml` sends each arrival to Wendy Cloud.
- `front-door-clips.collection.yaml` records an episode around each arrival.
  Leave it out to keep nothing.

Apply them with `wendy data apply <file>`, check them with
`wendy data documents`, and remove the inference and watch with
`wendy data remove inference/front-door` and `wendy data remove
watch/front-door`.
```

- [ ] **Step 3: Document data documents**

In `data.md`, insert a new section directly before `## Campaign YAML reference`:

````markdown
## Data documents

A data document is one YAML file with one purpose. Every document starts
with the same header:

```yaml
kind: watch          # collection, inference or watch; no kind means collection
version: 1
name: front-door
owner: watch/chat-3fa91c0e-1   # optional
```

- A **collection** is a campaign: everything in the campaign reference below.
  A campaign file with no `kind` is a collection.
- An **inference** runs a detector on cameras and emits an event when
  something it looks for arrives. Its model fields are a campaign's
  `inference` block; it adds `sources` (cameras only), and its `event`
  defaults to its name.
- A **watch** delivers one inference's arrivals as notifications: always to
  the device's notification journal, and to Wendy Cloud (`- cloud`) or a
  webhook (`- webhook: <url>`) when listed. A watch with `lease` (15s to 10m)
  is removed, with what it owns, when its client stops renewing it, and at
  agent restart.

`owner` names another document as `<kind>/<name>`; removing the owner removes
what it owns. The owner must exist first, and cannot itself have an owner.

To keep what an inference sees, apply a collection whose
`capture.triggers` include the inference's event. An inference's records
reach only collections that trigger on its event. A watch stores nothing.

| Command | Description |
|---|---|
| `wendy data apply [file.yaml]` | Validate and apply any data document. Without a path, offers the documents in the current directory. |
| `wendy data documents [<kind>[/<name>]]` | List documents with state, owner and lease expiry, or inspect one. |
| `wendy data remove <kind>/<name>` | Remove an inference or watch and what it owns. Collections cannot be removed yet. |

A watch's state is `waiting` (its inference is missing or starting),
`active` or `error`. Applying a watch with `cloud` registers the Cloud app
`watch:<name>`; an owner or admin enables its notifications in Cloud.
Applying inference or watch documents needs an agent with data document
support; `wendy device update` brings one. Complete examples are in
`Examples/WendyDataWatch`.

````

In the `### Notifications` section, after the paragraph about `detections` (from PR 1), add:

```markdown
A watch's journal entries carry `watch` and `inference` instead of
`campaign`. Entries of a leased watch reach only readers that name the watch.
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd go && go test ./internal/agent/data/ -run TestWatchExamplesParse -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add Examples/WendyDataWatch go/internal/agent/data/example_documents_test.go go/internal/cli/assets/docs/clients/wendy-cli/commands/data.md
git commit -m "docs(data): data documents, with inference, watch and collection examples"
```

---

### Task 10: Verify locally and on the Orin Nano

Controller-run. Steps 3–4 need Ethan to walk in front of the camera; ask him at that point.

**Files:** none unless a check fails.

- [ ] **Step 1: Local verification**

```bash
cd go
gofmt -l .
go vet ./internal/shared/datadoc/ ./internal/agent/data/ ./internal/agent/services/ ./internal/cli/commands/
go test ./internal/shared/datadoc/ ./internal/agent/... ./internal/cli/commands/ ./internal/cli/mcp/
go test -race ./internal/agent/services/ -run 'Document|Watch|Lease|BootSweep|InferenceDocument'
go build ./...
GOOS=linux GOARCH=arm64 go build -o /dev/null ./cmd/wendy-agent/
```

Expected: `gofmt -l` prints nothing; everything else passes. Compare any failure outside the touched packages against `origin/main` in a scratch worktree before blaming this branch.

- [ ] **Step 2: Side-load the agent and CLI**

On `hopeful-glider` (Orin Nano, Logitech Brio attached): record the agent version with `wendy --json device info --device <dev>`; before updating, deploy `Examples/WendyDataPeople/campaign.yaml` with the old agent and note its revision from `wendy data campaign list`. Build `GOOS=linux GOARCH=arm64 go build -o "$SCRATCH/wendy-agent" ./cmd/wendy-agent` and `go build -o "$SCRATCH/wendy" ./cmd/wendy`, then `"$SCRATCH/wendy" device update --device <dev> --binary "$SCRATCH/wendy-agent"`. Check `wendy data campaign list` shows the same revision for the campaign.

- [ ] **Step 3: A person-deployed inference, watch and collection**

Point `Examples/WendyDataWatch/front-door.inference.yaml` at the Brio's camera id (from `wendy data sources`), and use a watch without `cloud` unless the device is enrolled with the grant enabled. Apply the inference, the watch and the collection. Check:

1. `wendy data documents` shows the inference `running` and the watch `active` (the first start installs the Python runtime, minutes on a fresh device).
2. Ethan walks into view: within 3 s the journal has a watch entry with `watch`, `inference` and `detections` (`wendy device shell --device <dev> -- sudo cat /var/lib/wendy-agent/data/episodes/.data-notifications.json`).
3. `wendy data episodes` shows a `front-door-clips` episode; its `events.jsonl` (`wendy data inspect`/`download`) holds `sh.wendy.inference.front-door` prediction records. Remove the `people-all-cameras` campaign's influence by checking that its episodes do not hold them.

- [ ] **Step 4: Leases, removal and restart**

1. Apply `kind: watch`, `name: lease-test`, `lease: 15s`, `on: {inference: lease-test}`, then an inference `lease-test` with `owner: watch/lease-test` on the Brio. Do not renew. Within 25 s `wendy data documents` lists neither, and the agent log (`wendy device logs` or `journalctl -u wendy-agent`) shows the job stopping.
2. Apply the same pair with `lease: 10m`; `wendy data remove watch/lease-test` prints `Removed inference/lease-test` then `Removed watch/lease-test`, and returns after the job stopped.
3. Apply the pair again with `lease: 10m`; restart the agent (`wendy device shell --device <dev> -- sudo systemctl restart wendy-agent`); after it comes back, neither document exists and the Brio was not opened for them (log).
4. With the release CLI (not `$SCRATCH/wendy`), `wendy data campaign deploy` of a campaign still works against the new agent.

Record each timing in the ledger.

- [ ] **Step 5: Restore the device**

Remove the documents and the test campaign, put back the recorded agent version from its release binary (`gh release download <version> -R wendylabsinc/WendyOS -p 'wendy-agent-linux-arm64-<version>.tar.gz'` into an empty scratch directory, extract, `wendy device update --device <dev> --binary <path>`), remove `/var/lib/wendy-agent/data/inference` if this test created it, and delete the scratch builds.

- [ ] **Step 6: Push and open the PR**

```bash
git push -u origin ed/data-documents-agent
gh pr create --draft --base ed/datadoc --head ed/data-documents-agent \
  --title "feat(agent,cli): inference and watch data documents" \
  --body-file <body written from the template below>
```

The body: what it adds (kinds, store, journal, records for chosen collections, inference documents, watch runner with leases, four calls, three CLI commands, docs and examples), that it is stacked on #2259 (PR 3) with #2258 (PR 1) merged in until both land, the on-device results from Steps 3–4, and the PR-body attribution line. Draft until PR 1 and PR 3 merge and the spec (#2257) is approved.
