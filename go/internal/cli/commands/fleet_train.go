package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// resolveFleetTargetsFn is the group resolver, a package variable so training
// tests can supply a fixed device set instead of browsing the network.
var resolveFleetTargetsFn = resolveFleetTargets

// trainStatusConcurrency bounds the parallel status polls. Status is a read of
// several devices at once; the cap keeps a large fleet from opening a
// connection per device simultaneously.
const trainStatusConcurrency = 8

func newFleetTrainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "train --group <group> --template <name|path>",
		Short: "Train a model across the devices in a group",
		Long: "Deploys a training template to every device in a group, giving each device\n" +
			"the identity its role needs: which node it is, who coordinates, how to reach\n" +
			"the others, and a shared token so the training endpoints are not open on the\n" +
			"network.\n\n" +
			"Templates ship with the Command Line Interface (" + strings.Join(embeddedTemplateNames(), ", ") + "),\n" +
			"or pass a path to any directory holding a wendy.json. The build context is\n" +
			"staged into a temporary directory with the training library alongside it,\n" +
			"because a wendy.json build context cannot reach outside its own directory.\n\n" +
			"Start with --dry-run: it prints every device's role and environment, and the\n" +
			"entitlement rewrite the local-network transport performs, without deploying\n" +
			"anything or writing any state.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("specify what to do: up, status, or stop (see --help)")
		},
	}
	cmd.AddCommand(newFleetTrainUpCmd(), newFleetTrainStatusCmd(), newFleetTrainStopCmd())
	return cmd
}

// trainCommonOptions are the flags every training subcommand shares: which
// devices, and how to reach them.
type trainCommonOptions struct {
	group      string
	template   string
	configPath string
	lan        bool
	cloudGRPC  string
	brokerURL  string
	timeout    time.Duration
}

func (o *trainCommonOptions) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.group, "group", "", "Device group to act on (cloud tag, or a name pattern with --lan)")
	cmd.Flags().StringVar(&o.template, "template", "", "Template name ("+strings.Join(embeddedTemplateNames(), ", ")+") or a path to a project directory")
	cmd.Flags().StringVar(&o.configPath, "config", "", "JSON file supplying these settings; explicit flags override it")
	cmd.Flags().BoolVar(&o.lan, "lan", false, "Resolve the group over the local network instead of the cloud")
	cmd.Flags().StringVar(&o.cloudGRPC, "cloud-grpc", "", "Cloud gRPC endpoint (optional when a default session is set via 'wendy auth use')")
	cmd.Flags().StringVar(&o.brokerURL, "broker-url", os.Getenv("WENDY_BROKER_URL"), "Tunnel broker host:port")
	cmd.Flags().DurationVar(&o.timeout, "discover-timeout", fleetLANDiscoverTimeout, "How long to browse for devices (with --lan)")
}

type trainUpOptions struct {
	trainCommonOptions
	transport string
	env       []string
	sweep     string
	roles     []string
	token     string
	dryRun    bool
	stageDir  string
	run       runOptions
}

func newFleetTrainUpCmd() *cobra.Command {
	var o trainUpOptions

	cmd := &cobra.Command{
		Use:   "up",
		Short: "Deploy a training template to every device in a group",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// A fleet deploy touches many devices: never prompt, never stream
			// logs from one of them.
			o.run.yes = true
			o.run.detach = true
			if err := mergeTrainConfig(cmd, &o); err != nil {
				return err
			}
			return runFleetTrainUp(cmd.Context(), &o)
		},
	}
	o.bind(cmd)
	cmd.Flags().StringVar(&o.transport, "transport", "", "How devices address each other: mesh (default) or lan")
	cmd.Flags().StringArrayVar(&o.env, "env", nil, "Extra KEY=VALUE for every device (repeatable)")
	cmd.Flags().StringVar(&o.sweep, "sweep", "", "Sweep parameters: a JSON file, or an inline JSON array of objects")
	cmd.Flags().StringArrayVar(&o.roles, "role", nil, "Pin a device's role as device=role (repeatable)")
	cmd.Flags().StringVar(&o.token, "token", "", "Shared fleet token; one is generated and saved when omitted")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "Print the plan and change nothing")
	cmd.Flags().StringVar(&o.stageDir, "stage-dir", "", "Stage the build context here instead of a temporary directory")
	cmd.Flags().BoolVar(&o.run.keepGoing, "keep-going", false, "Deploy to the remaining devices even if one fails")
	cmd.Flags().StringVar(&o.run.builder, "builder", "", "Image builder to force: docker or apple-container")
	cmd.Flags().StringVar(&o.run.chunking, "chunking", chunkingAuto, "Deploy path: auto, force, or off")
	return cmd
}

