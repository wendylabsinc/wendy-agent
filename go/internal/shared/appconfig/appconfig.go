// Package appconfig provides parsing and validation of wendy.json application configuration files.
package appconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// appIDPattern restricts appId to characters that are safe to embed in
// container env vars, OTEL_RESOURCE_ATTRIBUTES (key=value,… format), container
// labels, and the OTel service.name resource attribute. A stray comma, '=',
// space, or newline in appId would otherwise corrupt those downstream uses.
var appIDPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,253}$`)

// serviceNamePattern validates serviceName: a lowercase letter as the first
// character, lowercase letters, digits, or hyphens in the middle, and a letter
// or digit as the last character (RFC 1123 DNS label — no trailing hyphens).
// Capped at 57 chars so the derived mDNS hostname "{serviceName}.local" stays
// within 63 chars total (applying the RFC 1123 label limit conservatively to
// the full hostname rather than just the serviceName component).
var serviceNamePattern = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,55}[a-z0-9])?$`)

// EntitlementType enumerates the supported entitlement types.
const (
	EntitlementNetwork   = "network"
	EntitlementBluetooth = "bluetooth"
	EntitlementVideo     = "video"
	EntitlementGPU       = "gpu"
	EntitlementPersist   = "persist"
	EntitlementAudio     = "audio"
	EntitlementCamera    = "camera"
	EntitlementUSB       = "usb"
	EntitlementI2C       = "i2c"
	EntitlementGPIO      = "gpio"
	EntitlementSPI       = "spi"
	EntitlementInput     = "input"
	EntitlementSerial    = "serial"
	EntitlementMCP       = "mcp"
	EntitlementDisplay   = "display"
	// EntitlementAdmin grants full, unauthenticated local control of the agent
	// via its local unix socket — the most security-sensitive entitlement.
	// See entitlements.md for the blast radius.
	EntitlementAdmin = "admin"
	// EntitlementBuild grants the namespace/mount privileges a nested container
	// builder (BuildKit) needs — CAP_SYS_ADMIN plus the unshare/userns-clone
	// syscalls the default seccomp profile denies. It is privileged-equivalent
	// (container→host escape surface); grant only to fully-trusted first-party
	// apps. See entitlements.md for the blast radius.
	EntitlementBuild = "build"
)

// ValidEntitlementTypes is the set of all recognized entitlement type strings.
var ValidEntitlementTypes = []string{
	EntitlementNetwork,
	EntitlementBluetooth,
	EntitlementVideo,
	EntitlementGPU,
	EntitlementPersist,
	EntitlementAudio,
	EntitlementCamera,
	EntitlementUSB,
	EntitlementI2C,
	EntitlementGPIO,
	EntitlementSPI,
	EntitlementInput,
	EntitlementSerial,
	EntitlementMCP,
	EntitlementDisplay,
	EntitlementAdmin,
	EntitlementBuild,
}

var deprecatedEntitlementReplacements = map[string]string{
	EntitlementVideo: EntitlementCamera,
}

// allowedKeys maps each entitlement type to the set of JSON keys that are valid for it.
var allowedKeys = map[string][]string{
	EntitlementNetwork:   {"type", "mode", "ports"},
	EntitlementBluetooth: {"type", "mode"},
	EntitlementVideo:     {"type", "mode", "allowlist"},
	EntitlementGPU:       {"type"},
	EntitlementPersist:   {"type", "name", "path"},
	EntitlementAudio:     {"type"},
	EntitlementCamera:    {"type", "mode", "allowlist"},
	EntitlementUSB:       {"type"},
	EntitlementI2C:       {"type", "device"},
	EntitlementGPIO:      {"type", "pins"},
	EntitlementSPI:       {"type"},
	EntitlementInput:     {"type"},
	EntitlementSerial:    {"type", "device"},
	EntitlementMCP:       {"type", "port"},
	EntitlementDisplay:   {"type"},
	EntitlementAdmin:     {"type"},
	EntitlementBuild:     {"type"},
}

