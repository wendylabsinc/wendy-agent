package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

// This file is the pure half of 'wendy fleet train': given the devices a group
// resolved to, it computes what each device must be told and renders that plan
// for a human. It performs no input/output beyond reading a configuration file
// the user named and writing to a caller-supplied io.Writer, and it never
// touches the network, so every rule below is testable without hardware.

// trainTransport selects how devices address each other. mesh routes over the
// cloud mesh overlay by asset id; lan hands out routable addresses discovered
// over multicast Domain Name System (mDNS), which is what a fleet uses while
// the mesh overlay is unavailable.
type trainTransport string

const (
	transportMesh trainTransport = "mesh"
	transportLAN  trainTransport = "lan"

	// defaultTrainMeshPort is the port every training template listens on when
	// the operator does not override MESH_PORT.
	defaultTrainMeshPort = 8080
)

// Roles a device can hold. coordinator and learner both coordinate a run (the
// Proximal Policy Optimization (PPO) template names its coordinator "learner"),
// so exactly one device may hold either of them.
const (
	trainRoleCoordinator = "coordinator"
	trainRoleWorker      = "worker"
	trainRoleLearner     = "learner"
	trainRoleActor       = "actor"
)

// trainBlockedEnvPrefixes mirrors the agent's reserved prefixes
// (internal/agent/containerd/client.go). Rejecting them here means the operator
// learns about the clash while typing the command instead of watching every
// device fail at container create.
var trainBlockedEnvPrefixes = []string{"WENDY_", "LD_", "DYLD_"}

// trainDevicePlan is one device's share of a run: where it sits in the fleet
// ordering and the exact environment its container will be created with.
type trainDevicePlan struct {
	Target fleetTarget
	Role   string
	Rank   int
	Env    map[string]string
}

// trainPlan is the whole computed deployment: everything 'fleet train' will do,
// in a form that can be printed, serialized, or executed.
//
// Source and LANRewrite are the two fields a caller may adjust after
// computeTrainPlan returns: Source is the resolved template location (embedded
// asset or directory path), which plan computation cannot know, and LANRewrite
// starts true for the lan transport but the staging step clears it if it turns
// out there was no network entitlement to rewrite.
type trainPlan struct {
	Template   string
	Source     string
	AppID      string
	Group      string
	Transport  trainTransport
	MeshPort   int
	LANRewrite bool
	Devices    []trainDevicePlan
	Warnings   []string
}

// trainPlanInput is everything the command layer knows before targets are
// ranked. BaseEnv is the operator's environment (flags merged over the
// configuration file, including the fleet token); it is copied per device and
// never mutated.
type trainPlanInput struct {
	Template    string
	AppID       string
	Group       string
	Transport   trainTransport
	BaseEnv     map[string]string
	RolePins    map[string]string
	IsSweep     bool
	SweepParams []map[string]any
}

// trainConfigFile is the JavaScript Object Notation (JSON) file behind
// --config. It mirrors the flags one for one; the command layer lets an
// explicit flag override the file key by key.
type trainConfigFile struct {
	Group     string            `json:"group"`
	LAN       *bool             `json:"lan"`
	Template  string            `json:"template"`
	Transport string            `json:"transport"`
	Env       map[string]string `json:"env"`
	Sweep     []map[string]any  `json:"sweep"`
	Roles     map[string]string `json:"roles"`
}

// isTrainRole reports whether role is one the templates understand.
func isTrainRole(role string) bool {
	switch role {
	case trainRoleCoordinator, trainRoleWorker, trainRoleLearner, trainRoleActor:
		return true
	}
	return false
}

// isTrainCoordinatorRole reports whether role coordinates a run. Both names
// count: the earlier Python launcher looked the coordinator up by the literal
// name "coordinator" and crashed when the only pinned coordinating device was
// a learner.
func isTrainCoordinatorRole(role string) bool {
	return role == trainRoleCoordinator || role == trainRoleLearner
}

// trainRoleNames lists the accepted roles for error messages.
func trainRoleNames() string {
	return strings.Join([]string{trainRoleActor, trainRoleCoordinator, trainRoleLearner, trainRoleWorker}, ", ")
}