// mergeTrainConfig folds a configuration file under the flags: a flag the user
// actually typed wins, and the file supplies everything else. Martien's rule
// for this command is that both surfaces exist and flags override the file.
func mergeTrainConfig(cmd *cobra.Command, o *trainUpOptions) error {
	if o.configPath == "" {
		return nil
	}
	file, err := loadTrainConfigFile(o.configPath)
	if err != nil {
		return err
	}
	changed := func(name string) bool { return cmd.Flags().Changed(name) }
	if !changed("group") && file.Group != "" {
		o.group = file.Group
	}
	if !changed("template") && file.Template != "" {
		o.template = file.Template
	}
	if !changed("transport") && file.Transport != "" {
		o.transport = file.Transport
	}
	if !changed("lan") && file.LAN != nil {
		o.lan = *file.LAN
	}
	// File environment sits underneath the flags: prepend it so a repeated
	// --env of the same key still wins (later entries win in validateTrainEnv).
	if len(file.Env) > 0 {
		keys := make([]string, 0, len(file.Env))
		for k := range file.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fromFile := make([]string, 0, len(keys))
		for _, k := range keys {
			fromFile = append(fromFile, k+"="+file.Env[k])
		}
		o.env = append(fromFile, o.env...)
	}
	if len(file.Roles) > 0 {
		keys := make([]string, 0, len(file.Roles))
		for k := range file.Roles {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fromFile := make([]string, 0, len(keys))
		for _, k := range keys {
			fromFile = append(fromFile, k+"="+file.Roles[k])
		}
		o.roles = append(fromFile, o.roles...)
	}
	if !changed("sweep") && len(file.Sweep) > 0 {
		// Re-encode so the downstream parser has one input shape to handle.
		encoded, err := encodeSweepParams(file.Sweep)
		if err != nil {
			return err
		}
		o.sweep = encoded
	}
	return nil
}

func runFleetTrainUp(ctx context.Context, o *trainUpOptions) error {
	if err := validateTrainUpOptions(o); err != nil {
		return err
	}

	source, appCfg, err := resolveTemplateSource(o.template)
	if err != nil {
		return err
	}
	transport := trainTransport(o.transport)

	baseEnv, err := validateTrainEnv(o.env)
	if err != nil {
		return err
	}
	rolePins, err := parseRolePins(o.roles)
	if err != nil {
		return err
	}
	var sweepParams []map[string]any
	if o.sweep != "" {
		if sweepParams, err = parseSweepParams(o.sweep); err != nil {
			return err
		}
	}

	targets, err := resolveFleetTargetsFn(ctx, o.group, o.lan, o.cloudGRPC, o.brokerURL, o.timeout)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("group %q has no devices; on the local network check the name pattern, or add cloud devices with 'wendy fleet group add %s <device>...'", o.group, o.group)
	}
	addressWarnings, err := resolveTrainPeerHosts(ctx, targets, transport)
	if err != nil {
		return err
	}

	token, ephemeral, err := ensureFleetToken(o.token, baseEnv, o.group, appCfg.AppID, !o.dryRun)
	if err != nil {
		return err
	}
	baseEnv[trainFleetTokenEnvKey] = token

	plan, err := computeTrainPlan(targets, trainPlanInput{
		Template:    o.template,
		AppID:       appCfg.AppID,
		Group:       o.group,
		Transport:   transport,
		BaseEnv:     baseEnv,
		RolePins:    rolePins,
		IsSweep:     isSweepTemplate(source),
		SweepParams: sweepParams,
	})
	if err != nil {
		return err
	}
	plan.Source = templateSourceLabel(source)
	plan.Warnings = append(plan.Warnings, addressWarnings...)

	// The local-network transport needs host networking, since peers are
	// addressed by address rather than through the mesh. Rewriting the parsed
	// configuration leaves the staged files byte-identical to their source.
	if transport == transportLAN {
		if err := applyLANHostNetworking(appCfg); err != nil {
			return err
		}
	} else {
		plan.LANRewrite = false
	}

	// A dry run must be able to answer "what would happen" without staging or
	// writing anything, so staging only happens when it will be deployed or
	// when the operator asked for a directory to inspect.
	stagedDir := ""
	if !o.dryRun || o.stageDir != "" {
		if stagedDir, err = stageTrainingContext(source, o.stageDir); err != nil {
			return err
		}
	}

	renderTrainPlan(os.Stdout, plan, stagedDir, ephemeral)

	if o.dryRun {
		fmt.Println()
		fmt.Println("nothing was deployed; re-run without --dry-run to deploy")
		return nil
	}

	if err := saveTrainState(trainState{
		Token:     token,
		AppID:     appCfg.AppID,
		Template:  o.template,
		Transport: string(transport),
		MeshPort:  plan.MeshPort,
		Group:     o.group,
	}); err != nil {
		return err
	}

	if err := prepareTrainBuild(stagedDir, &o.run); err != nil {
		return err
	}

	results, failures := deployTrainPlan(ctx, plan, stagedDir, appCfg, o.run)
	if jsonOutput {
		if err := printJSON(results); err != nil {
			return err
		}
	} else {
		fmt.Println()
		fmt.Printf("deployed %s to %d/%d device(s) in group %q\n",
			appCfg.AppID, len(plan.Devices)-failures, len(plan.Devices), o.group)
		if coordinator := planCoordinator(plan); coordinator != nil && failures == 0 {
			fmt.Printf("follow the coordinator: wendy --device %s device logs %s\n",
				coordinator.Target.Name, appCfg.AppID)
		}
	}
	if failures > 0 {
		return fmt.Errorf("%d of %d device(s) failed", failures, len(plan.Devices))
	}
	return nil
}