// Platform constants identify the target hardware family.
const (
	PlatformLinux     = "linux"
	PlatformWendyOS   = "wendyos"
	PlatformWendyLite = "wendy-lite"
	PlatformDarwin    = "darwin"
)

// FileSyncEntry describes a file or directory to sync to the device's app
// working directory before the app starts. Path is relative to wendy.json.
// To is the destination path relative to the app working directory; it
// defaults to Path (with any leading ./ stripped) when omitted.
type FileSyncEntry struct {
	Path string `json:"path"`
	To   string `json:"to,omitempty"`
}

// RunConfig holds runtime configuration applied when the app is started.
type RunConfig struct {
	Args []string `json:"args,omitempty"`
}

// ROS2Config holds ROS 2 runtime configuration for a container.
type ROS2Config struct {
	// DomainID is the explicit ROS_DOMAIN_ID. When nil, a stable hash of
	// the appId in the range 0–232 is injected instead (WDY-884).
	DomainID *int `json:"domainId,omitempty"`
	// RMW selects the ROS middleware implementation. Accepts short names
	// ("cyclonedds", "fastrtps") or full identifiers ("rmw_cyclonedds_cpp").
	// Defaults to CycloneDDS.
	RMW string `json:"rmw,omitempty"`
	// Distro is the ROS 2 distribution the app targets (e.g. "humble",
	// "jazzy"). The agent uses it to pick the matching CLI sidecar image.
	// Defaults to "humble".
	Distro string `json:"distro,omitempty"`
}

// FrameworksConfig holds optional framework-level configuration (e.g. ROS 2).
// It is nested under the "frameworks" key in wendy.json (WDY-1339).
type FrameworksConfig struct {
	ROS2 *ROS2Config `json:"ros2,omitempty"`
}

// ServiceConfig holds the per-service build and runtime configuration for a
// multi-service wendy.json (the services map).
type ServiceConfig struct {
	// Context is the build context directory, relative to wendy.json.
	// Required for standalone multi-service apps; omitted in compose companion files.
	Context      string            `json:"context"`
	Entitlements []Entitlement     `json:"entitlements,omitempty"`
	DependsOn    []string          `json:"dependsOn,omitempty"`
	Frameworks   *FrameworksConfig `json:"frameworks,omitempty"`
	// Resources optionally caps this service's CPU/memory/PID usage, overriding
	// any app-level resources wholesale.
	Resources *ResourceLimits `json:"resources,omitempty"`
}

// AppConfig represents the wendy.json application configuration.
type AppConfig struct {
	AppID string `json:"appId"`
	// ServiceName is set when this AppConfig describes a single service within
	// a multi-service app.  When non-empty the agent uses the
	// {appId}_{serviceName} container naming convention (WDY-878).
	ServiceName  string           `json:"serviceName,omitempty"`
	Version      string           `json:"version,omitempty"`
	Platform     string           `json:"platform,omitempty"`
	Language     string           `json:"language,omitempty"`
	Xcode        *XcodeConfig     `json:"xcode,omitempty"`
	Run          *RunConfig       `json:"run,omitempty"`
	Entitlements []Entitlement    `json:"entitlements,omitempty"`
	Readiness    *ReadinessConfig `json:"readiness,omitempty"`
	Hooks        *HooksConfig     `json:"hooks,omitempty"`
	Python       *PythonConfig    `json:"python,omitempty"`
	Debug        bool             `json:"debug,omitempty"`
	Files        []FileSyncEntry  `json:"files,omitempty"`
	// Brewfile is an optional Homebrew Bundle manifest path for native Darwin
	// deployments. It is relative to wendy.json and synced to the target Mac
	// before the agent runs `brew bundle --file`.
	Brewfile string `json:"brewfile,omitempty"`
	// Isolation sets the namespace isolation mode for multi-container deployments
	// (e.g. "shared-ipc"). Enforced by the agent at container creation time.
	Isolation string `json:"isolation,omitempty"`
	// Frameworks holds optional framework-level configuration (e.g. ROS 2).
	// Nested under "frameworks" per WDY-1339.
	Frameworks *FrameworksConfig         `json:"frameworks,omitempty"`
	Services   map[string]*ServiceConfig `json:"services,omitempty"`
	// Resources optionally caps the app's CPU/memory/PID usage. For
	// multi-service apps it is the default; a service may override it.
	Resources *ResourceLimits `json:"resources,omitempty"`
}

