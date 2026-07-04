package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/distribution/reference"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// normalizeImageRef canonicalises a Docker short reference (e.g.
// "python:3.11-slim", "nginx") to a fully-qualified form
// ("docker.io/library/python:3.11-slim") that the agent's containerd
// reference parser accepts. References that already include a registry,
// digest, or tag pass through unchanged. When parsing fails (malformed
// reference), the original input is returned and the agent surfaces the
// resulting error.
func normalizeImageRef(ref string) string {
	named, err := reference.ParseNormalizedNamed(strings.TrimSpace(ref))
	if err != nil {
		return ref
	}
	return reference.TagNameOnly(named).String()
}

// composeConfig is a minimal representation of a docker-compose file.
type composeConfig struct {
	Services map[string]composeService `yaml:"services"`
}

type composeService struct {
	Image       string    `yaml:"image"`
	Build       yaml.Node `yaml:"build"` // string or build object
	Command     yaml.Node `yaml:"command"`
	Environment yaml.Node `yaml:"environment"` // map or list
	Ports       []string  `yaml:"ports"`
	Volumes     []string  `yaml:"volumes"`
	DependsOn   yaml.Node `yaml:"depends_on"` // list or map
	Restart     string    `yaml:"restart"`
	NetworkMode string    `yaml:"network_mode"`

	// Captured only for warning purposes — not used in deployment.
	Devices     yaml.Node `yaml:"devices"`
	Privileged  yaml.Node `yaml:"privileged"`
	CapAdd      yaml.Node `yaml:"cap_add"`
	SecurityOpt yaml.Node `yaml:"security_opt"`
	IPC         string    `yaml:"ipc"`
	PID         string    `yaml:"pid"`
	ShmSize     string    `yaml:"shm_size"`
	HealthCheck yaml.Node `yaml:"healthcheck"`
	Profiles    yaml.Node `yaml:"profiles"`
	Secrets     yaml.Node `yaml:"secrets"`
	ExtraHosts  yaml.Node `yaml:"extra_hosts"`
}

// unsupportedComposeWarnings returns field names from svc that Wendy does not
// honour during deployment. The caller should print these to the user.
func unsupportedComposeWarnings(svc composeService) []string {
	type check struct {
		name  string
		empty bool
	}
	checks := []check{
		{"devices", svc.Devices.IsZero()},
		{"privileged", svc.Privileged.IsZero()},
		{"cap_add", svc.CapAdd.IsZero()},
		{"security_opt", svc.SecurityOpt.IsZero()},
		{"ipc", svc.IPC == ""},
		{"pid", svc.PID == ""},
		{"shm_size", svc.ShmSize == ""},
		{"healthcheck", svc.HealthCheck.IsZero()},
		{"profiles", svc.Profiles.IsZero()},
		{"secrets", svc.Secrets.IsZero()},
		{"extra_hosts", svc.ExtraHosts.IsZero()},
	}
	var warned []string
	for _, c := range checks {
		if !c.empty {
			warned = append(warned, c.name)
		}
	}
	return warned
}

type composeBuildConfig struct {
	Context    string            `yaml:"context"`
	Dockerfile string            `yaml:"dockerfile"`
	Args       map[string]string `yaml:"args"`
}

// parseComposeFile reads and parses a docker-compose file.
func parseComposeFile(dir string) (*composeConfig, string, error) {
	for _, name := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg composeConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, "", fmt.Errorf("parsing %s: %w", name, err)
		}
		return &cfg, name, nil
	}
	return nil, "", fmt.Errorf("no docker-compose file found in %s", dir)
}

