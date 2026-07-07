# Claude-on-device SP2 — On-Device Builder Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let Claude build apps for the device, on the device, by running BuildKit inside the `claude-on-device` container — gated by a new `build` entitlement — and deploying over the existing admin-socket chunk-diff push.

**Architecture:** A new privileged-equivalent `build` entitlement relaxes the hardened OCI profile (adds `CAP_SYS_ADMIN`, un-denies `unshare`/`clone(CLONE_NEWUSER)` in seccomp, keeps module/kexec denials). The `claude-on-device` image bundles `buildkitd`+`buildctl`. The wendy CLI gains a third `buildkit` build backend that produces an OCI-layout tar — auto-selected on-device — which the unchanged chunk-diff push loads into the agent's containerd.

**Tech Stack:** Go (module `github.com/wendylabsinc/wendy`), the OCI runtime-spec types in `go/internal/agent/oci`, BuildKit (`buildkitd`/`buildctl`), Docker base image `node:22-bookworm-slim` (arm64), Swift-Testing-free Go `testing`.

## Global Constraints

- Go module path: `github.com/wendylabsinc/wendy`; all commands run from the `go/` directory.
- TDD throughout: failing test → run-fail → minimal impl → run-pass → commit.
- The `build` entitlement MUST keep the kernel-module (`init_module`/`finit_module`/`delete_module`/`create_module`) and `kexec_load`/`kexec_file_load` seccomp denials intact. It only un-denies `unshare` and `clone(CLONE_NEWUSER)` and adds `CAP_SYS_ADMIN`.
- A spec with NO `build` entitlement MUST be byte-for-byte unchanged (the relaxation is strictly opt-in).
- BuildKit runs **rootful** in the container (the container is already root); `NoNewPrivileges` stays `true` — do not change it.
- Off-device behavior MUST be unchanged: builder auto-selection only triggers when `WENDY_AGENT_SOCKET` is set AND `docker` is not on `PATH`.
- Target architecture for the app image is `arm64` (the Jetson).
- Deploy relies on the agent serving chunk-diff (`QueryChunks`); no new push transport is added.
- Build-arg values are secrets: every builder command line written to a log MUST have its build-arg values redacted.

---

### Task 1: `build` entitlement in appconfig + JSON schema

**Files:**
- Modify: `go/internal/shared/appconfig/appconfig.go` (entitlement enum ~line 51, `ValidEntitlementTypes` ~line 71, `allowedKeys` ~line 95)
- Modify: `go/internal/shared/appconfig/wendy.schema.json` (entitlement `oneOf`, after the `admin` block ~line 267)
- Test: `go/internal/shared/appconfig/appconfig_test.go`

**Interfaces:**
- Produces: the string constant `EntitlementBuild = "build"`; `ValidateJSON` accepts `{"type":"build"}` and rejects unknown keys on it.

- [ ] **Step 1: Write the failing test**

Add to `go/internal/shared/appconfig/appconfig_test.go`:

```go
func TestValidateJSON_BuildEntitlement(t *testing.T) {
	warnings := ValidateJSON([]byte(`{"appId":"test","entitlements":[{"type":"build"}]}`))
	if len(warnings) != 0 {
		t.Fatalf("expected build entitlement to validate, got warnings: %v", warnings)
	}
}

func TestValidateJSON_BuildEntitlementRejectsExtraKeys(t *testing.T) {
	warnings := ValidateJSON([]byte(`{"appId":"test","entitlements":[{"type":"build","bogus":1}]}`))
	if len(warnings) == 0 {
		t.Fatal("expected a warning for an unknown key on the build entitlement")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/shared/appconfig/ -run TestValidateJSON_BuildEntitlement -v`
Expected: FAIL (build is not yet a valid entitlement type; schema lacks the `build` block).

- [ ] **Step 3: Add the constant, valid-types entry, and allowed keys**

In `appconfig.go`, add the constant next to `EntitlementAdmin`:

```go
	// EntitlementBuild grants the namespace/mount privileges a nested container
	// builder (BuildKit) needs — CAP_SYS_ADMIN plus the unshare/userns-clone
	// syscalls the default seccomp profile denies. It is privileged-equivalent
	// (container→host escape surface); grant only to fully-trusted first-party
	// apps. See entitlements.md for the blast radius.
	EntitlementBuild = "build"
```