// ContainerName returns the container identifier for this app config.
// For multi-service apps (ServiceName != "") it returns "{AppID}_{ServiceName}";
// for single-container apps it returns AppID.
// "_" is the separator because containerd container IDs must match
// ^[A-Za-z0-9]+(?:[._-](?:[A-Za-z0-9]+))*$ and "/" is not permitted.
func (a *AppConfig) ContainerName() string {
	if a.ServiceName != "" {
		return a.AppID + "_" + a.ServiceName
	}
	return a.AppID
}

// XcodeConfig holds Xcode-specific build settings.
type XcodeConfig struct {
	Scheme string `json:"scheme,omitempty"`
}

// ReadinessConfig defines a probe the CLI uses to determine when the app is ready.
type ReadinessConfig struct {
	TCPSocket      *TCPSocketProbe `json:"tcpSocket,omitempty"`
	TimeoutSeconds int             `json:"timeoutSeconds,omitempty"` // Default 30
}

// TCPSocketProbe checks readiness by dialing a TCP port.
type TCPSocketProbe struct {
	Port int `json:"port"`
}

// HooksConfig holds optional lifecycle hook commands.
type HooksConfig struct {
	PostStart *HookCommand `json:"postStart,omitempty"`
}

// PostStartAgentHookMetadataKey carries hooks.postStart.agent on start RPCs
// that should run the agent-side postStart hook.
const PostStartAgentHookMetadataKey = "wendy-post-start-agent-command"

// HookCommand holds CLI and agent-side commands for a lifecycle hook.
//
// OpenURL is the portable way to open a URL in the developer's default browser
// at hook time — the CLI dispatches it directly without a shell, so it works
// uniformly on macOS, Linux, and Windows. Prefer it over `cli: "open …"` /
// `cli: "xdg-open …"` / `cli: "start …"`, which are platform-specific.
type HookCommand struct {
	OpenURL string `json:"openURL,omitempty"` // URL to open in the developer's default browser
	CLI     string `json:"cli,omitempty"`     // Command to run on the developer's machine
	Agent   string `json:"agent,omitempty"`   // Command to run on the device
}

// PythonConfig holds Python-specific configuration.
type PythonConfig struct {
	SourceRoot string `json:"sourceRoot,omitempty"`
}

// PortMapping maps a host port to a container port for network entitlements.
type PortMapping struct {
	Host      uint16 `json:"host"`
	Container uint16 `json:"container"`
}

// Entitlement represents a single entitlement entry in wendy.json.
type Entitlement struct {
	Type      string        `json:"type"`
	Mode      string        `json:"mode,omitempty"`      // Network, Bluetooth, Video
	Allowlist []string      `json:"allowlist,omitempty"` // Camera, Video
	Name      string        `json:"name,omitempty"`      // Persist
	Path      string        `json:"path,omitempty"`      // Persist
	Device    string        `json:"device,omitempty"`    // I2C, Serial
	Pins      []int         `json:"pins,omitempty"`      // GPIO
	Ports     []PortMapping `json:"ports,omitempty"`     // Network
	Port      int           `json:"port,omitempty"`      // MCP
}

// DeprecatedEntitlementReplacement reports the preferred replacement for a deprecated entitlement type.
func DeprecatedEntitlementReplacement(entType string) (string, bool) {
	replacement, ok := deprecatedEntitlementReplacements[entType]
	return replacement, ok
}