// validateTrainUpOptions rejects flag combinations before anything is built or
// any device is contacted.
func validateTrainUpOptions(o *trainUpOptions) error {
	if o.template == "" {
		return fmt.Errorf("--template is required (one of %s, or a path to a project directory)", strings.Join(embeddedTemplateNames(), ", "))
	}
	if o.transport == "" {
		o.transport = string(transportMesh)
	}
	switch trainTransport(o.transport) {
	case transportMesh, transportLAN:
	default:
		return fmt.Errorf("unknown transport %q; expected mesh or lan", o.transport)
	}
	// Local-network addressing needs the dial addresses that only local
	// discovery produces; cloud targets carry none.
	if trainTransport(o.transport) == transportLAN && !o.lan {
		return fmt.Errorf("--transport lan needs --lan: peers are addressed by their discovered address, which only local-network resolution provides")
	}
	if err := normalizeBuilderAndChunking(&o.run); err != nil {
		return err
	}
	return nil
}

func normalizeBuilderAndChunking(opts *runOptions) error {
	if _, err := normalizeImageBuilder(opts.builder); err != nil {
		return err
	}
	return validateChunkingMode(opts.chunking)
}

// prepareTrainBuild resolves the build inputs against the staged context the
// same way a single-device deploy would, so a template's Dockerfile is found
// before any device is contacted.
func prepareTrainBuild(stagedDir string, opts *runOptions) error {
	projectType, err := resolveRunProjectType(stagedDir, opts.buildType)
	if err != nil {
		return err
	}
	if projectType == "compose" {
		return fmt.Errorf("compose projects are not supported by 'wendy fleet train'")
	}
	if projectType == "docker" && opts.dockerfile == "" {
		resolved, err := resolveDockerfile(stagedDir, opts.dockerfile, false)
		if err != nil {
			return err
		}
		opts.dockerfile = resolved
	}
	return nil
}