// composeBuildContext returns the build context dir and Dockerfile for a service.
// Returns ("", "", nil) when the service uses a pre-built image.
func composeBuildContext(svc composeService, projectDir string) (ctxDir, dockerfile string, buildArgs map[string]string, err error) {
	if svc.Build.IsZero() {
		return "", "", nil, nil
	}

	switch svc.Build.Kind {
	case yaml.ScalarNode:
		// build: ./path
		ctxDir = filepath.Join(projectDir, svc.Build.Value)
		return ctxDir, "Dockerfile", nil, nil

	case yaml.MappingNode:
		var bc composeBuildConfig
		if err := svc.Build.Decode(&bc); err != nil {
			return "", "", nil, fmt.Errorf("decoding build config: %w", err)
		}
		ctxDir = projectDir
		if bc.Context != "" {
			ctxDir = filepath.Join(projectDir, bc.Context)
		}
		df := "Dockerfile"
		if bc.Dockerfile != "" {
			if err := validateComposeDockerfileName(bc.Dockerfile); err != nil {
				return "", "", nil, fmt.Errorf("compose dockerfile: %w", err)
			}
			if _, err := confinedDockerfilePath(ctxDir, bc.Dockerfile); err != nil {
				return "", "", nil, fmt.Errorf("compose dockerfile: %w", err)
			}
			df = bc.Dockerfile
		}
		return ctxDir, df, bc.Args, nil
	}

	return "", "", nil, fmt.Errorf("unsupported build directive (yaml kind %d); expected a path string or a mapping", svc.Build.Kind)
}

// composeCommand returns the command for a service as a slice. Sequence form
// preserves each argv element verbatim. Scalar form is shell-split into argv
// tokens, matching docker-compose's documented behaviour.
func composeCommand(svc composeService) []string {
	if svc.Command.IsZero() {
		return nil
	}
	switch svc.Command.Kind {
	case yaml.ScalarNode:
		return shellSplit(svc.Command.Value)
	case yaml.SequenceNode:
		var parts []string
		_ = svc.Command.Decode(&parts)
		return parts
	}
	return nil
}

// composeArgv splits a service's command into a (cmd, extraArgs) pair suitable
// for CreateContainerRequest.Cmd / UserArgs. cmd is guaranteed to be a single
// shell-safe token (no whitespace) so the agent's strings.Fields(cmd) split is
// a no-op; the remaining argv tokens flow through UserArgs unchanged so
// arguments containing whitespace (e.g. a `-c <script>` body) are preserved.
func composeArgv(svc composeService) (string, []string) {
	parts := composeCommand(svc)
	if len(parts) == 0 {
		return "", nil
	}
	return parts[0], parts[1:]
}

// shellSplit performs minimal POSIX-style splitting on a string command:
// whitespace separates tokens, and pairs of single or double quotes group a
// run of characters into one token. Backslash escapes are not interpreted —
// callers needing those should use the YAML sequence form.
func shellSplit(s string) []string {
	var (
		tokens []string
		cur    strings.Builder
		quote  rune
		inTok  bool
	)
	flush := func() {
		if inTok {
			tokens = append(tokens, cur.String())
			cur.Reset()
			inTok = false
		}
	}
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
			inTok = true
		case r == '\'' || r == '"':
			quote = r
			inTok = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		default:
			cur.WriteRune(r)
			inTok = true
		}
	}
	flush()
	return tokens
}

// composeEnv returns environment variables for a service as KEY=VALUE strings.
// Mapping values may be strings, numbers, bools, or null (inherit from process env).
// Sequence entries may be "KEY=VALUE" or bare "KEY" (inherit from process env).
func composeEnv(svc composeService) []string {
	if svc.Environment.IsZero() {
		return nil
	}
	var result []string
	switch svc.Environment.Kind {
	case yaml.MappingNode:
		var m map[string]any
		if err := svc.Environment.Decode(&m); err == nil {
			for k, v := range m {
				if v == nil {
					if inherited, ok := os.LookupEnv(k); ok {
						result = append(result, k+"="+inherited)
					}
					continue
				}
				result = append(result, k+"="+fmt.Sprint(v))
			}
		}
	case yaml.SequenceNode:
		var list []string
		if err := svc.Environment.Decode(&list); err == nil {
			for _, entry := range list {
				if strings.Contains(entry, "=") {
					result = append(result, entry)
					continue
				}
				if inherited, ok := os.LookupEnv(entry); ok {
					result = append(result, entry+"="+inherited)
				}
			}
		}
	}
	return result
}

// composeRestartPolicy converts a compose restart string to a proto RestartPolicy.
func composeRestartPolicy(restart string) *agentpb.RestartPolicy {
	switch restart {
	case "always", "unless-stopped":
		return &agentpb.RestartPolicy{Mode: agentpb.RestartPolicyMode_UNLESS_STOPPED}
	case "on-failure":
		return &agentpb.RestartPolicy{Mode: agentpb.RestartPolicyMode_ON_FAILURE}
	case "no", "":
		return &agentpb.RestartPolicy{Mode: agentpb.RestartPolicyMode_NO}
	default:
		return &agentpb.RestartPolicy{Mode: agentpb.RestartPolicyMode_DEFAULT}
	}
}