// HasEntitlement reports whether the config contains an entitlement of the given type.
func (c *AppConfig) HasEntitlement(entType string) bool {
	for _, e := range c.Entitlements {
		if e.Type == entType {
			return true
		}
	}
	return false
}

// LoadFromFile reads and parses a wendy.json file at the given path.
func LoadFromFile(path string) (*AppConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading wendy.json: %w", err)
	}
	return LoadFromBytes(data)
}

// LoadFromBytes parses a wendy.json from raw bytes.
func LoadFromBytes(data []byte) (*AppConfig, error) {
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing wendy.json: %w", err)
	}
	return &cfg, nil
}

// validateEntitlements checks a slice of entitlements for required fields and
// valid types. The prefix string is used in error messages (e.g.
// "entitlement" for top-level or "services[\"foo\"].entitlement" for service-
// level entitlements).
func validateEntitlements(entitlements []Entitlement, prefix string) error {
	for i, e := range entitlements {
		if e.Type == "" {
			return fmt.Errorf("%s[%d]: type is required", prefix, i)
		}
		if !slices.Contains(ValidEntitlementTypes, e.Type) {
			return fmt.Errorf("%s[%d]: unknown type %q", prefix, i, e.Type)
		}

		switch e.Type {
		case EntitlementNetwork:
			if e.Mode != "" && e.Mode != "host" && e.Mode != "host-admin" && e.Mode != "none" {
				return fmt.Errorf("%s[%d]: network mode must be \"host\", \"host-admin\", or \"none\", got %q", prefix, i, e.Mode)
			}
		case EntitlementPersist:
			if e.Name == "" {
				return fmt.Errorf("%s[%d]: persist entitlement requires a name", prefix, i)
			}
			if e.Path == "" {
				return fmt.Errorf("%s[%d]: persist entitlement requires a path", prefix, i)
			}
			// Persist paths are container destinations, so validate them as
			// POSIX paths regardless of the host OS running the CLI.
			if !path.IsAbs(e.Path) {
				return fmt.Errorf("%s[%d]: persist path must be absolute, got %q", prefix, i, e.Path)
			}
			if containsDotDot(e.Path) {
				return fmt.Errorf("%s[%d]: persist path must not contain '..' components", prefix, i)
			}
		case EntitlementI2C:
			if e.Device == "" {
				return fmt.Errorf("%s[%d]: i2c entitlement requires a device", prefix, i)
			}
			if !isValidI2CDevice(e.Device) {
				return fmt.Errorf("%s[%d]: i2c device must be in i2c-N format, got %q", prefix, i, e.Device)
			}
		case EntitlementSerial:
			if e.Device == "" {
				return fmt.Errorf("%s[%d]: serial entitlement requires a device", prefix, i)
			}
			if !isValidSerialDevice(e.Device) {
				return fmt.Errorf("%s[%d]: serial device must be a bare USB tty node name like ttyACM0 or ttyUSB0, got %q", prefix, i, e.Device)
			}
		case EntitlementGPIO:
			// Pins are optional; omitting them grants access to all GPIO chips.
		case EntitlementMCP:
			if e.Port < 1 || e.Port > 65535 {
				return fmt.Errorf("%s[%d]: mcp port must be between 1 and 65535, got %d", prefix, i, e.Port)
			}
		}
	}

	mcpCount := 0
	for _, e := range entitlements {
		if e.Type == EntitlementMCP {
			mcpCount++
		}
	}
	if mcpCount > 1 {
		return fmt.Errorf("at most one mcp entitlement is allowed in %s, found %d", prefix, mcpCount)
	}

	displayCount := 0
	for _, e := range entitlements {
		if e.Type == EntitlementDisplay {
			displayCount++
		}
	}
	if displayCount > 1 {
		return fmt.Errorf("at most one display entitlement is allowed in %s, found %d", prefix, displayCount)
	}

	adminCount := 0
	for _, e := range entitlements {
		if e.Type == EntitlementAdmin {
			adminCount++
		}
	}
	if adminCount > 1 {
		return fmt.Errorf("at most one admin entitlement is allowed in %s, found %d", prefix, adminCount)
	}

	return nil
}