Add `EntitlementBuild,` to the `ValidEntitlementTypes` slice, and add to `allowedKeys`:

```go
	EntitlementBuild:     {"type"},
```

- [ ] **Step 4: Add the schema block**

In `wendy.schema.json`, after the `admin` `oneOf` entry (the object ending `"description": "Full local control ..."`), add a sibling object:

```json
        ,{
          "properties": {
            "type": { "const": "build" }
          },
          "required": ["type"],
          "additionalProperties": false,
          "description": "Run a container image builder (BuildKit) inside the app container. Privileged-equivalent: grants CAP_SYS_ADMIN and the namespace syscalls a nested builder needs — a container→host escape surface. Grant only to fully-trusted first-party apps. At most one per app."
        }
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd go && go test ./internal/shared/appconfig/ -run TestValidateJSON_Build -v`
Expected: PASS (both).

- [ ] **Step 6: Commit**

```bash
git add go/internal/shared/appconfig/appconfig.go go/internal/shared/appconfig/wendy.schema.json go/internal/shared/appconfig/appconfig_test.go
git commit -m "feat(appconfig): add build entitlement type + schema"
```

---

### Task 2: `applyBuild` — relax the OCI sandbox for BuildKit

**Files:**
- Modify: `go/internal/agent/oci/entitlements.go` (add `applyBuild` + `relaxSeccompForBuild`; dispatch in `ApplyEntitlements` switch ~line 106)
- Test: `go/internal/agent/oci/entitlements_test.go`

**Interfaces:**
- Consumes: `EntitlementBuild` (Task 1); `DefaultSpec`, `ApplyEntitlements`, `appendUnique`, `hasCapability` (existing); the seccomp rule shape in `oci/spec.go` (`defaultSeccomp` denies `["ptrace","unshare"]`, the module/kexec set, and `clone` with `CLONE_NEWUSER`).
- Produces: `applyBuild(spec *Spec)` mutating the finalized spec.

- [ ] **Step 1: Write the failing test**

Add to `go/internal/agent/oci/entitlements_test.go` (reuse the existing `hasCapability` helper). Helpers for seccomp:

```go
func seccompDenies(spec *Spec, syscall string) bool {
	if spec.Linux == nil || spec.Linux.Seccomp == nil {
		return false
	}
	for _, r := range spec.Linux.Seccomp.Syscalls {
		if r.Action != ActErrno {
			continue
		}
		for _, n := range r.Names {
			if n == syscall {
				return true
			}
		}
	}
	return false
}

func TestApplyEntitlements_Build_RelaxesSandbox(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID:        "test-app",
		Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementBuild}},
	}
	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}

	// CAP_SYS_ADMIN granted in all four sets BuildKit's runc executor needs.
	for _, set := range [][]string{
		spec.Process.Capabilities.Bounding,
		spec.Process.Capabilities.Effective,
		spec.Process.Capabilities.Permitted,
		spec.Process.Capabilities.Inheritable,
	} {
		if !slices.Contains(set, "CAP_SYS_ADMIN") {
			t.Error("build entitlement must grant CAP_SYS_ADMIN in all capability sets")
		}
	}
	// The namespace syscalls BuildKit needs are no longer denied...
	if seccompDenies(spec, "unshare") {
		t.Error("build entitlement must un-deny unshare")
	}
	if seccompDenies(spec, "clone") {
		t.Error("build entitlement must remove the clone(CLONE_NEWUSER) deny rule")
	}
	// ...but the kernel-attack-surface denials are KEPT.
	if !seccompDenies(spec, "init_module") || !seccompDenies(spec, "kexec_load") {
		t.Error("build entitlement must keep module-load / kexec denials")
	}
}

func TestApplyEntitlements_NoBuild_LeavesSandboxHardened(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{AppID: "test-app"} // no entitlements
	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}
	if hasCapability(spec, "CAP_SYS_ADMIN") {
		t.Error("a spec without build must not gain CAP_SYS_ADMIN")
	}
	if !seccompDenies(spec, "unshare") || !seccompDenies(spec, "clone") {
		t.Error("a spec without build must keep the default unshare/clone denials")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/agent/oci/ -run 'TestApplyEntitlements_Build_RelaxesSandbox|TestApplyEntitlements_NoBuild' -v`
