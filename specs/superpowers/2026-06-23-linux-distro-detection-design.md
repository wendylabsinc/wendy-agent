# Linux Distribution Detection in wendy-agent

**Date:** 2026-06-23  
**Status:** Approved

## Problem

`GetDeviceInfoResponse.os` is always `runtime.GOOS` ("linux"), giving no signal about the host distribution. `os_version` is only populated for WendyOS devices. MCP tools, CLI display, and agent behavior all receive the same opaque `"linux"` string regardless of whether the device runs Ubuntu, Arch, RHEL, or Debian.

## Goals

- Populate `os` with the distro ID (e.g. `"ubuntu"`, `"arch"`, `"rhel"`, `"debian"`) on Linux
- Populate `os_version` with the distro version (e.g. `"22.04"`) on non-WendyOS Linux hosts
- No new proto fields; no new dependencies
- Transparent to existing CLI, MCP, and AI consumers — they already read `os` and `os_version` as plain strings

## Non-Goals

- Detecting distro variant names beyond the `ID` field (e.g. "Ubuntu Server" vs "Ubuntu Desktop")
- Package manager detection
- Any agent behavior change based on distro (future work)

## Detection Logic

New file: `go/internal/agent/services/distro_linux.go` (`//go:build linux`)  
Companion stub: `go/internal/agent/services/distro_other.go` (`//go:build !linux`)

```
detectDistro() (id, version string)
```

Detection order:

1. **`/etc/os-release`** (systemd standard; present on Ubuntu, Debian, Arch, RHEL 7+, Fedora, CentOS 7+)  
   - Parse `KEY="value"` lines; extract `ID` → id, `VERSION_ID` → version  
   - Strip surrounding quotes from values

2. **`/etc/redhat-release`** (RHEL 6 / CentOS 6 fallback)  
   - Regex: `(?i)(red hat|centos|fedora)[^\d]*([\d.]+)`  
   - id = normalized name (`"rhel"`, `"centos"`, `"fedora"`), version = captured digits

3. **`/etc/debian_version`** (ancient Debian fallback)  
   - id = `"debian"`, version = trimmed file content

4. **No file found** → return `"", ""`

The stub always returns `"", ""`.

## Integration

### `detectOS()` helper (also build-tagged)

```
// distro_linux.go
func detectOS() string {
    id, _ := detectDistro()
    if id != "" {
        return id
    }
    return runtime.GOOS
}

// distro_other.go
func detectOS() string {
    return runtime.GOOS
}
```

### `device_info_service.go` and `agent_service.go`

Replace:
```go
Os: runtime.GOOS,
```
with:
```go
Os: detectOS(),
```

For `OsVersion`: the existing `wendyOSVersion()` check runs first and takes precedence. After it, if `os_version` is still unset, call `detectDistro()` and populate `OsVersion` with the version string if non-empty.

## Proto

No changes. `os` (string) and `os_version` (optional string) already exist in `GetDeviceInfoResponse`. No proto regeneration needed.

## Testing

`go/internal/agent/services/distro_linux_test.go`:

- Table-driven test writing temp files for each source (`/etc/os-release`, `/etc/redhat-release`, `/etc/debian_version`)
- Asserts correct `(id, version)` for: ubuntu 22.04, debian 12, arch (no VERSION_ID), rhel 8.6 via legacy file, centos 7 via legacy file, unknown host
- Verifies priority: `/etc/os-release` wins when multiple files are present

## Example outcomes

| Host | `os` | `os_version` |
|------|------|--------------|
| WendyOS (Ubuntu base) | `"ubuntu"` | WendyOS version (from `/etc/wendyos/version.txt`) |
| Ubuntu 22.04 | `"ubuntu"` | `"22.04"` |
| Debian 12 | `"debian"` | `"12"` |
| Arch Linux | `"arch"` | _(empty — Arch has no VERSION\_ID)_ |
| RHEL 8.6 | `"rhel"` | `"8.6"` |
| RHEL 6 (legacy) | `"rhel"` | `"6.10"` |
| macOS | `"darwin"` | _(unchanged)_ |
| Unknown Linux | `"linux"` | _(unchanged)_ |
