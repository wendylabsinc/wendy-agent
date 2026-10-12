# Data documents: one envelope for collection, inference and watch

**Date:** 2026-10-12
**Branch:** `ed/data-documents`, based on `main` at `fbcbb402a`
**Owner:** Ethan (P-WDY-258 "Wendy Chat: Let LLM spawn 'any model'")
**Reviewers:** Martien (Wendy Data, campaigns), Joannis (campaign inference, notifications)
**Status:** first draft for review. §2 marks the decisions that were open questions; each has a default so the draft is complete, and any of them can change in review.

**Relation to earlier work:**

- Replaces §5 of the chat-watches design (leased campaigns, PR #2206) and §6.3 (its campaign backend). §6 and §7 of that design (the MCP watch manager and chat) stay; this spec says where they change. That design lives on branch `ed/chat-watches-leased-campaigns` as `specs/2026-10-05-chat-watches-design.md`.
- Replaces the `WendyModelService` control plane from the model watch design (PR #2063, `specs/2026-09-25-model-watch-design.md` on `ed/model-watch-design`). Its execution layer becomes an inference backend (D12).
- Follows the direction Martien proposed and Joannis agreed to in Slack on 2026-10-09/12.

## 1. Summary

Campaigns began as plans for collecting and storing data. Since then, inference (#1890), notifications (WDY-2939) and, in #2206, a client lease have each been added to the campaign object. Each addition made sense on its own; together they make one object responsible for four topics.

This spec pulls them apart into single-purpose **data documents**. Each document is one YAML file with a shared header (`kind`, `version`, `name`, `owner`), parsed by one shared library. The CLI and agent dispatch on `kind`, and each device subsystem reads only its own kind:

- `collection` is today's campaign, unchanged. A file without `kind` is a collection, so every existing `campaign.yaml` still deploys.
- `inference` is today's `inference:` block in its own file: a model, the cameras it watches, and the event it emits.
- `watch` receives one inference's events and delivers them as notifications. It may carry a lease, which the device enforces.

A new device process becomes a new kind, not another field and state on campaigns.

> "Tell me when someone comes to the door." The chat model starts a watch. Behind the tool call, the MCP server deploys a leased watch and an inference the watch owns. When someone arrives, the chat says so. If chat dies, the lease lapses and the device removes both, freeing the camera.
>
> A person who wants those arrivals kept deploys a collection triggered by the inference's event. A person who wants a phone notification for every arrival, with or without chat, deploys a watch without a lease that notifies through Wendy Cloud.

## 2. Decisions

| # | Decision | Origin |
|---|---|---|
| D1 | Three kinds: `collection`, `inference`, `watch`. One document per file. The header is `kind`, `version`, `name` and optional `owner`. A missing `kind` means `collection`. | Design review |
| D2 | One shared library parses every document in two passes, header then body. Each kind registers a strict decoder, its validation and its revision digest. The CLI and the agent use the same library. | Design review |
| D3 | Ownership is one level deep. An owner must exist when the owned document is deployed, and cannot itself be owned. Removing an owner removes what it owns. | Design review |
| D4 | A watch delivers one inference's events as notifications, and does nothing else. It has no filters in v1. Its `lease` is optional. Storage is a collection, never a watch field. | Design review |
| D5 | The device enforces leases. Deadlines live in memory, so at boot the device removes every leased watch and what it owns. | Carried over from chat-watches D2 |
| D6 | Chat watches are a leased watch plus an inference it owns, with journal notifications only. Chat never deploys a collection and never sets `cloud` or `webhook`. | Design review |
| D7 | `DataService` gains generic calls (`DocumentApply`, `DocumentRemove`, `DocumentRenew`, `Documents`) instead of calls per kind. A new kind needs no proto change. | **Default; open in review** |
| D8 | Collections are unchanged in v1. A file without `kind` keeps exactly today's revision. | Design review |
| D9 | Project files may live anywhere. The convention is one file per document, named `<name>.<kind>.yaml`, next to `wendy.json`. Deploying with no path offers only files whose header parses. | **Default; open in review** |
| D10 | When collections next need a new version, v2 drops the inline `inference:` and `notify:` blocks. v1 stays supported. Docs and examples move to inference and watch documents now. | **Default; open in review** |
| D11 | An inference document writes a `prediction` record per scored frame, with `inputs` naming the frame, and an `event` record per arrival. Both go only into episodes of collections that trigger on its event. Notifications never wait on storage. | **Default; open in review** |
| D12 | GPU inference comes from the model-watch execution layer (host, catalog, file cache, lockdown) as an inference `backend: model_host`. The `WendyModelService` control plane (`StartModel`, `WatchModel`, its leases) is retired. #2063 closes, and the execution layer returns in its own PR with its own spec. | **Default; open in review** |
| D13 | The MCP server offers a vetted list of detectors. v1 holds D-FINE nano. An entry needs a pinned revision, a licence that allows commercial use, safetensors weights, no remote code and an on-device benchmark. Open-vocabulary detectors wait for `model_host`. | **Default; open in review** |
| D14 | Chat cannot turn on storage. A later design for that needs both a user setting and approval text that says what is kept and where it goes. | **Default; open in review** |

## 3. Goals and non-goals

**Goals**

- Every document has one purpose, and every device subsystem reads only its own kind.
- One parser, used by the CLI and the agent, so the two cannot disagree about a document.
- Existing campaign files, examples, the gateway's generated campaigns and the campaigns already on devices keep working with the same revisions.
- Chat watches meet the success criteria of chat-watches §3: alert within 3 s, at least 1.5 frames per second per watch with two watches on the Orin Nano CPU, `READY` within 30 s warm, exactly two event turns for the scripted scene, removal within 90 s after `kill -9` of chat and within 5 s after a normal exit, and an "update the agent" message on older agents.
- Storing what a watch sees is an option a person turns on, by adding a collection, without changing the watch.

**Non-goals**

- Removing collections. There is no removal for campaigns today; it stays a separate feature.
- Sharing one inference between several owners. Several watches may reference one inference, but only one may own it.
- A device-wide limit on inference jobs. The MCP server's limit of two watches per session stays.
- Files with several documents, and bundles deployed as one unit.
- Moving the gateway's `deploy_yolo_detector` onto documents. It keeps generating campaigns (chat-watches D8).
- Designing collection v2, and designing `model_host`. Each needs its own spec.
- Watches that wake an agent when no chat is open.

## 4. Architecture

```
project files                     wendy CLI                          wendy-agent
  front-door.inference.yaml ─┐      shared parser                      DataService
  front-door.watch.yaml     ─┼────▶ dispatch by kind ──── gRPC ─────▶   DocumentApply / Remove / Renew / Documents
  campaign.yaml (no kind)   ─┘      Cloud registration                  shared parser, then the kind's runner:
                                                                          ├─ collection ─▶ campaign manager (unchanged)
wendy chat ── stdio MCP ──▶ wendy mcp serve                               ├─ inference ──▶ inference runner ──▶ worker.py
                              watch_* tools, document backend ── gRPC ─▶  └─ watch ──────▶ watch runner (leases)
                                                                                             ├─ notification journal
                                                                                             └─ Cloud / webhook delivery
```

Events from an inference document take two paths:

1. directly to the watch runner, for each watch whose `on.inference` names it;
2. to the device's record observer as an `event` record, where collections match `capture.triggers: - event:` by name, as they already do for application events.

## 5. The envelope

### 5.1 Header

```yaml
kind: watch                       # collection | inference | watch; absent means collection
version: 1                        # that kind's schema version
name: front-door
owner: watch/chat-3fa91c0e-1      # optional
```

| Field | Rules |
|---|---|
| `kind` | One of the registered kinds. Absent means `collection`. |
| `version` | Required. Each kind checks its own supported versions, with today's campaign wording: "version is required; this agent supports watch version 1". |
| `name` | Required. Letters, numbers, `.`, `-` and `_`, at most 128 bytes: today's campaign name rule. Unique per kind on a device, so a watch and an inference may share a name. |
| `owner` | Optional. `<kind>/<name>` of an inference or watch document. That document must exist when this one is deployed, and must have no owner of its own. A redeploy may not change `owner`, add one or drop one. Collections can neither own nor be owned in v1, because they cannot be removed. |

### 5.2 Parsing

The library parses a document in two passes:

1. Decode only the header fields, ignoring the rest, and check `kind`, `version`, `name` and `owner`. Errors at this stage name the field, so a document with a misspelled `kind` gets "unknown kind 'wach'", not a body error.
2. Decode the whole document with the kind's own decoder, with unknown fields rejected (`KnownFields(true)`), and require a clean end of input after one document. These are the rules `ParseCampaign` already applies.

For a collection, the second pass is `ParseCampaign` itself, so collections are validated exactly as today. `ParseCampaign` learns to accept `kind: collection` and nothing else from the header, and does not store it in the campaign plan.

### 5.3 Revisions

Each kind defines the exact fields its revision digest covers, the way `planDigestInput` does for campaigns: what the author wrote, enumerated, nothing else.

- For a collection, the digest is today's, field for field. `kind` and the other header fields are not in it, so writing `kind: collection` into an existing campaign file does not change its revision.
- For inference and watch documents, the digest covers `version`, `name`, `owner` and the author-written body fields. Each kind keeps its own revision schema number.

### 5.4 Library

The library lives in `go/internal/shared/datadoc`. It holds the header, the registry and the two-pass parser, and nothing kind-specific. The kinds themselves stay in `go/internal/agent/data`, which the CLI already imports, and register there. Indicatively:

```go
package datadoc

type Header struct {
	Kind    string
	Version int
	Name    string
	Owner   string // "<kind>/<name>" or empty
}

type Document struct {
	Header   Header
	Body     any    // the kind's own type, e.g. data.Watch
	Revision string
	Warnings []string
}

type Kind struct {
	Name     string
	Versions []int
	// Decode parses the whole document strictly and validates it.
	Decode func(header Header, contents []byte) (body any, revision string, warnings []string, err error)
}

func Register(kind Kind)
func Parse(contents []byte) (Document, error)
```

### 5.5 Files in a project

Documents may live anywhere. The convention, used by docs and examples, is one file per document named `<name>.<kind>.yaml`, next to `wendy.json`:

```
my-robot/
  wendy.json
  front-door.inference.yaml
  front-door.watch.yaml
  forklift-failures.collection.yaml
```

The name is a convention, not a rule; the header decides the kind. `wendy data apply` without a path offers the YAML files in the current directory whose header parses. Today the campaign picker offers every `.yaml` file there, including a stagefile or a compose file.

### 5.6 On the device

Collections stay in `data/campaigns/<name>.json`, so nothing on existing devices moves. Inference and watch documents are stored as JSON in `data/documents/<kind>/<name>.json`, beside it, with the same atomic writes. (`data/inference/` already holds the Python runtime.) With the default data directory these are `/var/lib/wendy-agent/data/campaigns/` and `/var/lib/wendy-agent/data/documents/`.

## 6. The `watch` kind

### 6.1 Schema

```yaml
kind: watch
version: 1
name: front-door
lease: 60s
on:
  inference: front-door
notify:
  - cloud
  - webhook: https://example.com/hooks/front-door
```

| Field | Rules |
|---|---|
| `lease` | Optional. A duration from 15 s to 10 min. A watch with `lease` is a *leased* watch. |
| `on.inference` | Required. The name of an inference document. It need not exist yet. |
| `notify` | Optional list. `cloud` at most once; `webhook` at most once, under today's campaign webhook rules (http or https, a host, no user info or fragment, at most 2048 bytes). The journal is always written and is not listed. |

Several watches may reference one inference.

### 6.2 Behaviour

- **Waiting.** A watch whose inference does not exist, or exists but has not started, reports `waiting`. It starts receiving events once the inference runs. Removing the inference returns the watch to `waiting`.
- **Events.** A watch receives each arrival its inference emits. Arrival, `clear_after` and `cooldown` are the inference's, as today: an event fires when a camera goes from no matching detection to one, at most once per cooldown, and clearing emits nothing.
- **Notifications.** Each event becomes one journal entry (§6.4). `cloud` and `webhook` deliveries go through today's campaign notification sender with its retries.

### 6.3 Lease

- **Deploy** sets the deadline to now + `lease`. Redeploying the same leased watch resets it.
- **`DocumentRenew`** pushes the deadline to now + `lease` and returns it.
- **Leased stays leased.** Adding or dropping `lease` on a redeploy is refused with FAILED_PRECONDITION.
- **Expiry.** The watch runner checks deadlines every 5 s and removes expired watches through the same path as `DocumentRemove` (§9.3), so what the watch owns is removed with it.
- **Before every notification**, the runner checks the deadline. An expired watch writes nothing, even before the 5 s pass removes it. This closes the window Martien found in #2206, where for up to 5 s a lapsed lease still produced journal entries and device events.
- **Agent restart.** Deadlines live in memory. At boot, before the runners start, the device removes every leased watch and what it owns (§9.3). A client's next renewal gets NOT_FOUND.
- A watch without `lease` stays until a person removes it.

### 6.4 Journal entries

A watch's journal entry extends today's `CampaignNotification`:

```json
{
  "id": "…", "sequence": 42,
  "event": "front-door",
  "watch": "front-door",
  "inference": "front-door",
  "source_id": "v4l2:/dev/video0",
  "model": "ustc-community/dfine-nano-coco",
  "model_revision": "066438d3d8f0da137a37b38fdf3368fd4afceced",
  "count": 1,
  "detections": [{"label": "person", "score": 0.91}],
  "occurred_at": "2026-10-12T09:41:07.123Z"
}
```

- `watch` and `inference` are new, and `campaign` is absent. A campaign's own `notify:` entries are unchanged.
- `detections` holds the top five detections by score that passed the inference's label and threshold filter, which keeps an entry well under the journal's 4096-byte cap. Campaign entries carry it too (from #2206). Older readers ignore it.

### 6.5 Reading events, and isolation

`DataEventsRequest` gains `repeated string watches = 6`.

- An entry from a **leased** watch is returned only to a request that names that watch in `watches`. Unfiltered reads, such as the robot gateway's, skip it.
- Entries from watches without a lease, and from campaigns, are returned as today.
- `gap` stays journal-wide. The journal keeps one sequence for every writer, so when entries are evicted before a reader catches up, the device cannot tell whose they were. A reader that names its watches may therefore see a gap caused by another writer's burst. A false "may have missed detections" is preferable to a silent miss.

The device cannot tell chat sessions apart; one operator certificate covers them all. Isolation between sessions is therefore two layers: the device keeps leased entries out of unfiltered reads, and the MCP server names only watches its own session created. The first closes the leak Martien found in the gateway's read on #2207. Neither makes the device a security boundary between sessions of the same operator.

### 6.6 Status

`Documents` reports, for each watch: `state`, a `state_detail`, `expires_unix_nanos` when leased, and the state of the inference it names. The MCP server therefore needs one call to tell `PREPARING` from `READY`.

| Watch state | When |
|---|---|
| `waiting` | Its inference does not exist, or is pending or loading. |
| `active` | Its inference is running. |
| `error` | Its inference reports an error. The inference runner retries, so this is not terminal. |

## 7. The `inference` kind

### 7.1 Schema

```yaml
kind: inference
version: 1
name: front-door
owner: watch/front-door
sources:
  - camera: v4l2:/dev/video0
model: ustc-community/dfine-nano-coco
revision: 066438d3d8f0da137a37b38fdf3368fd4afceced
labels: [person]
threshold: 0.5
rate: 2
event: front-door
clear_after: 5s
cooldown: 30s
```

- `sources` uses the campaign source syntax and selector rules, including `camera: "*"`. Cameras only, as today: the worker decodes the agent's own encoded camera streams, so ordinary USB webcams work. Any other source kind, or a per-source `capture`, is refused.
- `backend`, `model_file`, `model`, `revision`, `labels`, `threshold`, `rate`, `clear_after`, `cooldown` and `enabled` are today's `CampaignInference` fields, with the same Go type and validation, shared with a campaign's inline block. The model must be pinned to a 40-character commit; the worker loads safetensors only, with `trust_remote_code` off.
- `event` is optional and defaults to the document's `name`.

### 7.2 Lifetime

An inference runs while it is deployed and `enabled`. An owned inference is removed with its owner. Redeploying with a new revision restarts its job, as for campaign inference today.

### 7.3 Events and records

- **Event names are device-wide**, like application event names today. An inference document publishes each arrival to the device's record observer under its `event` name, so a collection with `capture.triggers: - event: front-door` starts an episode on it, with no change to the collection format. A campaign's inline inference keeps bypassing the observer, as today.
- **Records (D11).** For each scored frame, the inference writes a `prediction` record whose `inputs` name the frame (`source_id`, `sample_id`), and for each arrival an `event` record. Both go only into open episodes of collections whose triggers name its event. Today every open episode receives every campaign's predictions; that leak was plugged for leased campaigns in #2206 and is not repeated here. Writing predictions, not only events, puts the frame link into the episode's model-input ledger, which pairs only `prediction` records; that answers Martien's note on #2063.
- **Notifications do not wait on storage.** A watch is notified whether or not an episode was open or a record write succeeded. Live alerting and storage are separate paths; the episode is authoritative for what was kept. This makes explicit the ordering Martien questioned on #2063.
- The device-event record written for each detection notification today is written for each inference arrival.

### 7.4 Backends

`backend` selects the executor: `transformers` (the default) and `yolo_onnx` run on the CPU through `worker.py`, as today. D12 adds `model_host` later, run by the model-watch execution layer on the GPU, with `model` naming a catalog entry. A `model_host` backend must write `prediction` records with `inputs`, as above, so the device-to-training link holds on every backend.

## 8. The `collection` kind

A collection is a version 1 campaign: today's format, validation, revision, storage, Cloud registration and behaviour, with `kind` absent or `collection`. Its inline `inference:` and `notify:` blocks stay supported.

Under D10, docs and examples move to separate inference and watch documents, and the inline blocks are left out of collection v2 when that version is next needed. Nothing in this spec deprecates or removes them from v1.

## 9. Device runtime

### 9.1 Parts

- **Document store** (`go/internal/agent/data/documents.go`): reads and writes `data/documents/<kind>/<name>.json` atomically, and lists by kind. Collections stay with the campaign manager.
- **Inference runner**: today's campaign inference loop (`data_inference.go`), widened. A job is wanted for each armed campaign with enabled inline inference or `notify.on: event`, as today, and for each enabled inference document. Job code (`supervise`, `run`, `stream`) is shared.
- **Watch runner** (`data_watch.go`): holds lease deadlines, receives arrivals from the inference runner, checks the lease, writes journal entries and hands `cloud` and `webhook` deliveries to the existing sender. Its lease table and expiry logic come from #2206 (`data_campaign_lease.go`).

### 9.2 Calls

```proto
service DataService {
  // existing calls unchanged

  // Validates and stores a document of any kind, then hands it to its runner.
  // A collection takes the same path as CampaignDeploy.
  rpc DocumentApply(DataDocumentApplyRequest) returns (DataDocumentApplyResponse);
  // Removes an inference or watch document and everything it owns, owned
  // documents first. Returns once their jobs have stopped.
  rpc DocumentRemove(DataDocumentRemoveRequest) returns (DataDocumentRemoveResponse);
  // Pushes a leased document's deadline to now + lease.
  rpc DocumentRenew(DataDocumentRenewRequest) returns (DataDocumentRenewResponse);
  // Lists documents with their status; kind and name narrow it.
  rpc Documents(DataDocumentsRequest) returns (DataDocumentsResponse);
}

message DataDocumentApplyRequest { bytes document_yaml = 1; }
message DataDocumentApplyResponse {
  DataDocument document = 1;
}
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
  string state = 6;                   // per kind; see §6.6 and the campaign states
  string state_detail = 7;
  int64 deployed_unix_nanos = 8;
  int64 expires_unix_nanos = 9;       // leased documents only
  string document_json = 10;          // the stored document
  repeated string warnings = 11;
  string status_json = 12;            // kind-specific status, e.g. an inference's per-source states
}
```

The calls are served wherever `DataService` is today: the mTLS server and the local admin socket, not the provisioning listener.

### 9.3 Lifecycle

- **Deploy order.** `DocumentApply` of an owned document whose owner does not exist is refused with FAILED_PRECONDITION. A client deploying a watch and the inference it owns therefore deploys the watch first. If the client dies between the two calls, the watch's lease removes it, and nothing else was deployed.
- **Removal.** `DocumentRemove` stops and deletes owned documents first, then the owner. It returns once the removed inference jobs have stopped, waiting at most 10 s. Expiry runs the same path. A crash part-way leaves the owner, or the owner and some owned documents, and removing again finishes the job.
- **Boot sweep.** Before the inference and watch runners start, the device deletes every leased watch, everything those watches own, and every document whose owner is missing.
- **Locking.** `DocumentRenew` and the per-notification lease check take only the lease table's lock, never the data service's deployment lock, which capture start can hold for seconds (#2206, #2207). `DocumentApply` and `DocumentRemove` take the deployment lock, as campaign deploy does.

### 9.4 Errors

| Case | Response |
|---|---|
| Header or body fails to parse or validate | INVALID_ARGUMENT, naming the field |
| Owner missing, owner has an owner, `owner` or `lease` changed on redeploy | FAILED_PRECONDITION |
| `DocumentRemove` of a collection | FAILED_PRECONDITION ("collections cannot be removed yet") |
| `DocumentRemove` or `DocumentRenew` of a missing or expired document | NOT_FOUND |
| `DocumentRenew` of a document without a lease | FAILED_PRECONDITION |
| Removal exceeds 10 s | DEADLINE_EXCEEDED; the documents are deleted and the job stops in the background |
| An agent without these calls | UNIMPLEMENTED, which the CLI and MCP server report as "This device's agent is too old for watches; update it with `wendy device update`." |

An older agent that receives a `kind` header through `CampaignDeploy` rejects it as an unknown field, so a new CLI never sends inference or watch documents through the old call. `wendy data apply` of a collection calls `DocumentApply` and falls back to `CampaignDeploy` on UNIMPLEMENTED; `wendy data campaign deploy` keeps calling `CampaignDeploy`. Either way, a file without `kind` deploys to an older agent, and a file with `kind: collection` fails there with the update message.

## 10. CLI

`wendy data` gains three commands. `wendy data campaign …` stays as it is, except that `campaign deploy` parses through the shared library and refuses another kind with "this is a watch document; deploy it with `wendy data apply`".

| Command | Does |
|---|---|
| `wendy data apply [file]` | Parses the document, runs its kind's client-side steps, and calls `DocumentApply`. Without a path, offers the YAML files in the current directory whose header parses. |
| `wendy data documents [<kind>[/<name>]]` | Lists documents of every kind, or one kind, or inspects one. |
| `wendy data remove <kind>/<name>` | Calls `DocumentRemove` and lists what was removed. |

(`wendy data inspect` already means episodes, so inspection lives under `documents`.)

**Client-side steps by kind.** A collection registers `campaign:<name>` with Cloud, as campaign deploy does today. A watch with `cloud` registers `watch:<name>`. Inference documents need no registration. `--skip-cloud-registration` applies to both.

## 11. MCP and chat

### 11.1 Document backend

The watch manager, the five `watch_*` tools and their notifications (chat-watches §6.1, §6.2, §6.5, §6.6) keep their interface. A document backend replaces the campaign backend of chat-watches §6.3:

- **Start.** Applies a leased watch (`lease: 60s`, `on.inference: <name>`, no `notify`), then an inference it owns, with the watch's classes as `labels`, `min_confidence` as `threshold` and the camera as `sources`. Both are named `chat-<server id>-<n>`, where the server id is 8 random bytes in hex (Martien, #2207). It renews every 20 s.
- **Status.** Polls `Documents` for the watch every 5 s and maps the inference's state as chat-watches §6.3 does. A renewal that returns NOT_FOUND ends the watch with "the device restarted or the watch expired".
- **Events.** One poller per server reads `Events` with `notifications_only`, `replay` and `watches` set to this server's active watches, every second while any is active. Its first cursor comes from a call made before the first watch is applied, as today. Each response is capped at 4 MiB and 512 entries, like the gateway's reader (Martien, #2207).
- **Stop.** `DocumentRemove` of the watch, which removes its inference.

### 11.2 Detector list

`watch_detector.go` becomes a vetted list. `watch_sources` returns each entry with its labels, and `watch_start` picks the first entry whose labels cover the requested classes.

An entry needs, recorded beside it: a Hugging Face repository pinned to a commit; a licence that allows commercial use; safetensors weights loadable by the Transformers backend without remote code; its labels as read from that commit's `id2label`; and an on-device result of at least 1.5 frames per second per watch, with two watches, on the Orin Nano CPU at the worker's default 2 threads.

v1 holds `ustc-community/dfine-nano-coco` at `066438d3d8f0da137a37b38fdf3368fd4afceced` (Apache-2.0; COCO's 80 classes; 1.65–1.74 frames per second each with two watches). Open-vocabulary detectors are too heavy for that budget on the CPU (RT-DETR r18 alone ran at 0.44) and wait for `model_host`. A user-named model checked against Hugging Face's metadata, instead of a list, is a possible later step and is not in this spec.

### 11.3 Chat

Chat is unchanged from chat-watches §7 and PR #2210, with two fixes from review: the classes in an event turn's header go through `jsonQuote` like the other fields, and a test asserts that every MCP tool that changes device state requires approval, since event turns run only tools that don't.

### 11.4 Robot gateway

`deploy_yolo_detector` keeps generating campaigns. Its notification read gains nothing; leased watch entries no longer reach it (§6.5).

## 12. Security and privacy

- Chat watches store nothing and send nothing off the device beyond the chat itself. Storage needs a collection, and only a person deploys one (D14).
- A person-deployed watch with `cloud` or `webhook` sends what campaign notifications send today, plus `detections` (labels and scores, no images).
- The device keeps leased watch entries out of unfiltered journal reads; the MCP server names only its own session's watches (§6.5).
- Model validation is unchanged: a pinned commit, safetensors only, no remote code.
- The new calls are served only where `DataService` is today.
- Ownership grants nothing: an owner and what it owns are deployed by the same operator, and removal only ever removes.

## 13. Testing and validation

**Unit**

- Library: header errors name the field; unknown kind; unsupported version per kind; unknown body fields rejected; a second document rejected; a campaign file without `kind`, and with `kind: collection`, has exactly today's revision (golden test against `main`).
- Inference documents: shared validation with the inline block; `event` defaults to the name; records go only into episodes of collections triggered by the event; events reach the record observer.
- Watch runner, with a fake clock: lease expiry removes the watch and its inference; no journal entry after the deadline, before the 5 s pass; renew; adding or dropping `lease` refused; `waiting` until the inference runs and after it is removed.
- Lifecycle: owner must exist; owned-by-owned refused; removal order; boot sweep removes leased watches, their documents and orphans.
- `Events`: leased entries only when named; unleased and campaign entries as today; caps.
- MCP: the document backend against a fake `DataService`, including old-agent UNIMPLEMENTED; the isolation test uses a different label for the entry that must not leak (Martien, #2207).

**On device** (Orin Nano with the Brio, as for #2206 and #2207)

- The chat-watches §3 criteria (§3 above).
- A person-deployed collection triggered by an inference document's event records an episode whose model-input ledger links the predictions to frames.
- A watch without a lease, with `cloud`, delivers through Wendy Cloud.
- `kill -9` of chat: watch and inference gone within lease + 5 s. Agent restart: both gone at boot.
- An existing campaign deployed before the upgrade keeps its revision and keeps running.

## 14. Delivery

| # | PR | Contents |
|---|---|---|
| 1 | New, small | From #2206: the worker survives a failing frame, counting failures over a rolling window instead of resetting on any success (Martien, #2206); journal entries carry `detections`. Useful on `main` without anything else here. |
| 2 | This spec | Draft PR for review. |
| 3 | Library | `datadoc`, with `collection` registered. `wendy data campaign deploy` and its picker parse through it. No agent or proto change. |
| 4 | Agent and CLI | Inference and watch kinds, document store, runners, the four calls, `Events.watches`, and `wendy data apply/documents/remove`. Built from #2206's lease code. |
| 5 | #2207, rebased onto 4 | The document backend, the detector list, and the #2207 review fixes. |
| 6 | #2210, rebased onto 5 | The two fixes in §11.3. |

Existing PRs:

- **#2206** closes, pointing here. Its general parts are PR 1; its lease code moves into PR 4.
- **#2207 and #2210** stay open and are rebased, so their review threads stay attached.
- **#2063** closes, pointing here. The execution layer comes back in its own PR with the `model_host` spec (D12), including Martien's two #2063 findings: check a cached TensorRT engine's provenance record (`from`, `gpu_arch`, `tensorrt_version`) before loading it, or build into a per-instance folder and promote it once checked; and release the device-wide build slot when a host crash-loops during a build.
- **Model watch PR C** (the model-watch backend for the watch manager) is dropped: `model_host` makes it one more inference backend.

## 15. Risks and open items

- **Cloud registration of `watch:<name>`.** Cloud registers `campaign:<name>` today. It needs to accept the new prefix, or watches register as something it already accepts. To confirm with Joannis before PR 4.
- **Event name collisions.** Event names are device-wide, so a person's inference named like an application event triggers the same collections. That is today's behaviour for application events; docs say so.
- **One journal for every writer.** A burst from one writer can evict another's entries before they are read; readers see a gap (§6.5). Unchanged from today.
- **CPU budget.** Two chat watches run two detectors, as before. Sharing an inference between owners would halve that and is the first follow-up if the budget is tight.
- **Timing.** The format is the part to agree on first. PR 1 and PR 3 can land while PR 4 is reviewed.

## 16. Files (indicative)

| Area | Files |
|---|---|
| Library | `go/internal/shared/datadoc/{datadoc.go,datadoc_test.go}` |
| Kinds | `go/internal/agent/data/{documents.go,inference_document.go,watch.go}`, registration beside `campaign.go` |
| Agent | `go/internal/agent/services/{data_documents.go,data_watch.go}`, `data_inference.go`, `data_service.go`, `notifications.go` |
| Proto | `Proto/wendy/agent/services/v2/data_service.proto` |
| CLI | `go/internal/cli/commands/data_documents.go` |
| MCP | `go/internal/cli/mcp/{watch_documents.go,watch_detector.go}` |
| Docs | `go/internal/cli/assets/docs/clients/wendy-cli/commands/data.md`, a new data documents page, `Examples/` |