// computeTrainPlan ranks the targets, assigns roles, and builds each device's
// environment. It is deterministic and side effect free: the same targets and
// input always produce the same plan.
func computeTrainPlan(targets []fleetTarget, in trainPlanInput) (*trainPlan, error) {
	transport := in.Transport
	if transport == "" {
		transport = transportMesh
	}
	if transport != transportMesh && transport != transportLAN {
		return nil, fmt.Errorf("unknown transport %q: expected %q or %q", string(transport), string(transportMesh), string(transportLAN))
	}
	if len(targets) == 0 {
		return nil, errors.New("no devices to train on: the group resolved to zero devices")
	}

	plan := &trainPlan{
		Template:   in.Template,
		AppID:      in.AppID,
		Group:      in.Group,
		Transport:  transport,
		MeshPort:   defaultTrainMeshPort,
		LANRewrite: transport == transportLAN,
	}

	if raw, ok := in.BaseEnv["MESH_PORT"]; ok && strings.TrimSpace(raw) != "" {
		port, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || port < 1 || port > 65535 {
			// Silently falling back would hide a typo that sends every device
			// to the wrong port, so say so and keep going with the default.
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("MESH_PORT %q is not a port number; using %d", raw, defaultTrainMeshPort))
		} else {
			plan.MeshPort = port
		}
	}

	ordered, rankWarning, err := rankTrainTargets(targets, transport)
	if err != nil {
		return nil, err
	}
	if rankWarning != "" {
		plan.Warnings = append(plan.Warnings, rankWarning)
	}

	if transport == transportLAN {
		var unaddressable []string
		for _, t := range ordered {
			if strings.TrimSpace(t.Address) == "" {
				unaddressable = append(unaddressable, t.Name)
			}
		}
		if len(unaddressable) > 0 {
			return nil, fmt.Errorf("transport lan needs a routable address for every device, but %s %s none; lan peers are addresses, so discover the fleet with --lan or use --transport mesh",
				strings.Join(unaddressable, ", "), plural(len(unaddressable), "has", "have"))
		}
	}

	roles, err := assignTrainRoles(ordered, in.RolePins)
	if err != nil {
		return nil, err
	}
	coordinator, err := trainCoordinator(ordered, roles)
	if err != nil {
		return nil, err
	}

	sweepJSON, sweepWarning, err := trainSweepEnvValue(in, len(ordered))
	if err != nil {
		return nil, err
	}
	if sweepWarning != "" {
		plan.Warnings = append(plan.Warnings, sweepWarning)
	}

	peers := trainPeerValues(ordered, transport, plan.MeshPort)
	coordinatorAddr := trainCoordinatorAddress(coordinator, transport, plan.MeshPort)

	plan.Devices = make([]trainDevicePlan, 0, len(ordered))
	for rank, target := range ordered {
		// A fresh map per device: devices differ only in a handful of keys, and
		// sharing one map would give every device the last device's identity.
		env := make(map[string]string, len(in.BaseEnv)+8)
		for k, v := range in.BaseEnv {
			env[k] = v
		}
		if target.AssetID > 0 {
			env["MESH_SELF"] = strconv.FormatInt(int64(target.AssetID), 10)
		} else {
			// No asset id means no mesh identity. Drop any inherited value
			// rather than shipping another device's id.
			delete(env, "MESH_SELF")
		}
		env["MESH_PEERS"] = peers[rank]
		env["WT_ROLE"] = roles[rank]
		env["WT_COORDINATOR"] = coordinatorAddr
		env["WT_NODE_INDEX"] = strconv.Itoa(rank)
		env["WT_NODE_COUNT"] = strconv.Itoa(len(ordered))
		if sweepJSON != "" {
			env["WT_SWEEP_INDEX"] = strconv.Itoa(rank)
			env["WT_SWEEP_PARAMS"] = sweepJSON
		}
		plan.Devices = append(plan.Devices, trainDevicePlan{
			Target: target,
			Role:   roles[rank],
			Rank:   rank,
			Env:    env,
		})
	}
	return plan, nil
}

