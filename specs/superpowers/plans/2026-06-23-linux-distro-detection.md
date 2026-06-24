# Linux Distro Detection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Populate `GetDeviceInfoResponse.os` with the Linux distro ID (e.g. `"ubuntu"`, `"arch"`, `"rhel"`) and `os_version` with the distro version on non-WendyOS hosts.

**Architecture:** Add a build-tagged `detectDistro()`/`detectOS()` helper pair that reads `/etc/os-release` (with legacy fallbacks), then wire those helpers into the two existing gRPC service handlers that currently hard-code `runtime.GOOS`.

**Tech Stack:** Go 1.21+, standard library only (`bufio`, `os`, `regexp`, `strings`, `runtime`), Go build tags for Linux/non-Linux split.

## Global Constraints

- No new external dependencies — standard library only
- Build tag `//go:build linux` on Linux-specific file; `//go:build !linux` on the stub
- Package: `services` (same as all files in `go/internal/agent/services/`)
- `os_version` WendyOS value takes precedence: only fall back to distro version when `wendyOSVersion()` returns `false`
- On non-Linux platforms, `detectOS()` must return `runtime.GOOS` unchanged
- All new tests live in `go/internal/agent/services/distro_linux_test.go` with `//go:build linux`

---

### Task 1: Detection helpers + tests

**Files:**
- Create: `go/internal/agent/services/distro_linux.go`
- Create: `go/internal/agent/services/distro_other.go`
- Create: `go/internal/agent/services/distro_linux_test.go`

**Interfaces:**
- Produces:
  - `detectDistro() (id, version string)` — returns distro ID and version; empty strings when unknown
  - `detectOS() string` — returns distro ID on Linux if known, otherwise `runtime.GOOS`

---

- [ ] **Step 1: Write the failing tests**

Create `go/internal/agent/services/distro_linux_test.go`:

```go
//go:build linux

package services

import (
	"os"
	"path/filepath"
	"testing"
)

// tempFile writes content to a named file in a temp dir and returns the path.
func tempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseOSRelease(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantID      string
		wantVersion string
		wantOK      bool
	}{
		{
			name:        "ubuntu 22.04 with quotes",
			content:     "ID=\"ubuntu\"\nVERSION_ID=\"22.04\"\nNAME=\"Ubuntu\"\n",
			wantID:      "ubuntu",
			wantVersion: "22.04",
			wantOK:      true,
		},
		{
			name:        "arch linux no version",
			content:     "ID=arch\nNAME=\"Arch Linux\"\n",
			wantID:      "arch",
			wantVersion: "",
			wantOK:      true,
		},
		{
			name:        "debian 12",
			content:     "ID=debian\nVERSION_ID=\"12\"\n",
			wantID:      "debian",
			wantVersion: "12",
			wantOK:      true,
		},
		{
			name:        "rhel 8 via os-release",
			content:     "ID=\"rhel\"\nVERSION_ID=\"8.6\"\n",
			wantID:      "rhel",
			wantVersion: "8.6",
			wantOK:      true,
		},
		{
			name:    "empty file",
			content: "\n",
			wantOK:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tempFile(t, "os-release", tt.content)
			id, version, ok := parseOSRelease(path)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v; want %v", ok, tt.wantOK)
			}
			if id != tt.wantID {
				t.Errorf("id = %q; want %q", id, tt.wantID)
			}
			if version != tt.wantVersion {
				t.Errorf("version = %q; want %q", version, tt.wantVersion)
			}
		})
	}
}

func TestParseRedHatRelease(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantID      string
		wantVersion string
		wantOK      bool
	}{
		{
			name:        "rhel 8",
			content:     "Red Hat Enterprise Linux release 8.6 (Ootpa)\n",
			wantID:      "rhel",
			wantVersion: "8.6",
			wantOK:      true,
		},
		{
			name:        "centos 7",
			content:     "CentOS Linux release 7.9.2009 (Core)\n",
			wantID:      "centos",
			wantVersion: "7.9.2009",
			wantOK:      true,
		},
		{
			name:        "fedora 38",
			content:     "Fedora release 38 (Thirty Eight)\n",
			wantID:      "fedora",
			wantVersion: "38",
			wantOK:      true,
		},
		{
			name:    "unrecognised content",
			content: "Something else entirely\n",
			wantOK:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tempFile(t, "redhat-release", tt.content)
			id, version, ok := parseRedHatRelease(path)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v; want %v", ok, tt.wantOK)
			}
			if id != tt.wantID {
				t.Errorf("id = %q; want %q", id, tt.wantID)
			}
			if version != tt.wantVersion {
				t.Errorf("version = %q; want %q", version, tt.wantVersion)
			}
		})
	}
}

func TestParseDebianVersion(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantVersion string
		wantOK      bool
	}{
		{
			name:        "debian 11",
			content:     "11.5\n",
			wantVersion: "11.5",
			wantOK:      true,
		},
		{
			name:    "empty",
			content: "  \n",
			wantOK:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tempFile(t, "debian_version", tt.content)
			version, ok := parseDebianVersion(path)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v; want %v", ok, tt.wantOK)
			}
			if version != tt.wantVersion {
				t.Errorf("version = %q; want %q", version, tt.wantVersion)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail (functions not yet defined)**

```bash
cd go && go test ./internal/agent/services/ -run 'TestParseOSRelease|TestParseRedHatRelease|TestParseDebianVersion' -v
```

Expected: compile error — `parseOSRelease`, `parseRedHatRelease`, `parseDebianVersion` undefined.

- [ ] **Step 3: Implement `distro_linux.go`**

Create `go/internal/agent/services/distro_linux.go`:

```go
//go:build linux