Expected: FAIL (`applyBuild` undefined / no dispatch case).

- [ ] **Step 3: Implement `applyBuild` + `relaxSeccompForBuild`**

Add to `go/internal/agent/oci/entitlements.go`:

```go
// applyBuild relaxes the hardened container profile just enough to run a nested
// container image builder (BuildKit) in-container: it adds CAP_SYS_ADMIN (mount /
// pivot_root / namespace creation for BuildKit's runc executor) and un-denies the
// unshare + clone(CLONE_NEWUSER) syscalls the default seccomp profile blocks. The
// module-load and kexec denials are deliberately KEPT (relaxSeccompForBuild), so
// the grant is scoped to what BuildKit needs. This is privileged-equivalent — see
// the security note in entitlements.md.
func applyBuild(spec *Spec) {
	if spec.Process.Capabilities == nil {
		spec.Process.Capabilities = &LinuxCapabilities{}
	}
	spec.Process.Capabilities.Bounding = appendUnique(spec.Process.Capabilities.Bounding, "CAP_SYS_ADMIN")
	spec.Process.Capabilities.Effective = appendUnique(spec.Process.Capabilities.Effective, "CAP_SYS_ADMIN")
	spec.Process.Capabilities.Permitted = appendUnique(spec.Process.Capabilities.Permitted, "CAP_SYS_ADMIN")
	spec.Process.Capabilities.Inheritable = appendUnique(spec.Process.Capabilities.Inheritable, "CAP_SYS_ADMIN")
	relaxSeccompForBuild(spec)
}

// relaxSeccompForBuild removes only the seccomp deny rules that block the
// namespace syscalls a nested builder needs: it drops "unshare" from any deny
// rule (the default profile denies ["ptrace","unshare"] together, so ptrace stays
// denied) and removes the dedicated clone(CLONE_NEWUSER) deny rule. Every other
// rule — notably the kernel-module and kexec denials — is left untouched.
func relaxSeccompForBuild(spec *Spec) {
	if spec.Linux == nil || spec.Linux.Seccomp == nil {
		return
	}
	const cloneNewuser = uint64(0x10000000) // CLONE_NEWUSER
	kept := spec.Linux.Seccomp.Syscalls[:0]
	for _, rule := range spec.Linux.Seccomp.Syscalls {
		// Drop the dedicated clone(CLONE_NEWUSER) deny rule.
		if len(rule.Names) == 1 && rule.Names[0] == "clone" {
			isNewuserRule := false
			for _, a := range rule.Args {
				if a.Value == cloneNewuser {
					isNewuserRule = true
					break
				}
			}
			if isNewuserRule {
				continue
			}
		}
		// Remove "unshare" from this rule's names, keeping the rest (e.g. ptrace).
		names := rule.Names[:0]
		for _, n := range rule.Names {
			if n == "unshare" {
				continue
			}
			names = append(names, n)
		}
		rule.Names = names
		if len(rule.Names) == 0 {
			continue // the rule only covered unshare
		}
		kept = append(kept, rule)
	}
	spec.Linux.Seccomp.Syscalls = kept
}
```

Add the dispatch case in `ApplyEntitlements`'s switch (next to `case appconfig.EntitlementAdmin:`):

```go
		case appconfig.EntitlementBuild:
			applyBuild(spec)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./internal/agent/oci/ -run 'TestApplyEntitlements_Build_RelaxesSandbox|TestApplyEntitlements_NoBuild' -v`