// plural picks the singular or plural form for a count, so error messages read
// as sentences rather than as templates.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// rankTrainTargets orders the fleet. Asset ids are the primary ordering because
// they are stable across reboots, renames, and address changes, so a restarted
// run keeps the same coordinator. Devices without one (a LAN device that is not
// enrolled, or whose agent predates the assetid record) cannot be addressed
// over the mesh at all, so mesh refuses; lan falls back to names and says so.
func rankTrainTargets(targets []fleetTarget, transport trainTransport) ([]fleetTarget, string, error) {
	var missing []string
	for _, t := range targets {
		if t.AssetID <= 0 {
			missing = append(missing, t.Name)
		}
	}
	ordered := make([]fleetTarget, len(targets))
	copy(ordered, targets)

	if len(missing) == 0 {
		sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].AssetID < ordered[j].AssetID })
		return ordered, "", nil
	}
	sort.Strings(missing)
	if transport == transportMesh {
		return nil, "", fmt.Errorf("mesh transport addresses peers by asset id, but %s %s none; enroll %s, or use --transport lan with --lan discovery",
			strings.Join(missing, ", "), plural(len(missing), "has", "have"),
			plural(len(missing), "it", "them"))
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	return ordered, fmt.Sprintf("ranking by device name because %s %s no asset id; enroll every device to rank by asset id, which stays stable across renames",
		strings.Join(missing, ", "), plural(len(missing), "has", "have")), nil
}

// assignTrainRoles returns the role of each ranked device. Pins win; otherwise
// rank 0 coordinates and the rest are workers, unless a pin already claimed a
// coordinating role, in which case rank 0 is a worker like everyone else.
func assignTrainRoles(ordered []fleetTarget, pins map[string]string) ([]string, error) {
	normalized := make(map[string]string, len(pins))
	for name, role := range pins {
		role = strings.ToLower(strings.TrimSpace(role))
		if !isTrainRole(role) {
			return nil, fmt.Errorf("--role %s=%s: unknown role; expected one of %s", name, role, trainRoleNames())
		}
		normalized[strings.ToLower(strings.TrimSpace(name))] = role
	}

	known := make(map[string]bool, len(ordered))
	names := make([]string, 0, len(ordered))
	for _, t := range ordered {
		known[strings.ToLower(t.Name)] = true
		names = append(names, t.Name)
	}
	var unknown []string
	for name := range normalized {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		// A pin that matches nothing silently changes who coordinates, so it is
		// an error rather than a warning.
		sort.Strings(unknown)
		return nil, fmt.Errorf("--role names %s, which %s not in this group; the group resolved to %s",
			strings.Join(unknown, ", "), plural(len(unknown), "is", "are"), strings.Join(names, ", "))
	}

	pinnedCoordinator := false
	for _, role := range normalized {
		if isTrainCoordinatorRole(role) {
			pinnedCoordinator = true
			break
		}
	}

	roles := make([]string, len(ordered))
	for i, t := range ordered {
		if role, ok := normalized[strings.ToLower(t.Name)]; ok {
			roles[i] = role
			continue
		}
		if i == 0 && !pinnedCoordinator {
			roles[i] = trainRoleCoordinator
			continue
		}
		roles[i] = trainRoleWorker
	}
	return roles, nil
}

