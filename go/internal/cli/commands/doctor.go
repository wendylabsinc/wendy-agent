package commands

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/diskspace"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// checkStatus is the outcome of a single doctor check.
type checkStatus string

const (
	statusPass checkStatus = "pass"
	statusWarn checkStatus = "warn"
	statusFail checkStatus = "fail"
	statusSkip checkStatus = "skip"
)

// checkResult is one line in the doctor report.
type checkResult struct {
	Name   string      `json:"name"`
	Status checkStatus `json:"status"`
	Detail string      `json:"detail,omitempty"`
	Hint   string      `json:"hint,omitempty"`
}

// doctorSummary counts results by status.
type doctorSummary struct {
	Pass int `json:"pass"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

// doctorReport is the full structured output of `wendy device doctor`.
type doctorReport struct {
	Device  string        `json:"device"`
	Checks  []checkResult `json:"checks"`
	Summary doctorSummary `json:"summary"`
}

// Thresholds for the resource and app-health checks. The disk free-% thresholds
// live in shared/diskspace so the agent's image GC engages at the same level
// this check warns at.
const (
	mtlsExpiryWarnDays = 14 // warn when the client cert expires within this many days
	crashLoopFailCount = 5  // fail an app whose restart count reaches this
)

// agentPorts are the ports the agent serves on: 50051 (plaintext, shut down
// after provisioning) and 50052 (mTLS on provisioned agents).
var agentPorts = []int{50051, 50052}

func newDoctorCmd() *cobra.Command {
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run device health & connectivity diagnostics",
		// Hidden: the reachability diagnostics surface automatically on a failed
		// connection (see maybePrintConnectionDiagnostics). The command stays
		// available as a manual full-health probe and for --json scripting.
		Hidden: true,
		Long: `Run a battery of health and connectivity checks against a device and print a
pass / warn / fail report with remediation hints.

Reachability and mTLS are checked from this machine even when the agent is
unreachable, so 'doctor' can explain *why* a device won't connect. The
remaining checks (version skew, resources, runtime, app health) query the
agent and are skipped when it can't be reached.

Exits non-zero if any check fails (warnings do not fail). Use --json for
scripting.`,
		// A failing check returns an error to drive the exit code; don't dump
		// Cobra usage in that case.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd.Context(), timeout)
		},
	}

	cmd.Flags().DurationVar(&timeout, "timeout", 6*time.Second, "Per-probe timeout for reachability checks")
	return cmd
}

func runDoctor(ctx context.Context, timeout time.Duration) error {
	addr, _, err := resolveDeviceAddress()
	if err != nil {
		return err
	}
	host := hostOnly(addr)

	report := doctorReport{Device: addr}

	// Tier 1 — CLI-side reachability (runs even when the agent is down).
	report.Checks = append(report.Checks, runReachabilityChecks(ctx, host, timeout)...)

	// Attempt a connection. On success we can also confirm the org match for
	// the mTLS check and run the agent-RPC checks.
	conn, connErr := connectToAgent(ctx, SuppressUpdateCheck(), SuppressProvisioningHint(), NonInteractive())
	if conn != nil {
		defer conn.Close()
	}

	// Tier 1 — mTLS (CLI-side cert validation).
	report.Checks = append(report.Checks, runMTLSCheck(conn, connErr))

	// Tier 2 — agent-RPC checks.
	if conn != nil {
		report.Checks = append(report.Checks, runAgentChecks(ctx, conn)...)
	} else {
		for _, name := range []string{"Version skew", "Resources", "Runtime (containerd)", "App health"} {
			report.Checks = append(report.Checks, checkResult{
				Name:   name,
				Status: statusSkip,
				Detail: "agent unreachable — see Reachability",
			})
		}
	}

	report.Summary = summarize(report.Checks)

	if jsonOutput {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
	} else {
		printReport(report)
	}

	if report.Summary.Fail > 0 {
		return fmt.Errorf("doctor: %d check(s) failed", report.Summary.Fail)
	}
	return nil
}

// ---- Reachability (CLI-side) -----------------------------------------------

func runReachabilityChecks(ctx context.Context, host string, timeout time.Duration) []checkResult {
	var out []checkResult
	out = append(out, evaluateMDNS(ctx, host, timeout))

	open, tcp := evaluateTCP(host, agentPorts, timeout)
	out = append(out, tcp)

	if !open {
		// Dig into the host's networking to explain the failure. macOS gets the
		// full route/VPN/subnet/MAC probe; other platforms get a plain dial.
		out = append(out, hostNetworkDiagnostics(resolveIP(host), agentPorts)...)
		out = append(out, checkResult{
			Name:   "Cloud tunnel",
			Status: statusSkip,
			Detail: "LAN unreachable — try 'wendy cloud connect' as a fallback path",
		})
	}

	out = append(out, evaluateDefaultDevice(host))
	return out
}

func evaluateMDNS(ctx context.Context, host string, timeout time.Duration) checkResult {
	const name = "Discovery (mDNS)"
	devs, err := discovery.DiscoverLAN(ctx, timeout)
	if err != nil {
		return checkResult{Name: name, Status: statusWarn, Detail: "mDNS discovery failed: " + err.Error()}
	}
	for _, d := range devs {
		if lanDeviceMatchesHost(d, host) {
			detail := fmt.Sprintf("%s found via mDNS", d.Hostname)
			if d.IPAddress != "" {
				detail = fmt.Sprintf("%s found via mDNS at %s", d.Hostname, d.IPAddress)
			}
			return checkResult{Name: name, Status: statusPass, Detail: detail}
		}
	}
	return checkResult{
		Name:   name,
		Status: statusWarn,
		Detail: host + " not seen via mDNS",
		Hint:   "Device may be on another subnet or mDNS is blocked; a direct IP can still work.",
	}
}

func lanDeviceMatchesHost(d models.LANDevice, host string) bool {
	target := normalizeHost(host)
	for _, candidate := range []string{d.Hostname, d.DisplayName, d.IPAddress, d.ID} {
		if candidate != "" && normalizeHost(candidate) == target {
			return true
		}
	}
	return false
}

func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	return strings.TrimSuffix(h, ".local")
}

func evaluateTCP(host string, ports []int, timeout time.Duration) (bool, checkResult) {
	const name = "Reachability (gRPC)"
	var open []int
	for _, p := range ports {
		if tcpReachable(host, p, timeout) {
			open = append(open, p)
		}
	}
	if len(open) > 0 {
		return true, checkResult{
			Name:   name,
			Status: statusPass,
			Detail: fmt.Sprintf("%s accepting connections on port %s", host, joinPorts(open)),
		}
	}
	return false, checkResult{
		Name:   name,
		Status: statusFail,
		Detail: fmt.Sprintf("%s refused connections on %s", host, joinPorts(ports)),
		Hint:   "Agent not reachable on the LAN — see host network diagnostics below.",
	}
}

func tcpReachable(host string, port int, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// evaluateDefaultDevice warns when the resolved host differs from the saved
// default device, a common source of "works on one Mac but not another".
func evaluateDefaultDevice(host string) checkResult {
	const name = "Default device"
	cfg, err := config.Load()
	if err != nil || cfg.DefaultDevice == "" {
		return checkResult{Name: name, Status: statusSkip, Detail: "no default device configured"}
	}
	if normalizeHost(hostOnly(cfg.DefaultDevice)) == normalizeHost(host) {
		return checkResult{Name: name, Status: statusPass, Detail: "matches saved default " + cfg.DefaultDevice}
	}
	return checkResult{
		Name:   name,
		Status: statusWarn,
		Detail: fmt.Sprintf("targeting %s but saved default is %s", host, cfg.DefaultDevice),
		Hint:   "If the default is stale, update it with 'wendy device set-default'.",
	}
}

// ---- mTLS (CLI-side) -------------------------------------------------------

func runMTLSCheck(conn *grpcclient.AgentConnection, connErr error) checkResult {
	// When connected via mTLS we know exactly which cert was used and that the
	// org matched the device.
	if conn != nil && conn.IsMTLS && conn.CertInfo != nil {
		return evaluateMTLS(conn.CertInfo, time.Now(), true)
	}
	// Surface an org mismatch explicitly — the client has a cert, it's just for
	// the wrong org.
	var mismatch *certs.OrgMismatchError
	if connErr != nil && errors.As(connErr, &mismatch) {
		return checkResult{
			Name:   "mTLS",
			Status: statusFail,
			Detail: fmt.Sprintf("device belongs to org %d, no matching certificate", mismatch.Got),
			Hint:   "Authenticate for that org with 'wendy auth login'.",
		}
	}
	// Offline (or plaintext): report on the best client cert we hold.
	cfg, err := config.Load()
	if err != nil {
		return checkResult{Name: "mTLS", Status: statusWarn, Detail: "could not load config: " + err.Error()}
	}
	return evaluateMTLS(firstClientCert(cfg), time.Now(), false)
}

// evaluateMTLS validates a client certificate's presence, expiry, and (when
// known) org match. It is pure so it can be unit-tested with a fixed `now`.
func evaluateMTLS(cert *config.CertificateInfo, now time.Time, orgVerified bool) checkResult {
	const name = "mTLS"
	if cert == nil || cert.PemCertificate == "" {
		return checkResult{
			Name:   name,
			Status: statusFail,
			Detail: "no client certificate",
			Hint:   "Run 'wendy auth login' to obtain a certificate.",
		}
	}
	x, err := parseLeafCert(cert.PemCertificate)
	if err != nil {
		return checkResult{Name: name, Status: statusWarn, Detail: "could not parse certificate: " + err.Error()}
	}

	org := fmt.Sprintf("org %d", cert.OrganizationID)
	if now.After(x.NotAfter) {
		return checkResult{
			Name:   name,
			Status: statusFail,
			Detail: fmt.Sprintf("%s, expired %s", org, x.NotAfter.Format("2006-01-02")),
			Hint:   "Certificate expired — run 'wendy auth login' to renew.",
		}
	}

	daysLeft := int(x.NotAfter.Sub(now).Hours() / 24)
	detail := fmt.Sprintf("%s, valid until %s (%dd left)", org, x.NotAfter.Format("2006-01-02"), daysLeft)
	if orgVerified {
		detail += ", org verified against device"
	}
	if daysLeft < mtlsExpiryWarnDays {
		return checkResult{
			Name:   name,
			Status: statusWarn,
			Detail: detail,
			Hint:   "Certificate expiring soon — run 'wendy auth login' to renew.",
		}
	}
	return checkResult{Name: name, Status: statusPass, Detail: detail}
}

func parseLeafCert(certPEM string) (*x509.Certificate, error) {
	leafPEM, err := certs.LeafCertificatePEM(certPEM)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode([]byte(leafPEM))
	if block == nil {
		return nil, fmt.Errorf("no PEM block in certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

func firstClientCert(cfg *config.Config) *config.CertificateInfo {
	for i := range cfg.Auth {
		for j := range cfg.Auth[i].Certificates {
			if cfg.Auth[i].Certificates[j].PemCertificate != "" {
				return &cfg.Auth[i].Certificates[j]
			}
		}
	}
	return nil
}

// ---- Agent-RPC checks ------------------------------------------------------

func runAgentChecks(ctx context.Context, conn *grpcclient.AgentConnection) []checkResult {
	var out []checkResult

	resp, err := conn.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
	if err != nil {
		out = append(out,
			checkResult{Name: "Version skew", Status: statusSkip, Detail: "agent did not respond: " + err.Error()},
			checkResult{Name: "Resources", Status: statusSkip, Detail: "agent did not respond"},
		)
	} else {
		latest := ""
		if rel, relErr := fetchAgentRelease(false); relErr == nil {
			latest = rel.TagName
		}
		out = append(out, evaluateVersionSkew(resp.GetVersion(), version.Version, latest))
		out = append(out, evaluateResources(resp)...)
	}

	containers, cerr := fetchContainers(ctx, conn)
	if cerr != nil {
		out = append(out,
			checkResult{
				Name:   "Runtime (containerd)",
				Status: statusFail,
				Detail: "container runtime unreachable: " + cerr.Error(),
				Hint:   "Check the agent and containerd on the device.",
			},
			checkResult{Name: "App health", Status: statusSkip, Detail: "container list unavailable"},
		)
		return out
	}
	out = append(out, checkResult{Name: "Runtime (containerd)", Status: statusPass, Detail: "container runtime reachable"})
	out = append(out, runtimeEnvChecks()...)
	out = append(out, evaluateAppHealth(containers)...)
	return out
}

func fetchContainers(ctx context.Context, conn *grpcclient.AgentConnection) ([]*agentpb.AppContainer, error) {
	stream, err := conn.ContainerService.ListContainers(ctx, &agentpb.ListContainersRequest{})
	if err != nil {
		return nil, err
	}
	var containers []*agentpb.AppContainer
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if c := resp.GetContainer(); c != nil {
			containers = append(containers, c)
		}
	}
	return containers, nil
}

// runtimeEnvChecks reports runtime environment facts that have no clean agent
// data source today. Both are explicit skips so the report is honest about
// what wasn't checked (planned follow-ups, see WDY-1756).
func runtimeEnvChecks() []checkResult {
	return []checkResult{
		{Name: "Network manager", Status: statusSkip, Detail: "NetworkManager assumed; ConnMan detection is a planned follow-up"},
		{Name: "Time sync", Status: statusSkip, Detail: "no agent data source (planned follow-up)"},
	}
}

func evaluateVersionSkew(agentVer, cliVer, latestVer string) checkResult {
	const name = "Version skew"
	if agentVer == "" {
		return checkResult{Name: name, Status: statusSkip, Detail: "agent version unavailable"}
	}

	parts := []string{"agent " + agentVer, "CLI " + cliVer}
	if latestVer != "" {
		parts = append(parts, "latest "+latestVer)
	}
	detail := strings.Join(parts, ", ")

	// "dev" builds compare unhelpfully; don't flag agent-vs-CLI skew for them.
	if agentVer != "dev" && cliVer != "dev" {
		if version.CompareVersions(cliVer, agentVer) > 0 {
			return checkResult{Name: name, Status: statusWarn, Detail: detail, Hint: "Agent is behind the CLI — run 'wendy device update'."}
		}
		if version.CompareVersions(agentVer, cliVer) > 0 {
			return checkResult{Name: name, Status: statusWarn, Detail: detail, Hint: "CLI is behind the agent — consider updating the CLI."}
		}
	}
	if latestVer != "" && agentVer != "dev" && version.CompareVersions(latestVer, agentVer) > 0 {
		return checkResult{Name: name, Status: statusWarn, Detail: detail, Hint: fmt.Sprintf("Agent update available (%s) — run 'wendy device update'.", latestVer)}
	}
	return checkResult{Name: name, Status: statusPass, Detail: detail}
}

func evaluateResources(resp *agentpb.GetAgentVersionResponse) []checkResult {
	var out []checkResult
	out = append(out, evaluateDisk(resp)...)
	out = append(out, checkResult{
		Name:   "Memory",
		Status: statusSkip,
		Detail: "no agent data source (planned follow-up)",
	})
	out = append(out, evaluateGPU(resp))
	return out
}

func evaluateDisk(resp *agentpb.GetAgentVersionResponse) []checkResult {
	parts := resp.GetPartitions()
	if len(parts) == 0 {
		if resp.GetDiskTotalBytes() > 0 {
			return []checkResult{evaluateDiskUsage("/", resp.GetDiskUsedBytes(), resp.GetDiskTotalBytes())}
		}
		return []checkResult{{Name: "Disk", Status: statusSkip, Detail: "no filesystem data"}}
	}

	var out []checkResult
	for _, p := range parts {
		if mp := p.GetMountpoint(); mp == "/" || mp == "/data" {
			out = append(out, evaluateDiskUsage(mp, p.GetUsedBytes(), p.GetTotalBytes()))
		}
	}
	// If neither / nor /data is present, report every real filesystem so the
	// resource check still says something useful.
	if len(out) == 0 {
		for _, p := range parts {
			out = append(out, evaluateDiskUsage(p.GetMountpoint(), p.GetUsedBytes(), p.GetTotalBytes()))
		}
	}
	return out
}

func evaluateDiskUsage(mount string, used, total int64) checkResult {
	name := "Disk " + mount
	if total <= 0 {
		return checkResult{Name: name, Status: statusSkip, Detail: "size unknown"}
	}
	free := total - used // clamp: agents can report used > total (reserved blocks)
	if free < 0 {
		free = 0
	}
	freePct := float64(free) / float64(total) * 100
	detail := fmt.Sprintf("%s free of %s (%.0f%% free)", formatBytes(free), formatBytes(total), freePct)
	switch {
	case freePct < diskspace.FailFreePct:
		return checkResult{Name: name, Status: statusFail, Detail: detail, Hint: "Disk almost full — free space or new deploys will fail."}
	case freePct < diskspace.WarnFreePct:
		return checkResult{Name: name, Status: statusWarn, Detail: detail, Hint: "Disk running low — consider clearing old images and volumes."}
	default:
		return checkResult{Name: name, Status: statusPass, Detail: detail}
	}
}

func evaluateGPU(resp *agentpb.GetAgentVersionResponse) checkResult {
	const name = "GPU"
	if !resp.GetHasGpu() {
		return checkResult{Name: name, Status: statusPass, Detail: "no GPU detected (CPU-only board)"}
	}
	vendor := resp.GetGpuVendor()
	if vendor == "" {
		vendor = "unknown"
	}
	var extra []string
	if v := resp.GetJetpackVersion(); v != "" {
		extra = append(extra, "JetPack "+v)
	}
	if v := resp.GetCudaVersion(); v != "" {
		extra = append(extra, "CUDA "+v)
	}
	if v := resp.GetGpuArch(); v != "" {
		extra = append(extra, v)
	}
	detail := vendor
	if len(extra) > 0 {
		detail += " (" + strings.Join(extra, ", ") + ")"
	}

	if strings.EqualFold(vendor, "nvidia") && resp.GetCudaVersion() == "" && resp.GetJetpackVersion() == "" {
		return checkResult{
			Name:   name,
			Status: statusWarn,
			Detail: detail,
			Hint:   "GPU present but no CUDA/JetPack detected — the driver stack may be incomplete.",
		}
	}
	return checkResult{Name: name, Status: statusPass, Detail: detail}
}

func evaluateAppHealth(containers []*agentpb.AppContainer) []checkResult {
	if len(containers) == 0 {
		return []checkResult{{Name: "App health", Status: statusPass, Detail: "no apps deployed"}}
	}
	var out []checkResult
	for _, c := range containers {
		name := "App " + c.GetAppName()
		fc := c.GetFailureCount()
		running := c.GetRunningState() == agentpb.AppRunningState_RUNNING
		state := "stopped"
		if running {
			state = "running"
		}
		switch {
		case fc >= crashLoopFailCount:
			out = append(out, checkResult{
				Name:   name,
				Status: statusFail,
				Detail: fmt.Sprintf("%d restarts, %s", fc, state),
				Hint:   "App is crash-looping — check 'wendy device logs'.",
			})
		case fc > 0:
			out = append(out, checkResult{
				Name:   name,
				Status: statusWarn,
				Detail: fmt.Sprintf("%d restarts, %s", fc, state),
				Hint:   "App has restarted — check 'wendy device logs' if unexpected.",
			})
		default:
			out = append(out, checkResult{Name: name, Status: statusPass, Detail: state})
		}
	}
	return out
}

// ---- Output ----------------------------------------------------------------

func summarize(checks []checkResult) doctorSummary {
	var s doctorSummary
	for _, c := range checks {
		switch c.Status {
		case statusPass:
			s.Pass++
		case statusWarn:
			s.Warn++
		case statusFail:
			s.Fail++
		case statusSkip:
			s.Skip++
		}
	}
	return s
}

func printReport(report doctorReport) {
	fmt.Printf("%s %s\n\n", tui.Dim("Device:"), tui.Device(report.Device))
	for _, c := range report.Checks {
		label := fmt.Sprintf("%s %s", statusGlyph(c.Status), c.Name)
		if c.Detail != "" {
			fmt.Printf("%s  %s\n", label, tui.Dim(c.Detail))
		} else {
			fmt.Println(label)
		}
		if c.Hint != "" {
			fmt.Printf("    %s %s\n", tui.Dim("→"), c.Hint)
		}
	}

	s := report.Summary
	fmt.Println()
	fmt.Printf("%s\n", tui.Dim(fmt.Sprintf("%d passed, %d warning(s), %d failure(s), %d skipped",
		s.Pass, s.Warn, s.Fail, s.Skip)))
	switch {
	case s.Fail > 0:
		fmt.Println(tui.ErrorMessage("Device has failing checks."))
	case s.Warn > 0:
		fmt.Println(tui.WarningMessage("Device healthy with warnings."))
	default:
		fmt.Println(tui.SuccessMessage("Device healthy."))
	}
}

func statusGlyph(s checkStatus) string {
	switch s {
	case statusPass:
		return tui.SuccessMessage("✓")
	case statusWarn:
		return tui.WarningMessage("⚠")
	case statusFail:
		return tui.ErrorMessage("✗")
	default:
		return tui.Dim("–")
	}
}

// ---- small helpers ---------------------------------------------------------

// hostOnly strips a trailing :port from an address, returning the host alone.
// It tolerates bare hosts (no port) and bracketed IPv6.
func hostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// resolveIP returns an IP for host: host itself if it's already an IP, else the
// first resolved address, else host unchanged (best effort for diagnostics).
// hostNetworkDiagnostics is macOS-only, and the macOS CLI is built with cgo (it
// links CoreBluetooth), so the OS resolver here resolves ".local" mDNS names
// natively — no separate mDNS fallback is needed.
func resolveIP(host string) string {
	if net.ParseIP(host) != nil {
		return host
	}
	if ips, err := net.LookupHost(host); err == nil && len(ips) > 0 {
		return ips[0]
	}
	return host
}

// maybePrintConnectionDiagnostics passively explains why an agent is
// unreachable. It is called from the connection-failure path so a raw
// "connection refused" becomes an actionable hint, reusing the doctor host
// network diagnostics. To stay fast on every failed command it runs only the
// host-side probe (route/VPN/subnet/ping) — not mDNS or a redial, since the
// caller already knows the dial failed. It is a no-op in JSON mode, when
// suppressed (e.g. the doctor command itself, which runs its own checks), or
// when the failure isn't a network reachability problem.
func maybePrintConnectionDiagnostics(host string, suppress bool) {
	if jsonOutput || suppress || host == "" {
		return
	}
	top := topConnectionFinding(hostNetworkDiagnostics(resolveIP(host), agentPorts))
	if top == nil {
		return
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, tui.WarningMessage(fmt.Sprintf("Can't reach %s — %s", host, top.Detail)))
	if top.Hint != "" {
		fmt.Fprintf(os.Stderr, "  %s %s\n", tui.Dim("→"), top.Hint)
	}
	fmt.Fprintf(os.Stderr, "  %s %s\n", tui.Dim("Full diagnostics:"), tui.Command("wendy device doctor --device "+host))
}

// topConnectionFinding picks the single most-relevant reachability problem to
// surface passively: a hard failure (wrong subnet, VPN diverting the route,
// host down) by specificity, or — absent any failure — the "host up but agent
// port closed" warning. Returns nil when nothing actionable was found, so we
// stay quiet rather than guess.
func topConnectionFinding(checks []checkResult) *checkResult {
	rank := map[string]int{"Subnet": 4, "VPN / tunnel": 3, "Host reachability": 2}
	var best *checkResult
	bestRank := -1
	for i := range checks {
		c := &checks[i]
		surface := c.Status == statusFail || (c.Name == "Host reachability" && c.Status == statusWarn)
		if !surface || c.Hint == "" {
			continue
		}
		if r := rank[c.Name]; best == nil || r > bestRank {
			best, bestRank = c, r
		}
	}
	return best
}

func joinPorts(ports []int) string {
	strs := make([]string, len(ports))
	for i, p := range ports {
		strs[i] = strconv.Itoa(p)
	}
	return strings.Join(strs, "/")
}
