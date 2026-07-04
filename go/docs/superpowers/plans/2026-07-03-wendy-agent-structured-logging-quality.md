# wendy-agent Structured Logging Quality Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the wendy-agent's existing zap→OTel structured logging export faithfully and use consistent field keys.

**Architecture:** The agent already logs via `zap` and bridges every entry to an `otelpb.LogRecord` through `services.TelemetryCore`. Two focused changes: (1) fix `TelemetryCore.fieldToKeyValue` so field types that currently degrade (notably `zap.Time`) export correctly; (2) add a small `logfields` constants package and canonicalize the handful of near-duplicate field keys.

**Tech Stack:** Go, `go.uber.org/zap` / `zapcore`, `otelpb` (generated OTel protobufs), Go standard `testing`.

## Global Constraints

- Module root for all commands: `/Users/joannisorlandos/git/wendy/wendyos-agent-logging/go`.
- Field keys are **snake_case** (already dominant in the codebase).
- Errors are logged with `zap.Error(err)` (key `"error"`) — do not introduce a custom error key.
- Additive changes only to `fieldToKeyValue`: new `case` clauses go **before** the existing `default`; the `default` `fmt.Sprint` fallback stays.
- `zap.Time` timestamps export as **RFC3339Nano strings** (human-readable in `wendy device logs`), not epoch ints.
- Do **not** rewrite all 636 log sites. Only touch the audited near-duplicate keys and files already open for another task.
- Every task ends by running `go build ./...` from the module root and committing.

---

### Task 1: `logfields` constants package

**Files:**
- Create: `internal/agent/logfields/logfields.go`
- Create: `internal/agent/logfields/logfields_test.go`
- Create: `internal/agent/logfields/CONVENTIONS.md`

**Interfaces:**
- Produces: package `logfields` with exported `const` string keys used by Task 3:
  `AppID="app_id"`, `AppName="app_name"`, `ContainerID="container_id"`,
  `ContainerName="container_name"`, `ServiceName="service_name"`, `Image="image"`,
  `Path="path"`, `Device="device"`, `Hostname="hostname"`, `SSID="ssid"`,
  `Reason="reason"`, `Method="method"`, `Serial="serial"`, `Digest="digest"`,
  `Duration="duration"`, `Size="size"`, `ArtifactURL="artifact_url"`,
  `Status="status"`, `Address="address"`, `Output="output"`.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/logfields/logfields_test.go`:

```go
package logfields

import "testing"