// deployTrainPlan deploys each device in plan order with that device's own
// environment. Each device gets a freshly built slice: appending onto a shared
// one would let two devices end up sharing a backing array and, with it, each
// other's identity.
func deployTrainPlan(ctx context.Context, plan *trainPlan, stagedDir string, appCfg *appconfig.AppConfig, base runOptions) (results []fleetRunResult, failures int) {
	results = make([]fleetRunResult, 0, len(plan.Devices))
	for _, device := range plan.Devices {
		res := fleetRunResult{Device: device.Target.Name}
		fmt.Printf("\n── %s (role %s, rank %d) ──\n", device.Target.Name, device.Role, device.Rank)

		opts := base
		opts.env = sortedEnvEntries(device.Env)

		if err := deployToTargetFn(ctx, device.Target, stagedDir, appCfg, opts); err != nil {
			res.Error = err.Error()
			failures++
			results = append(results, res)
			fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", device.Target.Name, err)
			if !base.keepGoing {
				return results, failures
			}
			continue
		}
		res.OK = true
		results = append(results, res)
	}
	return results, failures
}

func planCoordinator(plan *trainPlan) *trainDevicePlan {
	for i := range plan.Devices {
		if isTrainCoordinatorRole(plan.Devices[i].Role) {
			return &plan.Devices[i]
		}
	}
	return nil
}

// --- status ------------------------------------------------------------------

type trainStatusOptions struct {
	trainCommonOptions
	token string
}

func newFleetTrainStatusCmd() *cobra.Command {
	var o trainStatusOptions
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report each device's training status",
		Long: "Polls every device's training endpoint with the fleet's bearer token and\n" +
			"prints what each one reports. The token comes from the state a deploy saved,\n" +
			"unless --token overrides it.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runFleetTrainStatus(cmd.Context(), &o, http.DefaultClient)
		},
	}
	o.bind(cmd)
	cmd.Flags().StringVar(&o.token, "token", "", "Fleet bearer token; defaults to the one saved by the deploy")
	return cmd
}

type trainStatusRow struct {
	Device  string `json:"device"`
	AssetID int32  `json:"assetId,omitempty"`
	Status  string `json:"status"`
}

func runFleetTrainStatus(ctx context.Context, o *trainStatusOptions, client *http.Client) error {
	appID, state, err := resolveTrainTarget(o.trainCommonOptions)
	if err != nil {
		return err
	}
	token := o.token
	port := defaultTrainMeshPort
	if state != nil {
		if token == "" {
			token = state.Token
		}
		if state.MeshPort > 0 {
			port = state.MeshPort
		}
	}

	targets, err := resolveFleetTargetsFn(ctx, o.group, o.lan, o.cloudGRPC, o.brokerURL, o.timeout)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("group %q has no devices", o.group)
	}

	rows := make([]trainStatusRow, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, trainStatusConcurrency)
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target fleetTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows[i] = trainStatusRow{
				Device:  target.Name,
				AssetID: target.AssetID,
				Status:  pollTrainStatus(ctx, client, target, port, token),
			}
		}(i, target)
	}
	wg.Wait()

	if jsonOutput || !isInteractiveTerminal() {
		return printJSON(struct {
			AppID string           `json:"appId"`
			Rows  []trainStatusRow `json:"devices"`
		}{AppID: appID, Rows: rows})
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "DEVICE\tASSET\tSTATUS")
	for _, row := range rows {
		asset := "—"
		if row.AssetID > 0 {
			asset = fmt.Sprintf("%d", row.AssetID)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", row.Device, asset, row.Status)
	}
	return tw.Flush()
}

// --- stop --------------------------------------------------------------------

func newFleetTrainStopCmd() *cobra.Command {
	var o trainCommonOptions
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop this training run's containers across the group",
		Long: "Stops only the containers belonging to the training application, matched\n" +
			"exactly by application id, and leaves everything else on the device running.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runFleetTrainStop(cmd.Context(), &o)
		},
	}
	o.bind(cmd)
	return cmd
}

type trainStopRow struct {
	Device  string   `json:"device"`
	Stopped []string `json:"stopped"`
	Error   string   `json:"error,omitempty"`
}

// stopTrainAppsOnTargetFn is the per-device stop, a package variable so tests
// can record what would be stopped without an agent.
var stopTrainAppsOnTargetFn = stopTrainAppsOnTarget

