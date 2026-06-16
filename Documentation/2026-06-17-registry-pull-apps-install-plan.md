# Registry pull + `wendy device apps install` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let wendy-agent pull container images directly from registries (with optional auth), and add `wendy device apps install [name|image]` that installs apps from a curated catalog plus the org's cloud app releases.

**Architecture:** Add an optional `RegistryAuth` to the existing `CreateContainerRequest`; the agent threads it into containerd's docker resolver as an authorizer on the `Pull` fallback. A new embedded-catalog package supplies common public apps; the install command synthesizes a `wendy.json` `AppConfig` and calls `CreateContainer` then `StartContainer`. Org apps are listed best-effort via the cloud `DeploymentService.ListAppReleases`.

**Tech Stack:** Go, Cobra CLI, containerd v2.3.1 (`core/remotes/docker`), protoc (`go/scripts/generate-proto.sh`), bubbletea/lipgloss TUI.

## Global Constraints

- Go module root is the repo root; package import prefix `github.com/wendylabsinc/wendy/go/...`. Build/test from the `go/` directory with relative paths (`go build ./internal/...`).
- containerd version in use: **v2.3.1**. Docker auth API: `docker.NewDockerAuthorizer(docker.WithAuthCreds(func(host string)(string,string,error)))`, `docker.NewResolver(docker.ResolverOptions{Hosts: docker.ConfigureDefaultRegistries(docker.WithAuthorizer(a))})`.
- Proto regen command (run from repo root): `bash go/scripts/generate-proto.sh`. Generated agent bindings live in `go/proto/gen/agentpb`.
- Agent-connected path only for the new command; provider/BLE targets return `"not supported on this device"` (match existing `apps` subcommands in `go/internal/cli/commands/apps.go`).
- Do NOT return registry credentials to hosts other than the configured one (host-match in the creds callback). The repo has had credential-handling review scrutiny.
- All commits end with the trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.

---

### Task 1: Proto — add `RegistryAuth` to `CreateContainerRequest`

**Files:**
- Modify: `Proto/wendy/agent/services/v1/wendy_agent_v1_container_service.proto` (CreateContainerRequest, ~line 90-102)
- Regenerate: `go/proto/gen/agentpb/*` via `bash go/scripts/generate-proto.sh`

**Interfaces:**
- Produces: `agentpb.RegistryAuth{RegistryHost, Username, Password string}`; `(*agentpb.CreateContainerRequest).GetRegistryAuth() *agentpb.RegistryAuth`.

- [ ] **Step 1: Add the message + field**

In `CreateContainerRequest`, append:
```protobuf
    // Optional credentials for pulling the image from a private registry.
    // When unset, the agent performs an anonymous pull (public images).
    optional RegistryAuth registry_auth = 9;
```
And add a new top-level message:
```protobuf
// RegistryAuth carries credentials for a single registry pull. The agent only
// presents these credentials to a request host matching registry_host.
message RegistryAuth {
    string registry_host = 1; // e.g. "docker.io", "ghcr.io", "registry.wendy.sh"
    string username      = 2;
    string password      = 3; // password or token
}
```

- [ ] **Step 2: Regenerate bindings**

Run: `bash go/scripts/generate-proto.sh`
Expected: no errors; `git diff --stat go/proto/gen/agentpb` shows changes to the generated container-service files.

- [ ] **Step 3: Verify it compiles**

Run: `cd go && go build ./proto/gen/agentpb/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add Proto/wendy/agent/services/v1/wendy_agent_v1_container_service.proto go/proto/gen/agentpb
git commit -m "feat(proto): add RegistryAuth to CreateContainerRequest

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Agent — thread `RegistryAuth` into the pull resolver

**Files:**
- Create: `go/internal/agent/containerd/registryauth.go`
- Create: `go/internal/agent/containerd/registryauth_test.go`
- Modify: `go/internal/agent/containerd/client.go` (the `GetImage`→`Pull` fallback, ~lines 524-536, inside `CreateContainerWithProgress`)
- Modify: `go/internal/agent/services/container_service.go` — `RunContainer`'s `createReq` build (~line 230-237) to forward `RegistryAuth` (CreateContainer/CreateContainerWithProgress already pass the whole `req`).

**Interfaces:**
- Consumes: `agentpb.RegistryAuth` (Task 1).
- Produces: `func registryHostMatches(requested, configured string) bool`; `func authorizerResolver(auth *agentpb.RegistryAuth) remotes.Resolver` (returns a `docker.NewResolver` with an authorizer).

- [ ] **Step 1: Write the failing test for host matching**

`go/internal/agent/containerd/registryauth_test.go`:
```go
package containerd

