# OS Update Check in `device update` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After `wendy device update` / `wendy cloud device update` updates the agent, also check for a newer WendyOS image (GCS-hosted Mender artifact, `--nightly` aware) and prompt to apply it.

**Architecture:** Extract small shared helpers from `wendy os update`'s GCS auto-detect path — a pure version comparator (`osAlreadyCurrent`), a pure decision function (`decideOSUpdate`), and the `UpdateOS` streaming block (`streamOSUpdate`). `device update` calls a new orchestrator (`maybeCheckOSUpdate`) after the agent upload. The OS decision uses the version already queried before the agent restart, so a reconnect is only needed when actually applying. Reconnect honors the cloud tunnel (re-runs `connectToAgent`) for cloud, and re-dials the known host (`waitForAgentRestart`) for direct.

**Tech Stack:** Go, Cobra, gRPC (`agentpb`), existing CLI helpers in `internal/cli/commands`.

---

## File Structure

- `internal/cli/commands/os_cmd.go` — add `osAlreadyCurrent`, `decideOSUpdate`, the `osUpdateAction` type, and `streamOSUpdate`; refactor `newOSUpdateCmd` to use `osAlreadyCurrent` and `streamOSUpdate` (behavior-preserving).
- `internal/cli/commands/device.go` — add `maybeCheckOSUpdate` and `reconnectAgentAfterRestart`; wire the `--yes` flag and the OS-check call into `newDeviceUpdateCmd`.
- `internal/cli/commands/os_cmd_test.go` — unit tests for `osAlreadyCurrent` and `decideOSUpdate`.
- `internal/cli/commands/device_test.go` — unit tests for `maybeCheckOSUpdate` skip conditions.

Key facts the implementer must rely on (verified):
- `GetAgentVersionResponse` carries `OsVersion` (e.g. `"WendyOS-0.10.4"`), `DeviceType`, `StorageMedium`, `Featureset`. An *agent* update does not change `OsVersion`/`DeviceType`/`StorageMedium`, so the pre-restart values are valid for the OS decision.
- `version.CompareVersions(a, b)` returns `-1` if `a<b`, `0` if equal, `+1` if `a>b` (`internal/shared/version/version.go`).
- `getLatestOTAInfoForDeviceType(deviceType, storageMedium, nightly) (artifactURL, latestVersion string, err error)` (os_cmd.go:179) fetches the GCS manifest and returns the GCS artifact URL + latest version string.
- `isWendyOSUpdateTarget(resp)` (os_cmd.go:67) and `agentVersionHasFeature(resp, "mender")` (os_cmd.go:72) gate WendyOS OTA capability.
- `cloudDeviceConfigFromContext(ctx) (cloudDeviceConfig, bool)` (cloud.go:72) reports cloud mode.
- `connectToAgent(ctx, opts...)` (helpers.go:615) branches on cloud context, so re-running it reconnects correctly for cloud.
- `waitForAgentRestart(ctx, addr)` (helpers.go:992) re-dials a known `host:port` directly (direct/LAN only).
- `waitForDeviceOnline(ctx, host)` (os_cmd.go:432) polls reboot online (direct/LAN only).
- `promptYesNoDefaultNoFn(prompt) bool` (helpers.go:124) and `isInteractiveTerminalFn`/`isInteractiveTerminal()` (helpers.go:537) are stubbable test seams.
- `hostPort(host, defaultAgentPort)` (helpers.go:250) and `drainOSUpdateStream`, `phaseLabel`, `tui` are already available in `os_cmd.go`.

---

### Task 1: Pure version comparator `osAlreadyCurrent`

**Files:**
- Modify: `internal/cli/commands/os_cmd.go` (add function; refactor lines 169-180)
- Test: `internal/cli/commands/os_cmd_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/cli/commands/os_cmd_test.go`:

```go
func TestOSAlreadyCurrent(t *testing.T) {
	tests := []struct {
		name      string
		current   string
		latest    string
		nightly   bool
		want      bool
	}{
		{"stable equal is current", "WendyOS-0.10.4", "0.10.4", false, true},
		{"stable newer available", "WendyOS-0.10.4", "0.12.0", false, false},
		{"stable device ahead is current", "WendyOS-0.12.0", "0.10.4", false, true},
		{"nightly equal is current", "WendyOS-0.12.0-nightly", "0.12.0-nightly", true, true},
		{"nightly different available", "WendyOS-0.12.0-nightly", "0.13.0-nightly", true, false},
		{"empty current not current", "", "0.10.4", false, false},
		{"empty latest not current", "WendyOS-0.10.4", "", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := osAlreadyCurrent(tc.current, tc.latest, tc.nightly); got != tc.want {
				t.Fatalf("osAlreadyCurrent(%q,%q,%v) = %v, want %v", tc.current, tc.latest, tc.nightly, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos/go && go test ./internal/cli/commands/ -run TestOSAlreadyCurrent -v`
Expected: FAIL — `undefined: osAlreadyCurrent`.

- [ ] **Step 3: Add the function**

Add to `internal/cli/commands/os_cmd.go` (near the other OS helpers, e.g. just below `agentVersionHasFeature`):

```go
// osAlreadyCurrent reports whether the device's current OS version is at or
// ahead of the latest available version. The "WendyOS-" display prefix is
// stripped before comparing so that "WendyOS-0.10.4" and "0.12.0-nightly"
// compare correctly. Returns false when either version is unknown.
func osAlreadyCurrent(currentOSVersion, latestVersion string, nightly bool) bool {
	if currentOSVersion == "" || latestVersion == "" {
		return false
	}
	normalized := strings.TrimPrefix(currentOSVersion, "WendyOS-")
	return nightly && latestVersion == normalized ||
		!nightly && version.CompareVersions(latestVersion, normalized) <= 0
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos/go && go test ./internal/cli/commands/ -run TestOSAlreadyCurrent -v`
Expected: PASS.

- [ ] **Step 5: Refactor `newOSUpdateCmd` to use it (behavior-preserving)**

In `internal/cli/commands/os_cmd.go`, replace the inline comparison (currently lines 169-180):

```go
				if osVer := versionResp.GetOsVersion(); osVer != "" && latestVer != "" {
					// Strip the "WendyOS-" display prefix before comparing so that
					// "WendyOS-0.10.4" and "0.12.0-nightly" compare correctly.
					normalizedOsVer := strings.TrimPrefix(osVer, "WendyOS-")
					alreadyCurrent := nightly && latestVer == normalizedOsVer ||
						!nightly && version.CompareVersions(latestVer, normalizedOsVer) <= 0
					if alreadyCurrent {
						fmt.Printf("OS is already at the latest version (%s).\n", osVer)
						return nil
					}
					fmt.Printf("Latest OS version: %s\n", latestVer)
				}
```

with:

```go
				if osVer := versionResp.GetOsVersion(); osVer != "" && latestVer != "" {
					if osAlreadyCurrent(osVer, latestVer, nightly) {
						fmt.Printf("OS is already at the latest version (%s).\n", osVer)
						return nil
					}
					fmt.Printf("Latest OS version: %s\n", latestVer)
				}
```