package services

import (
	"bufio"
	"os"
	"regexp"
	"runtime"
	"strings"
)

// detectDistro reads /etc/os-release (or legacy fallback files) and returns
// the distribution ID (e.g. "ubuntu", "arch", "rhel") and version (e.g. "22.04").
// Both strings are empty when the host distribution cannot be identified.
func detectDistro() (id, version string) {
	if id, version, ok := parseOSRelease("/etc/os-release"); ok {
		return id, version
	}
	if id, version, ok := parseRedHatRelease("/etc/redhat-release"); ok {
		return id, version
	}
	if version, ok := parseDebianVersion("/etc/debian_version"); ok {
		return "debian", version
	}
	return "", ""
}

// detectOS returns the Linux distribution ID when known, otherwise runtime.GOOS.
func detectOS() string {
	if id, _ := detectDistro(); id != "" {
		return id
	}
	return runtime.GOOS
}

// parseOSRelease parses a KEY="value" file (typically /etc/os-release) and
// returns the ID and VERSION_ID fields.
func parseOSRelease(path string) (id, version string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer f.Close()

	vals := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		k, v, found := strings.Cut(scanner.Text(), "=")
		if !found {
			continue
		}
		vals[k] = strings.Trim(v, `"`)
	}
	id = vals["ID"]
	if id == "" {
		return "", "", false
	}
	return id, vals["VERSION_ID"], true
}

var rhRelRe = regexp.MustCompile(`(?i)(red\s*hat|centos|fedora)[^\d]*([\d.]+)`)

// parseRedHatRelease parses /etc/redhat-release used by RHEL 6 / CentOS 6.
func parseRedHatRelease(path string) (id, version string, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	m := rhRelRe.FindSubmatch(data)
	if len(m) < 3 {
		return "", "", false
	}
	raw := strings.ToLower(string(m[1]))
	switch {
	case strings.Contains(raw, "red"):
		id = "rhel"
	case strings.Contains(raw, "centos"):
		id = "centos"
	default:
		id = "fedora"
	}
	return id, string(m[2]), true
}

// parseDebianVersion parses /etc/debian_version used by old Debian installs.
func parseDebianVersion(path string) (version string, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	v := strings.TrimSpace(string(data))
	if v == "" {
		return "", false
	}
	return v, true
}
```

- [ ] **Step 4: Implement `distro_other.go` stub**

Create `go/internal/agent/services/distro_other.go`:

```go
//go:build !linux

package services

import "runtime"

func detectDistro() (string, string) { return "", "" }
func detectOS() string               { return runtime.GOOS }
```

- [ ] **Step 5: Run the tests — expect PASS**

```bash
cd go && go test ./internal/agent/services/ -run 'TestParseOSRelease|TestParseRedHatRelease|TestParseDebianVersion' -v
```

Expected output (all pass):
```
--- PASS: TestParseOSRelease/ubuntu_22.04_with_quotes
--- PASS: TestParseOSRelease/arch_linux_no_version
--- PASS: TestParseOSRelease/debian_12
--- PASS: TestParseOSRelease/rhel_8_via_os-release
--- PASS: TestParseOSRelease/empty_file
--- PASS: TestParseRedHatRelease/rhel_8
--- PASS: TestParseRedHatRelease/centos_7
--- PASS: TestParseRedHatRelease/fedora_38
--- PASS: TestParseRedHatRelease/unrecognised_content
--- PASS: TestParseDebianVersion/debian_11
--- PASS: TestParseDebianVersion/empty
PASS
```

- [ ] **Step 6: Verify the package compiles on non-Linux (the stub)**

```bash
cd go && GOOS=darwin go build ./internal/agent/services/
```

Expected: no output (clean compile).

- [ ] **Step 7: Commit**

```bash
git add go/internal/agent/services/distro_linux.go \
        go/internal/agent/services/distro_other.go \
        go/internal/agent/services/distro_linux_test.go
