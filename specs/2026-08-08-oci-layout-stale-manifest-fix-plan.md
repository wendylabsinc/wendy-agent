# OCI Layout Stale-Manifest Fix + Debug/Crash-Loop Follow-ups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the critical stale-manifest bug in PR #1608's persistent OCI layout dir (deps changes silently never deploy), and scope the three adjacent findings from the 2026-08-08 on-device pass into a follow-up PR.

**Architecture:** Part 1 (this PR, branch `ed/oci-layout-dir-export`) makes the layout-dir pipeline correct under BuildKit's append-only `tar=false` export: the platform resolver prefers the *newest* manifest, the export step prunes superseded index entries, and a deploy-fingerprint salt bump forces one clean rebuild for anyone bitten. Part 2 (follow-up PR off `main`, suggested branch `ed/debug-crashloop-followups`) fixes `--debug` crash-looping Stagefile apps, the container replace/stop races against the restart monitor, and missing live crash-loop logs in `device logs --tail`.

**Tech Stack:** Go (CLI in `go/internal/cli/commands`, agent in `go/internal/agent`), standard `go test`, BuildKit/buildx `--output type=oci,tar=false`.

## Global Constraints

- Branch naming: everything Claude pushes is prefixed `ed/`.
- Run `gofmt -l .` from `go/` before every push; `gofmt -w` anything listed.
- Build/test from `go/`: `go build ./...`, `go test ./internal/cli/commands/ -run <Name> -v`.
- Linux-only agent tests can be pre-verified via `docker run golang:1.26 go test ./go/internal/agent/...`.
- The full bug evidence chain and reproduction transcript live in `specs/2026-08-08-on-device-test-plan.md` (main checkout, untracked) — RESULTS section.

## Background: the bug (all links verified live on hardware 2026-08-08)