Expected: PASS (both). Then `go test ./internal/agent/oci/` to confirm no regressions.

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/oci/entitlements.go go/internal/agent/oci/entitlements_test.go
git commit -m "feat(oci): build entitlement relaxes seccomp/caps for in-container BuildKit"
```

---

### Task 3: `buildkit` build backend in the CLI

**Files:**
- Modify: `go/internal/cli/commands/docker.go` (add `imageBuilderBuildkit` const ~line 51; `normalizeImageBuilder` ~line 55; `imageBuilderDisplayName` ~line 68)
- Modify: `go/internal/cli/commands/ocilayers.go` (add `buildkitOCIArgs`, `redactBuildctlArgsForLog`, `buildImageToOCILayoutWithBuildkit`; dispatch in `buildImageToOCILayout` ~line 478)
- Test: `go/internal/cli/commands/buildkit_test.go` (new)

**Interfaces:**
- Consumes: `confinedDockerfilePath(cwd, dockerfile) (string, error)` (existing in ocilayers.go); `imageBuildFailedError` (existing).
- Produces: `imageBuilderBuildkit = "buildkit"`; `buildkitOCIArgs(contextDir, dockerfileDir, dockerfileName, platform string, buildArgs map[string]string, dest string) []string`; `buildImageToOCILayoutWithBuildkit(ctx, cwd, dockerfile, platform, buildArgs, dest, stdout, stderr) error`.

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/commands/buildkit_test.go`:

```go
package commands

import (
	"slices"
	"testing"
)

func TestNormalizeImageBuilder_Buildkit(t *testing.T) {
	got, err := normalizeImageBuilder("buildkit")
	if err != nil {
		t.Fatalf("normalizeImageBuilder(buildkit) error = %v", err)
	}
	if got != imageBuilderBuildkit {
		t.Fatalf("got %q, want %q", got, imageBuilderBuildkit)
	}
}

func TestBuildkitOCIArgs(t *testing.T) {
	args := buildkitOCIArgs("/work", "/work", "Dockerfile", "linux/arm64",
		map[string]string{"FOO": "bar", "ABC": "1"}, "/tmp/out.tar")
	want := []string{
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context=/work",
		"--local", "dockerfile=/work",
		"--opt", "filename=Dockerfile",
		"--opt", "platform=linux/arm64",
		"--opt", "build-arg:ABC=1", // sorted keys → ABC before FOO
		"--opt", "build-arg:FOO=bar",
		"--output", "type=oci,dest=/tmp/out.tar",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("buildkitOCIArgs mismatch:\n got: %v\nwant: %v", args, want)
	}
}

func TestRedactBuildctlArgsForLog(t *testing.T) {
	in := []string{"--opt", "build-arg:TOKEN=secret", "--output", "type=oci,dest=/x"}
	out := redactBuildctlArgsForLog(in)
	for _, a := range out {
		if a == "build-arg:TOKEN=secret" {
			t.Fatal("build-arg value was not redacted")
		}
	}
	if !slices.Contains(out, "build-arg:TOKEN=<redacted>") {
		t.Fatalf("expected redacted build-arg, got %v", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run 'TestNormalizeImageBuilder_Buildkit|TestBuildkitOCIArgs|TestRedactBuildctlArgsForLog' -v`
Expected: FAIL (undefined `imageBuilderBuildkit`, `buildkitOCIArgs`, `redactBuildctlArgsForLog`).

- [ ] **Step 3: Add the constant + normalize + display name (docker.go)**

Add to the const block at docker.go:48:

```go
	imageBuilderBuildkit       = "buildkit"
```

Add a case to `normalizeImageBuilder` and fix the error message:

```go
	case imageBuilderBuildkit:
		return imageBuilderBuildkit, nil
	default:
		return "", fmt.Errorf("invalid value %q for --builder: must be one of docker, apple-container, or buildkit", builder)
```

Add to `imageBuilderDisplayName`:

```go
	case imageBuilderBuildkit:
		return "BuildKit"
```

- [ ] **Step 4: Add the buildkit backend (ocilayers.go)**

Add these functions to `go/internal/cli/commands/ocilayers.go` (it already imports `context`, `fmt`, `os`, `os/exec`, `path/filepath`, `sort`, `strings`, `io`):