// parseComposeVolume parses a docker-compose short volume spec into its
// (source, target, mode) parts. Handles three forms:
//
//	target                       — anonymous volume
//	source:target                — named volume or bind mount
//	source:target:options        — with options like "ro" or "rw"
//
// Windows-style absolute paths (e.g. "C:\\data:/in") are detected when the
// first segment is a single drive letter and merged back with the second.
// Returns empty strings when the input cannot be parsed.
func parseComposeVolume(v string) (source, target, mode string) {
	parts := strings.Split(v, ":")
	// Re-merge a leading "<letter>:<path>" Windows-style drive prefix.
	if len(parts) >= 2 && len(parts[0]) == 1 {
		c := parts[0][0]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			parts = append([]string{parts[0] + ":" + parts[1]}, parts[2:]...)
		}
	}
	switch len(parts) {
	case 1:
		return "", parts[0], ""
	case 2:
		return parts[0], parts[1], ""
	case 3:
		return parts[0], parts[1], parts[2]
	default:
		return "", "", ""
	}
}

// composeAppConfig builds an AppConfig for a compose service.
// It synthesises network entitlements from ports/network_mode and persist
// entitlements from named volumes.
//
// numServices is the total number of services in the compose file. When
// numServices > 1 and no companion wendy.json overrides the appID, all
// services are grouped under the project name (appID = projectName,
// ServiceName = serviceName). Single-service apps keep the legacy
// "projectName-serviceName" appID so existing deployments are unaffected.
func composeAppConfig(projectName, serviceName string, svc composeService, numServices int) *appconfig.AppConfig {
	var appID string
	var svcName string
	if numServices > 1 {
		appID = projectName
		svcName = serviceName
	} else {
		appID = projectName + "-" + serviceName
	}

	var entitlements []appconfig.Entitlement

	// Network entitlement.
	if svc.NetworkMode == "host" {
		entitlements = append(entitlements, appconfig.Entitlement{
			Type: appconfig.EntitlementNetwork,
			Mode: "host",
		})
	} else if len(svc.Ports) > 0 {
		var ports []appconfig.PortMapping
		for _, p := range svc.Ports {
			// Parse "host:container" or "container" format.
			parts := strings.SplitN(p, ":", 2)
			var pm appconfig.PortMapping
			if len(parts) == 2 {
				fmt.Sscanf(parts[0], "%d", &pm.Host)
				fmt.Sscanf(parts[1], "%d", &pm.Container)
			} else {
				fmt.Sscanf(parts[0], "%d", &pm.Container)
				pm.Host = pm.Container
			}
			if pm.Host > 0 && pm.Container > 0 {
				ports = append(ports, pm)
			}
		}
		if len(ports) > 0 {
			entitlements = append(entitlements, appconfig.Entitlement{
				Type:  appconfig.EntitlementNetwork,
				Ports: ports,
			})
		}
	}

	// Persist entitlements from named volumes (skip host-bind mounts ./path:).
	for _, v := range svc.Volumes {
		source, target, _ := parseComposeVolume(v)
		if source == "" || target == "" {
			continue
		}
		// Named volumes start with a letter; bind mounts start with . or /
		// (or, on Windows, a drive letter like "C:\\…" — already merged into source).
		if strings.HasPrefix(source, ".") || strings.HasPrefix(source, "/") {
			continue
		}
		if len(source) >= 2 && source[1] == ':' {
			// Windows-style absolute path bind mount.
			continue
		}
		entitlements = append(entitlements, appconfig.Entitlement{
			Type: appconfig.EntitlementPersist,
			Name: source,
			Path: target,
		})
	}

	return &appconfig.AppConfig{
		AppID:        appID,
		ServiceName:  svcName,
		Entitlements: entitlements,
	}
}

// composeCompanionWarnings returns warnings for service names declared in the
// companion wendy.json that have no matching service in the compose file.
// A nil companion produces no warnings.
func composeCompanionWarnings(companion *appconfig.AppConfig, composeCfg *composeConfig) []string {
	if companion == nil || len(companion.Services) == 0 {
		return nil
	}
	var warnings []string
	for name := range companion.Services {
		if _, ok := composeCfg.Services[name]; !ok {
			warnings = append(warnings, fmt.Sprintf("wendy.json: service %q is not defined in the compose file", name))
		}
	}
	sort.Strings(warnings)
	return warnings
}

