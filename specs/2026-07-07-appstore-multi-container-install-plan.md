# Multi-container AppStore install — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `wendy device install <app-id>` bring up a multi-service app (e.g. `immich` = server+ML+redis+postgres, `paperless` = web+redis), networked so services reach each other by name, self-contained with auto-generated secrets and zero required flags.

**Architecture:** The cloud AppStore gains a per-app **structured JSON manifest** (`app_manifests` table + `GET /v1/apps/{id}/manifest`), synthesizing a one-service manifest from `app_images` when no explicit row exists. The CLI resolves the manifest, generates per-install secrets, synthesizes a per-service `appconfig.AppConfig` in `isolation:"isolated"` mode (reusing the agent's existing CNI-bridge + `/etc/hosts` DNS), and creates+starts each service **sequentially in dependency order** (create → start → wait-for-Started-ack) so each service's hosts file resolves its already-started dependencies.

**Tech Stack:** Cloud — Swift, SwiftNIO HTTP handler (`VideoHTTPHandler.swift`), postgres-nio, golang-migrate, swift-testing. CLI — Go, cobra, `agentpb` gRPC client, `net/http`, `crypto/rand`, Go `testing` + `httptest`.

## Global Constraints

- **Prerequisite: PR #1229 (`jo/app-install`)** introduces `go/internal/cli/commands/apps_install.go` and the `wendy device install <app-id>` command. Part B modifies that file, so the CLI branch must be based on `jo/app-install` (or on `main` once #1229 merges). Part A (cloud) has no dependency on #1229.
- **Two PRs.** Part A (cloud repo `~/git/wendy/cloud`) ships first and independently. Part B (wendyos repo, this worktree) depends on Part A's endpoint being deployed and on #1229.
- **Migration number:** at authoring time run `ls ~/git/wendy/cloud/services/migrations | tail -3` and pick the next free integer. This plan writes `000043_*` assuming the registry-fix `000042` (cloud PR #261) has merged; renumber if the next free number differs.
- **Manifest image references are fully-qualified and pre-resolved** (e.g. `ghcr.io/immich-app/immich-server:release`) — the manifest is served verbatim; there is no per-service `source`→reference resolution like `app_images` has.
- **Cross-service addressing = literal service names** in env (`postgres`, `redis`, `http://machine-learning:3003`) — resolved by the agent's `/etc/hosts`. No hostname templating.
- **Secrets** are generated **once per install** by the CLI; the same `${secret:NAME}` resolves to the same value across services. Reinstall regenerates them (documented v1 limitation).
- Test framework: cloud uses **swift-testing** (`import Testing`, `@Suite`, `@Test`, `#expect`) — never XCTest. CLI uses Go `testing`.

---

# Part A — Cloud: manifest storage + API (`~/git/wendy/cloud`)

## File Structure (Part A)

- Create: `services/migrations/000043_create_app_manifests.up.sql` / `.down.sql` — table + seed for `paperless`, `immich`.
- Modify: `swift/Sources/GRPCServices/VideoHTTPHandler.swift` — add `synthesizeSingleServiceManifest(...)` free function, `handleAppManifest(...)` method, and a route branch.
- Create: `swift/Tests/GRPCServicesTests/AppManifestTests.swift` — unit tests for the pure synthesis function.

### Task A1: `app_manifests` table + curated manifest seed

**Files:**
- Create: `services/migrations/000043_create_app_manifests.up.sql`
- Create: `services/migrations/000043_create_app_manifests.down.sql`

**Interfaces:**
- Produces: table `app_manifests(app_id TEXT PK, manifest JSONB NOT NULL, updated_at TIMESTAMPTZ)` with seeded `paperless` and `immich` rows. The manifest JSON shape (consumed by Part B) is:
  `{"app_id":str,"secrets":[str],"services":[{"name":str,"image":str,"env":{k:v},"volumes":[{"name":str,"path":str}],"ports":[{"host":int,"container":int,"proto":str}],"dependsOn":[str]}]}`

- [ ] **Step 1: Confirm the next free migration number**

Run: `ls ~/git/wendy/cloud/services/migrations | grep up.sql | tail -3`
Expected: highest is `000042_fix_public_app_image_sources.up.sql` → use `000043`. If different, use the next free integer everywhere below.

- [ ] **Step 2: Write the up migration**

Create `services/migrations/000043_create_app_manifests.up.sql`:

```sql
-- Multi-service AppStore manifests. One curated JSON document per multi-service
-- app, served verbatim by GET /v1/apps/{id}/manifest. Apps without a row here
-- fall back to a single-service manifest synthesized from app_images.
-- Image references are fully-qualified/pre-resolved; secrets referenced as
-- "${secret:NAME}" in env are generated per-install by the CLI.
CREATE TABLE app_manifests (
    app_id     TEXT        PRIMARY KEY,
    manifest   JSONB       NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO app_manifests (app_id, manifest) VALUES
('paperless', '{
  "app_id": "paperless",
  "secrets": ["redis_noop"],
  "services": [
    { "name": "redis", "image": "docker.io/redis:7", "ports": [], "dependsOn": [] },
    { "name": "webserver",
      "image": "ghcr.io/paperless-ngx/paperless-ngx:latest",
      "env": {
        "PAPERLESS_REDIS": "redis://redis:6379",
        "PAPERLESS_TIME_ZONE": "Etc/UTC",
        "PAPERLESS_OCR_LANGUAGE": "eng",
        "USERMAP_UID": "1000",
        "USERMAP_GID": "1000"
      },
      "volumes": [
        { "name": "paperless-data", "path": "/usr/src/paperless/data" },
        { "name": "paperless-media", "path": "/usr/src/paperless/media" }
      ],
      "ports": [ { "host": 8000, "container": 8000, "proto": "tcp" } ],
      "dependsOn": [ "redis" ] }
  ]
}'::jsonb),
('immich', '{
  "app_id": "immich",
  "secrets": ["db_password"],
  "services": [
    { "name": "postgres",
      "image": "ghcr.io/immich-app/postgres:14-vectorchord0.3.0-pgvectors0.2.0",
      "env": {
        "POSTGRES_USER": "immich",
        "POSTGRES_DB": "immich",
        "POSTGRES_PASSWORD": "${secret:db_password}",
        "POSTGRES_INITDB_ARGS": "--data-checksums"
      },
      "volumes": [ { "name": "immich-pgdata", "path": "/var/lib/postgresql/data" } ],
      "ports": [], "dependsOn": [] },
    { "name": "redis", "image": "docker.io/redis:7", "ports": [], "dependsOn": [] },
    { "name": "machine-learning",
      "image": "ghcr.io/immich-app/immich-machine-learning:release",
      "volumes": [ { "name": "immich-model-cache", "path": "/cache" } ],
      "ports": [], "dependsOn": [] },
    { "name": "server",
      "image": "ghcr.io/immich-app/immich-server:release",
      "env": {
        "DB_HOSTNAME": "postgres",
        "DB_USERNAME": "immich",
        "DB_DATABASE_NAME": "immich",
        "DB_PASSWORD": "${secret:db_password}",
        "REDIS_HOSTNAME": "redis",
        "IMMICH_MACHINE_LEARNING_URL": "http://machine-learning:3003"
      },
      "volumes": [ { "name": "immich-upload", "path": "/data" } ],
      "ports": [ { "host": 2283, "container": 2283, "proto": "tcp" } ],
      "dependsOn": [ "postgres", "redis", "machine-learning" ] }
  ]
}'::jsonb);
```

> Note on `paperless.secrets`: paperless-ngx with the SQLite default needs no shared secret, but Part B's substitution must be exercised. The `redis_noop` entry is unused by any `${secret:}` reference and is harmless; if you prefer, drop the `"secrets"` key entirely — Part B treats a missing/empty `secrets` list as "no secrets". Keep `immich`'s `db_password` as the real shared-secret example.

- [ ] **Step 3: Write the down migration**

Create `services/migrations/000043_create_app_manifests.down.sql`:

```sql
DROP TABLE IF EXISTS app_manifests;
```

- [ ] **Step 4: Verify up/down round-trip against a local Postgres**

Run:
```bash
docker rm -f wendy-mig-a1 >/dev/null 2>&1
docker run -d --name wendy-mig-a1 -e POSTGRES_PASSWORD=test -e POSTGRES_DB=appstore -p 55440:5432 postgres:16-alpine >/dev/null
until docker exec wendy-mig-a1 pg_isready -U postgres >/dev/null 2>&1; do sleep 1; done
docker exec -i wendy-mig-a1 psql -U postgres -d appstore -v ON_ERROR_STOP=1 -q < services/migrations/000043_create_app_manifests.up.sql && echo UP_OK
docker exec -i wendy-mig-a1 psql -U postgres -d appstore -c "SELECT app_id, jsonb_array_length(manifest->'services') AS n_services FROM app_manifests ORDER BY app_id;"
docker exec -i wendy-mig-a1 psql -U postgres -d appstore -v ON_ERROR_STOP=1 -q < services/migrations/000043_create_app_manifests.down.sql && echo DOWN_OK
docker rm -f wendy-mig-a1 >/dev/null
```
Expected: `UP_OK`, a table showing `immich=4` and `paperless=2`, then `DOWN_OK`. Any JSON syntax error in the seed aborts at `UP_OK` — fix and re-run.

- [ ] **Step 5: Commit**

```bash
cd ~/git/wendy/cloud
git add services/migrations/000043_create_app_manifests.up.sql services/migrations/000043_create_app_manifests.down.sql
git commit -m "feat(appstore): app_manifests table + curated paperless/immich manifests"
```

### Task A2: `synthesizeSingleServiceManifest` pure function + tests

**Files:**
- Modify: `swift/Sources/GRPCServices/VideoHTTPHandler.swift` (add a free function near `resolveAppImageReference`, ~line 760)
- Create: `swift/Tests/GRPCServicesTests/AppManifestTests.swift`

**Interfaces:**
- Produces: `func synthesizeSingleServiceManifest(appID: String, source: String, repository: String, tag: String, registryHost: String) -> String` returning a manifest JSON string with exactly one service named `appID` and no ports/env/volumes. Consumed by `handleAppManifest` (Task A3).
- Consumes: existing `resolveAppImageReference(source:repository:tag:registryHost:)` (`VideoHTTPHandler.swift:764`).

- [ ] **Step 1: Write the failing test**

Create `swift/Tests/GRPCServicesTests/AppManifestTests.swift`:

```swift
import Foundation
import Testing

@testable import GRPCServices

@Suite("AppManifest")
struct AppManifestTests {
    @Test func synthesizesOneServiceFromImageRow() throws {
        let json = synthesizeSingleServiceManifest(
            appID: "jellyfin", source: "dockerhub",
            repository: "jellyfin/jellyfin", tag: "latest",
            registryHost: "ignored")
        let obj = try JSONSerialization.jsonObject(with: Data(json.utf8)) as! [String: Any]
        #expect(obj["app_id"] as? String == "jellyfin")
        let services = obj["services"] as! [[String: Any]]
        #expect(services.count == 1)
        #expect(services[0]["name"] as? String == "jellyfin")
        #expect(services[0]["image"] as? String == "docker.io/jellyfin/jellyfin:latest")
    }

    @Test func synthesizedServiceHasNoPorts() throws {
        let json = synthesizeSingleServiceManifest(
            appID: "ollama", source: "wendy", repository: "ollama", tag: "latest",
            registryHost: "reg.example/apps")
        let obj = try JSONSerialization.jsonObject(with: Data(json.utf8)) as! [String: Any]
        let services = obj["services"] as! [[String: Any]]
        #expect((services[0]["ports"] as? [Any])?.isEmpty ?? true)
        #expect(services[0]["image"] as? String == "reg.example/apps/ollama:latest")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/git/wendy/cloud/swift && swift test --filter AppManifest 2>&1 | tail -20`
Expected: FAIL — `cannot find 'synthesizeSingleServiceManifest' in scope`.

- [ ] **Step 3: Write minimal implementation**

In `swift/Sources/GRPCServices/VideoHTTPHandler.swift`, immediately after `resolveAppImageReference` (after line 770), add:

```swift
// synthesizeSingleServiceManifest builds a one-service manifest JSON for an app
// that has an app_images row but no explicit app_manifests row. The single
// service is named after the app id and carries no ports/env/volumes, matching
// the pre-multi-container single-container install behavior.
func synthesizeSingleServiceManifest(
    appID: String, source: String, repository: String, tag: String, registryHost: String
) -> String {
    let reference = resolveAppImageReference(
        source: source, repository: repository, tag: tag, registryHost: registryHost)
    let manifest: [String: Any] = [
        "app_id": appID,
        "secrets": [String](),
        "services": [
            [
                "name": appID,
                "image": reference,
                "env": [String: String](),
                "volumes": [Any](),
                "ports": [Any](),
                "dependsOn": [String](),
            ] as [String: Any]
        ],
    ]
    let data = (try? JSONSerialization.data(withJSONObject: manifest)) ?? Data("{}".utf8)
    return String(decoding: data, as: UTF8.self)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd ~/git/wendy/cloud/swift && swift test --filter AppManifest 2>&1 | tail -20`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
cd ~/git/wendy/cloud
git add swift/Sources/GRPCServices/VideoHTTPHandler.swift swift/Tests/GRPCServicesTests/AppManifestTests.swift
git commit -m "feat(appstore): synthesizeSingleServiceManifest helper + tests"
```

### Task A3: `handleAppManifest` route + handler

**Files:**
- Modify: `swift/Sources/GRPCServices/VideoHTTPHandler.swift` — add route branch in `route(context:head:)` (~line 170) and the `handleAppManifest` method (near `handleAppImage`, ~line 720).

**Interfaces:**
- Consumes: `dbClient: PostgresClient`, `synthesizeSingleServiceManifest(...)` (A2), `resolveAppImageReference(...)`, `sendJSON`, `sendJSONError`.
- Produces: `GET /v1/apps/{app_id}/manifest` → `200` with the manifest JSON body (explicit `app_manifests` row served verbatim, else synthesized from `app_images`); `404` when the app id is in neither table; `405` on non-GET; `400` on empty/slashed app id.

- [ ] **Step 1: Add the route branch**

In `route(context:head:)`, add a branch alongside the `/image` one (order among these two does not matter — the suffixes are distinct — but place it before the final `else`):

```swift
        } else if path.range(of: "/v1/apps/") != nil && path.hasSuffix("/manifest") {
            handleAppManifest(context: context, head: head)
        } else if path.range(of: "/v1/apps/") != nil && path.hasSuffix("/image") {
```

- [ ] **Step 2: Add the handler**

Immediately after `handleAppImage` (after its closing brace ~line 720), add:

```swift
    // handleAppManifest resolves an AppStore app id to a multi-service manifest.
    // GET /v1/apps/{app_id}/manifest -> the curated app_manifests row (served
    // verbatim), else a one-service manifest synthesized from app_images.
    private func handleAppManifest(context: ChannelHandlerContext, head: HTTPRequestHead) {
        guard head.method == .GET else {
            sendJSONError(context: context, status: .methodNotAllowed, message: "method not allowed")
            return
        }
        let path = uriPath(head.uri)
        guard let r = path.range(of: "/v1/apps/") else {
            sendJSONError(context: context, status: .notFound, message: "not found")
            return
        }
        let rest = String(path[r.upperBound...])
        guard rest.hasSuffix("/manifest") else {
            sendJSONError(context: context, status: .notFound, message: "not found")
            return
        }
        let appID = String(rest.dropLast("/manifest".count))
        guard !appID.isEmpty, !appID.contains("/") else {
            sendJSONError(context: context, status: .badRequest, message: "invalid app id")
            return
        }

        let registryHost = ProcessInfo.processInfo.environment["WENDY_REGISTRY_HOST"]
            ?? "us-central1-docker.pkg.dev/cloud-c7e56/apps"
        let db = dbClient
        let log = logger
        let eventLoop = context.eventLoop

        Task {
            do {
                // 1. Explicit curated manifest?
                let manifestRows = try await db.query(
                    "SELECT manifest::text AS manifest_json FROM app_manifests WHERE app_id = \(appID)",
                    logger: log
                )
                var manifestJSON: String? = nil
                for try await row in manifestRows {
                    manifestJSON = try row.decode(String.self, context: .default)
                    break
                }
                if let body = manifestJSON {
                    eventLoop.execute {
                        guard let ctx = self.currentContext else { return }
                        self.sendJSON(context: ctx, status: .ok, body: body)
                    }
                    return
                }

                // 2. Fall back to a synthesized single-service manifest from app_images.
                let imageRows = try await db.query(
                    "SELECT source, repository, tag FROM app_images WHERE app_id = \(appID)",
                    logger: log
                )
                var image: (String, String, String)? = nil
                for try await row in imageRows {
                    image = try row.decode((String, String, String).self, context: .default)
                    break
                }
                guard let (source, repository, tag) = image else {
                    eventLoop.execute {
                        guard let ctx = self.currentContext else { return }
                        self.sendJSONError(context: ctx, status: .notFound, message: "app manifest not found")
                    }
                    return
                }
                let body = synthesizeSingleServiceManifest(
                    appID: appID, source: source, repository: repository, tag: tag,
                    registryHost: registryHost)
                eventLoop.execute {
                    guard let ctx = self.currentContext else { return }
                    self.sendJSON(context: ctx, status: .ok, body: body)
                }
            } catch {
                log.error("failed to resolve app manifest", metadata: ["error": "\(error)"])
                eventLoop.execute {
                    guard let ctx = self.currentContext else { return }
                    self.sendJSONError(context: ctx, status: .internalServerError, message: "internal error")
                }
            }
        }
    }
```

- [ ] **Step 3: Build**

Run: `cd ~/git/wendy/cloud/swift && swift build 2>&1 | tail -20`
Expected: `Compiling`/`Build complete!` with no errors.

- [ ] **Step 4: Manual endpoint check against a local DB (optional but recommended)**

There is **no HTTP+DB test fixture** in this repo (`BrokerFixture` registers only gRPC handlers, not `BrokerHTTPRouter`), so drive it manually if a local broker + Postgres is available:
Run (against a locally-running broker with the migrations applied):
```bash
curl -s localhost:8080/v1/apps/immich/manifest | jq '.services | length'      # -> 4
curl -s localhost:8080/v1/apps/jellyfin/manifest | jq '.services[0].image'    # synthesized -> docker.io/jellyfin/jellyfin:latest
curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/v1/apps/nope/manifest # -> 404
```
Expected: `4`, the jellyfin reference, `404`. If no local broker is available, note this as deferred to deploy-time verification.

- [ ] **Step 5: Commit**

```bash
cd ~/git/wendy/cloud
git add swift/Sources/GRPCServices/VideoHTTPHandler.swift
git commit -m "feat(appstore): GET /v1/apps/{id}/manifest endpoint"
```

### Task A4: Open the cloud PR

- [ ] **Step 1: Push and open PR**

```bash
cd ~/git/wendy/cloud
git push -u origin HEAD
gh pr create --base main --title "feat(appstore): multi-service app manifests + /v1/apps/{id}/manifest" \
  --body "Adds app_manifests (JSONB) with curated paperless/immich manifests and GET /v1/apps/{id}/manifest (explicit row, else synthesized one-service manifest from app_images). Consumed by the CLI multi-container install (wendyos). Verified: migration round-trips; synthesis unit-tested; swift build clean."
```
Expected: PR URL printed.

---

# Part B — CLI: multi-container install (this worktree, `go/`)

Base branch: `jo/app-install` (per Global Constraints). All paths below are under `go/`.

## File Structure (Part B)

- Create: `internal/cli/commands/apps_manifest.go` — manifest types, `resolveAppManifest`, secret generation/substitution, manifest→`CreateContainerRequest` assembly (`buildServiceInstall`). All pure/testable except the HTTP call.
- Create: `internal/cli/commands/apps_manifest_test.go` — unit tests.
- Modify: `internal/cli/commands/apps_install.go` — replace the single-image resolve/create/start with the manifest-driven ordered create+start loop.

### Task B1: manifest types + `resolveAppManifest` HTTP client

**Files:**
- Create: `go/internal/cli/commands/apps_manifest.go`
- Create: `go/internal/cli/commands/apps_manifest_test.go`

**Interfaces:**
- Produces:
  - `type appManifest struct { AppID string; Secrets []string; Services []manifestService }`
  - `type manifestService struct { Name, Image string; Env map[string]string; Volumes []manifestVolume; Ports []manifestPort; DependsOn []string }`
  - `type manifestVolume struct { Name, Path string }`
  - `type manifestPort struct { Host, Container uint16; Proto string }`
  - `func resolveAppManifest(ctx context.Context, base, appID string) (appManifest, error)` — GET `{base}/v1/apps/{appID}/manifest`. 404 → `fmt.Errorf("app %q is not in the AppStore", appID)`; non-200 → status+body error; empty `Services` → error.

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/commands/apps_manifest_test.go`:

```go
package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveAppManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/apps/immich/manifest" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"app_id":"immich","secrets":["db_password"],
			"services":[
			  {"name":"postgres","image":"ghcr.io/immich-app/postgres:14",
			   "env":{"POSTGRES_PASSWORD":"${secret:db_password}"},
			   "volumes":[{"name":"pg","path":"/var/lib/postgresql/data"}]},
			  {"name":"server","image":"ghcr.io/immich-app/immich-server:release",
			   "env":{"DB_HOSTNAME":"postgres","DB_PASSWORD":"${secret:db_password}"},
			   "ports":[{"host":2283,"container":2283,"proto":"tcp"}],
			   "dependsOn":["postgres"]}
			]}`))
	}))
	defer srv.Close()

	m, err := resolveAppManifest(context.Background(), srv.URL, "immich")
	if err != nil {
		t.Fatalf("resolveAppManifest: %v", err)
	}
	if m.AppID != "immich" || len(m.Services) != 2 {
		t.Fatalf("got app_id=%q services=%d", m.AppID, len(m.Services))
	}
	if m.Services[1].Ports[0].Host != 2283 {
		t.Fatalf("server host port = %d, want 2283", m.Services[1].Ports[0].Host)
	}
	if got := m.Services[0].Env["POSTGRES_PASSWORD"]; got != "${secret:db_password}" {
		t.Fatalf("env not preserved: %q", got)
	}
}

func TestResolveAppManifestNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := resolveAppManifest(context.Background(), srv.URL, "nope"); err == nil {
		t.Fatal("expected error for 404")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestResolveAppManifest 2>&1 | tail -20`
Expected: FAIL — `undefined: resolveAppManifest` (and the types).

- [ ] **Step 3: Write minimal implementation**

Create `go/internal/cli/commands/apps_manifest.go`:

```go
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// appManifest is the JSON returned by GET /v1/apps/{app_id}/manifest. A manifest
// describes one or more services that together make up an installable app.
type appManifest struct {
	AppID    string            `json:"app_id"`
	Secrets  []string          `json:"secrets,omitempty"`
	Services []manifestService `json:"services"`
}

type manifestService struct {
	Name      string            `json:"name"`
	Image     string            `json:"image"`
	Env       map[string]string `json:"env,omitempty"`
	Volumes   []manifestVolume  `json:"volumes,omitempty"`
	Ports     []manifestPort    `json:"ports,omitempty"`
	DependsOn []string          `json:"dependsOn,omitempty"`
}

type manifestVolume struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type manifestPort struct {
	Host      uint16 `json:"host"`
	Container uint16 `json:"container"`
	Proto     string `json:"proto,omitempty"`
}

// resolveAppManifest asks the AppStore API for the multi-service manifest of an
// app id. Single-image apps come back as a one-service manifest.
func resolveAppManifest(ctx context.Context, base, appID string) (appManifest, error) {
	endpoint := fmt.Sprintf("%s/v1/apps/%s/manifest", base, url.PathEscape(appID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return appManifest{}, err
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return appManifest{}, fmt.Errorf("contacting the AppStore: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return appManifest{}, fmt.Errorf("app %q is not in the AppStore", appID)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return appManifest{}, fmt.Errorf("AppStore returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out appManifest
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return appManifest{}, fmt.Errorf("parsing AppStore manifest: %w", err)
	}
	if len(out.Services) == 0 {
		return appManifest{}, fmt.Errorf("AppStore returned an empty manifest for %q", appID)
	}
	return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/commands/ -run TestResolveAppManifest 2>&1 | tail -20`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/commands/apps_manifest.go internal/cli/commands/apps_manifest_test.go
git add internal/cli/commands/apps_manifest.go internal/cli/commands/apps_manifest_test.go
git commit -m "feat(cli): appManifest types + resolveAppManifest client"
```

### Task B2: secret generation + `${secret:}` substitution

**Files:**
- Modify: `go/internal/cli/commands/apps_manifest.go`
- Modify: `go/internal/cli/commands/apps_manifest_test.go`

**Interfaces:**
- Produces:
  - `func generateSecrets(names []string) (map[string]string, error)` — one 32-hex-char random value per name (dedup names).
  - `func substituteSecrets(env map[string]string, secrets map[string]string) (map[string]string, error)` — replaces every `${secret:NAME}` token; returns an error if a referenced NAME is absent from `secrets`. Non-secret values pass through.

- [ ] **Step 1: Write the failing test**

Append to `go/internal/cli/commands/apps_manifest_test.go`:

```go
func TestSubstituteSecretsSharedValue(t *testing.T) {
	secrets, err := generateSecrets([]string{"db_password"})
	if err != nil {
		t.Fatalf("generateSecrets: %v", err)
	}
	pg, err := substituteSecrets(map[string]string{"POSTGRES_PASSWORD": "${secret:db_password}"}, secrets)
	if err != nil {
		t.Fatalf("substituteSecrets pg: %v", err)
	}
	srv, err := substituteSecrets(map[string]string{"DB_PASSWORD": "${secret:db_password}", "DB_HOSTNAME": "postgres"}, secrets)
	if err != nil {
		t.Fatalf("substituteSecrets srv: %v", err)
	}
	if pg["POSTGRES_PASSWORD"] == "${secret:db_password}" || pg["POSTGRES_PASSWORD"] == "" {
		t.Fatalf("secret not substituted: %q", pg["POSTGRES_PASSWORD"])
	}
	if pg["POSTGRES_PASSWORD"] != srv["DB_PASSWORD"] {
		t.Fatalf("shared secret differs across services: %q vs %q", pg["POSTGRES_PASSWORD"], srv["DB_PASSWORD"])
	}
	if srv["DB_HOSTNAME"] != "postgres" {
		t.Fatalf("non-secret value mangled: %q", srv["DB_HOSTNAME"])
	}
}

func TestSubstituteSecretsMissingName(t *testing.T) {
	if _, err := substituteSecrets(map[string]string{"X": "${secret:absent}"}, map[string]string{}); err == nil {
		t.Fatal("expected error for missing secret name")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestSubstituteSecrets 2>&1 | tail -20`
Expected: FAIL — `undefined: generateSecrets` / `substituteSecrets`.

- [ ] **Step 3: Write minimal implementation**

Append to `go/internal/cli/commands/apps_manifest.go` (add `crypto/rand`, `encoding/hex`, `regexp` to imports):

```go
// secretRefPattern matches ${secret:NAME} tokens in manifest env values.
var secretRefPattern = regexp.MustCompile(`\$\{secret:([a-zA-Z0-9_]+)\}`)

// generateSecrets returns one random 32-hex-character value per (deduplicated)
// name. Values are stable within a single install so services sharing a secret
// name receive the same value.
func generateSecrets(names []string) (map[string]string, error) {
	out := make(map[string]string, len(names))
	for _, name := range names {
		if _, ok := out[name]; ok {
			continue
		}
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			return nil, fmt.Errorf("generating secret %q: %w", name, err)
		}
		out[name] = hex.EncodeToString(buf)
	}
	return out, nil
}

// substituteSecrets replaces every ${secret:NAME} token in the env values with
// the corresponding generated secret. It errors if a referenced name has no
// generated value (a manifest bug). The input map is not modified.
func substituteSecrets(env map[string]string, secrets map[string]string) (map[string]string, error) {
	if env == nil {
		return nil, nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		var subErr error
		out[k] = secretRefPattern.ReplaceAllStringFunc(v, func(match string) string {
			name := secretRefPattern.FindStringSubmatch(match)[1]
			val, ok := secrets[name]
			if !ok {
				subErr = fmt.Errorf("env %q references unknown secret %q", k, name)
				return match
			}
			return val
		})
		if subErr != nil {
			return nil, subErr
		}
	}
	return out, nil
}
```

Add to the import block:

```go
	"crypto/rand"
	"encoding/hex"
	"regexp"
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/commands/ -run TestSubstituteSecrets 2>&1 | tail -20`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/commands/apps_manifest.go internal/cli/commands/apps_manifest_test.go
git add internal/cli/commands/apps_manifest.go internal/cli/commands/apps_manifest_test.go
git commit -m "feat(cli): per-install secret generation + \${secret:} substitution"
```

### Task B3: manifest → per-service `CreateContainerRequest` assembly

**Files:**
- Modify: `go/internal/cli/commands/apps_manifest.go`
- Modify: `go/internal/cli/commands/apps_manifest_test.go`

**Interfaces:**
- Consumes: `appconfig.AppConfig`, `appconfig.ServiceConfig`, `appconfig.Entitlement`, `appconfig.PortMapping`, `appconfig.EntitlementNetwork`, `appconfig.EntitlementPersist`, `appconfig.ServiceTopoOrder` (all in `internal/shared/appconfig`); `normalizeImageRef` (`compose.go:27`); `agentpb.CreateContainerRequest`, `agentpb.RestartPolicy`, `agentpb.RestartPolicyMode_UNLESS_STOPPED`.
- Produces: `func buildServiceInstall(m appManifest) (order []string, reqs map[string]*agentpb.CreateContainerRequest, err error)` — topologically orders services by `DependsOn`, generates+substitutes secrets, and builds one `CreateContainerRequest` per service. For multi-service manifests each request's `AppConfig` has `Isolation:"isolated"`, `ServiceName`, and a fully-populated `Services` map (so the agent injects `/etc/hosts`); single-service manifests get a plain `AppConfig` (no isolation/ServiceName) to preserve legacy single-container behavior. `AppName` = `appconfig.(*AppConfig).ContainerName()`.

- [ ] **Step 1: Write the failing test**

Append to `go/internal/cli/commands/apps_manifest_test.go` (add `encoding/json` and the appconfig import at the top of the file):

```go
func TestBuildServiceInstallMultiService(t *testing.T) {
	m := appManifest{
		AppID:   "immich",
		Secrets: []string{"db_password"},
		Services: []manifestService{
			{Name: "server", Image: "ghcr.io/immich-app/immich-server:release",
				Env:       map[string]string{"DB_PASSWORD": "${secret:db_password}", "DB_HOSTNAME": "postgres"},
				Volumes:   []manifestVolume{{Name: "upload", Path: "/data"}},
				Ports:     []manifestPort{{Host: 2283, Container: 2283, Proto: "tcp"}},
				DependsOn: []string{"postgres"}},
			{Name: "postgres", Image: "ghcr.io/immich-app/postgres:14",
				Env:     map[string]string{"POSTGRES_PASSWORD": "${secret:db_password}"},
				Volumes: []manifestVolume{{Name: "pg", Path: "/var/lib/postgresql/data"}}},
		},
	}
	order, reqs, err := buildServiceInstall(m)
	if err != nil {
		t.Fatalf("buildServiceInstall: %v", err)
	}
	// postgres has no deps; server depends on postgres -> postgres first.
	if len(order) != 2 || order[0] != "postgres" || order[1] != "server" {
		t.Fatalf("topo order = %v, want [postgres server]", order)
	}

	srv := reqs["server"]
	if srv.AppName != "immich_server" {
		t.Fatalf("server AppName = %q, want immich_server", srv.AppName)
	}
	if srv.ImageName != "ghcr.io/immich-app/immich-server:release" {
		t.Fatalf("server image = %q", srv.ImageName)
	}
	// Env carries the substituted secret and the literal hostname.
	var dbPass, dbHost string
	for _, kv := range srv.Env {
		if strings.HasPrefix(kv, "DB_PASSWORD=") {
			dbPass = strings.TrimPrefix(kv, "DB_PASSWORD=")
		}
		if strings.HasPrefix(kv, "DB_HOSTNAME=") {
			dbHost = strings.TrimPrefix(kv, "DB_HOSTNAME=")
		}
	}
	if dbHost != "postgres" {
		t.Fatalf("DB_HOSTNAME = %q, want postgres", dbHost)
	}
	if dbPass == "" || strings.Contains(dbPass, "${secret") {
		t.Fatalf("DB_PASSWORD not substituted: %q", dbPass)
	}

	// AppConfig: isolated, ServiceName set, Services fully populated, port+volume entitlements.
	var cfg appconfig.AppConfig
	if err := json.Unmarshal(srv.AppConfig, &cfg); err != nil {
		t.Fatalf("unmarshal AppConfig: %v", err)
	}
	if cfg.Isolation != "isolated" {
		t.Fatalf("isolation = %q, want isolated", cfg.Isolation)
	}
	if cfg.ServiceName != "server" {
		t.Fatalf("ServiceName = %q", cfg.ServiceName)
	}
	if len(cfg.Services) != 2 {
		t.Fatalf("Services len = %d, want 2 (agent needs >1 to inject /etc/hosts)", len(cfg.Services))
	}
	var haveNet, havePersist bool
	for _, e := range cfg.Entitlements {
		if e.Type == appconfig.EntitlementNetwork && len(e.Ports) == 1 && e.Ports[0].Host == 2283 {
			haveNet = true
		}
		if e.Type == appconfig.EntitlementPersist && e.Name == "upload" && e.Path == "/data" {
			havePersist = true
		}
	}
	if !haveNet || !havePersist {
		t.Fatalf("missing entitlements: net=%v persist=%v", haveNet, havePersist)
	}

	// The shared secret is identical in postgres.
	var pgPass string
	for _, kv := range reqs["postgres"].Env {
		if strings.HasPrefix(kv, "POSTGRES_PASSWORD=") {
			pgPass = strings.TrimPrefix(kv, "POSTGRES_PASSWORD=")
		}
	}
	if pgPass != dbPass {
		t.Fatalf("shared secret differs: server=%q postgres=%q", dbPass, pgPass)
	}
}

func TestBuildServiceInstallSingleService(t *testing.T) {
	m := appManifest{
		AppID:    "jellyfin",
		Services: []manifestService{{Name: "jellyfin", Image: "docker.io/jellyfin/jellyfin:latest"}},
	}
	order, reqs, err := buildServiceInstall(m)
	if err != nil {
		t.Fatalf("buildServiceInstall: %v", err)
	}
	if len(order) != 1 {
		t.Fatalf("order = %v", order)
	}
	r := reqs["jellyfin"]
	if r.AppName != "jellyfin" {
		t.Fatalf("AppName = %q, want jellyfin (plain, no service suffix)", r.AppName)
	}
	var cfg appconfig.AppConfig
	if err := json.Unmarshal(r.AppConfig, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Isolation != "" || cfg.ServiceName != "" {
		t.Fatalf("single service must not set isolation/serviceName: iso=%q svc=%q", cfg.Isolation, cfg.ServiceName)
	}
}
```

Add to the test file's import block: `"encoding/json"` and `"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestBuildServiceInstall 2>&1 | tail -20`
Expected: FAIL — `undefined: buildServiceInstall`.

- [ ] **Step 3: Write minimal implementation**

Append to `go/internal/cli/commands/apps_manifest.go`. Add imports `"sort"`, `"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"`, and `"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"`:

```go
// buildServiceInstall turns a resolved manifest into an ordered set of
// per-service CreateContainerRequests. Services are ordered so every service
// follows its dependsOn entries (create+start must happen in this order so each
// service's /etc/hosts already resolves its dependencies). Secrets are generated
// once and shared across services by name.
func buildServiceInstall(m appManifest) ([]string, map[string]*agentpb.CreateContainerRequest, error) {
	secrets, err := generateSecrets(m.Secrets)
	if err != nil {
		return nil, nil, err
	}

	// Build the appconfig.ServiceConfig map for topo ordering and for the agent's
	// group awareness (len(Services) > 1 triggers /etc/hosts injection).
	svcConfigs := make(map[string]*appconfig.ServiceConfig, len(m.Services))
	byName := make(map[string]manifestService, len(m.Services))
	for _, s := range m.Services {
		svcConfigs[s.Name] = &appconfig.ServiceConfig{DependsOn: s.DependsOn}
		byName[s.Name] = s
	}

	order, err := appconfig.ServiceTopoOrder(svcConfigs)
	if err != nil {
		return nil, nil, err
	}

	multi := len(m.Services) > 1
	reqs := make(map[string]*agentpb.CreateContainerRequest, len(m.Services))
	for _, name := range order {
		svc := byName[name]

		env, err := substituteSecrets(svc.Env, secrets)
		if err != nil {
			return nil, nil, err
		}
		envList := make([]string, 0, len(env))
		for k, v := range env {
			envList = append(envList, k+"="+v)
		}
		sort.Strings(envList) // stable order for reproducible requests/tests

		var entitlements []appconfig.Entitlement
		if len(svc.Ports) > 0 {
			ports := make([]appconfig.PortMapping, 0, len(svc.Ports))
			for _, p := range svc.Ports {
				ports = append(ports, appconfig.PortMapping{Host: p.Host, Container: p.Container})
			}
			entitlements = append(entitlements, appconfig.Entitlement{
				Type:  appconfig.EntitlementNetwork,
				Ports: ports,
			})
		}
		for _, v := range svc.Volumes {
			entitlements = append(entitlements, appconfig.Entitlement{
				Type: appconfig.EntitlementPersist,
				Name: v.Name,
				Path: v.Path,
			})
		}

		cfg := &appconfig.AppConfig{
			AppID:        m.AppID,
			Entitlements: entitlements,
		}
		if multi {
			cfg.ServiceName = name
			cfg.Isolation = "isolated"
			cfg.Services = svcConfigs
		}

		cfgData, err := json.Marshal(cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("marshaling config for service %s: %w", name, err)
		}

		reqs[name] = &agentpb.CreateContainerRequest{
			ImageName:     normalizeImageRef(svc.Image),
			AppName:       cfg.ContainerName(),
			AppConfig:     cfgData,
			Env:           envList,
			RestartPolicy: &agentpb.RestartPolicy{Mode: agentpb.RestartPolicyMode_UNLESS_STOPPED},
		}
	}
	return order, reqs, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/commands/ -run TestBuildServiceInstall 2>&1 | tail -20`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/commands/apps_manifest.go internal/cli/commands/apps_manifest_test.go
git add internal/cli/commands/apps_manifest.go internal/cli/commands/apps_manifest_test.go
git commit -m "feat(cli): build per-service CreateContainerRequests from a manifest"
```

### Task B4: wire the ordered create+start loop into `apps_install.go`

**Files:**
- Modify: `go/internal/cli/commands/apps_install.go` (the `RunE` closure and the AppStore-app-id branch, lines ~138-181)

**Interfaces:**
- Consumes: `resolveAppManifest` (B1), `buildServiceInstall` (B3), `createContainerWithProgress` (`run.go:434`), `target.Agent.ContainerService` (`agentpb.WendyContainerServiceClient`), `agentpb.StartContainerRequest`.
- Produces: an install that creates+starts every service in dependency order, waiting for each service's Started ack before creating the next (required for `/etc/hosts` correctness). Direct-image-ref installs (the `looksLikeImageRef` branch) are unchanged.

- [ ] **Step 1: Replace the AppStore branch + create/start section**

In `apps_install.go`, the current `RunE` has (a) an `else` branch that calls `resolveAppImage` and sets `imageRef`/`appName`, and (b) a single `CreateContainerRequest` + `StartContainer` block. Replace **only the AppStore-app-id path** (the `else { ... }` at lines ~138-148) and the single create/start block (lines ~160-180) with a manifest-driven flow. The direct-image `if looksLikeImageRef(arg)` path stays exactly as is.

Replace the `RunE` body from the `var imageRef, appName string` declaration through the end of the success handling with:

```go
			ctx := cmd.Context()
			arg := args[0]

			target, err := resolveTarget(ctx)
			if err != nil {
				return err
			}
			defer target.Close()
			if target.Agent == nil {
				return fmt.Errorf("selected device does not support installing apps")
			}
			svc := target.Agent.ContainerService

			// Direct OCI image reference — install it verbatim as a single container.
			if looksLikeImageRef(arg) {
				imageRef := normalizeImageRef(arg)
				appName := deriveAppNameFromImage(arg)
				cliLogln("Installing image %s as %s", imageRef, appName)
				req := &agentpb.CreateContainerRequest{
					ImageName:     imageRef,
					AppName:       appName,
					RestartPolicy: &agentpb.RestartPolicy{Mode: agentpb.RestartPolicyMode_UNLESS_STOPPED},
				}
				if err := createContainerWithProgress(ctx, svc, req); err != nil {
					return fmt.Errorf("installing %s: %w", appName, err)
				}
				if noStart {
					cliSuccess("Installed %s (not started).", appName)
					return nil
				}
				if err := startInstalledService(ctx, svc, appName); err != nil {
					return fmt.Errorf("starting %s: %w", appName, err)
				}
				cliSuccess("Installed and started %s.", appName)
				return nil
			}

			// AppStore app id — resolve to a (possibly multi-service) manifest.
			base := resolveAppStoreAPIBase(apiBase)
			manifest, err := resolveAppManifest(ctx, base, arg)
			if err != nil {
				return err
			}
			order, reqs, err := buildServiceInstall(manifest)
			if err != nil {
				return fmt.Errorf("preparing install for %s: %w", arg, err)
			}
			if len(order) == 1 {
				cliLogln("Resolved %s to %s", arg, reqs[order[0]].ImageName)
			} else {
				cliLogln("Resolved %s to %d services: %s", arg, len(order), strings.Join(order, ", "))
			}

			// Create + start each service in dependency order. Each service's
			// Started ack must complete before the next is created so that the
			// next service's /etc/hosts already resolves its dependencies
			// (the agent records a service's bridge IP at StartContainer time).
			for _, name := range order {
				cliLogln("Installing service %s...", name)
				if err := createContainerWithProgress(ctx, svc, reqs[name]); err != nil {
					return fmt.Errorf("installing service %s: %w", name, err)
				}
				if noStart {
					continue
				}
				if err := startInstalledService(ctx, svc, reqs[name].AppName); err != nil {
					return fmt.Errorf("starting service %s: %w", name, err)
				}
			}

			if noStart {
				cliSuccess("Installed %s (%d service(s), not started).", arg, len(order))
				return nil
			}
			cliSuccess("Installed and started %s (%d service(s)).", arg, len(order))
			return nil
```

- [ ] **Step 2: Add the `startInstalledService` helper**

`StartContainer` is server-streaming (`grpc.ServerStreamingClient[RunContainerLayersResponse]`); the first `Recv()` is the Started ack. Add this helper to `apps_install.go` (below `newAppsInstallCmd`), mirroring the drain pattern in `startAndStreamServices`:

```go
// startInstalledService starts a container and blocks until the agent's first
// stream message (the Started ack) or EOF, so that a subsequent service's
// container is created only after this service's bridge IP is recorded.
func startInstalledService(ctx context.Context, svc agentpb.WendyContainerServiceClient, appName string) error {
	stream, err := svc.StartContainer(ctx, &agentpb.StartContainerRequest{
		AppName:       appName,
		RestartPolicy: &agentpb.RestartPolicy{Mode: agentpb.RestartPolicyMode_UNLESS_STOPPED},
	})
	if err != nil {
		return err
	}
	if _, err := stream.Recv(); err != nil && err != io.EOF {
		return err
	}
	return nil
}
```

Ensure `io` and `strings` are imported in `apps_install.go` (both are already used elsewhere in the file per its current imports; add any missing).

- [ ] **Step 3: Build and vet**

Run: `cd go && go build ./... && go vet ./internal/cli/commands/ 2>&1 | tail -20`
Expected: no output / clean build. Fix any unused-import or symbol errors (e.g. remove the now-unused `resolveAppImage`/`appImageResolution` only if nothing else references them — check with `grep -rn "resolveAppImage\b\|appImageResolution" internal/`; if unused, delete them and their `--api`-path references, else leave them).

- [ ] **Step 4: Run the full package tests**

Run: `cd go && go test ./internal/cli/commands/ 2>&1 | tail -20`
Expected: PASS, including the existing `apps_install_test.go` and the new manifest tests. If `apps_install_test.go` asserted the old `/image` resolution, update those assertions to the `/manifest` endpoint (the httptest server path becomes `/v1/apps/{id}/manifest` returning a one-service manifest JSON).

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/commands/apps_install.go
git add internal/cli/commands/apps_install.go
git commit -m "feat(cli): multi-service AppStore install via manifest (ordered create+start)"
```

### Task B5: end-to-end verification + PR

**Files:** none (verification + PR).

- [ ] **Step 1: Build the CLI binary**

Run: `cd go && go build -o bin/wendy ./cmd/wendy 2>&1 | tail -5 && ls -la bin/wendy`
Expected: binary built. (Confirm the `cmd/wendy` path matches the repo; adjust if the main package lives elsewhere.)

- [ ] **Step 2: Resolve-only smoke test against the deployed manifest endpoint**

Requires Part A deployed. Run:
```bash
WENDY_APPSTORE_API="https://wendy-cloud-services-114319063177.us-central1.run.app" ./bin/wendy device install immich 2>&1 | head -5
```
Expected: `Resolved immich to 4 services: machine-learning, postgres, redis, server` (order alphabetized among independents, `server` last), then it proceeds to device selection / fails only at "no device specified" if none is set. If Part A is not yet deployed, note this step as blocked on the cloud deploy.

- [ ] **Step 3: On-device E2E (requires a WendyOS device)**

Run:
```bash
./bin/wendy device set-default    # select a device
./bin/wendy device install paperless
./bin/wendy device apps           # or container_list: expect paperless_redis + paperless_webserver running
```
Then from the host, hit the device IP on port 8000 (see `wendy device info` for the IP) and confirm the paperless UI loads; restart the device/app and confirm data persists. Record results in the PR description. If no device is available, mark this step deferred and say so explicitly in the PR.

- [ ] **Step 4: Open the CLI PR**

```bash
cd go
git push -u origin jo/appstore-multi-container
gh pr create --base main --title "feat(cli): multi-container AppStore install (paperless, immich)" \
  --body "Resolves apps via GET /v1/apps/{id}/manifest and installs all services in dependency order (create->start->ack) in isolation:isolated mode so services reach each other by name. Self-contained: per-install secrets generated CLI-side. Depends on cloud PR (Part A) + PR #1229. Verified: unit tests for resolve/substitute/build; <E2E status>."
```
Expected: PR URL. Note the two dependencies (cloud Part A, PR #1229) in the body.

---

## Self-Review

**Spec coverage:**
- Cloud manifest storage + API → Tasks A1 (table+seed), A2 (synthesis), A3 (endpoint). ✓
- Synthesized single-service fallback → A2/A3. ✓
- `/image` untouched → A3 adds a parallel branch only. ✓
- Manifest schema (services/env/ports/volumes/dependsOn/secrets) → A1 seed + B1 types (matching shapes). ✓
- Literal service-name addressing → encoded in A1 seed env (`postgres`,`redis`,`http://machine-learning:3003`); no templating code. ✓
- CLI resolve + secret gen/substitute + AppConfig synthesis + topo order + ordered create/start → B1, B2, B3, B4. ✓
- Isolated-mode DNS via populated `Services` + create/start ordering → B3 (`cfg.Services = svcConfigs`, `Isolation:"isolated"`) + B4 (Started-ack wait). ✓
- Only externally-reachable services declare ports → A1 seed (internal services have `"ports":[]`); B3 emits a Network entitlement only when `len(svc.Ports)>0`. ✓
- Secrets persistence limitation → documented in spec; no code (correct — it's a known non-goal). ✓
- v1 scope: paperless (2 svc, SQLite) + immich (4 svc) → A1 seed. ✓
- Testing (cloud unit + migration round-trip; CLI unit; E2E) → A1.4, A2, B1-B3, B5. ✓
- Non-goals (overrides, healthchecks, resource limits, config command, durable secrets) → not implemented. ✓

**Placeholder scan:** No TBD/TODO. Every code step shows complete code; every run step shows the command + expected output. The only conditional instructions (delete-if-unused `resolveAppImage`; update `apps_install_test.go` if it asserted `/image`) are explicit branch conditions with grep checks, not placeholders.

**Type consistency:** `appManifest`/`manifestService`/`manifestVolume`/`manifestPort` field names and json tags are identical across B1 (definition), the A1 seed (JSON keys), and B3 (consumption). `buildServiceInstall` returns `([]string, map[string]*agentpb.CreateContainerRequest, error)` and B4 consumes exactly that. `PortMapping{Host,Container}`, `Entitlement{Type,Name,Path,Ports}`, `EntitlementNetwork`/`EntitlementPersist`, `AppConfig.{AppID,ServiceName,Isolation,Services,Entitlements}`, `ContainerName()`, and `ServiceTopoOrder(map[string]*ServiceConfig)` all match the verbatim source. `startInstalledService` uses the server-streaming `StartContainer` + first-`Recv` pattern confirmed in `startAndStreamServices`.