func runFleetTrainStop(ctx context.Context, o *trainCommonOptions) error {
	appID, _, err := resolveTrainTarget(*o)
	if err != nil {
		return err
	}
	targets, err := resolveFleetTargetsFn(ctx, o.group, o.lan, o.cloudGRPC, o.brokerURL, o.timeout)
	if err != nil {
		return err
	}

	rows := make([]trainStopRow, 0, len(targets))
	for _, target := range targets {
		row := trainStopRow{Device: target.Name}
		stopped, err := stopTrainAppsOnTargetFn(ctx, target, appID)
		if err != nil {
			// One unreachable device must not hide the others: record and continue.
			row.Error = err.Error()
		}
		row.Stopped = stopped
		rows = append(rows, row)
	}

	if jsonOutput || !isInteractiveTerminal() {
		return printJSON(rows)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "DEVICE\tSTOPPED")
	for _, row := range rows {
		detail := strings.Join(row.Stopped, ", ")
		if row.Error != "" {
			detail = "error: " + row.Error
		} else if detail == "" {
			detail = "nothing matching " + appID
		}
		fmt.Fprintf(tw, "%s\t%s\n", row.Device, detail)
	}
	return tw.Flush()
}

// stopTrainAppsOnTarget stops exactly this application's containers on one
// device and returns the names it stopped.
func stopTrainAppsOnTarget(ctx context.Context, target fleetTarget, appID string) ([]string, error) {
	conn, err := target.connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connecting: %w", err)
	}
	defer conn.Conn.Close()

	containers, err := listContainersOnConn(ctx, conn)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(containers))
	for _, container := range containers {
		names = append(names, container.GetAppName())
	}

	stopped := make([]string, 0, 2)
	for _, name := range matchTrainContainers(names, appID) {
		if _, err := conn.ContainerService.StopContainer(ctx, &agentpb.StopContainerRequest{AppName: name}); err != nil {
			return stopped, fmt.Errorf("stopping %s: %w", name, err)
		}
		stopped = append(stopped, name)
	}
	return stopped, nil
}

// matchTrainContainers selects the containers belonging to appID: the
// application itself and its per-service containers, which the platform names
// "<appId>_<service>". The match is exact on that shape so a device running an
// unrelated application with a similar prefix is never touched.
func matchTrainContainers(names []string, appID string) []string {
	if appID == "" {
		return nil
	}
	matched := make([]string, 0, 2)
	for _, name := range names {
		if name == appID || strings.HasPrefix(name, appID+"_") {
			matched = append(matched, name)
		}
	}
	sort.Strings(matched)
	return matched
}

// resolveTrainTarget determines which application the operator means. A
// --template names it directly; otherwise the state a deploy saved for this
// group supplies it, which is what makes status and stop usable with just
// --group.
func resolveTrainTarget(o trainCommonOptions) (appID string, state *trainState, err error) {
	if o.template != "" {
		_, appCfg, err := resolveTemplateSource(o.template)
		if err != nil {
			return "", nil, err
		}
		if saved, ok := loadTrainState(o.group, appCfg.AppID); ok {
			return appCfg.AppID, saved, nil
		}
		return appCfg.AppID, nil, nil
	}
	for _, name := range embeddedTemplateNames() {
		if saved, ok := loadTrainState(o.group, trainAppIDForTemplate(name)); ok {
			return saved.AppID, saved, nil
		}
	}
	return "", nil, fmt.Errorf("no training run recorded for group %q; pass --template to say which one", o.group)
}

// --- helpers ------------------------------------------------------------------

// encodeSweepParams renders parameters from a configuration file back to the
// inline JSON the sweep parser accepts, so both surfaces meet in one place.
func encodeSweepParams(params []map[string]any) (string, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("encoding sweep parameters from the configuration file: %w", err)
	}
	return string(data), nil
}

// isSweepTemplate reports whether this template is the sweep one, which is the
// only template that reads per-device parameters.
func isSweepTemplate(src templateSource) bool {
	return strings.EqualFold(filepath.Base(strings.TrimSuffix(src.Name, "/")), "sweep")
}