```go
// buildkitOCIArgs builds the buildctl argument vector (excluding the leading
// "buildctl") for a Dockerfile build that exports an OCI-layout tar to dest.
// Build-arg keys are sorted for a reproducible command line.
func buildkitOCIArgs(contextDir, dockerfileDir, dockerfileName, platform string, buildArgs map[string]string, dest string) []string {
	args := []string{
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context=" + contextDir,
		"--local", "dockerfile=" + dockerfileDir,
	}
	if dockerfileName != "" {
		args = append(args, "--opt", "filename="+dockerfileName)
	}
	if platform != "" {
		args = append(args, "--opt", "platform="+platform)
	}
	keys := make([]string, 0, len(buildArgs))
	for k := range buildArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--opt", "build-arg:"+k+"="+buildArgs[k])
	}
	args = append(args, "--output", "type=oci,dest="+dest)
	return args
}

// redactBuildctlArgsForLog masks build-arg values in a buildctl command line
// (the key is kept). buildctl passes build args as "build-arg:KEY=VALUE" tokens.
func redactBuildctlArgsForLog(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i, a := range out {
		const p = "build-arg:"
		if strings.HasPrefix(a, p) {
			if k, _, ok := strings.Cut(a[len(p):], "="); ok && k != "" {
				out[i] = p + k + "=<redacted>"
			}
		}
	}
	return out
}

// buildImageToOCILayoutWithBuildkit builds the image with buildctl against the
// in-container buildkitd (its address comes from BUILDKIT_HOST in the container
// env) and exports it as an OCI-layout tar at dest for the chunk-diff deploy
// path. This is the no-Docker on-device builder; it mirrors the Apple-Container
// backend's contract (produce the tar, no registry push).
func buildImageToOCILayoutWithBuildkit(ctx context.Context, cwd, dockerfile, platform string, buildArgs map[string]string, dest string, stdout, stderr io.Writer) error {
	dfDir := cwd
	dfName := ""
	if dockerfile != "" {
		resolved, err := confinedDockerfilePath(cwd, dockerfile)
		if err != nil {
			return err
		}
		dfDir = filepath.Dir(resolved)
		dfName = filepath.Base(resolved)
	}
	args := buildkitOCIArgs(cwd, dfDir, dfName, platform, buildArgs, dest)
	fmt.Fprintf(stderr, "[buildkit] starting OCI export: buildctl %s\n", strings.Join(redactBuildctlArgsForLog(args), " "))
	cmd := exec.CommandContext(ctx, "buildctl", args...)
	cmd.Dir = cwd
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return &imageBuildFailedError{fmt.Errorf("buildctl build (OCI export) failed: %w", err)}
	}
	return nil
}
```

Add the dispatch near the top of `buildImageToOCILayout` (right after the existing `if normalized == imageBuilderAppleContainer { ... }` block at ~line 478):

```go
	if normalized == imageBuilderBuildkit {
		return buildImageToOCILayoutWithBuildkit(ctx, cwd, dockerfile, platform, buildArgs, dest, stdout, stderr)
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd go && go test ./internal/cli/commands/ -run 'TestNormalizeImageBuilder_Buildkit|TestBuildkitOCIArgs|TestRedactBuildctlArgsForLog' -v`
Expected: PASS (all three).

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/commands/docker.go go/internal/cli/commands/ocilayers.go go/internal/cli/commands/buildkit_test.go
git commit -m "feat(cli): buildkit OCI-export build backend (buildctl)"
```

---

### Task 4: Auto-select `buildkit` on-device

**Files:**
- Modify: `go/internal/cli/commands/docker.go` (add `shouldUseBuildkitOnDevice`; reuse the existing `imageBuilderLookPath` var at docker.go:43)
- Modify: `go/internal/cli/commands/ocilayers.go` (resolve the effective builder at the top of `buildImageToOCILayout`)
- Modify: `go/internal/cli/commands/run.go` (line 554 `--builder` flag help), `go/internal/cli/commands/build.go` (line 216 flag help), `go/internal/cli/commands/cloud_run.go` (line 31 flag help)
- Test: `go/internal/cli/commands/buildkit_test.go`

**Interfaces:**
- Consumes: `imageBuilderWasExplicit` (existing), `imageBuilderLookPath` (existing var), `imageBuilderBuildkit` (Task 3).
- Produces: `shouldUseBuildkitOnDevice() bool`.

- [ ] **Step 1: Write the failing test**

Add to `buildkit_test.go`:

```go
func TestShouldUseBuildkitOnDevice(t *testing.T) {
	origLook := imageBuilderLookPath
	t.Cleanup(func() { imageBuilderLookPath = origLook })

	// On-device (socket set) + docker absent → use buildkit.
	t.Setenv("WENDY_AGENT_SOCKET", "/run/wendy/agent/agent.sock")
	imageBuilderLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	if !shouldUseBuildkitOnDevice() {
		t.Error("on-device with no docker should select buildkit")
	}

	// docker present → do not auto-select (let docker handle it).
	imageBuilderLookPath = func(string) (string, error) { return "/usr/bin/docker", nil }
	if shouldUseBuildkitOnDevice() {
		t.Error("docker present must not auto-select buildkit")
	}

	// Off-device (no socket) → never auto-select, regardless of docker.
	t.Setenv("WENDY_AGENT_SOCKET", "")
	imageBuilderLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	if shouldUseBuildkitOnDevice() {
		t.Error("off-device must not auto-select buildkit")
	}
}
```

Add `"os/exec"` to the test file imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestShouldUseBuildkitOnDevice -v`
Expected: FAIL (`shouldUseBuildkitOnDevice` undefined).