import "testing"

func TestRegistryHostMatches(t *testing.T) {
	tests := []struct {
		name, requested, configured string
		want                        bool
	}{
		{"docker hub canonical", "registry-1.docker.io", "docker.io", true},
		{"docker hub index alias", "index.docker.io", "docker.io", true},
		{"exact match", "ghcr.io", "ghcr.io", true},
		{"empty configured matches nothing", "ghcr.io", "", false},
		{"mismatch", "ghcr.io", "docker.io", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := registryHostMatches(tt.requested, tt.configured); got != tt.want {
				t.Errorf("registryHostMatches(%q,%q)=%v want %v", tt.requested, tt.configured, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `cd go && go test ./internal/agent/containerd/ -run TestRegistryHostMatches`
Expected: FAIL (undefined: registryHostMatches).

- [ ] **Step 3: Implement `registryauth.go`**

```go
package containerd

import (
	"strings"

	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/remotes/docker"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// dockerHubAliases are the hosts containerd may contact for Docker Hub images.
var dockerHubAliases = map[string]bool{
	"docker.io":           true,
	"index.docker.io":     true,
	"registry-1.docker.io": true,
}

// registryHostMatches reports whether a credential configured for `configured`
// should be presented to a request to `requested`. An empty `configured`
// matches nothing (credentials are never presented to an unknown host).
func registryHostMatches(requested, configured string) bool {
	if configured == "" {
		return false
	}
	if strings.EqualFold(requested, configured) {
		return true
	}
	return dockerHubAliases[strings.ToLower(requested)] && dockerHubAliases[strings.ToLower(configured)]
}

// authorizerResolver builds a docker resolver that presents the supplied
// credentials only to the configured registry host. Returns nil when auth is
// empty (caller should fall back to the default anonymous resolver).
func authorizerResolver(auth *agentpb.RegistryAuth) remotes.Resolver {
	if auth == nil || (auth.GetUsername() == "" && auth.GetPassword() == "") {
		return nil
	}
	configured := auth.GetRegistryHost()
	authorizer := docker.NewDockerAuthorizer(docker.WithAuthCreds(func(host string) (string, string, error) {
		if registryHostMatches(host, configured) {
			return auth.GetUsername(), auth.GetPassword(), nil
		}
		return "", "", nil
	}))
	return docker.NewResolver(docker.ResolverOptions{
		Hosts: docker.ConfigureDefaultRegistries(docker.WithAuthorizer(authorizer)),
	})
}
```

- [ ] **Step 4: Run it to confirm it passes**

Run: `cd go && go test ./internal/agent/containerd/ -run TestRegistryHostMatches`
Expected: PASS.

- [ ] **Step 5: Wire the resolver into the pull fallback**

In `client.go`, the fallback currently reads (around line 524-536):
```go
	image, err = c.client.GetImage(ctx, imageName)
	if err != nil {
		c.logger.Info("Image not in local store, attempting pull from registry",
			zap.String("image", imageName),
		)
		pullOpts := []containerd.RemoteOpt{containerd.WithPullUnpack}
		if isLocalRegistryImage(imageName) {
			pullOpts = append(pullOpts,
				containerd.WithResolver(docker.NewResolver(docker.ResolverOptions{PlainHTTP: true})),
			)
		}
		image, err = c.client.Pull(ctx, imageName, pullOpts...)
		if err != nil {
			return fmt.Errorf("getting/pulling image %q: %w", imageName, err)
		}
	}
```
Change the middle so a non-local image with auth uses the authorizer resolver:
```go
		pullOpts := []containerd.RemoteOpt{containerd.WithPullUnpack}
		if isLocalRegistryImage(imageName) {
			pullOpts = append(pullOpts,
				containerd.WithResolver(docker.NewResolver(docker.ResolverOptions{PlainHTTP: true})),
			)
		} else if r := authorizerResolver(req.GetRegistryAuth()); r != nil {
			pullOpts = append(pullOpts, containerd.WithResolver(r))
		}
		image, err = c.client.Pull(ctx, imageName, pullOpts...)
		if err != nil {
			return fmt.Errorf("getting/pulling image %q: %w", imageName, err)
		}
```
(`req` is the `*agentpb.CreateContainerRequest` already in scope in `CreateContainerWithProgress`.)

- [ ] **Step 6: Forward RegistryAuth in RunContainer's createReq**

In `container_service.go` `RunContainer`, add to the `createReq` literal (~line 230):
```go
		RegistryAuth:  req.GetRegistryAuth(),
```
Note: `RunContainerLayersRequest` has no `registry_auth` field, so `req.GetRegistryAuth()` is on `CreateContainerRequest` only. RunContainer's `req` is `*RunContainerLayersRequest` — it has no such method. So instead leave RunContainer untouched (layer-upload path never pulls). Only CreateContainer/CreateContainerWithProgress carry RegistryAuth, and they pass the whole `req` to the containerd client, which now reads `req.GetRegistryAuth()`. **Skip this step's code change; confirm RunContainer build is unaffected.**

- [ ] **Step 7: Build + test the agent packages**

Run: `cd go && go build ./internal/agent/... && go test ./internal/agent/containerd/ ./internal/agent/services/`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add go/internal/agent/containerd/registryauth.go go/internal/agent/containerd/registryauth_test.go go/internal/agent/containerd/client.go
git commit -m "feat(agent): pull from private registries using RegistryAuth

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: CLI catalog package

**Files:**
- Create: `go/internal/cli/catalog/catalog.go`
- Create: `go/internal/cli/catalog/catalog.json`
- Create: `go/internal/cli/catalog/catalog_test.go`

**Interfaces:**
- Produces:
  - `type Entry struct { Name, Image, Description string; DefaultConfig appconfig.AppConfig }`
  - `func Load() ([]Entry, error)` — parses the embedded JSON.
  - `func Lookup(name string) (Entry, bool)`.

- [ ] **Step 1: Write `catalog.json`**

`go/internal/cli/catalog/catalog.json`:
```json
[
  {
    "name": "redis",
    "image": "docker.io/library/redis:7",
    "description": "In-memory key-value store",
    "defaultConfig": {
      "appId": "redis",
      "version": "7",
      "entitlements": [
        { "type": "network", "ports": [{ "host": 6379, "container": 6379 }] },
        { "type": "persist", "name": "data", "path": "/data" }
      ]
    }
  },
  {
    "name": "postgres",
    "image": "docker.io/library/postgres:16",
    "description": "PostgreSQL relational database",
    "defaultConfig": {
      "appId": "postgres",
      "version": "16",
      "entitlements": [
        { "type": "network", "ports": [{ "host": 5432, "container": 5432 }] },
        { "type": "persist", "name": "data", "path": "/var/lib/postgresql/data" }
      ]
    }
  },
  {
    "name": "homeassistant",
    "image": "ghcr.io/home-assistant/home-assistant:stable",
    "description": "Home Assistant smart-home hub",
    "defaultConfig": {
      "appId": "homeassistant",
      "version": "stable",
      "entitlements": [
        { "type": "network", "mode": "host" },
        { "type": "persist", "name": "config", "path": "/config" }
      ]
    }
  },
  {
    "name": "mosquitto",
    "image": "docker.io/library/eclipse-mosquitto:2",
    "description": "Eclipse Mosquitto MQTT broker",
    "defaultConfig": {
      "appId": "mosquitto",
      "version": "2",
      "entitlements": [
        { "type": "network", "ports": [{ "host": 1883, "container": 1883 }] },
        { "type": "persist", "name": "data", "path": "/mosquitto/data" }
      ]
    }
  },
  {
    "name": "grafana",
    "image": "docker.io/grafana/grafana:11",
    "description": "Grafana observability dashboards",
    "defaultConfig": {
      "appId": "grafana",
      "version": "11",
      "entitlements": [
        { "type": "network", "ports": [{ "host": 3000, "container": 3000 }] },
        { "type": "persist", "name": "data", "path": "/var/lib/grafana" }
      ]
    }
  }
]
```

- [ ] **Step 2: Write the failing test**

`go/internal/cli/catalog/catalog_test.go`:
```go
package catalog

import "testing"

func TestLoadParsesAndValidates(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("catalog is empty")
	}
	for _, e := range entries {
		if e.Name == "" || e.Image == "" {
			t.Errorf("entry %+v missing name or image", e)
		}
		if e.DefaultConfig.AppID == "" {
			t.Errorf("entry %q has empty defaultConfig.appId", e.Name)
		}
	}
}

func TestLookup(t *testing.T) {
	if _, ok := Lookup("redis"); !ok {
		t.Error("expected to find redis")
	}
	if _, ok := Lookup("nope-not-real"); ok {
		t.Error("did not expect to find nope-not-real")
	}
}
```

- [ ] **Step 3: Run to confirm it fails**

Run: `cd go && go test ./internal/cli/catalog/`
Expected: FAIL (undefined: Load, Lookup).

- [ ] **Step 4: Implement `catalog.go`**

```go
// Package catalog provides a curated list of common container apps that can be
// installed onto a device with `wendy device apps install`.
package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

//go:embed catalog.json
var catalogJSON []byte

// Entry is a single installable app in the curated catalog.
type Entry struct {
	Name          string              `json:"name"`
	Image         string              `json:"image"`
	Description   string              `json:"description"`
	DefaultConfig appconfig.AppConfig `json:"defaultConfig"`
}

// Load parses the embedded catalog.
func Load() ([]Entry, error) {
	var entries []Entry
	if err := json.Unmarshal(catalogJSON, &entries); err != nil {
		return nil, fmt.Errorf("parsing embedded catalog: %w", err)
	}
	return entries, nil
}

// Lookup returns the catalog entry with the given name.
func Lookup(name string) (Entry, bool) {
	entries, err := Load()
	if err != nil {
		return Entry{}, false
	}
	for _, e := range entries {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}
```

- [ ] **Step 5: Run to confirm it passes**

Run: `cd go && go test ./internal/cli/catalog/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/catalog
git commit -m "feat(cli): curated app catalog package

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: CLI `wendy device apps install` command (core, public-image path)

**Files:**
- Create: `go/internal/cli/commands/apps_install.go`
- Create: `go/internal/cli/commands/apps_install_test.go`
- Modify: `go/internal/cli/commands/apps.go` — register `newAppsInstallCmd()` in `newAppsCmd().AddCommand(...)` (~line 38-43).

**Interfaces:**
- Consumes: `catalog.Lookup`, `catalog.Load` (Task 3); `agentpb.CreateContainerRequest`, `agentpb.RegistryAuth`, `agentpb.StartContainerRequest` (Task 1); `resolveTarget`, `SelectedDevice`, `cliSuccess` (existing in `apps.go`).
- Produces:
  - `func resolveInstallSource(arg string) (image string, cfg appconfig.AppConfig, err error)` — catalog name → entry; otherwise raw image ref with a minimal config (appId derived from the image's repo basename).
  - `func deriveAppID(image string) string` — repo basename without tag/digest (e.g. `docker.io/library/redis:7` → `redis`).
  - `func registryHostFromImage(image string) string` — registry host of a ref (defaults to `docker.io`).

- [ ] **Step 1: Write the failing test**

`go/internal/cli/commands/apps_install_test.go`:
```go
package commands

import "testing"

func TestDeriveAppID(t *testing.T) {
	cases := map[string]string{
		"docker.io/library/redis:7":                  "redis",
		"redis:7":                                     "redis",
		"ghcr.io/home-assistant/home-assistant:stable": "home-assistant",
		"registry.wendy.sh/org-1/edge-api@sha256:abc": "edge-api",
	}
	for in, want := range cases {
		if got := deriveAppID(in); got != want {
			t.Errorf("deriveAppID(%q)=%q want %q", in, got, want)
		}
	}
}

func TestRegistryHostFromImage(t *testing.T) {
	cases := map[string]string{
		"redis:7":                       "docker.io",
		"docker.io/library/redis:7":     "docker.io",
		"ghcr.io/x/y:z":                 "ghcr.io",
		"registry.wendy.sh:5000/a/b:c":  "registry.wendy.sh:5000",
	}
	for in, want := range cases {
		if got := registryHostFromImage(in); got != want {
			t.Errorf("registryHostFromImage(%q)=%q want %q", in, got, want)
		}
	}
}

func TestResolveInstallSourceCatalog(t *testing.T) {
	img, cfg, err := resolveInstallSource("redis")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if img == "" || cfg.AppID == "" {
		t.Errorf("expected catalog image+config, got img=%q appId=%q", img, cfg.AppID)
	}
}

func TestResolveInstallSourceRawImage(t *testing.T) {
	img, cfg, err := resolveInstallSource("ghcr.io/foo/bar:1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if img != "ghcr.io/foo/bar:1" || cfg.AppID != "bar" {
		t.Errorf("got img=%q appId=%q", img, cfg.AppID)
	}
}
```

- [ ] **Step 2: Run to confirm it fails**

Run: `cd go && go test ./internal/cli/commands/ -run 'TestDeriveAppID|TestRegistryHostFromImage|TestResolveInstallSource'`
Expected: FAIL (undefined functions).

- [ ] **Step 3: Implement helpers + command in `apps_install.go`**

```go
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/catalog"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// deriveAppID returns a container-safe app id from an image reference: the
// repository basename, without registry, tag, or digest.
func deriveAppID(image string) string {
	ref := image
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		ref = ref[:i]
	}
	// Strip tag: the last ':' that appears after the last '/'.
	if slash := strings.LastIndexByte(ref, '/'); slash >= 0 {
		if colon := strings.LastIndexByte(ref[slash:], ':'); colon >= 0 {
			ref = ref[:slash+colon]
		}
	} else if colon := strings.LastIndexByte(ref, ':'); colon >= 0 {
		ref = ref[:colon]
	}
	if slash := strings.LastIndexByte(ref, '/'); slash >= 0 {
		ref = ref[slash+1:]
	}
	return ref
}

// registryHostFromImage returns the registry host of an image ref, defaulting
// to docker.io for short names (no host component).
func registryHostFromImage(image string) string {
	first := image
	if i := strings.IndexByte(first, '/'); i >= 0 {
		first = first[:i]
	} else {
		return "docker.io"
	}
	// A host component contains a '.' or ':' or is "localhost".
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return first
	}
	return "docker.io"
}

// resolveInstallSource maps a CLI argument to an image reference and a default
// app config. A catalog name uses the curated entry; anything else is treated
// as a raw image reference with a minimal config.
func resolveInstallSource(arg string) (string, appconfig.AppConfig, error) {
	if e, ok := catalog.Lookup(arg); ok {
		return e.Image, e.DefaultConfig, nil
	}
	if arg == "" {
		return "", appconfig.AppConfig{}, fmt.Errorf("no app name or image specified")
	}
	cfg := appconfig.AppConfig{AppID: deriveAppID(arg)}
	return arg, cfg, nil
}

func newAppsInstallCmd() *cobra.Command {
	var username, password string
	var passwordStdin bool
	var nameOverride string

	cmd := &cobra.Command{
		Use:   "install [name|image]",
		Short: "Install a common app or container image onto the device",
		Long: "Install an app from the curated catalog (e.g. redis, postgres, " +
			"homeassistant) or any container image reference. The device pulls " +
			"the image directly from the registry.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			arg := ""
			if len(args) > 0 {
				arg = args[0]
			} else {
				picked, err := pickInstallApp(ctx)
				if err != nil {
					return err
				}
				arg = picked
			}

			image, cfg, err := resolveInstallSource(arg)
			if err != nil {
				return err
			}
			if nameOverride != "" {
				cfg.AppID = nameOverride
			}

			auth, err := resolveRegistryAuth(ctx, image, username, password, passwordStdin)
			if err != nil {
				return err
			}

			target, err := resolveTarget(ctx)
			if err != nil {
				return err
			}
			defer target.Close()
			if target.Agent == nil {
				return fmt.Errorf("installing apps is supported on agent-connected devices only")
			}

			cfgBytes, err := json.Marshal(cfg)
			if err != nil {
				return fmt.Errorf("encoding app config: %w", err)
			}

			cliLogln("Installing %s (image %s) on the device…", cfg.AppID, image)
			if _, err := target.Agent.ContainerService.CreateContainer(ctx, &agentpb.CreateContainerRequest{
				ImageName:    image,
				AppName:      cfg.AppID,
				AppConfig:    cfgBytes,
				RegistryAuth: auth,
			}); err != nil {
				return fmt.Errorf("installing %s: %w", cfg.AppID, err)
			}

			if _, err := target.Agent.ContainerService.StartContainer(ctx, &agentpb.StartContainerRequest{
				AppName:       cfg.AppID,
				RestartPolicy: &agentpb.RestartPolicy{Mode: agentpb.RestartPolicyMode_UNLESS_STOPPED},
			}); err != nil {
				return fmt.Errorf("starting %s: %w", cfg.AppID, err)
			}

			cliSuccess("Installed and started %s.", cfg.AppID)
			return nil
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "Registry username")
	cmd.Flags().StringVar(&password, "password", "", "Registry password or token")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "Read the registry password from stdin")
	cmd.Flags().StringVar(&nameOverride, "name", "", "Override the installed app name")
	return cmd
}

// resolveRegistryAuth returns RegistryAuth for the pull, or nil for anonymous.
// Order: explicit flags → (Task 5 adds docker config + cloud creds) → anonymous.
func resolveRegistryAuth(ctx context.Context, image, username, password string, passwordStdin bool) (*agentpb.RegistryAuth, error) {
	if passwordStdin {
		data, err := os.ReadFile("/dev/stdin")
		if err != nil {
			return nil, fmt.Errorf("reading password from stdin: %w", err)
		}
		password = strings.TrimRight(string(data), "\r\n")
	}
	if username != "" || password != "" {
		return &agentpb.RegistryAuth{
			RegistryHost: registryHostFromImage(image),
			Username:     username,
			Password:     password,
		}, nil
	}
	return nil, nil
}
```

Note: `StartContainer` returns a server stream (`grpc.ServerStreamingClient[...]`). Adjust the call to consume the stream like `newAppsStartCmd` does (the `_, err :=` form will not compile for a streaming RPC). Use:
```go
			startStream, err := target.Agent.ContainerService.StartContainer(ctx, &agentpb.StartContainerRequest{
				AppName:       cfg.AppID,
				RestartPolicy: &agentpb.RestartPolicy{Mode: agentpb.RestartPolicyMode_UNLESS_STOPPED},
			})
			if err != nil {
				return fmt.Errorf("starting %s: %w", cfg.AppID, err)
			}
			for {
				_, rerr := startStream.Recv()
				if rerr != nil {
					break // io.EOF on clean detach-style start
				}
			}
```

- [ ] **Step 4: Add a temporary `pickInstallApp` stub (catalog-only; org apps in Task 5)**

In `apps_install.go`:
```go
// pickInstallApp shows an interactive picker of installable apps. Task 5 adds
// org (cloud) apps; for now it lists the curated catalog only.
func pickInstallApp(ctx context.Context) (string, error) {
	entries, err := catalog.Load()
	if err != nil {
		return "", err
	}
	var items []tui.PickerItem
	for _, e := range entries {
		items = append(items, tui.PickerItem{Name: e.Name, Description: e.Description, Value: e.Name})
	}
	return runInstallPicker("Select an app to install", items)
}
```
Add `runInstallPicker` mirroring `pickApp`'s bubbletea flow (returns the selected `Value.(string)`), and import `tea "github.com/charmbracelet/bubbletea"` + `"github.com/wendylabsinc/wendy/go/internal/cli/tui"`. Reuse the exact pattern from `pickApp` in `apps.go` (NewPickerWithTitle, PickerAddMsg, PickerDoneMsg, Selected()).

- [ ] **Step 5: Register the command**

In `apps.go` `newAppsCmd`:
```go
	cmd.AddCommand(
		newAppsListCmd(),
		newAppsInstallCmd(),
		newAppsStartCmd(),
		newAppsStopCmd(),
		newAppsRemoveCmd(),
	)
```

- [ ] **Step 6: Run unit tests + build**

Run: `cd go && go build ./internal/cli/... && go test ./internal/cli/commands/ -run 'TestDeriveAppID|TestRegistryHostFromImage|TestResolveInstallSource'`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add go/internal/cli/commands/apps_install.go go/internal/cli/commands/apps_install_test.go go/internal/cli/commands/apps.go
git commit -m "feat(cli): wendy device apps install (catalog + raw image)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Org app releases in the picker + cloud/docker auth resolution

**Files:**
- Create: `go/internal/cli/commands/apps_install_org.go`
- Create: `go/internal/cli/commands/apps_install_org_test.go`
- Modify: `go/internal/cli/commands/apps_install.go` — `pickInstallApp` to append org apps; `resolveRegistryAuth` to add docker-config + cloud-credential fallbacks.

**Interfaces:**
- Consumes: `config.Load()`, `config.AuthConfig` (`go/internal/shared/config`); `cloudpb.NewDeploymentServiceClient`, `cloudpb.ListAppReleasesRequest` (`go/proto/gen/cloudpb`); `dialCloudGRPC` (`cloud_tunnel.go`).
- Produces:
  - `func dockerConfigAuth(host string) (*agentpb.RegistryAuth, bool)` — reads `~/.docker/config.json` for `host`.
  - `func listOrgApps(ctx context.Context) ([]orgApp, error)` where `type orgApp struct { Name, Image string }` — best-effort; returns `(nil, nil)` when not logged in.

- [ ] **Step 1: Write the failing test for docker-config parsing**

`go/internal/cli/commands/apps_install_org_test.go`:
```go
package commands

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestDockerConfigAuth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	enc := base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	cfg := `{"auths":{"ghcr.io":{"auth":"` + enc + `"}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := dockerConfigAuth("ghcr.io")
	if !ok {
		t.Fatal("expected creds for ghcr.io")
	}
	if got.GetUsername() != "alice" || got.GetPassword() != "s3cret" {
		t.Errorf("got %q/%q", got.GetUsername(), got.GetPassword())
	}
	if _, ok := dockerConfigAuth("missing.example.com"); ok {
		t.Error("did not expect creds for missing host")
	}
}
```

- [ ] **Step 2: Run to confirm it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestDockerConfigAuth`
Expected: FAIL (undefined: dockerConfigAuth).

- [ ] **Step 3: Implement `apps_install_org.go`**

```go
package commands

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// dockerConfigAuth reads ~/.docker/config.json (honoring DOCKER_CONFIG) and
// returns credentials for host if present.
func dockerConfigAuth(host string) (*agentpb.RegistryAuth, bool) {
	base := os.Getenv("DOCKER_CONFIG")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, false
		}
		base = filepath.Join(home, ".docker")
	}
	data, err := os.ReadFile(filepath.Join(base, "config.json"))
	if err != nil {
		return nil, false
	}
	var parsed struct {
		Auths map[string]struct {
			Auth     string `json:"auth"`
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, false
	}
	entry, ok := parsed.Auths[host]
	if !ok {
		return nil, false
	}
	user, pass := entry.Username, entry.Password
	if entry.Auth != "" {
		if dec, err := base64.StdEncoding.DecodeString(entry.Auth); err == nil {
			if i := strings.IndexByte(string(dec), ':'); i >= 0 {
				user = string(dec)[:i]
				pass = string(dec)[i+1:]
			}
		}
	}
	if user == "" && pass == "" {
		return nil, false
	}
	return &agentpb.RegistryAuth{RegistryHost: host, Username: user, Password: pass}, true
}

type orgApp struct {
	Name  string
	Image string
}

// listOrgApps returns the org's app releases from the cloud, best-effort.
// Returns (nil, nil) when the user is not logged in.
func listOrgApps(ctx context.Context) ([]orgApp, error) {
	cfg, err := config.Load()
	if err != nil || len(cfg.Auth) == 0 {
		return nil, nil
	}
	auth := &cfg.Auth[0]
	if len(auth.Certificates) == 0 {
		return nil, nil
	}
	orgID := int32(auth.Certificates[0].OrganizationID)

	conn, err := dialCloudGRPC(auth)
	if err != nil {
		return nil, fmt.Errorf("connecting to cloud: %w", err)
	}
	defer conn.Close()

	stream, err := cloudpb.NewDeploymentServiceClient(conn).ListAppReleases(ctx, &cloudpb.ListAppReleasesRequest{
		OrganizationId: orgID,
	})
	if err != nil {
		return nil, fmt.Errorf("listing app releases: %w", err)
	}
	seen := map[string]bool{}
	var apps []orgApp
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("receiving app releases: %w", err)
		}
		for _, r := range resp.GetAppReleases() {
			if seen[r.GetAppId()] {
				continue
			}
			seen[r.GetAppId()] = true
			apps = append(apps, orgApp{Name: r.GetAppId()})
		}
	}
	return apps, nil
}
```
Note: resolving an org app's full image ref + pull credentials uses `GetPullCredentials(app_release_id)`. For the draft, the picker lists org app names for visibility; selecting one resolves its image via `GetPullCredentials` at install time. If wiring `GetPullCredentials` proves larger than the draft warrants, leave org entries display-only with a clear "select by image ref" message and open a follow-up. Confirm the exact `AppRelease`/`GetPullCredentialsResponse` field names against `go/proto/gen/cloudpb` before finalizing.

- [ ] **Step 4: Run to confirm the docker-config test passes**

Run: `cd go && go test ./internal/cli/commands/ -run TestDockerConfigAuth`
Expected: PASS.

- [ ] **Step 5: Extend `resolveRegistryAuth` with docker-config fallback**

In `apps_install.go`, before the final `return nil, nil`:
```go
	if a, ok := dockerConfigAuth(registryHostFromImage(image)); ok {
		return a, nil
	}