- [ ] **Step 6: Build and run package tests**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos/go && go build ./internal/cli/... && go test ./internal/cli/commands/ -run 'TestOSAlreadyCurrent|TestValidateOSUpdate' -v`
Expected: build succeeds; tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/commands/os_cmd.go internal/cli/commands/os_cmd_test.go
git commit -m "refactor(cli): extract osAlreadyCurrent version comparator

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Pure decision function `decideOSUpdate`

**Files:**
- Modify: `internal/cli/commands/os_cmd.go` (add type + function)
- Test: `internal/cli/commands/os_cmd_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/cli/commands/os_cmd_test.go`:

```go
func TestDecideOSUpdate(t *testing.T) {
	tests := []struct {
		name        string
		current     string
		latest      string
		nightly     bool
		assumeYes   bool
		interactive bool
		want        osUpdateAction
	}{
		{"already current", "WendyOS-0.10.4", "0.10.4", false, false, false, osActionAlreadyCurrent},
		{"newer with yes", "WendyOS-0.10.4", "0.12.0", false, true, false, osActionApply},
		{"newer with yes overrides tty", "WendyOS-0.10.4", "0.12.0", false, true, true, osActionApply},
		{"newer interactive prompts", "WendyOS-0.10.4", "0.12.0", false, false, true, osActionPrompt},
		{"newer noninteractive reports", "WendyOS-0.10.4", "0.12.0", false, false, false, osActionReportOnly},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := decideOSUpdate(tc.current, tc.latest, tc.nightly, tc.assumeYes, tc.interactive)
			if got != tc.want {
				t.Fatalf("decideOSUpdate(%q,%q,nightly=%v,yes=%v,tty=%v) = %v, want %v",
					tc.current, tc.latest, tc.nightly, tc.assumeYes, tc.interactive, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos/go && go test ./internal/cli/commands/ -run TestDecideOSUpdate -v`
Expected: FAIL — `undefined: osUpdateAction` / `decideOSUpdate`.

- [ ] **Step 3: Add the type and function**

Add to `internal/cli/commands/os_cmd.go` (below `osAlreadyCurrent`):

```go
// osUpdateAction is the decision for the OS-update step of `device update`.
type osUpdateAction int

const (
	osActionAlreadyCurrent osUpdateAction = iota // device is already at/ahead of latest
	osActionApply                                // apply without prompting (--yes)
	osActionPrompt                               // interactive: ask the user
	osActionReportOnly                           // non-interactive, no --yes: report and skip
)

// decideOSUpdate chooses how the OS-update step behaves when a newer OS may be
// available. It is pure so it can be unit-tested; the caller is responsible for
// running the interactive prompt when the result is osActionPrompt.
func decideOSUpdate(currentOSVersion, latestVersion string, nightly, assumeYes, interactive bool) osUpdateAction {
	if osAlreadyCurrent(currentOSVersion, latestVersion, nightly) {
		return osActionAlreadyCurrent
	}
	switch {
	case assumeYes:
		return osActionApply
	case interactive:
		return osActionPrompt
	default:
		return osActionReportOnly
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos/go && go test ./internal/cli/commands/ -run TestDecideOSUpdate -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/commands/os_cmd.go internal/cli/commands/os_cmd_test.go
git commit -m "feat(cli): add decideOSUpdate decision helper

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Extract `streamOSUpdate` (behavior-preserving)

**Files:**
- Modify: `internal/cli/commands/os_cmd.go` (extract block; call from `newOSUpdateCmd`)

- [ ] **Step 1: Add the `streamOSUpdate` function**

Add to `internal/cli/commands/os_cmd.go` (e.g. just below `newOSUpdateCmd`):

```go
// streamOSUpdate starts an UpdateOS stream for artifactURL on conn and reports
// progress: a spinner when interactive, a silent drain otherwise. It does not
// wait for the post-update reboot.
func streamOSUpdate(ctx context.Context, conn *grpcclient.AgentConnection, artifactURL string) error {
	stream, err := conn.AgentService.UpdateOS(ctx, &agentpb.UpdateOSRequest{
		ArtifactUrl: artifactURL,
	})
	if err != nil {
		return fmt.Errorf("starting OS update: %w", err)
	}

	if !isInteractiveTerminal() {
		return drainOSUpdateStream(stream)
	}

	spin := tui.NewSpinner("Downloading update...")
	p := tea.NewProgram(spin)

	go func() {
		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				p.Send(tui.SpinnerDoneMsg{})
				return
			}
			if err != nil {
				p.Send(tui.SpinnerDoneMsg{Err: err})
				return
			}
			if progress := resp.GetProgress(); progress != nil {
				p.Send(tui.SpinnerUpdateMsg{Label: phaseLabel(progress.GetPhase())})
			}
			if completed := resp.GetCompleted(); completed != nil {
				p.Send(tui.SpinnerDoneMsg{})
				return
			}
			if failed := resp.GetFailed(); failed != nil {
				p.Send(tui.SpinnerDoneMsg{Err: fmt.Errorf("update failed: %s", failed.GetErrorMessage())})
				return
			}
		}
	}()

	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}
	spinModel, ok := finalModel.(tui.SpinnerModel)
	if !ok {
		return fmt.Errorf("TUI error: unexpected model type %T", finalModel)
	}
	if !spinModel.Done() {
		return ErrUserCancelled
	}
	if _, spinErr := spinModel.Result(); spinErr != nil {
		return spinErr
	}
	return nil
}
```

- [ ] **Step 2: Replace the inline streaming block in `newOSUpdateCmd`**

In `internal/cli/commands/os_cmd.go`, replace the current block (lines 233-287, beginning `stream, err := conn.AgentService.UpdateOS(` and ending at the close of the `} else { if err := drainOSUpdateStream(stream); ... }` section) with:

```go
			if err := streamOSUpdate(ctx, conn, artifactURL); err != nil {
				return err
			}
```

Leave the following lines (the `deviceHost := conn.Host`, "rebooting" message, and `waitForDeviceOnline` call) unchanged.

- [ ] **Step 3: Build and run tests**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos/go && go build ./internal/cli/... && go test ./internal/cli/commands/ -run 'TestValidateOSUpdate|TestOSAlreadyCurrent|TestDecideOSUpdate' -v`
Expected: build succeeds; tests PASS. (`go vet ./internal/cli/commands/` should also report no unused imports — `io`, `tea`, `tui` remain used by `streamOSUpdate`.)

- [ ] **Step 4: Commit**

```bash
git add internal/cli/commands/os_cmd.go
git commit -m "refactor(cli): extract streamOSUpdate from os update

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Reconnect helper `reconnectAgentAfterRestart`

**Files:**
- Modify: `internal/cli/commands/device.go` (add function)

- [ ] **Step 1: Add the reconnect helpers**

Add to `internal/cli/commands/device.go` (e.g. just above `newDeviceUpdateCmd`):

```go
// reconnectAgentAfterRestart re-establishes a connection to the device the
// command is operating on, after the agent has restarted. For a direct/LAN
// connection it re-dials the known host. For a cloud-tunnel connection it
// re-runs connectToAgent so the broker tunnel is rebuilt (a direct host:port
// dial would not traverse the tunnel).
func reconnectAgentAfterRestart(ctx context.Context, host string) (*grpcclient.AgentConnection, error) {
	if _, isCloud := cloudDeviceConfigFromContext(ctx); isCloud {
		return reconnectCloudAgentAfterRestart(ctx)
	}
	return waitForAgentRestart(ctx, hostPort(host, defaultAgentPort))
}

// reconnectCloudAgentAfterRestart retries connectToAgent through the cloud
// tunnel until the agent answers GetAgentVersion or the deadline passes.
func reconnectCloudAgentAfterRestart(ctx context.Context) (*grpcclient.AgentConnection, error) {
	deadline := time.Now().Add(90 * time.Second)
	time.Sleep(2 * time.Second) // give the agent a moment to begin shutdown
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		conn, err := connectToAgent(ctx, ExcludeProviders("local", "docker", "wendy-lite"), ExcludeBluetooth(), SuppressUpdateCheck())
		if err == nil {
			probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			_, probeErr := conn.AgentService.GetAgentVersion(probeCtx, &agentpb.GetAgentVersionRequest{})
			cancel()
			if probeErr == nil {
				return conn, nil
			}
			conn.Close()
			lastErr = probeErr
		} else {
			lastErr = err
		}
		time.Sleep(2 * time.Second)
	}
	if lastErr != nil {
		return nil, fmt.Errorf("agent did not come back after update: %w", lastErr)
	}
	return nil, fmt.Errorf("agent did not come back after update")
}
```

- [ ] **Step 2: Build**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos/go && go build ./internal/cli/...`
Expected: build succeeds. (Confirm `time`, `context`, `grpcclient`, `agentpb` are already imported in `device.go` — they are.)

- [ ] **Step 3: Commit**

```bash
git add internal/cli/commands/device.go
git commit -m "feat(cli): add cloud-aware agent reconnect after restart

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Orchestrator `maybeCheckOSUpdate`

**Files:**
- Modify: `internal/cli/commands/device.go` (add function)
- Test: `internal/cli/commands/device_test.go`

- [ ] **Step 1: Write the failing test (skip conditions, no network)**

Add to `internal/cli/commands/device_test.go`:

```go
func TestMaybeCheckOSUpdateSkips(t *testing.T) {
	strp := func(s string) *string { return &s }

	tests := []struct {
		name    string
		version *agentpb.GetAgentVersionResponse
	}{
		{"nil version", nil},
		{"non-wendyos darwin", &agentpb.GetAgentVersionResponse{Os: "darwin", OsVersion: strp("14.4")}},
		{"wendyos with mender but no device type",
			&agentpb.GetAgentVersionResponse{Os: "linux", OsVersion: strp("WendyOS-0.10.4"), Featureset: []string{"mender"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// These inputs must return before any manifest/network call.
			if err := maybeCheckOSUpdate(context.Background(), tc.version, "", false, false); err != nil {
				t.Fatalf("maybeCheckOSUpdate() error = %v, want nil", err)
			}
		})
	}
}
```

(Confirm `context` and `agentpb` are imported in `device_test.go`; add them to the import block if missing.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos/go && go test ./internal/cli/commands/ -run TestMaybeCheckOSUpdateSkips -v`
Expected: FAIL — `undefined: maybeCheckOSUpdate`.

- [ ] **Step 3: Add the orchestrator**

Add to `internal/cli/commands/device.go` (below `reconnectAgentAfterRestart`):

```go
// maybeCheckOSUpdate runs the OS-update step for `device update` after the
// agent has been updated. preUpdateVersion is the version queried before the
// agent restart; its OsVersion/DeviceType/StorageMedium are unaffected by an
// agent update, so they are valid here. The decision (already-current,
// report-only, prompt, apply) needs no live connection; only the apply path
// reconnects. Non-WendyOS / no-mender / unknown-device-type targets are
// skipped silently — `device update` still succeeds as an agent-only update.
func maybeCheckOSUpdate(ctx context.Context, preUpdateVersion *agentpb.GetAgentVersionResponse, host string, nightly, assumeYes bool) error {
	if preUpdateVersion == nil {
		return nil
	}
	if !isWendyOSUpdateTarget(preUpdateVersion) || !agentVersionHasFeature(preUpdateVersion, "mender") {
		return nil
	}
	deviceType := preUpdateVersion.GetDeviceType()
	if deviceType == "" {
		// No device type → cannot auto-select the GCS artifact; skip quietly.
		return nil
	}

	otaURL, latestVer, err := getLatestOTAInfoForDeviceType(deviceType, preUpdateVersion.GetStorageMedium(), nightly)
	if err != nil {
		fmt.Printf("Could not check for OS updates: %v\n", err)
		return nil
	}

	currentOS := preUpdateVersion.GetOsVersion()
	fromVer := strings.TrimPrefix(currentOS, "WendyOS-")
	if fromVer == "" {
		fromVer = "unknown"
	}

	apply := false
	switch decideOSUpdate(currentOS, latestVer, nightly, assumeYes, isInteractiveTerminal()) {
	case osActionAlreadyCurrent:
		fmt.Printf("OS is already at the latest version (%s).\n", currentOS)
		return nil
	case osActionReportOnly:
		fmt.Printf("OS update available (%s). Re-run with --yes or run 'wendy os update' to apply.\n", latestVer)
		return nil
	case osActionApply:
		apply = true
	case osActionPrompt:
		if promptYesNoDefaultNoFn(fmt.Sprintf("OS update available (%s → %s). Apply now? [y/N] ", fromVer, latestVer)) {
			apply = true
		} else {
			fmt.Println("Skipping OS update. Run 'wendy os update' to apply later.")
			return nil
		}
	}
	if !apply {
		return nil
	}

	fmt.Println("Reconnecting to apply the OS update...")
	conn, err := reconnectAgentAfterRestart(ctx, host)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := streamOSUpdate(ctx, conn, otaURL); err != nil {
		return err
	}

	if _, isCloud := cloudDeviceConfigFromContext(ctx); isCloud {
		fmt.Println("OS update applied; the device is rebooting. Reconnect once it is back online.")
		return nil
	}
	fmt.Println("WendyOS update applied. Device is rebooting...")
	if err := waitForDeviceOnline(ctx, host); err != nil {
		return err
	}
	fmt.Println("Device is back online.")
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos/go && go test ./internal/cli/commands/ -run TestMaybeCheckOSUpdateSkips -v`
Expected: PASS (returns nil for all three inputs without any network call). Confirm `strings` is imported in `device.go` (it is).

- [ ] **Step 5: Commit**

```bash
git add internal/cli/commands/device.go internal/cli/commands/device_test.go
git commit -m "feat(cli): add maybeCheckOSUpdate orchestrator for device update

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Wire `--yes` flag and the OS check into `newDeviceUpdateCmd`

**Files:**
- Modify: `internal/cli/commands/device.go:1587-1736`

- [ ] **Step 1: Add the `--yes` variable and capture pre-update state**

In `newDeviceUpdateCmd`, change the variable declarations (currently lines 1588-1589) from:

```go
	var binaryPath string
	var nightly bool
```

to:

```go
	var binaryPath string
	var nightly bool
	var assumeYes bool
```

Immediately after `defer conn.Close()` (line 1602), add:

```go
		deviceHost := conn.Host
		var preUpdateVersion *agentpb.GetAgentVersionResponse
```

- [ ] **Step 2: Capture the version in both branches**

In the `--binary` branch, change the success block (currently lines 1616-1624) from:

```go
				versionResp, versionErr := conn.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
				if versionErr == nil {
					deviceArch := versionResp.GetCpuArchitecture()
					if deviceArch != "" {
						if err := checkELFArchitecture(binaryData, deviceArch); err != nil {
							return err
						}
					}
				}
```

to:

```go
				versionResp, versionErr := conn.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
				if versionErr == nil {
					preUpdateVersion = versionResp
					deviceArch := versionResp.GetCpuArchitecture()
					if deviceArch != "" {
						if err := checkELFArchitecture(binaryData, deviceArch); err != nil {
							return err
						}
					}
				}
```

In the auto-download branch, immediately after the version query + error check (currently after line 1633, the closing `}` of `if err != nil { return ... }` that follows `versionResp, err := conn.AgentService.GetAgentVersion(...)`), add:

```go
				preUpdateVersion = versionResp
```

(So `arch := versionResp.GetCpuArchitecture()` is immediately preceded by the `preUpdateVersion = versionResp` assignment.)

- [ ] **Step 3: Call the OS check after the agent-success message**

Change the final success block (currently lines 1715-1727) from:

```go
			if jsonOutput {
				resp := map[string]string{
					"status":  "success",
					"message": "Agent updated successfully.",
				}
				b, err := json.Marshal(resp)
				if err != nil {
					return fmt.Errorf("failed to marshal JSON response: %w", err)
				}
				fmt.Println(string(b))
			} else {
				fmt.Println("Agent updated successfully.")
			}
			return nil
```

to:

```go
			if jsonOutput {
				resp := map[string]string{
					"status":  "success",
					"message": "Agent updated successfully.",
				}
				b, err := json.Marshal(resp)
				if err != nil {
					return fmt.Errorf("failed to marshal JSON response: %w", err)
				}
				fmt.Println(string(b))
				// OS update check is skipped in JSON mode to keep output stable.
				return nil
			}
			fmt.Println("Agent updated successfully.")
			if err := maybeCheckOSUpdate(ctx, preUpdateVersion, deviceHost, nightly, assumeYes); err != nil {
				return err
			}
			return nil
```

- [ ] **Step 4: Register the `--yes` flag and update the help text**

Change the flag registration + `Long` (currently lines 1594 and 1732-1733):

`Long` from:

```go
		Long:  "Downloads the latest agent binary from GitHub and uploads it to the device. Use --binary to provide a local binary instead.",
```

to:

```go
		Long: "Updates the agent binary on the device (downloaded from GitHub, or --binary for a local file), then checks for a newer WendyOS image. " +
			"When an OS update is available it prompts before applying (default no); use --yes to apply without prompting. Non-interactive runs report the available update without applying it. " +
			"--nightly selects the nightly channel for both the agent and the OS.",
```

flags from:

```go
	cmd.Flags().StringVar(&binaryPath, "binary", "", "Path to a local agent binary to upload (skips download)")
	cmd.Flags().BoolVar(&nightly, "nightly", false, "Use the latest nightly (prerelease) build")
```

to:

```go
	cmd.Flags().StringVar(&binaryPath, "binary", "", "Path to a local agent binary to upload (skips download)")
	cmd.Flags().BoolVar(&nightly, "nightly", false, "Use the latest nightly (prerelease) build for both the agent and the OS")
	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "Apply an available OS update without prompting")
```

- [ ] **Step 5: Build and run the package tests**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos/go && go build ./internal/cli/... && go test ./internal/cli/commands/ -run 'TestOSAlreadyCurrent|TestDecideOSUpdate|TestMaybeCheckOSUpdateSkips|TestValidateOSUpdate' -v`
Expected: build succeeds; all tests PASS.

- [ ] **Step 6: Verify the flag is wired**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos/go && go run ./cmd/wendy device update --help`
Expected: help text shows `-y, --yes` and the new `--nightly` description; no panic.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/commands/device.go
git commit -m "feat(cli): check for OS updates after device update

device update (and cloud device update) now check for a newer WendyOS
image after updating the agent, prompting before applying (--yes to skip
the prompt). Non-interactive runs report without applying.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Full build, vet, and package test sweep

**Files:** none (verification only)

- [ ] **Step 1: Build everything**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos/go && go build ./...`
Expected: succeeds.

- [ ] **Step 2: Vet the package**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos/go && go vet ./internal/cli/commands/`
Expected: no findings (in particular, no unused-import findings in `os_cmd.go` after the `streamOSUpdate` extraction).

- [ ] **Step 3: Run the full commands package test suite**

Run: `cd /Users/joannisorlandos/git/wendy/wendyos/go && go test ./internal/cli/commands/`
Expected: PASS.

- [ ] **Step 4: Manual verification against a device (notes for the operator)**

These require hardware and are run by the operator, not the agent:
- Direct, up-to-date OS: `wendy device update` → agent updates, then `OS is already at the latest version (...)`.
- Direct, OS behind, interactive: `wendy device update` → prompts `OS update available (...). Apply now? [y/N]`; `y` applies and waits for reboot; `n` prints the skip hint.
- Direct, OS behind, non-interactive (pipe stdin): reports availability, does not apply.
- Direct, `--yes`: applies without prompting.
- `--nightly`: selects nightly OS channel.
- Cloud: `wendy cloud device update --yes` → applies over the tunnel, prints "device is rebooting; reconnect once it is back online" (no online-poll).

---

## Self-Review

**Spec coverage:**
- "Check OS after agent update, GCS artifact, --nightly aware" → Tasks 5/6 (`maybeCheckOSUpdate` calls `getLatestOTAInfoForDeviceType`, passes `nightly`).
- "Check & prompt; default No" → Task 2 `osActionPrompt` + Task 5 `promptYesNoDefaultNoFn(... [y/N] ...)`.
- "Non-interactive report-only, opt-in flag applies" → Task 2 (`osActionReportOnly` / `osActionApply`) + Task 6 `--yes`.
- "Always check; non-WendyOS/no-mender/no-device-type skip silently" → Task 5 early returns.
- "Cloud applies over tunnel, skips reboot poll" → Task 4 cloud reconnect + Task 5 cloud branch.
- "os update behavior unchanged" → Tasks 1 & 3 are behavior-preserving refactors; existing `TestValidateOSUpdate*` still pass.
- "GCS only, never serve from the Mac" → `maybeCheckOSUpdate` only ever uses `otaURL` from `getLatestOTAInfoForDeviceType`; the local-serve paths live solely in `os update`.

**Placeholder scan:** none — every code step contains complete code.

**Type consistency:** `osUpdateAction` constants (`osActionAlreadyCurrent`/`osActionApply`/`osActionPrompt`/`osActionReportOnly`), `osAlreadyCurrent(current, latest, nightly)`, `decideOSUpdate(current, latest, nightly, assumeYes, interactive)`, `streamOSUpdate(ctx, conn, url)`, `reconnectAgentAfterRestart(ctx, host)`, and `maybeCheckOSUpdate(ctx, version, host, nightly, assumeYes)` are used consistently across Tasks 1-6.

**Known limitation (documented in spec):** the cloud apply path relies on `connectToAgent` re-establishing the broker tunnel after the agent restart; this is validated only by the operator's manual cloud test in Task 7. If broker reconnect needs a live-device fix, it is isolated to `reconnectCloudAgentAfterRestart`.