// ValidateAppID reports whether id is a well-formed appId. It is the appId
// portion of Validate, exported so the agent can reject unsafe ids on the RPC
// path before they are used to build container env vars (WENDY_APP_ID,
// OTEL_SERVICE_NAME, OTEL_RESOURCE_ATTRIBUTES) and labels.
func ValidateAppID(id string) error {
	if id == "" {
		return fmt.Errorf("appId is required")
	}
	if !appIDPattern.MatchString(id) {
		return fmt.Errorf("appId %q is invalid: only letters, digits, '.', '_', and '-' are allowed (max 253 chars)", id)
	}
	// Reject appIDs whose every character is a dot (".", "..", "..." …).
	// Such names would traverse the filesystem when used as a directory component
	// (e.g. "/run/wendy/hosts/.."), which no legitimate Wendy app ID ever requires
	// (SOC2-CC6, ISO27001-A.8, NIST-SI-10).
	if strings.ReplaceAll(id, ".", "") == "" {
		return fmt.Errorf("appId %q is invalid: must contain at least one non-dot character", id)
	}
	return nil
}

// ValidateServiceName reports whether name is a well-formed serviceName.
// serviceName is used to build container IDs, snapshot keys, cgroup paths,
// container labels, and env vars (e.g. WENDY_HOSTNAME={serviceName}.local), so
// it must be a safe DNS label: lowercase letter, then lowercase letters/digits/hyphens,
// ending with a letter or digit.
func ValidateServiceName(name string) error {
	// Cheap length guard before the regex — makes the 57-char cap explicit and
	// avoids running the regex on pathologically long inputs.
	if len(name) > 57 {
		return fmt.Errorf("serviceName too long: %d chars (max 57)", len(name))
	}
	// Fast-fail on characters that break env var or container name invariants,
	// providing defence-in-depth against potential regex edge cases.
	if strings.ContainsAny(name, "\x00\n\r=\t") {
		return fmt.Errorf("serviceName contains invalid control character")
	}
	if !serviceNamePattern.MatchString(name) {
		return fmt.Errorf("serviceName %q is invalid: must start with a lowercase letter, contain only lowercase letters, digits, or hyphens, end with a letter or digit, and be at most 57 chars (RFC 1123)", name)
	}
	return nil
}