// trainCoordinator returns the one device that coordinates the run, or an error
// naming what it found. Both coordinator and learner count as coordinating.
func trainCoordinator(ordered []fleetTarget, roles []string) (fleetTarget, error) {
	var found []fleetTarget
	var described []string
	for i, role := range roles {
		if isTrainCoordinatorRole(role) {
			found = append(found, ordered[i])
			described = append(described, fmt.Sprintf("%s=%s", ordered[i].Name, role))
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	list := strings.Join(described, ", ")
	if list == "" {
		list = "none"
	}
	return fleetTarget{}, fmt.Errorf("the plan needs exactly one coordinator, got %d (%s); pin --role <device>=<role> until exactly one device is the coordinator (coordinator and learner both coordinate)",
		len(found), list)
}

// trainPeerValues returns each ranked device's MESH_PEERS value. Mesh peer
// lists include the device itself because the templates skip themselves by
// matching MESH_SELF against the list; address lists cannot be self-matched
// that way, so lan lists exclude the device.
func trainPeerValues(ordered []fleetTarget, transport trainTransport, port int) []string {
	out := make([]string, len(ordered))
	if transport == transportMesh {
		ids := make([]string, 0, len(ordered))
		for _, t := range ordered {
			ids = append(ids, strconv.FormatInt(int64(t.AssetID), 10))
		}
		joined := strings.Join(ids, ",")
		for i := range out {
			out[i] = joined
		}
		return out
	}
	for i := range ordered {
		peers := make([]string, 0, len(ordered)-1)
		for j, peer := range ordered {
			if i == j {
				continue
			}
			peers = append(peers, fmt.Sprintf("%s:%d", peer.Address, port))
		}
		out[i] = strings.Join(peers, ",")
	}
	return out
}

// trainCoordinatorAddress renders the address every device dials to reach the
// coordinator. The mesh form matches the overlay name the agent publishes; see
// computePeers in fleet_manifest.go, which builds the same host.
func trainCoordinatorAddress(coordinator fleetTarget, transport trainTransport, port int) string {
	if transport == transportLAN {
		return fmt.Sprintf("%s:%d", coordinator.Address, port)
	}
	return fmt.Sprintf("device-%d.cloud.wendy.dev:%d", coordinator.AssetID, port)
}

// trainSweepEnvValue validates the sweep parameters against the device count
// and returns the compact JSON every device receives. The whole array goes to
// every device, and WT_SWEEP_INDEX tells each which entry is its own, so a
// device can report the full sweep it belongs to.
func trainSweepEnvValue(in trainPlanInput, deviceCount int) (string, string, error) {
	if !in.IsSweep {
		if in.SweepParams != nil {
			return "", "sweep parameters are ignored: only the sweep template reads them", nil
		}
		return "", "", nil
	}
	if in.SweepParams == nil {
		return "", "", errors.New("the sweep template needs sweep parameters: pass --sweep <file.json> or inline JSON with one object per device")
	}
	if len(in.SweepParams) != deviceCount {
		return "", "", fmt.Errorf("--sweep has %d entries for %d device(s); give each device exactly one", len(in.SweepParams), deviceCount)
	}
	encoded, err := json.Marshal(in.SweepParams)
	if err != nil {
		return "", "", fmt.Errorf("encoding sweep parameters: %w", err)
	}
	return string(encoded), "", nil
}

// maskSecretValue describes a secret without disclosing it. The length and
// alphabet are enough for an operator to recognize their own token while the
// rendered plan stays safe to paste into an issue.
func maskSecretValue(v string) string {
	if v != "" && isHexString(v) {
		return fmt.Sprintf("<masked, %d hex chars>", len(v))
	}
	return fmt.Sprintf("<masked, %d chars>", len(v))
}

// isHexString reports whether v is a non-empty run of hexadecimal digits.
func isHexString(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// validateTrainEnv parses --env entries into a map, rejecting anything the
// agent would reject later. Later duplicates win, matching how a shell reads
// repeated assignments.
func validateTrainEnv(entries []string) (map[string]string, error) {
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("--env %q must be KEY=VALUE", entry)
		}
		if err := appconfig.ValidateEnvKey("--env", key); err != nil {
			return nil, err
		}
		for _, prefix := range trainBlockedEnvPrefixes {
			if strings.HasPrefix(key, prefix) {
				return nil, fmt.Errorf("--env %s: the agent rejects keys prefixed %q at container create, so the deploy would fail on every device; pick another name (the training contract uses the WT_ prefix)", key, prefix)
			}
		}
		out[key] = value
	}
	return out, nil
}

// parseSweepParams reads sweep parameters from a file path or from inline JSON.
// Inline is recognized by a leading '[' or '{', neither of which can start a
// usable path; taking '{' as inline too means a JSON object is answered with
// "expected an array" rather than with a confusing missing-file error.
func parseSweepParams(arg string) ([]map[string]any, error) {
	trimmed := strings.TrimSpace(arg)
	if trimmed == "" {
		return nil, errors.New("--sweep needs a file path or an inline JSON array")
	}
	raw := []byte(trimmed)
	source := "inline JSON"
	if !strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "{") {
		data, err := os.ReadFile(arg)
		if err != nil {
			return nil, fmt.Errorf("--sweep %s: %w", arg, err)
		}
		raw = data
		source = arg
	}
	return decodeSweepParams(raw, source)
}