1. BuildKit's `tar=false` dir export **appends** each build's manifest to `index.json`; the previous entries stay, oldest first (verified: index went 1 → 2 entries, old entry first).
2. `pickOCIDescriptor` (`go/internal/cli/commands/ocilayers.go:159`) returns the **first** platform match — the *oldest* manifest. Every reader (`readOCILayoutDirLayers`, and #1610's `adoptNativeLayers` downstream) therefore resolves the first build the layout dir ever produced.
3. `gcOCILayoutDir` keeps only index-reachable blobs — once anything rewrites the index to the old manifest (as #1610's splice does), **the correct new build's blobs are deleted**.
4. `saveDeployFingerprint` records the *current* input hash against the stale deploy, so subsequent identical runs skip the build entirely and the staleness locks in.

Net effect: after the first build lands in a layout dir, requirements/deps changes never reach the device again, with success reported every time. App-only (final COPY layer) changes masked the bug in earlier verification. Recovery for bitten users: wipe the app's layout dir (`~/Library/Caches/wendy/ocilayout/<app>-<platform>/` on macOS) — verified to heal.

---

# Part 1 — PR #1608 fixes (this branch)

### Task 1: Resolver prefers the newest manifest

**Files:**
- Modify: `go/internal/cli/commands/ocilayers.go:156-173` (`pickOCIDescriptor`)
- Test: `go/internal/cli/commands/ocilayers_test.go`

**Interfaces:**
- Consumes: `ociDescriptor` struct (`ocilayers.go:86`: `MediaType`, `Digest`, `Platform{Architecture, OS}`).
- Produces: `pickOCIDescriptor(descs []ociDescriptor, wantOS, wantArch string) *ociDescriptor` — same signature, now returns the LAST match in slice order. Tasks 2–3 rely on this.

- [ ] **Step 1: Write the failing test**

Add to `ocilayers_test.go` (reuse the file's existing synthetic-layout helpers if present; otherwise this is self-contained):

```go
func TestPickOCIDescriptorPrefersNewestPlatformMatch(t *testing.T) {
	arm := &struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
	}{Architecture: "arm64", OS: "linux"}
	descs := []ociDescriptor{
		{MediaType: "application/vnd.oci.image.manifest.v1+json", Digest: "sha256:aaaa", Platform: arm},
		{MediaType: "application/vnd.oci.image.manifest.v1+json", Digest: "sha256:bbbb", Platform: arm},
	}
	got := pickOCIDescriptor(descs, "linux", "arm64")
	if got == nil || got.Digest != "sha256:bbbb" {
		t.Fatalf("want newest (last) match sha256:bbbb, got %+v", got)
	}
}

func TestPickOCIDescriptorFallbackPrefersNewest(t *testing.T) {
	// No platform info at all: the fallback loop must also prefer the last
	// image-manifest entry (buildx appends newest last).
	descs := []ociDescriptor{
		{MediaType: "application/vnd.oci.image.manifest.v1+json", Digest: "sha256:aaaa"},
		{MediaType: "application/vnd.oci.image.manifest.v1+json", Digest: "sha256:bbbb"},
	}
	got := pickOCIDescriptor(descs, "linux", "arm64")
	if got == nil || got.Digest != "sha256:bbbb" {
		t.Fatalf("want newest (last) fallback sha256:bbbb, got %+v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd go && go test ./internal/cli/commands/ -run TestPickOCIDescriptor -v`
Expected: both FAIL with `got ...sha256:aaaa` (current code returns the first match).

- [ ] **Step 3: Reverse both selection loops**

Replace the body of `pickOCIDescriptor`:

```go
// pickOCIDescriptor chooses the best descriptor for the target platform:
// the NEWEST exact os/arch match if present, otherwise the newest image
// manifest or image index (skipping attestation/unknown entries).
//
// Newest means LAST in slice order: buildx's tar=false dir export APPENDS
// each build's manifest to index.json, so several entries can match the
// platform and the current build is always the final one. Resolving the
// first match shipped the layout dir's first-ever build forever (stale
// deps bug, on-device pass 2026-08-08).
func pickOCIDescriptor(descs []ociDescriptor, wantOS, wantArch string) *ociDescriptor {
	for i := len(descs) - 1; i >= 0; i-- {
		d := &descs[i]
		if d.Platform != nil && d.Platform.OS == wantOS && d.Platform.Architecture == wantArch {
			return d
		}
	}
	for i := len(descs) - 1; i >= 0; i-- {
		d := &descs[i]
		if isOCIImageManifestMediaType(d.MediaType) || isOCIImageIndexMediaType(d.MediaType) {
			return d
		}
	}
	return nil
}
```

- [ ] **Step 4: Run the new tests plus the whole package**

Run: `cd go && go test ./internal/cli/commands/ -run TestPickOCIDescriptor -v && go test ./internal/cli/commands/`
Expected: new tests PASS; full package green (existing single-manifest fixtures are order-insensitive, but if any test pinned first-entry semantics, update it to pin newest-entry semantics instead — that is the new contract).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/ocilayers.go go/internal/cli/commands/ocilayers_test.go
git commit -m "fix(cli): resolve the newest OCI index manifest, not the oldest

buildx's tar=false dir export appends manifests to index.json; picking
the first platform match pinned every read to the layout dir's first
build, so deps changes never deployed (found in on-device verification)."
```

### Task 2: Prune superseded index entries after every dir export

**Files:**
- Modify: `go/internal/cli/commands/ocilayers.go` (add `pruneOCILayoutDirIndex`; call it from `buildImageToOCILayoutDirWithDocker:817-826`)
- Test: `go/internal/cli/commands/ocilayers_test.go`

**Interfaces:**
- Consumes: `pickOCIDescriptor` (Task 1 semantics: newest), `parseOCIPlatform(platform string) (os, arch string)` (already in `ocilayers.go`).
- Produces: `pruneOCILayoutDirIndex(dir, platform string) error` — rewrites `dir/index.json` to reference only the newest matching image manifest. Task 3's GC test depends on this running before `gcOCILayoutDir`.

- [ ] **Step 1: Write the failing test**

```go
// writeLayoutIndex writes an index.json with the given raw manifest entries.
func writeLayoutIndex(t *testing.T, dir string, entries ...string) {
	t.Helper()
	idx := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[` +
		strings.Join(entries, ",") + `]}`
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(idx), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPruneOCILayoutDirIndexKeepsOnlyNewestPlatformManifest(t *testing.T) {
	dir := t.TempDir()
	old := `{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:aaaa","size":1,"platform":{"architecture":"arm64","os":"linux"}}`
	att := `{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:cccc","size":1,"platform":{"architecture":"unknown","os":"unknown"},"annotations":{"vnd.docker.reference.type":"attestation-manifest"}}`
	niu := `{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:bbbb","size":1,"platform":{"architecture":"arm64","os":"linux"},"annotations":{"org.opencontainers.image.created":"x"}}`
	writeLayoutIndex(t, dir, old, niu, att)

	if err := pruneOCILayoutDirIndex(dir, "linux/arm64"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Manifests []json.RawMessage `json:"manifests"`
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Manifests) != 1 {
		t.Fatalf("want exactly 1 manifest entry after prune, got %d: %s", len(idx.Manifests), data)
	}
	// The kept entry must be the newest platform match, raw bytes preserved
	// (annotations intact).
	if !strings.Contains(string(idx.Manifests[0]), "sha256:bbbb") ||
		!strings.Contains(string(idx.Manifests[0]), "org.opencontainers.image.created") {
		t.Fatalf("kept entry lost identity or annotations: %s", idx.Manifests[0])
	}
}

func TestPruneOCILayoutDirIndexSingleEntryNoop(t *testing.T) {
	dir := t.TempDir()
	only := `{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:aaaa","size":1,"platform":{"architecture":"arm64","os":"linux"}}`
	writeLayoutIndex(t, dir, only)
	before, _ := os.ReadFile(filepath.Join(dir, "index.json"))
	if err := pruneOCILayoutDirIndex(dir, "linux/arm64"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "index.json"))
	if !bytes.Equal(before, after) {
		t.Fatalf("single-entry index must be untouched\nbefore: %s\nafter: %s", before, after)
	}
}

func TestPruneOCILayoutDirIndexNoMatchErrorsAndPreservesIndex(t *testing.T) {
	dir := t.TempDir()
	amd := `{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:aaaa","size":1,"platform":{"architecture":"amd64","os":"linux"}}`
	writeLayoutIndex(t, dir, amd)
	before, _ := os.ReadFile(filepath.Join(dir, "index.json"))
	if err := pruneOCILayoutDirIndex(dir, "linux/arm64"); err == nil {
		t.Fatal("want error when no manifest matches the platform")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "index.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("index must be untouched on error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd go && go test ./internal/cli/commands/ -run TestPruneOCILayoutDirIndex -v`
Expected: FAIL — `pruneOCILayoutDirIndex` undefined.

- [ ] **Step 3: Implement the prune**

Add to `ocilayers.go` (near `gcOCILayoutDir`). Raw-message parallel parsing preserves descriptor fields we don't model (annotations, artifactType):

```go
// pruneOCILayoutDirIndex rewrites dir's index.json to reference only the
// newest image manifest matching platform. buildx's tar=false export
// appends manifests, so without pruning the index accumulates one entry
// per build; older entries (and attestation manifests) keep superseded
// blobs GC-reachable and — before pickOCIDescriptor preferred the newest
// — pinned every reader to the first build. Runs under the caller's
// layout-dir flock. A single already-pruned index is left byte-identical.
func pruneOCILayoutDirIndex(dir, platform string) error {
	indexPath := filepath.Join(dir, "index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return fmt.Errorf("reading index.json for prune: %w", err)
	}
	var typed struct {
		Manifests []ociDescriptor `json:"manifests"`
	}
	var raw struct {
		Manifests []json.RawMessage `json:"manifests"`
	}
	if err := json.Unmarshal(data, &typed); err != nil {
		return fmt.Errorf("parsing index.json for prune: %w", err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing index.json for prune: %w", err)
	}
	if len(typed.Manifests) != len(raw.Manifests) {
		return fmt.Errorf("index.json manifest parse mismatch (%d vs %d)", len(typed.Manifests), len(raw.Manifests))
	}
	if len(typed.Manifests) == 1 {
		// Common case after a prior prune: nothing to do, keep bytes stable.
		if chosen := pickOCIDescriptor(typed.Manifests, "", ""); chosen != nil {
		}
	}

	wantOS, wantArch := parseOCIPlatform(platform)
	chosen := pickOCIDescriptor(typed.Manifests, wantOS, wantArch)
	if chosen == nil {
		return fmt.Errorf("no manifest for %s in index.json; refusing to prune", platform)
	}
	chosenIdx := -1
	for i := range typed.Manifests {
		if &typed.Manifests[i] == chosen {
			chosenIdx = i
			break
		}
	}
	if chosenIdx < 0 {
		return fmt.Errorf("internal: chosen descriptor not found in index")
	}
	if len(typed.Manifests) == 1 && chosenIdx == 0 {
		return nil
	}

	out := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests":     []json.RawMessage{raw.Manifests[chosenIdx]},
	}
	pruned, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshaling pruned index.json: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".index-*.json")
	if err != nil {
		return fmt.Errorf("staging pruned index.json: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(pruned); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing pruned index.json: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("closing pruned index.json: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("chmod pruned index.json: %w", err)
	}
	if err := os.Rename(tmpName, indexPath); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("replacing index.json: %w", err)
	}
	return nil
}
```

Delete the leftover empty `if len(typed.Manifests) == 1 { ... }` block above `wantOS` — the real single-entry early-return is the `len==1 && chosenIdx==0` check. (It is shown here only so you notice it must NOT survive; the final function has exactly one single-entry check.)

Then wire it into `buildImageToOCILayoutDirWithDocker` (`ocilayers.go:817`), after the successful build:

```go
	if err := buildImageWithBuildxOCIExport(ctx, cwd, dockerfile, platform, buildArgs, destDir, true, stdout, stderr); err != nil {
		return err
	}
	// The export appended this build's manifest; drop every older entry so
	// readers and GC see exactly one current image.
	if err := pruneOCILayoutDirIndex(destDir, platform); err != nil {
		return fmt.Errorf("pruning OCI layout index: %w", err)
	}
	return nil
```

(The current body `return buildImageWithBuildxOCIExport(...)` becomes the block above.)

- [ ] **Step 4: Run the tests**

Run: `cd go && go test ./internal/cli/commands/ -run TestPruneOCILayoutDirIndex -v && go test ./internal/cli/commands/`
Expected: PASS, package green.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/ocilayers.go go/internal/cli/commands/ocilayers_test.go
git commit -m "fix(cli): prune superseded manifests from the OCI layout index after export

Keeps index.json at exactly one current image manifest so readers and
the blob GC agree on what the layout contains."
```

### Task 3: GC regression test — superseded build's blobs are collected, current kept

**Files:**
- Test: `go/internal/cli/commands/ocilayers_test.go` (extend; `gcOCILayoutDir` itself should need no change)

**Interfaces:**
- Consumes: `pruneOCILayoutDirIndex` (Task 2), `gcOCILayoutDir(dir string) error` (`ocilayers.go:467`), existing test helpers for writing blobs if present (otherwise the helper below).

- [ ] **Step 1: Write the test**

```go
// writeBlob writes content under blobs/sha256/<sha256(content)> and returns
// the digest string.
func writeBlob(t *testing.T, dir string, content []byte) string {
	t.Helper()
	sum := sha256.Sum256(content)
	hexd := hex.EncodeToString(sum[:])
	p := filepath.Join(dir, "blobs", "sha256")
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, hexd), content, 0o600); err != nil {
		t.Fatal(err)
	}
	return "sha256:" + hexd
}

func TestPruneThenGCDropsSupersededBuild(t *testing.T) {
	dir := t.TempDir()
	// Old build: config + one layer + manifest referencing them.
	oldCfg := writeBlob(t, dir, []byte(`{"old":"config"}`))
	oldLayer := writeBlob(t, dir, []byte("old-layer"))
	oldManifest := writeBlob(t, dir, []byte(`{"config":{"digest":"`+oldCfg+`"},"layers":[{"digest":"`+oldLayer+`"}]}`))
	// New build: shares nothing with the old one.
	newCfg := writeBlob(t, dir, []byte(`{"new":"config"}`))
	newLayer := writeBlob(t, dir, []byte("new-layer"))
	newManifest := writeBlob(t, dir, []byte(`{"config":{"digest":"`+newCfg+`"},"layers":[{"digest":"`+newLayer+`"}]}`))

	entry := func(digest string) string {
		return `{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + digest + `","size":1,"platform":{"architecture":"arm64","os":"linux"}}`
	}
	writeLayoutIndex(t, dir, entry(oldManifest), entry(newManifest))

	if err := pruneOCILayoutDirIndex(dir, "linux/arm64"); err != nil {
		t.Fatal(err)
	}
	if err := gcOCILayoutDir(dir); err != nil {
		t.Fatal(err)
	}

	mustExist := func(digest string) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(dir, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))); err != nil {
			t.Fatalf("blob %s should survive GC: %v", digest, err)
		}
	}
	mustBeGone := func(digest string) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(dir, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))); !os.IsNotExist(err) {
			t.Fatalf("blob %s should be GC'd, stat err=%v", digest, err)
		}
	}
	mustExist(newManifest)
	mustExist(newCfg)
	mustExist(newLayer)
	mustBeGone(oldManifest)
	mustBeGone(oldCfg)
	mustBeGone(oldLayer)
}
```

- [ ] **Step 2: Run it**

Run: `cd go && go test ./internal/cli/commands/ -run TestPruneThenGCDropsSupersededBuild -v`
Expected: PASS with Tasks 1–2 in place (this is a regression lock, not a new feature — if it fails, the prune or GC has a bug; fix there, not in the test).

- [ ] **Step 3: Commit**

```bash
git add go/internal/cli/commands/ocilayers_test.go
git commit -m "test(cli): lock prune+GC contract for the OCI layout dir"
```

### Task 4: Deploy-fingerprint salt bump (invalidate poisoned fingerprints)

**Files:**
- Modify: `go/internal/cli/commands/deployfastpath.go:145`
- Test: `go/internal/cli/commands/deployfastpath_test.go` (or the file holding existing `computeBuildInputHash` tests)

**Interfaces:**
- Consumes/produces: `computeBuildInputHash` signature unchanged; only the salt line changes.

Why: deploys made while the bug was live recorded `saveDeployFingerprint(inputHash → stale image)`. After Tasks 1–2 the *next build* is correct, but an unchanged project hits the detached fast path (`run.go` `tryDeployFastPath`) and keeps running the stale container, because the device really does still hold the stale layers. Bumping the salt makes every pre-fix fingerprint miss exactly once, forcing one honest rebuild+deploy per app.

- [ ] **Step 1: Write the failing tripwire test**

```go
func TestBuildInputHashSaltIsV2(t *testing.T) {
	// The v1→v2 bump deliberately invalidates fingerprints recorded while
	// the stale-manifest bug (2026-08-08) could pair a current input hash
	// with a stale deploy. Do not revert to v1; bump again only with a
	// matching migration rationale.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := computeBuildInputHash(dir, "Dockerfile", "linux/arm64", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Recompute what v1 would have produced by checking the source constant
	// is gone: the simplest stable assertion is on the salt itself.
	data, err := os.ReadFile("deployfastpath.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"wendy-deploy-fingerprint-v2\n"`) {
		t.Fatalf("deploy fingerprint salt must be v2 (see comment); hash was %s", h)
	}
	if strings.Contains(string(data), `"wendy-deploy-fingerprint-v1\n"`) {
		t.Fatal("v1 salt string still present in deployfastpath.go")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestBuildInputHashSaltIsV2 -v`
Expected: FAIL — salt still v1.

- [ ] **Step 3: Bump the salt**

In `deployfastpath.go:145`:

```go
	// v2: invalidates fingerprints recorded while the stale-manifest bug
	// (fixed 2026-08-08 in this PR) could pair a fresh input hash with a
	// stale deploy — forces one honest rebuild per app after upgrade.
	io.WriteString(h, "wendy-deploy-fingerprint-v2\n")
```

- [ ] **Step 4: Run tests**

Run: `cd go && go test ./internal/cli/commands/ -run "TestBuildInputHashSaltIsV2|TestComputeBuildInputHash" -v && go test ./internal/cli/commands/`
Expected: PASS; if existing tests pinned a literal v1 hash value, update those fixtures.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/deployfastpath.go go/internal/cli/commands/deployfastpath_test.go
git commit -m "fix(cli): bump deploy fingerprint salt to v2

Pre-fix fingerprints could pair a current input hash with a stale
deploy; one forced rebuild per app clears them."
```

### Task 5: Full-suite check, on-device re-verify, PR text update (manual gate)

**Files:**
- Modify: PR #1608 description (via `gh pr edit 1608` or the web UI)

- [ ] **Step 1: Full local gates**

Run from `go/`: `gofmt -l .` (expect empty), `go build ./...`, `go test ./internal/cli/...`
Expected: all green.

- [ ] **Step 2: On-device deps-change re-verification (the exact scenario that failed)**

On a provisioned device (Ethan-Orin-Nano used for the original find), from `Examples/MCPExample` with this branch's CLI:
1. `rm -rf ~/Library/Caches/wendy/ocilayout/com.wendylabs.examples.mcp-example-linux_arm64` (clean start).
2. `wendy run --detach` → app RUNNING, MCP serves.
3. Append `six>=1.16.0` to `requirements.txt`; `wendy run --detach` again.
4. Verify on device: `wendy device shell -- ls /run/containerd/io.containerd.runtime.v2.task/default/com.wendylabs.examples.mcp-example/rootfs/usr/local/lib/python3.11/site-packages/ | grep six` → present (this was MISSING pre-fix).
5. Deploy once more with no changes → detached fast path may skip; then `git checkout -- requirements.txt`, deploy, verify six is gone again.
6. Bonus (index hygiene): `python3 -c "import json;print(len(json.load(open('<layout dir>/index.json'))['manifests']))"` → `1` after every deploy.

- [ ] **Step 3: Update PR #1608 description**

Replace the "On-device verify pending" line with: the stale-manifest bug summary (one paragraph, link `specs/2026-08-08-oci-layout-stale-manifest-fix-plan.md`), the fix (newest-manifest resolver + post-export index prune + fingerprint salt v2), and the completed on-device verification results from Step 2. Note that #1610 must rebase onto this and re-run its deps-fallback (1.4) and kill-switch (1.5) tests before it can leave draft.

- [ ] **Step 4: Push**

```bash
git push origin ed/oci-layout-dir-export
```

---

# Part 2 — Follow-up PR: `--debug` crash-loop, replace/stop races, missing crash logs

Suggested branch: `ed/debug-crashloop-followups` off `main`. One PR, three independent tasks (F1 CLI-side, F2/F3 agent-side). Each is separately revertable.

### Task F1: `--debug` deploys must not ship debugpy-less images through the chunk path

**Context:** The registry-push path wraps the image via `injectDebugpy` (`go/internal/cli/commands/docker.go:596` — builds `FROM <image>\nUSER root\nRUN pip install debugpy`). The chunk-diff (CDC) path deploys the unmodified image while still passing the debug flag to the agent, whose `wrapWithDebugpy` (`go/internal/agent/containerd/client.go:836,1102`) rewrites the entrypoint to `python -m debugpy …`. Any image without debugpy — every Stagefile-built Python app — crash-loops instantly ("No module named debugpy", observed live 2026-08-08).

**Files:**
- Modify: `go/internal/cli/commands/run.go` — the chunk-path gate (`!isDarwinAgent && !opts.deploy && opts.chunking != chunkingOff`, currently ~line 1533) and the detached fast-path gate (~line 1512)
- Test: `go/internal/cli/commands/run_test.go` (or wherever run-path gating helpers are tested)

**Interfaces:**
- Consumes: `runOptions.debug bool` (verify the exact field name with `grep -n "debug" go/internal/cli/commands/run.go | head`; it is the flag behind `--debug`).
- Produces: extracted helper `chunkDeployEligible(opts runOptions, isDarwinAgent bool) bool` used by both gates.

- [ ] **Step 1: Write the failing test**

```go
func TestChunkDeployIneligibleWithDebug(t *testing.T) {
	opts := runOptions{detach: true, chunking: chunkingAuto, debug: true}
	if chunkDeployEligible(opts, false) {
		t.Fatal("--debug must route through the registry path (debugpy injection)")
	}
	opts.debug = false
	if !chunkDeployEligible(opts, false) {
		t.Fatal("non-debug detached deploy should stay on the chunk path")
	}
}
```

(Adjust the `runOptions` literal to the real field names/zero values — compile errors here are the point of Step 2.)

- [ ] **Step 2: Run to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestChunkDeployIneligibleWithDebug -v`
Expected: FAIL — `chunkDeployEligible` undefined.

- [ ] **Step 3: Extract the helper and gate both paths**

```go
// chunkDeployEligible reports whether this run may use the chunk-diff (CDC)
// deploy path. --debug is excluded: only the registry-push path wraps the
// image with debugpy (injectDebugpy), and deploying an unwrapped image with
// the agent-side debug entrypoint rewrite crash-loops any image that does
// not bundle debugpy (every Stagefile Python app).
func chunkDeployEligible(opts runOptions, isDarwinAgent bool) bool {
	return !isDarwinAgent && !opts.deploy && opts.chunking != chunkingOff && !opts.debug
}
```

Use it at both call sites (chunk gate and the `tryDeployFastPath` gate — the fast path just restarts the existing container, which under a debug-flag change would run the wrong entrypoint mode). Keep the existing `run.go:1472` user note ("--debug requires debugpy…") — it now only concerns user-authored Dockerfiles on the registry path.

- [ ] **Step 4: Run tests, commit**

Run: `cd go && go test ./internal/cli/commands/ -run TestChunkDeploy -v && go test ./internal/cli/commands/`

```bash
git add go/internal/cli/commands/run.go go/internal/cli/commands/run_test.go
git commit -m "fix(cli): route --debug deploys through the registry path

The chunk-diff path shipped unwrapped images while the agent injected a
debugpy entrypoint, crash-looping every Stagefile Python app run with
--debug."
```

- [ ] **Step 5: On-device verify**

`wendy run --detach --debug` on MCPExample → app RUNNING (not CRASH_LOOPING), `wendy device apps list` clean; without `--debug` the chunk path still runs (log shows "Diffing … layer(s)").

### Task F2: Container replace/stop must win against the restart monitor

**Context (observed live):** replacing or stopping a crash-looping app races `ContainerMonitor.restartSingle` (`go/internal/agent/container/monitor.go:258,296`): `existing.Delete` in the replace path (`go/internal/agent/containerd/client.go:993`) fails with "cannot delete running task … failed precondition" when the monitor restarts the task between the kill and the delete; a half-dead task (runc state dir missing) wedges both stop and `ctr tasks rm -f` until the empty dir is recreated.

**Files:**
- Modify: `go/internal/agent/container/monitor.go` (add suppression API), `go/internal/agent/containerd/client.go` (replace path ~971-1003, stop path, `forceDeleteTask`)
- Test: `go/internal/agent/container/monitor_test.go`, `go/internal/agent/containerd/client_test.go`

**Interfaces:**
- Produces: `(*ContainerMonitor).Suppress(containerName string) (resume func())` — while suppressed, exit events for that container are ignored (no restart scheduled). The containerd client calls it at the top of replace/stop and `defer resume()`.
- Produces: retry-once-on-`errdefs.IsFailedPrecondition` around `existing.Delete`, re-running `terminateTask` before the retry.
- Produces: `forceDeleteTask` tolerates the missing-runc-state-dir error by `os.MkdirAll(/run/containerd/runc/<ns>/<id>, 0o711)` and retrying the delete once (linux-only; guard with the existing platform split `client_linux.go`).

- [ ] **Step 1: Write the failing monitor test**

```go
func TestMonitorSuppressSkipsRestart(t *testing.T) {
	// Construct the monitor exactly as the existing monitor tests do (reuse
	// their fake client/fixtures). Then:
	resume := m.Suppress("com.example.app")
	deliverExitEvent(t, m, "com.example.app") // reuse the test's event-injection helper
	assertNoRestartScheduled(t, m, "com.example.app")
	resume()
	deliverExitEvent(t, m, "com.example.app")
	assertRestartScheduled(t, m, "com.example.app")
}
```

(The three helpers exist in some form in `monitor_test.go` — mirror the file's existing fake/assert patterns; if event injection has a different shape there, adapt while keeping the assertion pair: suppressed → no restart, resumed → restart.)

- [ ] **Step 2: Run to verify it fails; implement Suppress**

Run: `cd go && go test ./internal/agent/container/ -run TestMonitorSuppress -v`

Implementation sketch — a `suppressed map[string]int` (counter, not bool: concurrent replaces of different services of one app) guarded by the monitor's existing mutex; the exit-event handler checks it before scheduling `restartSingle`:

```go
// Suppress makes the monitor ignore exit events for containerName until the
// returned resume func runs. Replace/stop paths hold this while they kill
// and delete the task so the monitor cannot resurrect it mid-operation.
func (m *ContainerMonitor) Suppress(containerName string) func() {
	m.mu.Lock()
	m.suppressed[containerName]++
	m.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if m.suppressed[containerName]--; m.suppressed[containerName] <= 0 {
				delete(m.suppressed, containerName)
			}
			m.mu.Unlock()
		})
	}
}
```

- [ ] **Step 3: Wire suppression + delete retry into the client**

In the replace block (`client.go:971-1003`) and the stop path (find with `grep -n "func (c \*Client) StopContainer" go/internal/agent/containerd/client.go`):

```go
	if c.monitor != nil {
		resume := c.monitor.Suppress(containerName)
		defer resume()
	}