- [ ] **Step 3: Implement `shouldUseBuildkitOnDevice` (docker.go)**

Add near `shouldAutoAttemptAppleContainerBuilder` (docker.go:81):

```go
// shouldUseBuildkitOnDevice reports whether an unspecified builder should default
// to BuildKit: true only when running inside the device (WENDY_AGENT_SOCKET set)
// and Docker is unavailable. Off-device, or when docker exists, behavior is
// unchanged.
func shouldUseBuildkitOnDevice() bool {
	if os.Getenv("WENDY_AGENT_SOCKET") == "" {
		return false
	}
	if _, err := imageBuilderLookPath("docker"); err == nil {
		return false
	}
	return true
}
```

(`os` is already imported in docker.go.)

- [ ] **Step 4: Resolve the effective builder in `buildImageToOCILayout`**

In `ocilayers.go`, replace the opening of `buildImageToOCILayout`:

```go
func buildImageToOCILayout(ctx context.Context, cwd, dockerfile, platform string, buildArgs map[string]string, builder, dest string, stdout, stderr io.Writer) error {
	if !imageBuilderWasExplicit(builder) && shouldUseBuildkitOnDevice() {
		builder = imageBuilderBuildkit
	}
	normalized, err := normalizeImageBuilder(builder)
	if err != nil {
		return err
	}
```

- [ ] **Step 5: Update the three `--builder` flag help strings**

Change each occurrence (run.go:554, build.go:216, cloud_run.go:31) from:

```go
"Image builder to force for Dockerfile/Containerfile builds: docker or apple-container"
```
to:
```go
"Image builder to force for Dockerfile/Containerfile builds: docker, apple-container, or buildkit"
```

- [ ] **Step 6: Run tests + build to verify**

Run: `cd go && go test ./internal/cli/commands/ -run TestShouldUseBuildkitOnDevice -v && go build ./...`
Expected: PASS and a clean build.

- [ ] **Step 7: Commit**

```bash
git add go/internal/cli/commands/docker.go go/internal/cli/commands/ocilayers.go go/internal/cli/commands/run.go go/internal/cli/commands/build.go go/internal/cli/commands/cloud_run.go go/internal/cli/commands/buildkit_test.go
git commit -m "feat(cli): auto-select buildkit builder on-device"
```

---

### Task 5: `claude-on-device` image bundles BuildKit + declares `build`

**Files:**
- Modify: `Examples/ClaudeOnDevice/Dockerfile` (install buildkitd/buildctl/buildkit-runc; set BUILDKIT_HOST)
- Modify: `Examples/ClaudeOnDevice/init.sh` (launch buildkitd before idling)
- Modify: `Examples/ClaudeOnDevice/wendy.json` (add `build` entitlement + `/var/lib/buildkit` persist)
- Test: `go/internal/shared/appconfig/claude_on_device_test.go`

**Interfaces:**
- Consumes: `EntitlementBuild` (Task 1).
- Produces: an app config that declares `build` and a `buildkit-cache` persist mount at `/var/lib/buildkit`.

- [ ] **Step 1: Write the failing test**

Extend `go/internal/shared/appconfig/claude_on_device_test.go`'s loop and assertions:

```go
	var admin, home, workspace, build, buildkitCache bool
	for _, e := range cfg.Entitlements {
		switch {
		case e.Type == EntitlementAdmin:
			admin = true
		case e.Type == EntitlementBuild:
			build = true
		case e.Type == EntitlementPersist && e.Path == "/root":
			home = true
		case e.Type == EntitlementPersist && e.Path == "/workspace":
			workspace = true
		case e.Type == EntitlementPersist && e.Path == "/var/lib/buildkit":
			buildkitCache = true
		}
	}
	if !admin {
		t.Error("missing admin entitlement")
	}
	if !build {
		t.Error("missing build entitlement (on-device BuildKit)")
	}
	if !home {
		t.Error("missing persist mount for /root (home: OAuth token + MCP config)")
	}
	if !workspace {
		t.Error("missing persist mount for /workspace")
	}
	if !buildkitCache {
		t.Error("missing persist mount for /var/lib/buildkit (BuildKit cache)")
	}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/shared/appconfig/ -run TestClaudeOnDeviceExampleValidates -v`
Expected: FAIL (wendy.json lacks `build` and the buildkit-cache persist).

- [ ] **Step 3: Update `wendy.json`**

Add to the `entitlements` array in `Examples/ClaudeOnDevice/wendy.json`:

```json
    {
      "type": "build"
    },
    {
      "type": "persist",
      "name": "buildkit-cache",
      "path": "/var/lib/buildkit"
    }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/shared/appconfig/ -run TestClaudeOnDeviceExampleValidates -v`
Expected: PASS.

- [ ] **Step 5: Install BuildKit in the Dockerfile**

In `Examples/ClaudeOnDevice/Dockerfile`, after the existing `apt-get install` layer, add a BuildKit install layer (arm64 release tarball ships `buildkitd`, `buildctl`, and `buildkit-runc`):

```dockerfile
# BuildKit (buildkitd + buildctl + buildkit-runc) for on-device image builds —
# no Docker daemon required. Pinned release, arm64.
ARG BUILDKIT_VERSION=v0.16.0
RUN curl -fsSL "https://github.com/moby/buildkit/releases/download/${BUILDKIT_VERSION}/buildkit-${BUILDKIT_VERSION}.linux-arm64.tar.gz" \
      | tar -xz -C /usr/local bin/buildkitd bin/buildctl bin/buildkit-runc \
  && /usr/local/bin/buildkitd --version
# buildctl reaches the in-container daemon over this socket (init.sh starts it).
ENV BUILDKIT_HOST=unix:///run/buildkit/buildkitd.sock
```

Ensure `curl` is in the earlier `apt-get install` list (add `curl` alongside `git ca-certificates ripgrep less`).

- [ ] **Step 6: Launch buildkitd in `init.sh`**

Insert before the final `exec sleep infinity` in `Examples/ClaudeOnDevice/init.sh`:

```sh
# Start buildkitd (rootful; the container is root) so `wendy run` can build images
# on-device via buildctl. BUILDKIT_SNAPSHOTTER lets the operator force "native" if
# overlayfs-on-overlayfs is unavailable on the device kernel (default: auto).
mkdir -p /run/buildkit /var/lib/buildkit
buildkitd \
  ${BUILDKIT_SNAPSHOTTER:+--oci-worker-snapshotter="$BUILDKIT_SNAPSHOTTER"} \
  >/var/log/buildkitd.log 2>&1 &

# Wait briefly for the control socket so the first `wendy run` doesn't race it.
for _ in 1 2 3 4 5 6 7 8 9 10; do
  [ -S /run/buildkit/buildkitd.sock ] && break
  sleep 0.5
done
[ -S /run/buildkit/buildkitd.sock ] || echo "warning: buildkitd socket not up; on-device builds will fail until it is" >&2
```

- [ ] **Step 7: Commit**

```bash
git add Examples/ClaudeOnDevice/Dockerfile Examples/ClaudeOnDevice/init.sh Examples/ClaudeOnDevice/wendy.json go/internal/shared/appconfig/claude_on_device_test.go
git commit -m "feat(examples): claude-on-device bundles BuildKit + build entitlement"
```

---