// applyComposeCompanion merges Wendy-specific config from a companion wendy.json
// into an AppConfig synthesised from compose fields. companion may be nil (no
// wendy.json present).
//
// Merge rules:
//   - AppID and ServiceName are set from the companion so the agent creates the
//     container as "{appId}/{serviceName}" (WDY-878) and injects WENDY_APP_ID /
//     WENDY_HOSTNAME correctly.
//   - Top-level isolation and frameworks from the companion are applied to every service.
//   - Top-level entitlements from the companion are appended to every service's
//     entitlements (compose-derived network/persist entitlements are preserved).
//   - Per-service entitlements are appended on top of the shared ones.
//   - Per-service frameworks override the group-level frameworks for that service.
//   - Duplicate entitlements (same type+name+mode) are removed; the first
//     occurrence wins so compose-synthesised entitlements take precedence.
func applyComposeCompanion(appCfg *appconfig.AppConfig, companion *appconfig.AppConfig, serviceName string) {
	if companion == nil {
		return
	}
	appCfg.AppID = companion.AppID
	appCfg.ServiceName = serviceName
	appCfg.Isolation = companion.Isolation
	appCfg.Frameworks = companion.Frameworks
	appCfg.Entitlements = append(appCfg.Entitlements, companion.Entitlements...)
	if svc, ok := companion.Services[serviceName]; ok && svc != nil {
		appCfg.Entitlements = append(appCfg.Entitlements, svc.Entitlements...)
		if svc.Frameworks != nil {
			appCfg.Frameworks = svc.Frameworks
		}
	}
	appCfg.Entitlements = deduplicateEntitlements(appCfg.Entitlements)
}

// deduplicateEntitlements returns a copy of ents with duplicates removed.
// Two entitlements are considered duplicates when their type, name, and mode
// are equal; the first occurrence is kept. This covers the common cases:
//   - GPU declared in both shared and per-service sections
//   - Network mode declared in both compose (network_mode:host) and companion
//   - Persist volumes declared multiple times with the same name
func deduplicateEntitlements(ents []appconfig.Entitlement) []appconfig.Entitlement {
	seen := make(map[string]bool, len(ents))
	out := make([]appconfig.Entitlement, 0, len(ents))
	for _, e := range ents {
		key := string(e.Type) + "\x00" + e.Name + "\x00" + e.Mode
		if !seen[key] {
			seen[key] = true
			out = append(out, e)
		}
	}
	return out
}

// serviceOrder returns service names sorted by depends_on so dependencies
// start before dependents. It returns an error if any depends_on entry
// references an undefined service. Cycles are ignored; remaining services are
// appended at the end.
func serviceOrder(cfg *composeConfig) ([]string, error) {
	// Build dependency map and validate that every dependency is a defined service.
	deps := make(map[string][]string, len(cfg.Services))
	for name, svc := range cfg.Services {
		var depends []string
		switch svc.DependsOn.Kind {
		case yaml.SequenceNode:
			_ = svc.DependsOn.Decode(&depends)
		case yaml.MappingNode:
			var m map[string]interface{}
			if svc.DependsOn.Decode(&m) == nil {
				for k := range m {
					depends = append(depends, k)
				}
			}
		}
		for _, dep := range depends {
			if _, ok := cfg.Services[dep]; !ok {
				return nil, fmt.Errorf("service %q depends on unknown service %q", name, dep)
			}
		}
		deps[name] = depends
	}

	var ordered []string
	visited := make(map[string]bool)

	var visit func(name string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true
		for _, dep := range deps[name] {
			visit(dep)
		}
		ordered = append(ordered, name)
	}

	for name := range cfg.Services {
		visit(name)
	}
	return ordered, nil
}

// serviceLogPalette is the fixed color rotation for service name prefixes.
var serviceLogPalette = []lipgloss.Color{
	tui.Sky500,                // cyan-ish
	tui.Amber500,              // yellow
	tui.Emerald400,            // green
	lipgloss.Color("#c084fc"), // magenta
	lipgloss.Color("#60a5fa"), // blue
	tui.Red500,                // red
}