// Validate checks the AppConfig for required fields and valid entitlement types.
func (c *AppConfig) Validate() error {
	if err := ValidateAppID(c.AppID); err != nil {
		return err
	}

	if c.ServiceName != "" {
		if err := ValidateServiceName(c.ServiceName); err != nil {
			return err
		}
	}

	if err := validateEntitlements(c.Entitlements, "entitlement"); err != nil {
		return err
	}

	for i, f := range c.Files {
		if f.Path == "" {
			return fmt.Errorf("files[%d]: path is required", i)
		}
		if strings.HasPrefix(f.Path, "/") {
			return fmt.Errorf("files[%d]: path must not be absolute", i)
		}
		if containsDotDot(f.Path) {
			return fmt.Errorf("files[%d]: path must not contain '..' components", i)
		}
		if f.To != "" {
			if strings.HasPrefix(f.To, "/") {
				return fmt.Errorf("files[%d]: to must not be absolute", i)
			}
			if containsDotDot(f.To) {
				return fmt.Errorf("files[%d]: to must not contain '..' components", i)
			}
		}
	}

	if c.Brewfile != "" {
		if !IsSafeRelativeBrewfilePath(c.Brewfile) {
			return fmt.Errorf("brewfile path must be relative and must not contain '.', '..', or empty components")
		}
	}

	if c.Readiness != nil {
		if c.Readiness.TCPSocket != nil {
			port := c.Readiness.TCPSocket.Port
			if port < 1 || port > 65535 {
				return fmt.Errorf("readiness.tcpSocket.port must be between 1 and 65535, got %d", port)
			}
		}
		if c.Readiness.TimeoutSeconds < 0 {
			return fmt.Errorf("readiness.timeoutSeconds must not be negative, got %d", c.Readiness.TimeoutSeconds)
		}
	}

	if c.Frameworks != nil {
		if err := validateROS2Config("frameworks.ros2", c.Frameworks.ROS2); err != nil {
			return err
		}
	}

	if err := c.Resources.validate("resources"); err != nil {
		return err
	}

	for name, svc := range c.Services {
		if svc == nil {
			return fmt.Errorf("services[%q]: must not be null", name)
		}
		if svc.Context == "" {
			return fmt.Errorf("services[%q]: context is required", name)
		}
		if filepath.IsAbs(svc.Context) {
			return fmt.Errorf("services[%q]: context must be a relative path", name)
		}
		if cleaned := filepath.Clean(svc.Context); strings.HasPrefix(cleaned, "..") {
			return fmt.Errorf("services[%q]: context must not contain '..' components", name)
		}
		for _, dep := range svc.DependsOn {
			if _, ok := c.Services[dep]; !ok {
				return fmt.Errorf("services[%q]: dependsOn references unknown service %q", name, dep)
			}
		}
		if err := validateEntitlements(svc.Entitlements, fmt.Sprintf("services[%q].entitlement", name)); err != nil {
			return err
		}
		if svc.Frameworks != nil {
			if err := validateROS2Config(fmt.Sprintf("services[%q].frameworks.ros2", name), svc.Frameworks.ROS2); err != nil {
				return err
			}
		}
		if err := svc.Resources.validate(fmt.Sprintf("services[%q].resources", name)); err != nil {
			return err
		}
	}

	return nil
}

// containsDotDot reports whether p has a path component equal to "..".
func containsDotDot(p string) bool {
	for _, component := range strings.Split(p, "/") {
		if component == ".." {
			return true
		}
	}
	return false
}

func IsSafeRelativeBrewfilePath(p string) bool {
	p = strings.TrimPrefix(p, "./")
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.Contains(p, "%") || strings.Contains(p, "\x00") {
		return false
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return false
		}
	}
	for _, component := range strings.Split(p, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

// LoadComposeCompanion looks for a wendy.json alongside a docker-compose file
// in dir. It returns (nil, nil, nil) when no wendy.json is present — not an
// error. When found, the file is parsed and validated for compose use:
// entitlements are checked, but service context and dependsOn fields are not
// required (they come from the compose file instead).
//
// The returned warnings come from ValidateJSON (unknown keys, deprecated types,
// etc). Service name mismatches against the compose file are the caller's
// responsibility.
func LoadComposeCompanion(dir string) (*AppConfig, []string, error) {
	companionPath := filepath.Join(dir, "wendy.json")
	data, err := os.ReadFile(companionPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("reading companion wendy.json: %w", err)
	}

	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, nil, fmt.Errorf("parsing companion wendy.json: %w", err)
	}

	if err := ValidateAppID(cfg.AppID); err != nil {
		return nil, nil, err
	}

	if err := validateEntitlements(cfg.Entitlements, "entitlement"); err != nil {
		return nil, nil, err
	}

	for name, svc := range cfg.Services {
		if svc == nil {
			return nil, nil, fmt.Errorf("services[%q]: must not be null", name)
		}
		if err := validateEntitlements(svc.Entitlements, fmt.Sprintf("services[%q].entitlement", name)); err != nil {
			return nil, nil, err
		}
	}

	warnings := ValidateJSON(data)
	return &cfg, warnings, nil
}