```

- [ ] **Step 6: Append org apps to the picker**

In `pickInstallApp`, after adding catalog items:
```go
	if orgApps, oerr := listOrgApps(ctx); oerr == nil {
		for _, a := range orgApps {
			items = append(items, tui.PickerItem{Name: a.Name, Description: "your org", Value: a.Name})
		}
	} else {
		cliNotice("Could not load org apps: %v", oerr)
	}
```

- [ ] **Step 7: Build + run the package tests**

Run: `cd go && go build ./internal/cli/... && go test ./internal/cli/commands/ -run 'TestDockerConfigAuth|TestResolveInstallSource'`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add go/internal/cli/commands/apps_install_org.go go/internal/cli/commands/apps_install_org_test.go go/internal/cli/commands/apps_install.go
git commit -m "feat(cli): org app listing + docker-config/cloud auth for install

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Full-suite verification + draft PR

- [ ] **Step 1: Build everything**

Run: `cd go && go build ./...`
Expected: PASS.

- [ ] **Step 2: Run affected tests**

Run: `cd go && go test ./internal/agent/containerd/ ./internal/agent/services/ ./internal/cli/catalog/ ./internal/cli/commands/`
Expected: PASS.

- [ ] **Step 3: Vet + lint (match repo CI)**

Run: `cd go && go vet ./internal/cli/... ./internal/agent/...`
Expected: no findings. (If the repo uses golangci-lint, run `golangci-lint run` on the changed packages.)

- [ ] **Step 4: Manual smoke (optional, requires a device)**

```bash
wendy device apps install redis
wendy device apps list   # redis should appear, running
wendy device apps remove redis --force --cleanup
```

- [ ] **Step 5: Push branch and open the draft PR**

Use the `wendy-pr` skill / `gh pr create --draft`. Title: "feat: registry pull + `wendy device apps install`". Body summarizes the design doc and lists what's in scope vs follow-up (org-app `GetPullCredentials` resolution if deferred).

## Self-Review notes

- Spec §1 (proto) → Task 1. §2 (agent pull auth) → Task 2. §3 (catalog) → Task 3. §4 (command, public path + auth order) → Tasks 4 (flags, docker-config in 5) + 5 (cloud). Data flow → Tasks 2+4. Error handling (agent-only, auth failures surfaced) → Tasks 2+4. Testing → tests in each task + Task 6.
- Known risk: exact cloud `AppRelease`/`GetPullCredentials` field names must be confirmed against generated `cloudpb` during Task 5 (image-ref resolution for org apps may be deferred to a follow-up; the public-image path in Tasks 1-4 stands alone and is the core deliverable).
- `StartContainer` is a streaming RPC — Task 4 Step 3 note corrects the call shape.