// serviceLogWriter buffers output for a single service and writes complete
// lines prefixed with a color-coded, column-aligned service name.
// It is safe to call Write from a single goroutine; Flush drains any partial line.
type serviceLogWriter struct {
	mu     *sync.Mutex // shared with all writers so lines don't interleave
	dest   *os.File
	buf    strings.Builder
	prefix string // pre-rendered "[name]  " with padding and color
}

func newServiceLogWriters(names []string) (stdout, stderr map[string]*serviceLogWriter) {
	mu := &sync.Mutex{}
	maxLen := 0
	for _, n := range names {
		if len(n) > maxLen {
			maxLen = len(n)
		}
	}
	stdout = make(map[string]*serviceLogWriter, len(names))
	stderr = make(map[string]*serviceLogWriter, len(names))
	for i, name := range names {
		color := serviceLogPalette[i%len(serviceLogPalette)]
		style := lipgloss.NewStyle().Foreground(color).Bold(true)
		errStyle := lipgloss.NewStyle().Foreground(color).Bold(true)
		padding := strings.Repeat(" ", maxLen-len(name)+1)
		prefix := style.Render("["+name+"]") + padding
		stdout[name] = &serviceLogWriter{mu: mu, dest: os.Stdout, prefix: prefix}
		stderr[name] = &serviceLogWriter{mu: mu, dest: os.Stderr, prefix: errStyle.Render("["+name+"]") + padding}
	}
	return stdout, stderr
}

func (w *serviceLogWriter) Write(p []byte) {
	for _, b := range p {
		if b == '\n' {
			w.mu.Lock()
			fmt.Fprintln(w.dest, w.prefix+w.buf.String())
			w.buf.Reset()
			w.mu.Unlock()
		} else {
			w.buf.WriteByte(b)
		}
	}
}

func (w *serviceLogWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() > 0 {
		fmt.Fprintln(w.dest, w.prefix+w.buf.String())
		w.buf.Reset()
	}
}