```

(Check how the client references the monitor — `grep -n "monitor" go/internal/agent/containerd/client.go | head`; if the client has no handle, add the narrow interface it needs: `type restartSuppressor interface{ Suppress(string) func() }` set during wiring in `bridge_wiring.go`.)

Around the delete:

```go
	delErr := existing.Delete(ctx, containerd.WithSnapshotCleanup)
	if delErr != nil && errdefs.IsFailedPrecondition(delErr) {
		// The task came back between kill and delete (crash-loop restart
		// racing us before suppression, or an in-flight start). Kill again
		// and retry once.
		if task, taskErr := existing.Task(ctx, nil); taskErr == nil {
			_ = c.terminateTask(ctx, task, containerName, syscall.SIGKILL, killWaitTimeout, killWaitTimeout)
		}
		delErr = existing.Delete(ctx, containerd.WithSnapshotCleanup)
	}
	if delErr != nil && !errdefs.IsNotFound(delErr) {
		return fmt.Errorf("deleting existing container %q during replace: %w", containerName, delErr)
	}
```

For the wedged runc state dir, in `forceDeleteTask` (find with `grep -n "func (c \*Client) forceDeleteTask" go/internal/agent/containerd/client.go`): on an error containing `"cannot open directory"` and `"/run/containerd/runc/"`, `os.MkdirAll` the missing dir (mode 0o711) and retry the task-service delete once. Unit-test the error-classification helper (`isMissingRuncStateDir(err) (dir string, ok bool)`) with the literal error string observed live: `cannot open directory `+"`"+`/run/containerd/runc/default/com.wendylabs.examples.mcp-example`+"`"+`: No such file or directory`.

- [ ] **Step 4: Tests + linux pre-verify + commit**

Run: `cd go && go test ./internal/agent/container/ ./internal/agent/containerd/` and `docker run --rm -v "$PWD/..":/src -w /src golang:1.26 go test ./go/internal/agent/...`

```bash
git add go/internal/agent/container/monitor.go go/internal/agent/container/monitor_test.go go/internal/agent/containerd/client.go go/internal/agent/containerd/client_test.go go/internal/agent/containerd/bridge_wiring.go
git commit -m "fix(agent): replace/stop suppress the restart monitor and retry racing deletes