// templateSourceLabel describes where a template came from, for the plan header.
func templateSourceLabel(src templateSource) string {
	if src.Embedded {
		return "embedded"
	}
	return src.Dir
}

// trainAppIDForTemplate is the application id a built-in template declares.
// Resolving the template is the authority; a failure here just means this
// template has no saved state to find.
func trainAppIDForTemplate(name string) string {
	_, appCfg, err := resolveTemplateSource(name)
	if err != nil {
		return ""
	}
	return appCfg.AppID
}

// trainDeviceBaseURL is the operator-reachable origin for a device's training
// endpoint: a locally discovered device is reached at its address, a cloud one
// through its mesh name.
func trainDeviceBaseURL(target fleetTarget, port int) (string, error) {
	if target.PeerHost != "" {
		return fmt.Sprintf("http://%s:%d", target.PeerHost, port), nil
	}
	if target.AssetID > 0 {
		return fmt.Sprintf("http://device-%d.cloud.wendy.dev:%d", target.AssetID, port), nil
	}
	return "", fmt.Errorf("device %s has neither a peer host nor an asset id", target.Name)
}

// pollTrainStatus asks one device for its training status, falling back to a
// liveness check. The reply comes from a network service, so it is reduced to
// printable characters and truncated before it reaches a terminal.
func pollTrainStatus(ctx context.Context, client *http.Client, target fleetTarget, port int, token string) string {
	base, err := trainDeviceBaseURL(target, port)
	if err != nil {
		return "unreachable: " + err.Error()
	}
	for _, route := range []string{"/status", "/healthz"} {
		reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base+route, nil)
		if err != nil {
			cancel()
			continue
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		resp.Body.Close()
		cancel()
		if readErr != nil || resp.StatusCode != http.StatusOK {
			if resp.StatusCode == http.StatusUnauthorized {
				return "unauthorized (wrong or missing fleet token)"
			}
			continue
		}
		return sanitizeStatusBody(string(body))
	}
	return "unreachable"
}

// sanitizeStatusBody makes an untrusted response safe to print: printable
// characters only, whitespace collapsed, and bounded length.
func sanitizeStatusBody(body string) string {
	var b strings.Builder
	for _, r := range body {
		if unicode.IsPrint(r) || r == ' ' {
			b.WriteRune(r)
		}
	}
	summary := strings.Join(strings.Fields(b.String()), " ")
	if len(summary) > 120 {
		summary = summary[:120]
	}
	if summary == "" {
		return "(empty response)"
	}
	return summary
}

// resolveTrainPeerHosts turns each device's peer host into something a
// container on another device can actually dial.
//
// Discovery reports a multicast hostname when it has no address for a device,
// and a slim container image cannot resolve multicast names: an earlier
// generation of this feature shipped .local names to the fleet and every
// worker failed with "Name or service not known". Resolution happens here, on
// the machine that can still do it, and a device that cannot be resolved is an
// error rather than a deploy that fails one layer down.
//
// Addresses outside private ranges are reported but not rejected: an operator
// may legitimately train across a routed network, and the warning is there so a
// surprising resolution is visible before the token travels to it.
func resolveTrainPeerHosts(ctx context.Context, targets []fleetTarget, transport trainTransport) ([]string, error) {
	if transport != transportLAN {
		return nil, nil
	}
	var warnings []string
	for i := range targets {
		host := targets[i].PeerHost
		if host == "" {
			return nil, fmt.Errorf("device %s reported no address to reach it at; it may have gone offline mid-discovery", targets[i].Name)
		}
		resolved := resolveHostMDNSFallback(ctx, host)
		if net.ParseIP(resolved) == nil {
			return nil, fmt.Errorf("cannot resolve %s to an address; containers on the other devices could not reach it by name", host)
		}
		if addr, err := netip.ParseAddr(resolved); err == nil && !addr.IsPrivate() && !addr.IsLoopback() {
			warnings = append(warnings, fmt.Sprintf(
				"%s resolved to %s, which is outside private address space; confirm that is the device you mean",
				targets[i].Name, resolved))
		}
		targets[i].PeerHost = resolved
	}
	return warnings, nil
}