### Task 6: Documentation — on-device build usage + `build` blast radius

**Files:**
- Modify: `Examples/ClaudeOnDevice/README.md` (add an on-device build section)
- Modify: `go/internal/cli/assets/docs/entitlements.md` (add a `build` entry with the privileged-equivalent warning)
- Modify: `go/internal/cli/assets/skills/wendy/references/wendy.json.md` (add a `build` entry next to `admin` — this is where `admin`/`display` are documented; commit 3bc2e898)

**Interfaces:** none (docs only).

> Both doc files already document `admin` and `display`; add `build` in the same place and style in each. Open each file, find the `admin` entitlement section, and add a sibling `build` section immediately after it.

- [ ] **Step 1: Add the on-device build section to the app README**

Append to `Examples/ClaudeOnDevice/README.md`:

```markdown
## Building apps on the device

This app bundles BuildKit (`buildkitd` + `buildctl`), so Claude can build and
deploy apps **from the device itself** — no laptop, no Docker. Inside an attached
session, edit an app under `/workspace` and run:

```
wendy run --yes
```

Because `WENDY_AGENT_SOCKET` is set and there is no Docker daemon, the CLI
auto-selects the BuildKit backend: `buildctl` builds an OCI image against the
in-container `buildkitd`, and the image is pushed into the device's containerd over
the local agent socket (the same chunk-diff path a laptop uses). The build cache
persists across restarts in the `/var/lib/buildkit` volume.

If builds fail with an overlayfs error on your device kernel, set
`BUILDKIT_SNAPSHOTTER=native` in the container environment (slower, but avoids
overlayfs-on-overlayfs).

### ⚠️ The `build` entitlement is privileged-equivalent

On-device building requires the `build` entitlement, which grants `CAP_SYS_ADMIN`
and the namespace syscalls a nested builder needs — a **container→host escape
surface**. In this app it stacks on `admin` (already full device control), so it
does not widen device control, but it does add host-escape capability. Deploy only
to trusted, first-party devices.
```

- [ ] **Step 2: Add the `build` entry to both entitlement reference docs**

In each of `go/internal/cli/assets/docs/entitlements.md` and
`go/internal/cli/assets/skills/wendy/references/wendy.json.md`, find the `admin`
entitlement section and add a sibling `build` section immediately after it, in the
same heading/format style that file uses, with this content:

> **`build`** — Runs a container image builder (BuildKit) inside the app
> container. Grants `CAP_SYS_ADMIN` and un-denies the `unshare` /
> `clone(CLONE_NEWUSER)` syscalls a nested builder needs (the kernel-module and
> `kexec` denials are kept). **Privileged-equivalent: a container→host escape
> surface.** Used so a device can build apps for itself (see the
> `claude-on-device` example). Grant only to fully-trusted, first-party apps. At
> most one per app; takes no parameters (`{"type":"build"}`).

- [ ] **Step 3: Commit**

```bash
git add Examples/ClaudeOnDevice/README.md go/internal/cli/assets/docs/entitlements.md go/internal/cli/assets/skills/wendy/references/wendy.json.md
git commit -m "docs: on-device build usage + build entitlement blast radius"
```

---

## On-device smoke (manual, after the tasks)

Mirror SP1's hardware check (no automated equivalent — GitHub runners lack nested virt and a Jetson):

1. Build branch CLIs: `cd go && go build ./cmd/wendy` (mac) and `GOOS=linux GOARCH=arm64 go build -o ../Examples/ClaudeOnDevice/wendy-linux-arm64 ./cmd/wendy`.
2. Deploy a branch agent: `wendy device update --binary <arm64-agent> --device <jetson>`.
3. Build + deploy the app: `cd Examples/ClaudeOnDevice && wendy run --yes --build-type docker --device <jetson>`.
4. `wendy device attach claude-on-device --device <jetson>`; complete OAuth.
5. Inside: clone/author a sample app under `/workspace`, run `wendy run --yes`, and confirm it builds via in-container BuildKit and the new app appears in `wendy device apps` and runs.
6. If overlayfs errors appear, recreate the container with `BUILDKIT_SNAPSHOTTER=native` and retry; record which snapshotter works for the design's open risk.