A crash-looping app's restart cycle could beat replace/stop to the task
('cannot delete running task: failed precondition'); a half-dead task
with a missing runc state dir wedged deletion entirely."
```

- [ ] **Step 5: On-device verify**

Deploy a deliberately crash-looping app (e.g. MCPExample with `raise SystemExit(1)` at the top of `main.py`), wait for CRASH_LOOPING with failures > 3, then: `wendy device apps stop <app>` succeeds first try; redeploy (`wendy run --detach` with fixed code) replaces first try. Repeat 5×.

### Task F3: Live crash-loop output must reach `device logs --tail`

**Context (observed live):** during an active crash loop (failureCount 2→56 while watched), `wendy device logs --app <app> --tail 20` replayed only the *previous day's* crash batches; the current loop's stderr ("No module named debugpy") never appeared. `TelemetryBuffer.ReadLastNMatching` (`go/internal/agent/services/telemetry_buffer.go:614`) is correct (segments newest→oldest, trailing frames, ascending return) — so the strong hypothesis is that output from monitor-restarted tasks never reaches the telemetry buffer: `restartSingle` (`go/internal/agent/container/monitor.go:296`) claims to drain output to the log manager; something in that chain drops it.

This task is investigate-then-fix; the repro is cheap and the verification is unambiguous.

**Files:**
- Investigate: `go/internal/agent/container/monitor.go:296` (restartSingle drain), `go/internal/agent/services/container_log_manager.go`, `go/internal/agent/containerd/client.go:1466` (`StartContainer` output channel)
- Modify: whichever link drops restarted-task output
- Test: `go/internal/agent/services/container_log_manager_test.go` and/or `monitor_test.go`

- [ ] **Step 1: Reproduce and localize**

On-device (or in the linux docker test env with a containerd fake if the fixtures support it):
1. Deploy an app that prints one line to stderr and exits 1 (`import sys; print("boom-<N>", file=sys.stderr); sys.exit(1)`).
2. Let the monitor restart it ≥3 times (`wendy device apps list` failureCount ≥ 3).
3. `wendy device logs --app <app> --tail 10` — count distinct `boom` lines. Pre-fix expectation per the live observation: only the first (pre-restart) lines appear.
4. Localize: instrument or read the drain path — does `restartSingle`'s output channel get consumed into `container_log_manager`, and does the manager publish to the `TelemetryBuffer` (the `--tail` source), or only to live subscribers?

- [ ] **Step 2: Write the failing test at the seam Step 1 identified**

Target shape (adapt to the real seam — the log manager's publish path is likelier unit-testable than the monitor):

```go
func TestRestartedTaskOutputReachesTelemetryBuffer(t *testing.T) {
	// Arrange the log manager with a fake telemetry buffer (the file's
	// existing test fakes), simulate: start → output "boom-1" → exit →
	// monitor-style restart → output "boom-2".
	// Assert the buffer received BOTH lines, not just boom-1.
}
```

- [ ] **Step 3: Fix the dropped link, keep the test green**

Likely shapes (pick what Step 1 proved): re-register the restarted task's output stream with the log manager under the same app/service key; or fix the manager to keep its buffer-publish subscription across container generations instead of binding to the first task's pipe.

- [ ] **Step 4: Commit + on-device verify**

```bash
git add go/internal/agent/...
git commit -m "fix(agent): keep crash-looping container output flowing to the log buffer