git commit -m "feat(agent): add Linux distro detection helpers"
```

---

### Task 2: Wire into device\_info\_service and agent\_service

**Files:**
- Modify: `go/internal/agent/services/device_info_service.go:28-37`
- Modify: `go/internal/agent/services/agent_service.go:58-67`

**Interfaces:**
- Consumes:
  - `detectOS() string` — from Task 1
  - `detectDistro() (string, string)` — from Task 1
  - `wendyOSVersion() (string, bool)` — existing function in `agent_service.go:615`

---

- [ ] **Step 1: Update `device_info_service.go`**

In `go/internal/agent/services/device_info_service.go`, replace the `Os` field assignment and the `wendyOSVersion` block (lines 28–37):

Before:
```go
resp := &agentpbv2.GetDeviceInfoResponse{
    Version:         version.Version,
    Os:              runtime.GOOS,
    CpuArchitecture: runtime.GOARCH,
    Featureset:      detectFeatureset(),
}

if v, ok := wendyOSVersion(); ok {
    resp.OsVersion = &v
}
```

After:
```go
resp := &agentpbv2.GetDeviceInfoResponse{
    Version:         version.Version,
    Os:              detectOS(),
    CpuArchitecture: runtime.GOARCH,
    Featureset:      detectFeatureset(),
}

if v, ok := wendyOSVersion(); ok {
    resp.OsVersion = &v
} else if _, distroVer := detectDistro(); distroVer != "" {
    resp.OsVersion = &distroVer
}
```

Also remove the now-unused `runtime` import from `device_info_service.go` if `runtime.GOARCH` is the only remaining usage — check: `runtime.GOARCH` is still present, so the import stays.

- [ ] **Step 2: Update `agent_service.go`**

In `go/internal/agent/services/agent_service.go`, replace the `Os` field and `wendyOSVersion` block (lines 58–67):

Before:
```go
resp := &agentpb.GetAgentVersionResponse{
    Version:         version.Version,
    Os:              runtime.GOOS,
    CpuArchitecture: runtime.GOARCH,
    Featureset:      detectFeatureset(),
}

if v, ok := wendyOSVersion(); ok {
    resp.OsVersion = &v
}
```

After:
```go
resp := &agentpb.GetAgentVersionResponse{
    Version:         version.Version,
    Os:              detectOS(),
    CpuArchitecture: runtime.GOARCH,
    Featureset:      detectFeatureset(),
}

if v, ok := wendyOSVersion(); ok {
    resp.OsVersion = &v
} else if _, distroVer := detectDistro(); distroVer != "" {
    resp.OsVersion = &distroVer
}
```

- [ ] **Step 3: Run the existing device-info service test**

```bash
cd go && go test ./internal/agent/services/ -run 'TestDeviceInfoService' -v
```

Expected:
```
--- PASS: TestDeviceInfoService_GetDeviceInfo
--- PASS: TestDeviceInfoService_ListHardwareCapabilities
PASS
```

Note: `TestDeviceInfoService_GetDeviceInfo` currently asserts `resp.Os == runtime.GOOS`. On a Linux host this will now return a distro string, not `"linux"`. The test needs a small update — see Step 4.

- [ ] **Step 4: Update the Os assertion in the device-info test**

In `go/internal/agent/services/device_info_service_test.go`, replace:

```go
if resp.Os != runtime.GOOS {
    t.Errorf("os = %q; want %q", resp.Os, runtime.GOOS)
}
```

with:

```go
if resp.Os == "" {
    t.Errorf("os is empty")
}
```

This accepts any non-empty string — `"linux"`, `"ubuntu"`, `"darwin"`, etc. — all valid depending on the host.

- [ ] **Step 5: Run the full services package tests**

```bash
cd go && go test ./internal/agent/services/ -v -count=1 2>&1 | tail -20
```

Expected: all tests PASS, zero failures.

- [ ] **Step 6: Verify clean compile on darwin**

```bash
cd go && GOOS=darwin go build ./...
```

Expected: no output (clean compile).

- [ ] **Step 7: Commit**

```bash
git add go/internal/agent/services/device_info_service.go \
        go/internal/agent/services/agent_service.go \
        go/internal/agent/services/device_info_service_test.go
git commit -m "feat(agent): populate os/os_version from Linux distro detection"
```