// decodeSweepParams turns sweep JSON into per-device objects. Numbers are kept
// as json.Number so a seed of 1 reaches the device as 1 rather than as 1e+00.
func decodeSweepParams(raw []byte, source string) ([]map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var items []any
	if err := dec.Decode(&items); err != nil {
		return nil, fmt.Errorf("--sweep %s: expected a JSON array of objects, one per device: %w", source, err)
	}
	params := make([]map[string]any, 0, len(items))
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("--sweep %s: entry %d is %T, but every entry must be a JSON object of parameters", source, i, item)
		}
		params = append(params, obj)
	}
	return params, nil
}

// parseRolePins turns repeated --role device=role flags into a map keyed by
// device name.
func parseRolePins(entries []string) (map[string]string, error) {
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		device, role, ok := strings.Cut(entry, "=")
		device = strings.TrimSpace(device)
		role = strings.ToLower(strings.TrimSpace(role))
		if !ok || device == "" || role == "" {
			return nil, fmt.Errorf("--role %q must be device=role, for example spark-48fd=coordinator", entry)
		}
		if !isTrainRole(role) {
			return nil, fmt.Errorf("--role %s=%s: unknown role; expected one of %s", device, role, trainRoleNames())
		}
		out[device] = role
	}
	return out, nil
}

// loadTrainConfigFile reads the --config file. Unknown fields are rejected so a
// misspelled key fails loudly instead of being ignored while the operator
// believes it took effect.
func loadTrainConfigFile(path string) (*trainConfigFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("--config %s: %w", path, err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	dec.UseNumber()
	var cfg trainConfigFile
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("--config %s: %w", path, err)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("--config %s: expected a single JSON object", path)
	}
	if cfg.Transport != "" && cfg.Transport != string(transportMesh) && cfg.Transport != string(transportLAN) {
		return nil, fmt.Errorf("--config %s: unknown transport %q; expected %q or %q", path, cfg.Transport, string(transportMesh), string(transportLAN))
	}
	for device, role := range cfg.Roles {
		if !isTrainRole(strings.ToLower(strings.TrimSpace(role))) {
			return nil, fmt.Errorf("--config %s: role %q for %s is unknown; expected one of %s", path, role, device, trainRoleNames())
		}
	}
	return &cfg, nil
}

// renderTrainPlan writes the plan a human reads before trusting it: the header,
// every warning, and each device's full environment with secrets masked. An
// empty stagedDir means nothing was staged (a plain --dry-run). tokenEphemeral
// says the fleet token exists only for this render.
func renderTrainPlan(w io.Writer, plan *trainPlan, stagedDir string, tokenEphemeral bool) {
	if plan == nil {
		return
	}
	template := plan.Template
	if plan.Source != "" {
		template = fmt.Sprintf("%s (%s)", plan.Template, plan.Source)
	}
	fmt.Fprintf(w, "%-10s %s\n", "template:", template)
	fmt.Fprintf(w, "%-10s %s\n", "app id:", plan.AppID)
	fmt.Fprintf(w, "%-10s %s\n", "group:", plan.Group)
	fmt.Fprintf(w, "%-10s %s\n", "transport:", string(plan.Transport))
	if stagedDir == "" {
		fmt.Fprintf(w, "%-10s %s\n", "staging:", "skipped (--dry-run; pass --stage-dir to stage)")
	} else {
		fmt.Fprintf(w, "%-10s %s\n", "staged:", stagedDir)
	}
	if plan.LANRewrite {
		// The rewrite happens in memory on the loaded configuration, so the
		// staged wendy.json still checksums against the source tree.
		fmt.Fprintf(w, "network entitlement rewritten to: %s\n", `{"type": "network", "mode": "host"}`)
	}
	for _, warning := range plan.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warning)
	}
	for _, device := range plan.Devices {
		asset := "unknown"
		if device.Target.AssetID > 0 {
			asset = strconv.FormatInt(int64(device.Target.AssetID), 10)
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "device %s (asset %s, role %s, rank %d)\n", device.Target.Name, asset, device.Role, device.Rank)
		keys := make([]string, 0, len(device.Env))
		for key := range device.Env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := device.Env[key]
			if key == trainFleetTokenEnvKey {
				value = maskSecretValue(value)
			}
			fmt.Fprintf(w, "  env %s=%s\n", key, value)
		}
	}
	if tokenEphemeral {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "note: the %s above was generated for this render and was not saved; a real deploy generates and persists one\n", trainFleetTokenEnvKey)
	}
}