Monitor-restarted tasks lost their stderr/stdout capture, so 'device
logs --tail' replayed stale history while an active crash loop stayed
invisible."
```

On-device: repeat Step 1's repro — `--tail 10` now shows the latest `boom` lines (one per recent restart), newest last.

### Follow-up PR assembly

- [ ] Branch `ed/debug-crashloop-followups` off current `main`; land F1–F3 as separate commits (already structured that way).
- [ ] PR body: link `specs/2026-08-08-on-device-test-plan.md` findings 1–3 and this plan file (Part 2); note F1 changes `--debug` deploy routing (perf under debug is registry-path speed, accepted).
- [ ] Full gates before push: `gofmt -l .` empty from `go/`; `go test ./internal/...`; linux agent tests via `docker run golang:1.26`.

---

## Self-review notes (done at write time)

- Coverage: bug link 1 (append) → Task 2 prune; link 2 (oldest-match resolver) → Task 1; link 3 (GC deletes fresh build) → guarded by Tasks 2+3; link 4 (fingerprint lock-in) → Task 4; the failed on-device scenario → Task 5 Step 2 re-verify. Findings 1/2/3 → F1/F2/F3. #1610 interaction: explicitly deferred to #1610's rebase (Task 5 Step 3 notes it) — its adoption/splice inherit correctness once the reader resolves newest-first and the index holds one entry.
- Type consistency: `pruneOCILayoutDirIndex(dir, platform string) error` used identically in Tasks 2, 3; `chunkDeployEligible(opts runOptions, isDarwinAgent bool) bool` defined and consumed only in F1; `Suppress(containerName string) func()` defined F2 Step 2, consumed F2 Step 3.
- Known judgment calls left to implementers: exact `runOptions` field names (F1 Step 1 verifies by compile), the client→monitor handle (F2 Step 3 gives the wiring fallback), and F3's seam (Step 1 localizes before the test is written — the plan constrains the assertion, not the file).
- Deliberate scope exclusions: no BuildKit/builder version pinning (the wendy-oci builder was exonerated), no dockerignore changes (exonerated), no `--debug` auto-injection into Stagefile compiles (F1's routing fix restores parity; auto-injection is a UX enhancement for the Stagefile DSL work, jo's #1606 track).