// isValidI2CDevice reports whether device is a safe I2C device name (i2c-N).
func isValidI2CDevice(device string) bool {
	if !strings.HasPrefix(device, "i2c-") {
		return false
	}
	suffix := device[len("i2c-"):]
	if suffix == "" {
		return false
	}
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// SerialDevicePrefixes is the set of accepted serial tty node-name prefixes for
// the serial entitlement. Each must be followed by one or more digits (the unit
// number). The entitlement is deliberately USB-only: USB CDC-ACM (ttyACM*) and
// USB-serial bridges like FTDI/CH340/CP210x (ttyUSB*). On-board UARTs (ttyAMA*,
// ttyS*) are excluded — ttyS shares its major with a board's system-console
// UART, so allowing it adds attack surface for no peripheral benefit. The
// matching kernel device majors live in the oci package, which builds the cgroup
// allow rule.
var SerialDevicePrefixes = []string{"ttyACM", "ttyUSB"}

// isValidSerialDevice reports whether device is a safe serial tty node name: one
// of SerialDevicePrefixes followed by one or more digits (e.g. "ttyACM0"). The
// value is a bare node name, not a path — applySerial prepends "/dev/". This
// rejects anything with slashes or "..", so it cannot escape /dev.
func isValidSerialDevice(device string) bool {
	for _, prefix := range SerialDevicePrefixes {
		if !strings.HasPrefix(device, prefix) {
			continue
		}
		suffix := device[len(prefix):]
		if suffix == "" {
			return false
		}
		for _, c := range suffix {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	return false
}

// ValidateJSON checks raw JSON data for non-fatal issues that should surface
// as user-visible warnings (unknown entitlement keys, deprecated entitlement
// types, non-portable hook commands) and returns them. Call this after
// decoding to detect potential typos or platform-specific configuration.
func ValidateJSON(data []byte) []string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}

	var warnings []string
	warnings = append(warnings, validateEntitlementsJSON(raw["entitlements"], "entitlement")...)
	warnings = append(warnings, validateHooksJSON(raw["hooks"])...)
	warnings = append(warnings, validateFrameworksJSON(raw["frameworks"], "frameworks")...)

	// Validate service-level entitlements and frameworks when a services map is present.
	// Unmarshal into map[string]json.RawMessage first so a null/invalid entry
	// for one service doesn't silently drop warnings for all other services.
	if servicesRaw, ok := raw["services"]; ok && len(servicesRaw) > 0 {
		var serviceEntries map[string]json.RawMessage
		if err := json.Unmarshal(servicesRaw, &serviceEntries); err == nil {
			for name, svcRaw := range serviceEntries {
				var svc map[string]json.RawMessage
				if err := json.Unmarshal(svcRaw, &svc); err != nil {
					continue
				}
				prefix := fmt.Sprintf("services[%q].entitlement", name)
				warnings = append(warnings, validateEntitlementsJSON(svc["entitlements"], prefix)...)
				warnings = append(warnings, validateFrameworksJSON(svc["frameworks"], fmt.Sprintf("services[%q].frameworks", name))...)
			}
		}
	}

	return warnings
}

// validateFrameworksJSON warns on unknown keys under frameworks.ros2 so a typo
// like "domian_id" surfaces instead of being silently ignored (WDY-1706 M5).
func validateFrameworksJSON(frameworksRaw json.RawMessage, prefix string) []string {
	if len(frameworksRaw) == 0 {
		return nil
	}
	var fw map[string]json.RawMessage
	if err := json.Unmarshal(frameworksRaw, &fw); err != nil {
		return nil
	}
	ros2Raw, ok := fw["ros2"]
	if !ok || len(ros2Raw) == 0 {
		return nil
	}
	var ros2 map[string]json.RawMessage
	if err := json.Unmarshal(ros2Raw, &ros2); err != nil {
		return nil
	}
	allowed := map[string]bool{"domainId": true, "rmw": true, "distro": true}
	var unknown []string
	for k := range ros2 {
		if !allowed[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return []string{fmt.Sprintf("Unknown key(s) in %s.ros2: %s. Allowed keys are: distro, domainId, rmw", prefix, strings.Join(unknown, ", "))}
}

// validateEntitlementsJSON checks raw JSON entitlements for deprecated types
// and unknown keys. prefix is used in warning messages (e.g. "entitlement" for
// top-level, or "services[\"foo\"].entitlement" for service-level).
func validateEntitlementsJSON(entRaw json.RawMessage, prefix string) []string {
	if len(entRaw) == 0 {
		return nil
	}

	var entitlements []map[string]json.RawMessage
	if err := json.Unmarshal(entRaw, &entitlements); err != nil {
		return nil
	}

	var warnings []string
	for i, ent := range entitlements {
		typeRaw, ok := ent["type"]
		if !ok {
			continue
		}
		var entType string
		if err := json.Unmarshal(typeRaw, &entType); err != nil {
			continue
		}

		if replacement, ok := DeprecatedEntitlementReplacement(entType); ok {
			warnings = append(warnings, fmt.Sprintf(
				"%s[%d]: %q is deprecated; use %q instead",
				prefix, i, entType, replacement,
			))
		}

		allowed, ok := allowedKeys[entType]
		if !ok {
			continue
		}

		allowedSet := make(map[string]bool, len(allowed))
		for _, k := range allowed {
			allowedSet[k] = true
		}

		var unknown []string
		for k := range ent {
			if !allowedSet[k] {
				unknown = append(unknown, k)
			}
		}

		if len(unknown) > 0 {
			sort.Strings(unknown)
			sortedAllowed := make([]string, len(allowed))
			copy(sortedAllowed, allowed)
			sort.Strings(sortedAllowed)
			warnings = append(warnings, fmt.Sprintf(
				"Unknown key(s) in %s[%d] (%s): %s. Allowed keys are: %s",
				prefix, i, entType,
				strings.Join(unknown, ", "),
				strings.Join(sortedAllowed, ", "),
			))
		}
	}

	return warnings
}

// nonPortableOpenerCommands maps the bare-binary prefix of a non-portable
// URL-opening shell command to the platform on which it works. Detected at
// the start of hooks.postStart.cli to suggest the portable openURL field.
var nonPortableOpenerCommands = map[string]string{
	"open":     "macOS",
	"xdg-open": "Linux",
	"start":    "Windows",
}

func validateHooksJSON(hooksRaw json.RawMessage) []string {
	if len(hooksRaw) == 0 {
		return nil
	}

	var hooks struct {
		PostStart *struct {
			CLI string `json:"cli"`
		} `json:"postStart"`
	}
	if err := json.Unmarshal(hooksRaw, &hooks); err != nil {
		return nil
	}
	if hooks.PostStart == nil || hooks.PostStart.CLI == "" {
		return nil
	}

	cli := strings.TrimLeft(hooks.PostStart.CLI, " \t")
	for opener, platform := range nonPortableOpenerCommands {
		if cli == opener || strings.HasPrefix(cli, opener+" ") || strings.HasPrefix(cli, opener+"\t") {
			return []string{fmt.Sprintf(
				"hooks.postStart.cli starts with %q, which only works on %s; use \"openURL\" to open a URL portably across macOS, Linux, and Windows",
				opener, platform,
			)}
		}
	}
	return nil
}

// IsSharedNamespaceIsolation reports whether isolation is a mode that shares
// Linux namespaces across containers in an app group.
func IsSharedNamespaceIsolation(isolation string) bool {
	return isolation == "shared-ipc" || isolation == "shared-network"
}

// GetROS2Config returns the ROS2 framework config if set, nil otherwise.
func (a *AppConfig) GetROS2Config() *ROS2Config {
	if a.Frameworks == nil {
		return nil
	}
	return a.Frameworks.ROS2
}