// runComposeWithAgent orchestrates a docker-compose project on a WendyOS device:
// builds service images, pushes them to the device registry, creates containers,
// and streams their combined output. When a companion wendy.json exists in the
// same directory it is merged to supply Wendy-specific config (entitlements,
// isolation, frameworks) without modifying the compose file.
func runComposeWithAgent(ctx context.Context, conn *grpcclient.AgentConnection, projectDir string, opts runOptions) error {
	cfg, composeFilename, err := parseComposeFile(projectDir)
	if err != nil {
		return err
	}
	if len(cfg.Services) == 0 {
		return fmt.Errorf("%s defines no services", composeFilename)
	}

	// Load an optional companion wendy.json from the same directory.
	companion, companionWarnings, err := appconfig.LoadComposeCompanion(projectDir)
	if err != nil {
		return fmt.Errorf("companion wendy.json: %w", err)
	}
	for _, w := range companionWarnings {
		cliLogln("warning: %s", w)
	}
	for _, w := range composeCompanionWarnings(companion, cfg) {
		cliLogln("warning: %s", w)
	}

	versionResp, err := conn.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
	if err != nil {
		return fmt.Errorf("querying device version: %w", err)
	}
	agentOS := versionResp.GetOs()
	architecture := versionResp.GetCpuArchitecture()
	if architecture == "" {
		architecture = "arm64"
	}
	platform := resolveAgentPlatform("", agentOS, architecture)
	if strings.EqualFold(agentOS, appconfig.PlatformDarwin) {
		return rejectUnsupportedMacRunProject("compose", platform)
	}

	if err := requireRegistryAuth(ctx, conn); err != nil {
		return err
	}

	regPort := registryPort(agentOS)

	if err := ensureAppleContainerSystemForBuilder(ctx, opts.builder, opts.yes); err != nil {
		return err
	}

	// Use the project directory name as the project name.
	projectName := strings.ToLower(filepath.Base(projectDir))

	// Build and push each service that has a build directive.
	for name, svc := range cfg.Services {
		ctxDir, dockerfile, buildArgs, err := composeBuildContext(svc, projectDir)
		if err != nil {
			return fmt.Errorf("service %s: %w", name, err)
		}
		if ctxDir == "" {
			continue // uses pre-built image
		}

		allBuildArgs := map[string]string{
			"WENDY_PLATFORM": wendyPlatform(versionResp.GetDeviceType()),
			"WENDY_DEBUG":    fmt.Sprintf("%t", opts.debug),
		}
		// Mirror the single-container build path so compose-built Dockerfiles
		// see the same WendyOS device hints (e.g. for GPU base-image selection).
		applyDeviceBuildArgHints(allBuildArgs, versionResp)
		for k, v := range buildArgs {
			allBuildArgs[k] = v
		}

		repo := fmt.Sprintf("%s-%s", projectName, name)
		// Compose builds run sequentially, so they share the local cache dir
		// (empty cache key) — no concurrent cache-export race to isolate.
		composeBuildTitle := fmt.Sprintf("Building service %s for %s...", name, tui.Value(platform))
		if err := runBuildWithProgress(ctx, composeBuildTitle, dumpRawAlways, func(stream, logw io.Writer) error {
			return buildAndPushImageForAgent(ctx, conn, regPort, opts.builder, ctxDir, repo, platform, dockerfile, allBuildArgs, "", stream, logw)
		}); err != nil {
			return fmt.Errorf("building service %s: %w", name, err)
		}
	}

	cliRestartPolicy := resolveRestartPolicy(opts)

	// Create all containers in dependency order.
	ordered, err := serviceOrder(cfg)
	if err != nil {
		return err
	}
	for _, name := range ordered {
		svc := cfg.Services[name]
		appCfg := composeAppConfig(projectName, name, svc, len(cfg.Services))
		applyComposeCompanion(appCfg, companion, name)

		// Determine image: built image or declared image. Public image refs
		// like "python:3.11-slim" must be canonicalised to "docker.io/library/…"
		// because the agent's containerd reference parser only accepts
		// fully-qualified names.
		ctxDir, _, _, _ := composeBuildContext(svc, projectDir)
		var imageName string
		if ctxDir != "" {
			imageName = fmt.Sprintf("localhost:%d/%s-%s:latest", regPort, projectName, name)
		} else if svc.Image != "" {
			imageName = normalizeImageRef(svc.Image)
		} else {
			return fmt.Errorf("service %s: no image or build directive", name)
		}

		appConfigData, err := json.Marshal(appCfg)
		if err != nil {
			return fmt.Errorf("marshaling config for service %s: %w", name, err)
		}

		// Split the compose command into argv: the first token becomes Cmd
		// (the agent runs strings.Fields on it, so it must contain no
		// whitespace) and the rest are passed verbatim through UserArgs so
		// arguments like a multi-line `python3 -c <script>` survive intact.
		cmd, extraArgs := composeArgv(svc)

		// CLI flags take precedence over per-service compose restart policies;
		// when the CLI didn't specify one (DEFAULT), honour the service's restart.
		restartPolicy := cliRestartPolicy
		if restartPolicy.GetMode() == agentpb.RestartPolicyMode_DEFAULT && svc.Restart != "" {
			restartPolicy = composeRestartPolicy(svc.Restart)
		}

		if warns := unsupportedComposeWarnings(svc); len(warns) > 0 {
			cliLogln("warning: service %q uses unsupported Compose fields (ignored by Wendy): %s",
				name, strings.Join(warns, ", "))
		}

		createReq := &agentpb.CreateContainerRequest{
			ImageName:     imageName,
			AppName:       appCfg.ContainerName(),
			AppConfig:     appConfigData,
			Cmd:           cmd,
			RestartPolicy: restartPolicy,
			UserArgs:      extraArgs,
			Env:           composeEnv(svc),
		}

		cliLogln("Creating container for service %s (%s)...", name, appCfg.ContainerName())
		if err := createContainerWithProgress(ctx, conn.ContainerService, createReq); err != nil {
			return fmt.Errorf("creating container for service %s: %w", name, err)
		}
		cliLogln("Container %s created.", appCfg.ContainerName())
	}

	if opts.deploy {
		cliLogln("All %d service containers created (not started).", len(cfg.Services))
		return nil
	}

	// Start all containers and stream their output concurrently.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case <-sigCh:
		case <-runCtx.Done():
			return
		}
		// Stop in reverse dependency order.
		stopCtx := context.Background()
		stopped := 0
		fmt.Println()
		for i := len(ordered) - 1; i >= 0; i-- {
			name := ordered[i]
			svc := cfg.Services[name]
			appCfg := composeAppConfig(projectName, name, svc, len(cfg.Services))
			applyComposeCompanion(appCfg, companion, name)
			cliLogln("Stopping %s...", name)
			// Use AppID (not ContainerName) so StopContainer's label-based lookup
			// finds the container regardless of whether a companion sets ServiceName.
			_, _ = conn.ContainerService.StopContainer(stopCtx, &agentpb.StopContainerRequest{
				AppName: appCfg.AppID,
			})
			stopped++
		}
		cliLogln("Stopped %d service(s).", stopped)
		runCancel()
	}()

	if opts.detach {
		for _, name := range ordered {
			svc := cfg.Services[name]
			appCfg := composeAppConfig(projectName, name, svc, len(cfg.Services))
			applyComposeCompanion(appCfg, companion, name)
			stream, err := conn.ContainerService.StartContainer(ctx, &agentpb.StartContainerRequest{
				AppName: appCfg.ContainerName(),
			})
			if err != nil {
				return fmt.Errorf("starting service %s: %w", name, err)
			}
			if _, err := stream.Recv(); err != nil && err != io.EOF {
				return fmt.Errorf("waiting for service %s start: %w", name, err)
			}
			cliLogln("Service %s started.", name)
		}
		cliLogln("All services running in detached mode.")
		cliLogln("Run 'wendy device logs' to stream logs (filter a service with --app %s-<service>).", projectName)
		return nil
	}

	// Attached mode: stream output from all containers concurrently with
	// color-coded, column-aligned service name prefixes.
	serviceNames := make([]string, len(ordered))
	copy(serviceNames, ordered)
	stdoutWriters, stderrWriters := newServiceLogWriters(serviceNames)

	var wg sync.WaitGroup
	errCh := make(chan error, len(ordered))

	for _, name := range ordered {
		svc := cfg.Services[name]
		appCfg := composeAppConfig(projectName, name, svc, len(cfg.Services))
		applyComposeCompanion(appCfg, companion, name)

		wg.Add(1)
		go func(serviceName, containerID string) {
			defer wg.Done()
			outW := stdoutWriters[serviceName]
			errW := stderrWriters[serviceName]
			defer outW.Flush()
			defer errW.Flush()

			// openStart falls back to the server-streaming StartContainer RPC,
			// used when the agent is too old to support AttachContainer.
			openStart := func() (containerOutputStream, error) {
				startStream, startErr := conn.ContainerService.StartContainer(runCtx, &agentpb.StartContainerRequest{
					AppName: containerID,
				})
				if startErr != nil {
					return nil, fmt.Errorf("starting service %s: %w", serviceName, startErr)
				}
				return startStream, nil
			}

			var outStream containerOutputStream
			attached := false
			attachStream, streamErr := conn.ContainerService.AttachContainer(runCtx)
			if streamErr == nil {
				streamErr = attachStream.Send(&agentpb.AttachContainerRequest{
					RequestType: &agentpb.AttachContainerRequest_AppName{AppName: containerID},
				})
				// Compose never forwards stdin; half-close the send side so the
				// container sees stdin EOF instead of hanging on a read.
				_ = attachStream.CloseSend()
			}
			if streamErr != nil {
				s, err := openStart()
				if err != nil {
					errCh <- err
					return
				}
				outStream = s
			} else {
				outStream = attachStream
				attached = true
			}

			gotFirstResponse := false
			for {
				resp, recvErr := outStream.Recv()
				if recvErr == io.EOF {
					return
				}
				if recvErr != nil {
					if runCtx.Err() != nil {
						return
					}
					// Older agents reject AttachContainer with Unimplemented on
					// the first Recv rather than at open/send time; fall back
					// silently to StartContainer.
					if attached && !gotFirstResponse && status.Code(recvErr) == codes.Unimplemented {
						s, err := openStart()
						if err != nil {
							errCh <- err
							return
						}
						outStream = s
						attached = false
						continue
					}
					errCh <- fmt.Errorf("service %s: %w", serviceName, recvErr)
					return
				}
				gotFirstResponse = true
				if out := resp.GetStdoutOutput(); out != nil {
					outW.Write(out.GetData())
				}
				if out := resp.GetStderrOutput(); out != nil {
					errW.Write(out.GetData())
				}
			}
		}(name, appCfg.ContainerName())
	}

	cliLogln("All services started.")

	wg.Wait()

	select {
	case err := <-errCh:
		if runCtx.Err() == nil {
			return err
		}
	default:
	}

	if runCtx.Err() == nil {
		cliLogln("\nAll services stopped.")
	}
	return nil
}