func TestKeysAreSnakeCaseAndStable(t *testing.T) {
	cases := map[string]string{
		"AppID":         AppID,
		"AppName":       AppName,
		"ContainerID":   ContainerID,
		"ContainerName": ContainerName,
		"ServiceName":   ServiceName,
	}
	want := map[string]string{
		"AppID":         "app_id",
		"AppName":       "app_name",
		"ContainerID":   "container_id",
		"ContainerName": "container_name",
		"ServiceName":   "service_name",
	}
	for name, got := range cases {
		if got != want[name] {
			t.Errorf("%s = %q, want %q", name, got, want[name])
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-agent-logging/go && go test ./internal/agent/logfields/`
Expected: FAIL — build error, `undefined: AppID` (package/constants do not exist yet).

- [ ] **Step 3: Write the package**

Create `internal/agent/logfields/logfields.go`:

```go
// Package logfields defines the canonical snake_case keys used for structured
// zap log fields in the wendy-agent. Using these constants keeps attribute
// keys consistent across the agent so logs are queryable once exported as OTel
// records via services.TelemetryCore.
//
// Prefer these constants for the common, reused fields. Log errors with
// zap.Error(err) (key "error"). Keys are snake_case; add new constants here
// rather than repeating string literals at call sites.
package logfields

const (
	AppID         = "app_id"
	AppName       = "app_name"
	ContainerID   = "container_id"
	ContainerName = "container_name"
	ServiceName   = "service_name"
	Image         = "image"
	Path          = "path"
	Device        = "device"
	Hostname      = "hostname"
	SSID          = "ssid"
	Reason        = "reason"
	Method        = "method"
	Serial        = "serial"
	Digest        = "digest"
	Duration      = "duration"
	Size          = "size"
	ArtifactURL   = "artifact_url"
	Status        = "status"
	Address       = "address"
	Output        = "output"
)
```

- [ ] **Step 4: Write the conventions doc**

Create `internal/agent/logfields/CONVENTIONS.md`:

```markdown
# wendy-agent logging conventions

The agent logs through `zap` and bridges every entry to an OTel `LogRecord`
(`internal/agent/services/telemetry_core.go`). Follow these rules so exported
logs stay consistent and queryable.

- **Keys are snake_case.** e.g. `app_id`, `container_id`, `artifact_url`.
- **Use the `logfields` constants** for the common, reused fields instead of
  string literals. Add new constants there rather than inventing keys inline.
- **Errors:** log with `zap.Error(err)` — this produces the `error` key.
- **Canonical names for shared concepts:**
  - container identifier → `container_id` (not `container`)
  - container human name → `container_name`
  - app/compose service name → `service_name` (not `service`)
- **Timestamps:** `zap.Time(key, t)` exports as an RFC3339Nano string.
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-agent-logging/go && go test ./internal/agent/logfields/`
Expected: PASS (`ok  .../internal/agent/logfields`).

- [ ] **Step 6: Build and commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-agent-logging/go
go build ./...
git add internal/agent/logfields/
git commit -m "feat(agent): add logfields package for canonical structured-log keys"
```

---

### Task 2: Harden `TelemetryCore.fieldToKeyValue`

**Files:**
- Modify: `internal/agent/services/telemetry_core.go` (function `fieldToKeyValue`, imports)
- Create: `internal/agent/services/telemetry_core_test.go`

**Interfaces:**
- Consumes: existing unexported `fieldToKeyValue(f zapcore.Field) *otelpb.KeyValue` and `stringKV(key, val string) *otelpb.KeyValue` in package `services`.
- Produces: no new exported symbols; behavior change only.

- [ ] **Step 1: Write the failing tests**

Create `internal/agent/services/telemetry_core_test.go`:

```go
package services

import (
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

// fieldToKeyValue is exercised via a real zap.Field so we cover how zap encodes
// each constructor, not just the zapcore.FieldType enum.
func kv(t *testing.T, f zap.Field) *otelpb.KeyValue {
	t.Helper()
	return fieldToKeyValue(zapcore.Field(f))
}

func TestFieldToKeyValue_TimeExportsTimestampNotLocation(t *testing.T) {
	ts := time.Date(2026, 7, 3, 12, 30, 45, 123456789, time.UTC)
	got := kv(t, zap.Time("ts", ts))
	if got == nil {
		t.Fatal("zap.Time produced a nil attribute")
	}
	sv, ok := got.Value.Value.(*otelpb.AnyValue_StringValue)
	if !ok {
		t.Fatalf("zap.Time value type = %T, want string", got.Value.Value)
	}
	if sv.StringValue != ts.Format(time.RFC3339Nano) {
		t.Errorf("zap.Time = %q, want RFC3339Nano %q (must not be a timezone name)",
			sv.StringValue, ts.Format(time.RFC3339Nano))
	}
}

func TestFieldToKeyValue_Primitives(t *testing.T) {
	if v := kv(t, zap.String("k", "v")).Value.GetStringValue(); v != "v" {
		t.Errorf("string = %q", v)
	}
	if v := kv(t, zap.Int("k", 7)).Value.GetIntValue(); v != 7 {
		t.Errorf("int = %d", v)
	}
	if v := kv(t, zap.Bool("k", true)).Value.GetBoolValue(); v != true {
		t.Errorf("bool = %v", v)
	}
	if v := kv(t, zap.Duration("k", 2*time.Second)).Value.GetStringValue(); v != "2s" {
		t.Errorf("duration = %q", v)
	}
}

func TestFieldToKeyValue_AnyStructIsJSON(t *testing.T) {
	type payload struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	got := kv(t, zap.Any("k", payload{Name: "x", Count: 3}))
	if got == nil {
		t.Fatal("zap.Any(struct) produced nil")
	}
	if v := got.Value.GetStringValue(); v != `{"name":"x","count":3}` {
		t.Errorf("zap.Any(struct) = %q, want JSON", v)
	}
}

func TestFieldToKeyValue_NamespaceAndSkipAreDropped(t *testing.T) {
	if got := kv(t, zap.Namespace("ns")); got != nil {
		t.Errorf("zap.Namespace = %+v, want nil", got)
	}
	if got := kv(t, zap.Skip()); got != nil {
		t.Errorf("zap.Skip = %+v, want nil", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-agent-logging/go && go test ./internal/agent/services/ -run TestFieldToKeyValue -v`
Expected: FAIL — `TestFieldToKeyValue_TimeExportsTimestampNotLocation` (exports `"UTC"` not the timestamp) and `TestFieldToKeyValue_AnyStructIsJSON` (exports `fmt.Sprint` of the struct, e.g. `{x 3}`, not JSON); the namespace/skip test may already pass.

- [ ] **Step 3: Add imports**

In `internal/agent/services/telemetry_core.go`, add `"encoding/json"` to the import block (keep it grouped with the other stdlib imports `fmt`, `math`, `os`, `sync`, `time`):

```go
import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"go.uber.org/zap/zapcore"

	"github.com/wendylabsinc/wendy/go/internal/shared/version"
	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)
```

- [ ] **Step 4: Add the new cases**

In `fieldToKeyValue`, insert these cases **immediately before** `case zapcore.StringerType:` (so they sit before the existing Stringer/default fallbacks). Reuse the existing `stringKV` helper for string-valued attributes:

```go
	case zapcore.TimeType:
		// Integer holds the time in UnixNano; Interface holds *time.Location
		// (nil means the field was built without location info). Without this
		// case zap.Time falls through to default and exports the location
		// name (e.g. "UTC") instead of the timestamp.
		ts := time.Unix(0, f.Integer)
		if loc, ok := f.Interface.(*time.Location); ok && loc != nil {
			ts = ts.In(loc)
		}
		return stringKV(f.Key, ts.Format(time.RFC3339Nano))
	case zapcore.TimeFullType:
		if t, ok := f.Interface.(time.Time); ok {
			return stringKV(f.Key, t.Format(time.RFC3339Nano))
		}
		return nil
	case zapcore.ByteStringType:
		if b, ok := f.Interface.([]byte); ok {
			return stringKV(f.Key, string(b))
		}
		return nil
	case zapcore.Complex128Type, zapcore.Complex64Type:
		return stringKV(f.Key, fmt.Sprint(f.Interface))
	case zapcore.UintptrType:
		return &otelpb.KeyValue{
			Key:   f.Key,
			Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_IntValue{IntValue: f.Integer}},
		}
	case zapcore.ReflectType:
		// Covers zap.Any(structOrSlice). JSON-encode for a faithful,
		// queryable representation; fall back to fmt.Sprint on marshal error.
		if f.Interface == nil {
			return nil
		}
		if b, err := json.Marshal(f.Interface); err == nil {
			return stringKV(f.Key, string(b))
		}
		return stringKV(f.Key, fmt.Sprint(f.Interface))
	case zapcore.NamespaceType, zapcore.SkipType:
		// Namespaces carry no value; Skip fields are intentionally empty.
		return nil
```

Leave `case zapcore.StringerType:` and `default:` unchanged. (`ObjectMarshalerType`/`ArrayMarshalerType` are intentionally left to the `default` fallback — faithful rendering needs a zap encoder and neither is used in the agent.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-agent-logging/go && go test ./internal/agent/services/ -run TestFieldToKeyValue -v`
Expected: PASS for all four `TestFieldToKeyValue_*` tests.

- [ ] **Step 6: Full package test + build + vet**

Run:
```bash
cd /Users/joannisorlandos/git/wendy/wendyos-agent-logging/go
go build ./...
go vet ./internal/agent/services/
go test ./internal/agent/services/
```
Expected: build clean, vet clean, package tests PASS.

- [ ] **Step 7: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-agent-logging/go
git add internal/agent/services/telemetry_core.go internal/agent/services/telemetry_core_test.go
git commit -m "fix(agent): export zap.Time and other field types faithfully in TelemetryCore

zap.Time fell through to the default case and exported *time.Location
(e.g. \"UTC\") instead of the timestamp. Add explicit cases for Time,
ByteString, Complex, Uintptr, and Reflect (JSON), and skip Namespace/Skip."
```

---

### Task 3: Canonicalize near-duplicate field keys

**Files:**
- Modify: `internal/agent/containerd/ros2.go:103,632,644` (`"container"` → `logfields.ContainerID`)
- Modify: `internal/agent/containerd/client.go:1782,1815,1821` (`"service"` → `logfields.ServiceName`)

**Interfaces:**
- Consumes: `logfields` constants from Task 1 (`logfields.ContainerID`, `logfields.ServiceName`).
- Produces: no new symbols; exported log attribute keys change from `container`→`container_id` and `service`→`service_name` at these sites only.

- [ ] **Step 1: Confirm the exact current sites**

Run:
```bash
cd /Users/joannisorlandos/git/wendy/wendyos-agent-logging/go
grep -n 'zap.String("container", ctr.ID())' internal/agent/containerd/ros2.go
grep -n 'zap.String("service", svc)' internal/agent/containerd/client.go
```
Expected: 3 matches in `ros2.go` and 3 matches in `client.go`. If line numbers differ from this plan, use the grep output as the source of truth.

- [ ] **Step 2: Update `ros2.go`**

Add the import `"github.com/wendylabsinc/wendy/go/internal/agent/logfields"` to `internal/agent/containerd/ros2.go` (grouped with the other internal imports). Replace all three occurrences of:

```go
zap.String("container", ctr.ID())
```
with:
```go
zap.String(logfields.ContainerID, ctr.ID())
```

- [ ] **Step 3: Update `client.go`**

Add the import `"github.com/wendylabsinc/wendy/go/internal/agent/logfields"` to `internal/agent/containerd/client.go` (grouped with the other internal imports). Replace all three occurrences of:

```go
zap.String("service", svc)
```
with:
```go
zap.String(logfields.ServiceName, svc)
```

(Leave the existing `zap.String("app_id", appID)` and `zap.String("service_name", ...)` on nearby lines as-is; those already use canonical keys. Do not touch the `"service"` string in `ros2_service.go` or `telemetry_service.go` — those are a ROS 2 CLI argument and a map key, not log fields.)

- [ ] **Step 4: Verify no stray keys remain and build**

Run:
```bash
cd /Users/joannisorlandos/git/wendy/wendyos-agent-logging/go
grep -rn 'zap.String("container",' internal/agent/containerd/ || echo "no container log-field remnants"
grep -rn 'zap.String("service",' internal/agent/containerd/ || echo "no service log-field remnants"
go build ./...
go vet ./internal/agent/containerd/
go test ./internal/agent/containerd/
```
Expected: both greps print the "no ... remnants" line, build/vet clean, containerd package tests PASS (or `no test files` for untested subpaths — that is acceptable, no failures).

- [ ] **Step 5: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos-agent-logging/go
git add internal/agent/containerd/ros2.go internal/agent/containerd/client.go
git commit -m "refactor(agent): canonicalize container/service log-field keys via logfields"
```

---

## Final verification

- [ ] **Whole-module build and agent tests**

Run:
```bash
cd /Users/joannisorlandos/git/wendy/wendyos-agent-logging/go
go build ./...
go test ./internal/agent/...
```
Expected: build clean; agent package tests PASS (packages with `no test files` are fine).

- [ ] **Confirm the commits**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos-agent-logging/go && git log --oneline origin/main..HEAD`
Expected: the design-doc commit plus the three task commits (logfields package, TelemetryCore fix, key canonicalization).
