package commands

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/wendylabsinc/wendy/go/internal/cli/ble"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/providers"
	clitimesync "github.com/wendylabsinc/wendy/go/internal/cli/timesync"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/devicepin"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"golang.org/x/term"
)

const defaultAgentPort = 50051
const agentMTLSPortOffset = 1

const agentPlaintextProbeTimeout = 3 * time.Second

// mtlsProbeTimeout bounds a single mTLS connect+probe. The dial target is
// already an IP (resolveAddrOnce), so this only needs to cover TCP + TLS
// handshake; keeping it tight stops an unreachable/plaintext-only mTLS port
// from stalling the connect before the plaintext fallback.
const mtlsProbeTimeout = 3 * time.Second

// lanAddressProbeTimeout bounds a single candidate address in the discover/
// picker version probe (resolveLANAgentVersion). It wraps connectWithAutoTLS,
// whose successful path for a provisioned device costs a TCP+TLS handshake plus
// one mtlsProbeTimeout GetAgentVersion probe (~2.2s observed). A budget shorter
// than mtlsProbeTimeout therefore cancelled the mTLS probe before it could
// answer, leaving provisioned rows — especially USB devices, probed via .local
// first — stuck on the failure glyph even though `wendy device info` (which
// connects with the un-capped root context) succeeded. Derive it from
// mtlsProbeTimeout with headroom so the two budgets can't silently invert again.
const lanAddressProbeTimeout = mtlsProbeTimeout + 2*time.Second
const provisionedAgentMetadataDiscoveryTimeout = 500 * time.Millisecond

const provisionedAgentUnauthorizedMessage = "Unauthorized. Run 'wendy auth login' with an account that can access this provisioned wendy-agent."

var errProvisionedAgentUnauthorized = errors.New(provisionedAgentUnauthorizedMessage)

// errTLSHandshakeRejected is the sentinel for "the device rejected our client
// certificate during the TLS handshake" — the signature of clock skew (the
// device clock lags the cert's validity window) or a genuine cert mismatch.
// connectWithAutoTLSDiagnostics returns a tlsHandshakeRejectedError, so callers
// can detect this case with errors.Is rather than string matching.
var errTLSHandshakeRejected = errors.New("TLS handshake rejected by device")

type tlsHandshakeRejectedError struct {
	cause error
}

func newTLSHandshakeRejectedError(cause error) error {
	return tlsHandshakeRejectedError{cause: cause}
}

func (e tlsHandshakeRejectedError) Is(target error) bool {
	return target == errTLSHandshakeRejected
}

func (e tlsHandshakeRejectedError) Unwrap() error {
	return e.cause
}

func (e tlsHandshakeRejectedError) Error() string {
	return "TLS handshake rejected by device (possible clock skew or cert mismatch).\n  Check the device clock: ssh wendy@<host> 'timedatectl status'\n  For full TLS details rerun with WENDY_TLS_DEBUG=1"
}

type provisionedAgentUnauthorizedError struct {
	cause error
}

func newProvisionedAgentUnauthorizedError(cause error) error {
	if cause == nil {
		return errProvisionedAgentUnauthorized
	}
	return provisionedAgentUnauthorizedError{cause: cause}
}

func (e provisionedAgentUnauthorizedError) Error() string {
	msg := fmt.Sprintf("%s\nLast mTLS error: %v", provisionedAgentUnauthorizedMessage, e.cause)
	if isCertRefreshableError(e.cause) {
		msg += "\nYour stored certificates may be outdated. Run 'wendy auth refresh-certs' to re-issue them."
	} else if isReachabilityTimeoutError(e.cause) {
		msg += "\nThe device is enrolled and only serves mTLS on the secure port. Your wendy CLI may be too old or its certificates stale — upgrade the CLI and run 'wendy auth refresh-certs'."
	}
	return msg
}

// isReachabilityTimeoutError reports whether an error is a connection timeout
// against an mTLS-enrolled device's plaintext port. This indicates the device
// is up and enrolled (only the mTLS port is open), which may mean the CLI is
// too old to speak mTLS or its certificates are stale.
func isReachabilityTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "connection timed out") ||
		strings.Contains(msg, "deadline exceeded")
}

// isCertRefreshableError reports whether an mTLS failure is one that
// re-issuing the client certificate can fix: the agent rejecting a cert
// without the clientAuth EKU, an expired or not-yet-valid cert, or a
// server-sent TLS alert rejecting the presented cert. Reachability problems
// and plaintext ports probed with TLS are excluded — new certs cannot fix
// those.
func isCertRefreshableError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, "first record does not look like a TLS handshake") {
		return false
	}
	for _, signal := range []string{
		"certificate is not valid for client authentication",
		"certificate not valid at current time",
		"certificate has expired",
		"expired certificate",
		"remote error: tls: bad certificate",
		"remote error: tls: certificate required",
	} {
		if strings.Contains(msg, signal) {
			return true
		}
	}
	return false
}

// promptYesNoFn reads a Y/n answer from the controlling terminal; empty input
// counts as yes. Stubbed in tests.
var promptYesNoFn = func(prompt string) bool {
	return promptYesNoLine(prompt, true)
}

// promptYesNoDefaultNoFn reads a y/N answer from the controlling terminal;
// empty input counts as no. Used for more speculative offers (e.g. a timeout
// against an enrolled device, where refreshing certs is a guess rather than a
// clear diagnosis). Stubbed in tests.
var promptYesNoDefaultNoFn = func(prompt string) bool {
	return promptYesNoLine(prompt, false)
}

func promptYesNoLine(prompt string, defaultYes bool) bool {
	in, out, cleanup := promptTTYIO()
	defer cleanup()

	// Clear any stale spinner/hint line before printing the prompt. This keeps a
	// simple line prompt from visually colliding with a previously-rendered TUI.
	fmt.Fprint(out, "\r\x1b[2K")
	fmt.Fprint(out, prompt)

	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && strings.TrimSpace(answer) == "" {
		return false
	}
	return parseYesNoAnswer(answer, defaultYes)
}

func parseYesNoAnswer(answer string, defaultYes bool) bool {
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer == "" {
		return defaultYes
	}
	return answer == "y" || answer == "yes"
}

func promptTTYIO() (io.Reader, io.Writer, func()) {
	if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		return tty, tty, func() { _ = tty.Close() }
	}
	return os.Stdin, os.Stderr, func() {}
}

var refreshAllCertsFn = refreshAllCerts

// offerCertRefreshAndRetry prompts to re-issue mTLS certificates after a
// provisioned agent rejected the client certificate for a reason that
// re-issuance fixes, then retries the connection once. Returns (conn, true)
// only when the user accepted, the refresh succeeded, and the retry
// connected; in every other case the caller should surface the original
// error (whose message already carries the refresh-certs hint).
func offerCertRefreshAndRetry(ctx context.Context, cause error, retry func() (*grpcclient.AgentConnection, error)) (*grpcclient.AgentConnection, bool) {
	certRejected := isCertRefreshableError(cause)
	enrolledTimeout := isReachabilityTimeoutError(cause)
	if jsonOutput || !isInteractiveTerminal() || !(certRejected || enrolledTimeout) {
		return nil, false
	}
	var accepted bool
	if certRejected {
		// Clear diagnosis: the agent rejected the cert. Default to yes.
		fmt.Fprintln(os.Stderr, "The device rejected your client certificate; it may be outdated.")
		accepted = promptYesNoFn("Refresh certificates and retry? [Y/n] ")
	} else {
		// Timeout against an enrolled (mTLS-only) device. Refreshing certs is a
		// reasonable guess (e.g. clock skew stalling the handshake) but less
		// certain, so default to no.
		fmt.Fprintln(os.Stderr, "The device is enrolled and only responds on the secure (mTLS) port. Your certificates may be stale or your CLI too old.")
		accepted = promptYesNoDefaultNoFn("Refresh certificates and retry? [y/N] ")
	}
	if !accepted {
		return nil, false
	}
	if err := refreshAllCertsFn(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Certificate refresh failed: %v\n", err)
		return nil, false
	}
	conn, err := retry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Still unable to connect after refreshing certificates: %v\n", err)
		return nil, false
	}
	return conn, true
}

// clockSkewSyncTimeout bounds the Roughtime query + multicast.
const clockSkewSyncTimeout = 5 * time.Second

// clockSkewRetryDelay gives the device a moment to receive the multicast time
// proof and advance its clock before we retry the TLS handshake.
const clockSkewRetryDelay = 1500 * time.Millisecond

// broadcastTimeFn fetches a signed time proof and multicasts it to devices on
// the LAN. Indirected for tests.
var broadcastTimeFn = func(ctx context.Context) error {
	_, err := clitimesync.BroadcastTime(ctx)
	return err
}

// clockSkewSyncSleep is the post-broadcast wait. Indirected for tests.
var clockSkewSyncSleep = func(d time.Duration) { time.Sleep(d) }

// clockSkewSyncAttempted guards against syncing more than once per CLI run, so
// repeated connect attempts don't trigger a sync-and-sleep storm.
var clockSkewSyncAttempted bool

// isClockSkewSuspectError reports whether a connection failure looks like the
// device rejected our client cert during the TLS handshake — the signature of
// clock skew (which a time sync can fix). It matches the typed handshake
// rejection as well as cert-refreshable TLS alerts.
func isClockSkewSuspectError(err error) bool {
	return errors.Is(err, errTLSHandshakeRejected) || isCertRefreshableError(err)
}

// autoSyncTimeAndRetry handles a likely clock-skew rejection automatically: it
// broadcasts a signed time proof to the device (the same work as
// `wendy sync-time`), waits briefly for the device to adopt it, then retries
// the connection once. Returns (conn, true) only when the sync ran and the
// retry connected; in every other case the caller falls through to its
// existing error handling (e.g. the interactive cert-refresh offer).
//
// The sync runs at most once per CLI invocation. Unlike offerCertRefreshAndRetry
// it is non-interactive — clock skew has an unambiguous, side-effect-free remedy
// — so it does not gate on an interactive terminal.
func autoSyncTimeAndRetry(ctx context.Context, cause error, retry func() (*grpcclient.AgentConnection, error)) (*grpcclient.AgentConnection, bool) {
	if !isClockSkewSuspectError(cause) || clockSkewSyncAttempted {
		return nil, false
	}
	clockSkewSyncAttempted = true

	if !jsonOutput {
		fmt.Fprintln(os.Stderr, "⏱  Possible clock skew — syncing device time and retrying...")
	}

	syncCtx, cancel := context.WithTimeout(ctx, clockSkewSyncTimeout)
	syncErr := broadcastTimeFn(syncCtx)
	cancel()
	if syncErr != nil {
		// Without a fresh time proof the device clock won't move, so retrying
		// would just fail again. Surface the cause under the TLS debug flag.
		if os.Getenv("WENDY_TLS_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "[tls-debug] time sync failed: %v\n", syncErr)
		}
		return nil, false
	}

	clockSkewSyncSleep(clockSkewRetryDelay)

	conn, err := retry()
	if err != nil {
		return nil, false
	}
	return conn, true
}

func (e provisionedAgentUnauthorizedError) Is(target error) bool {
	return target == errProvisionedAgentUnauthorized
}

func (e provisionedAgentUnauthorizedError) Unwrap() error {
	return e.cause
}

var getAgentVersionAtAddress = func(ctx context.Context, address string) (bool, *agentpb.GetAgentVersionResponse, error) {
	conn, err := connectWithAutoTLS(ctx, address)
	if err != nil {
		return false, nil, err
	}
	defer conn.Close()

	resp, err := conn.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
	return conn.IsMTLS, resp, err
}

var discoverLANDevices = discovery.DiscoverLAN

var isInteractiveTerminalFn = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

var runAgentConnectionSpinner = func(ctx context.Context, label string, fn func(context.Context) (*grpcclient.AgentConnection, error)) (*grpcclient.AgentConnection, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	prog := tui.NewProgressProgram(tui.NewSpinner(label))

	var (
		conn   *grpcclient.AgentConnection
		runErr error
		doneCh = make(chan struct{})
	)
	go func() {
		defer close(doneCh)
		conn, runErr = fn(ctx)
		// Keep spinner teardown quiet; callers handle the returned error.
		prog.Send(tui.SpinnerDoneMsg{})
	}()

	finalModel, err := prog.Run()
	if err != nil {
		cancel()
		<-doneCh
		if conn != nil {
			conn.Close()
		}
		return nil, fmt.Errorf("spinner TUI: %w", err)
	}

	if sm, ok := finalModel.(tui.SpinnerModel); ok && !sm.Done() {
		cancel()
		<-doneCh
		if conn != nil {
			conn.Close()
		}
		return nil, ErrUserCancelled
	}

	<-doneCh
	return conn, runErr
}

// ErrUserCancelled is returned when the user cancels an interactive prompt (e.g. Ctrl+C).
var ErrUserCancelled = errors.New("cancelled")

// ErrDefaultCleared is returned after the user chooses to unset the default
// device from the recovery menu. main.go treats this as a graceful exit (code 0).
var ErrDefaultCleared = errors.New("default device cleared")

// hostPort formats a host and port into an address string,
// wrapping IPv6 addresses in brackets as required by RFC 3986.
// Uses netip.ParseAddr so IPv6 link-local addresses with zone IDs
// (e.g. fe80::1%en0) are correctly detected and bracketed.
func hostPort(host string, port int) string {
	if addr, err := netip.ParseAddr(host); err == nil && addr.Is6() {
		return fmt.Sprintf("[%s]:%d", host, port)
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// lanAgentAddresses returns candidate gRPC addresses for a LAN device.
// Prefer the discovered IP address so commands still work when .local
// hostname resolution is unavailable on the host machine.
//
// For provisioned (mTLS) devices the Avahi advertisement carries the mTLS
// port. connectWithAutoTLS derives the mTLS port as plaintext plus
// agentMTLSPortOffset, so we subtract that offset here to keep that
// convention working correctly.
func lanAgentAddresses(dev models.LANDevice) []string {
	port := dev.Port
	if port == 0 {
		port = defaultAgentPort
	}
	if dev.IsMTLS && dev.Port != 0 && port > agentMTLSPortOffset {
		port -= agentMTLSPortOffset // advertised port is mTLS; connectWithAutoTLS will add the offset back
	}

	hosts := []string{strings.TrimSpace(dev.IPAddress), strings.TrimSpace(dev.Hostname)}
	if strings.TrimSpace(dev.USB) != "" {
		// A USB-NCM path exists. The routed Wi-Fi IP (dev.IPAddress) may be
		// black-holed by AP isolation on residential routers, so try the
		// link-local .local hostname (reachable over USB) first.
		hosts = []string{strings.TrimSpace(dev.Hostname), strings.TrimSpace(dev.IPAddress)}
	}

	var addresses []string
	seen := make(map[string]bool)
	for _, host := range hosts {
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		addresses = append(addresses, hostPort(host, port))
	}

	return addresses
}

func preferredLANAddress(dev models.LANDevice) string {
	addresses := lanAgentAddresses(dev)
	if len(addresses) == 0 {
		return ""
	}
	return addresses[0]
}

// resolveLANAgentVersion tries the discovered LAN addresses in order and
// returns the first one that answers GetAgentVersion, along with whether that
// connection used mTLS.
func resolveLANAgentVersion(ctx context.Context, dev models.LANDevice) (string, bool, *agentpb.GetAgentVersionResponse, error) {
	var lastErr error
	for _, address := range lanAgentAddresses(dev) {
		attemptCtx, cancel := context.WithTimeout(ctx, lanAddressProbeTimeout)
		isMTLS, resp, err := getAgentVersionAtAddress(attemptCtx, address)
		cancel()
		if err == nil {
			return address, isMTLS, resp, nil
		}
		lastErr = err
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no LAN address available for %q", dev.DisplayName)
	}
	return "", false, nil, lastErr
}

// resolveLANVersions queries each LAN device's gRPC endpoint concurrently to
// populate AgentVersion, OS, OSVersion, and CPUArchitecture.
// Devices stay in the returned slice even when the metadata probe fails.
func resolveLANVersions(ctx context.Context, devices []models.LANDevice) []models.LANDevice {
	type indexedResult struct {
		index int
		resp  *agentpb.GetAgentVersionResponse
	}

	ch := make(chan *indexedResult, len(devices))
	for i := range devices {
		go func(idx int) {
			d := &devices[idx]
			_, _, resp, err := resolveLANAgentVersion(ctx, *d)
			if err != nil {
				ch <- &indexedResult{index: idx}
				return
			}
			ch <- &indexedResult{index: idx, resp: resp}
		}(i)
	}

	for range devices {
		r := <-ch
		if r != nil && r.resp != nil {
			devices[r.index].AgentVersion = r.resp.GetVersion()
			devices[r.index].DeviceType = r.resp.GetDeviceType()
			devices[r.index].OS = r.resp.GetOs()
			devices[r.index].OSVersion = r.resp.GetOsVersion()
			devices[r.index].CPUArchitecture = r.resp.GetCpuArchitecture()
		}
	}
	return devices
}

// resolveLANVersion queries a single LAN device's gRPC endpoint to populate
// version metadata. It also returns whether that connection used mTLS.
func resolveLANVersion(ctx context.Context, dev models.LANDevice) (models.LANDevice, bool, error) {
	_, isMTLS, resp, err := resolveLANAgentVersion(ctx, dev)
	if err != nil {
		return dev, false, err
	}
	dev.AgentVersion = resp.GetVersion()
	dev.DeviceType = resp.GetDeviceType()
	dev.OS = resp.GetOs()
	dev.OSVersion = resp.GetOsVersion()
	dev.CPUArchitecture = resp.GetCpuArchitecture()
	return dev, isMTLS, nil
}

// SelectedDevice represents either a gRPC agent, BLE device, or an external provider device.
type SelectedDevice struct {
	// Exactly one of Agent/Bluetooth/External is set.
	Agent     *grpcclient.AgentConnection
	Bluetooth *models.BluetoothDevice
	External  *models.ExternalDevice
	Provider  providers.DeviceProvider
}

// Close releases any resources held by this SelectedDevice.
func (s *SelectedDevice) Close() {
	if s.Agent != nil {
		s.Agent.Close()
	}
}

func resolveDeviceAddress() (addr string, isDefault bool, err error) {
	hostname := deviceFlag
	if hostname == "" {
		cfg, loadErr := config.Load()
		if loadErr != nil {
			return "", false, fmt.Errorf("loading config: %w", loadErr)
		}
		hostname = cfg.DefaultDevice
		isDefault = hostname != ""
	}
	if hostname == "" {
		return "", false, fmt.Errorf("no device specified; use --device flag or set a default with 'wendy device set-default'")
	}
	// If the hostname already contains a port, use it as-is.
	if _, _, splitErr := net.SplitHostPort(hostname); splitErr == nil {
		return hostname, isDefault, nil
	}
	return hostPort(hostname, defaultAgentPort), isDefault, nil
}

// recoveryChoice represents the user's selection in the default-device recovery menu.
type recoveryChoice int

const (
	recoveryDiscover     recoveryChoice = iota // run device discovery picker
	recoveryUnsetDefault                       // clear the default device
	recoveryExit                               // exit with the original error
)

// recoveryModel is a minimal Bubble Tea model for the default-device recovery menu.
type recoveryModel struct {
	choices  []string
	cursor   int
	chosen   int
	hostname string
	quit     bool
}

func (m recoveryModel) Init() tea.Cmd { return nil }

func (m recoveryModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.choices)-1 {
				m.cursor++
			}
		case "enter":
			m.chosen = m.cursor
			return m, tea.Quit
		case "q", "ctrl+c":
			m.chosen = len(m.choices) - 1 // treat as Exit
			m.quit = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m recoveryModel) View() string {
	if m.quit {
		return ""
	}

	warnStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214")) // amber
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	selectStyle := lipgloss.NewStyle().Bold(true).Foreground(tui.ColorPrimary)

	var sb strings.Builder
	sb.WriteString(warnStyle.Render(fmt.Sprintf("Attempting to reach default device %q but it is unavailable.", m.hostname)))
	sb.WriteString("\n\n")
	sb.WriteString(dimStyle.Render("Would you like to:"))
	sb.WriteString("\n")

	for i, choice := range m.choices {
		if i == m.cursor {
			sb.WriteString(selectStyle.Render("  > " + choice))
		} else {
			sb.WriteString(dimStyle.Render("    " + choice))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// promptDefaultDeviceRecovery shows an interactive menu when the saved default
// device is unreachable. It returns the user's chosen recovery action.
func promptDefaultDeviceRecovery(hostname string) recoveryChoice {
	m := recoveryModel{
		hostname: hostname,
		choices: []string{
			"Discover another device",
			"Unset the default device",
			"Exit",
		},
	}
	final, err := tea.NewProgram(m).Run()
	if err != nil {
		return recoveryExit
	}
	fm, ok := final.(recoveryModel)
	if !ok {
		return recoveryExit
	}
	return recoveryChoice(fm.chosen)
}

// isInteractiveTerminal returns true when both stdin and stdout are TTYs,
// meaning it is safe to show interactive Bubble Tea prompts.
func isInteractiveTerminal() bool {
	return isInteractiveTerminalFn()
}

// handleDefaultDeviceRecovery runs the recovery flow after a default device
// connection failure. Shows a warning and immediately opens the device picker
// where the user can select a new device and optionally set/unset default
// via 'd'/'x' shortcuts.
func handleDefaultDeviceRecovery(ctx context.Context, hostname string, elapsed time.Duration, _ error, excludeProviders map[string]bool, excludeBluetooth bool, suppressUpdateCheck bool) (*SelectedDevice, error) {
	warnStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	fmt.Println(warnStyle.Render(fmt.Sprintf("⚠ Default device %q is unreachable after %s.", hostname, formatElapsedSeconds(elapsed))))
	fmt.Println()

	return pickDevice(ctx, excludeProviders, excludeBluetooth, suppressUpdateCheck)
}

func defaultDeviceSearchLabel(hostname string) string {
	return fmt.Sprintf("Searching for default device %q...", hostname)
}

func formatElapsedSeconds(elapsed time.Duration) string {
	roundedElapsed := elapsed.Round(10 * time.Millisecond)
	seconds := roundedElapsed.Seconds()
	unit := "seconds"
	if roundedElapsed == time.Second {
		unit = "second"
	}
	return fmt.Sprintf("%.2f %s", seconds, unit)
}

// deferProvisionedMTLSCheck starts the "does this address advertise an
// mTLS-only agent?" mDNS browse concurrently with the connection attempt and
// returns a getter for its result. The browse (~0.5s) is only consulted when a
// plaintext probe FAILS — to tell an unprovisioned device apart from a
// provisioned one rejecting plaintext — so on the common success path the
// getter is never called and the browse stays off the critical path. Starting
// it now (rather than after the probe) keeps the observation tied to this
// connection attempt, matching the original eager-snapshot intent.
func deferProvisionedMTLSCheck(ctx context.Context, addr string) func() bool {
	ch := make(chan bool, 1)
	go func() { ch <- provisionedAgentAdvertisedMTLS(ctx, addr) }()
	var (
		once sync.Once
		res  bool
	)
	return func() bool {
		once.Do(func() { res = <-ch })
		return res
	}
}

func connectAgentAtAddress(ctx context.Context, addr string) (*grpcclient.AgentConnection, error) {
	return connectAgentAtAddressWithProvisionedHint(ctx, addr, func() bool { return false })
}

func connectAgentAtAddressWithProvisionedHint(ctx context.Context, addr string, provisionedMTLS func() bool) (*grpcclient.AgentConnection, error) {
	tm := phaseTimer()
	conn, mtlsErr, err := connectWithAutoTLSDiagnostics(ctx, addr)
	if err != nil {
		return nil, err
	}
	tm("  ↳ mTLS attempts (connectWithAutoTLSDiagnostics)")
	if !conn.IsMTLS {
		// gRPC plaintext connections are lazy. Probe before returning so
		// command UIs don't surface delayed transport errors, and so provisioned
		// agents that only expose the mTLS port can report an auth error.
		probeCtx, cancel := context.WithTimeout(ctx, agentPlaintextProbeTimeout)
		_, probeErr := conn.AgentService.GetAgentVersion(probeCtx, &agentpb.GetAgentVersionRequest{})
		cancel()
		tm("  ↳ plaintext probe (GetAgentVersion)")
		if probeErr != nil {
			conn.Close()
			// The provisionedMTLS observation was initiated at connection time
			// (concurrently with this attempt); consult it now to tell an
			// unprovisioned device apart from a provisioned one rejecting
			// plaintext, rather than launching a second, later browse.
			if provisionedMTLS() {
				return nil, newProvisionedAgentUnauthorizedError(mtlsErr)
			}
			return nil, probeErr
		}
	}
	return conn, nil
}

func connectResolvedAgent(ctx context.Context, hostname, addr string, isDefault bool) (*grpcclient.AgentConnection, error) {
	return connectResolvedAgentWithProvisionedHint(ctx, hostname, addr, isDefault, func() bool { return false })
}

func connectResolvedAgentWithProvisionedHint(ctx context.Context, hostname, addr string, isDefault bool, provisionedMTLS func() bool) (*grpcclient.AgentConnection, error) {
	if isDefault && !jsonOutput && isInteractiveTerminal() {
		return runAgentConnectionSpinner(ctx, defaultDeviceSearchLabel(hostname), func(spinCtx context.Context) (*grpcclient.AgentConnection, error) {
			return connectAgentAtAddressWithProvisionedHint(spinCtx, addr, provisionedMTLS)
		})
	}
	return connectAgentAtAddressWithProvisionedHint(ctx, addr, provisionedMTLS)
}

// connectToAgent establishes a gRPC connection to the target device.
// If the CLI has auth certs, it connects via mTLS on the secure port.
// Otherwise, it falls back to plaintext on the default port.
// If no device is specified via --device or config default, an interactive
// device picker is presented (unless running in --json mode).
func connectToAgent(ctx context.Context, opts ...resolveOption) (*grpcclient.AgentConnection, error) {
	cfg := resolveConfig{excludeProviderKeys: make(map[string]bool)}
	for _, o := range opts {
		o(&cfg)
	}

	if cloudCfg, ok := cloudDeviceConfigFromContext(ctx); ok {
		conn, err := connectToCloudAgent(ctx, cloudCfg.CloudGRPC, cloudCfg.DeviceName, cloudCfg.BrokerURL)
		if err != nil {
			return nil, err
		}
		if !cfg.suppressProvisioningHint {
			suggestProvisioning(conn)
		}
		return conn, nil
	}

	addr, isDefault, err := resolveDeviceAddress()
	if err == nil {
		startedAt := time.Now()
		hostname := addr
		if host, _, splitErr := net.SplitHostPort(addr); splitErr == nil {
			hostname = host
		}
		provisionedMTLS := deferProvisionedMTLSCheck(ctx, addr)
		conn, connErr := connectResolvedAgentWithProvisionedHint(ctx, hostname, addr, isDefault, provisionedMTLS)
		if connErr != nil {
			if errors.Is(connErr, ErrUserCancelled) {
				return nil, connErr
			}
			if syncedConn, ok := autoSyncTimeAndRetry(ctx, connErr, func() (*grpcclient.AgentConnection, error) {
				return connectResolvedAgentWithProvisionedHint(ctx, hostname, addr, isDefault, provisionedMTLS)
			}); ok {
				conn = syncedConn
			} else if errors.Is(connErr, errProvisionedAgentUnauthorized) {
				refreshedConn, ok := offerCertRefreshAndRetry(ctx, connErr, func() (*grpcclient.AgentConnection, error) {
					return connectResolvedAgentWithProvisionedHint(ctx, hostname, addr, isDefault, provisionedMTLS)
				})
				if !ok {
					return nil, connErr
				}
				conn = refreshedConn
			} else if isDefault && !jsonOutput && !cfg.nonInteractive && isInteractiveTerminal() {
				// Default device is unreachable — offer interactive recovery.
				hostname, _, _ := net.SplitHostPort(addr)
				target, recErr := handleDefaultDeviceRecovery(ctx, hostname, time.Since(startedAt), connErr, cfg.excludeProviderKeys, cfg.excludeBluetooth, cfg.suppressUpdateCheck)
				if recErr != nil {
					return nil, recErr
				}
				return connectFromSelectedDevice(target, cfg)
			} else if isDefault {
				return nil, defaultDeviceUnreachableError(hostname, connErr)
			} else {
				return nil, connErr
			}
		}
		if !cfg.suppressProvisioningHint {
			suggestProvisioning(conn)
		}
		if !cfg.suppressUpdateCheck {
			var updateErr error
			conn, updateErr = checkAndOfferUpdate(ctx, conn)
			if updateErr != nil {
				return nil, updateErr
			}
		}
		// WDY-1149: verify the resolved default device still belongs to the
		// organisation + cloud it was pinned to (and pin it on first use).
		if isDefault {
			if pinErr := enforceDevicePin(hostname, conn); pinErr != nil {
				conn.Close()
				return nil, pinErr
			}
		}
		return conn, nil
	}

	// No device configured — fall back to interactive picker.
	if jsonOutput {
		return nil, fmt.Errorf("no device specified; use --device flag or set a default with 'wendy device set-default'")
	}

	target, pickErr := pickDevice(ctx, cfg.excludeProviderKeys, cfg.excludeBluetooth, cfg.suppressUpdateCheck)
	if pickErr != nil {
		return nil, pickErr
	}

	return connectFromSelectedDevice(target, cfg)
}

// connectFromSelectedDevice converts a SelectedDevice from the picker into a
// gRPC AgentConnection. Returns an error if the selected device does not
// support gRPC.
func connectFromSelectedDevice(target *SelectedDevice, cfg resolveConfig) (*grpcclient.AgentConnection, error) {
	if target.Agent != nil {
		if !cfg.suppressProvisioningHint {
			suggestProvisioning(target.Agent)
		}
		return target.Agent, nil
	}

	// The user picked a Bluetooth device — connectToAgent only supports gRPC.
	// Callers that support BLE should use resolveTarget() instead.
	if target.Bluetooth != nil {
		return nil, fmt.Errorf("selected device (%s) is a Bluetooth device; this command requires a LAN connection. Use 'wendy device wifi connect' which supports BLE", target.Bluetooth.DisplayName)
	}

	// The user picked a non-gRPC device (e.g. external provider) which
	// doesn't support agent commands like wifi/apps/hardware.
	if target.External != nil {
		return nil, fmt.Errorf("selected device (%s) does not support this command; select a WendyOS LAN device instead", target.External.DisplayName)
	}

	return nil, fmt.Errorf("selected device does not support gRPC agent commands")
}

// connectWithAutoTLS tries to connect using mTLS if the CLI has auth certs,
// falling back to plaintext if no certs are available or all mTLS attempts fail.
// It tries each stored certificate in order so that both production and local
// pki-core certs are attempted.
//
// If every mTLS probe fails with a TLS handshake error, the plaintext fallback
// is skipped and the TLS error is returned with a diagnostic hint. This prevents
// the misleading "connection refused" from the plaintext port masking the real
// cause (e.g. clock skew causing "certificate not yet valid").
func connectWithAutoTLS(ctx context.Context, plaintextAddr string) (*grpcclient.AgentConnection, error) {
	conn, _, err := connectWithAutoTLSDiagnostics(ctx, plaintextAddr)
	return conn, err
}

// mdnsBrowseTimeout bounds the mDNS fallback browse so an offline default device
// does not stall a command for the full default discovery window.
const mdnsBrowseTimeout = 4 * time.Second

// mdnsBrowseTimeoutValue returns the mDNS fallback browse timeout, allowing
// WENDY_MDNS_TIMEOUT (a Go duration like "8s") to raise it for slow networks
// where the default window is too short to hear a response. Values outside
// [1s, 30s] are ignored in favour of the default.
func mdnsBrowseTimeoutValue() time.Duration {
	if v := os.Getenv("WENDY_MDNS_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= time.Second && d <= 30*time.Second {
			return d
		}
	}
	return mdnsBrowseTimeout
}

// osLookupHostFn resolves a hostname via the operating system resolver. It is a
// package variable so tests can simulate a resolver that cannot see mDNS names.
var osLookupHostFn = func(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

// lanBrowseFn browses the LAN for WendyOS devices via mDNS. It is a package
// variable so tests can substitute a fixture instead of a real network browse.
var lanBrowseFn = discovery.DiscoverLAN

// resolveHostMDNSFallback resolves a bare hostname to a single IP, preferring
// IPv4. It tries the OS resolver first, then falls back to an mDNS browse for
// ".local" names. The OS resolver (and thus gRPC's) can't see mDNS ".local"
// names on Windows or on Linux hosts without nss-mdns/avahi — and the shipped
// binaries are built CGO_ENABLED=0, so they use Go's pure resolver which
// ignores nss-mdns entirely. Only macOS resolves ".local" natively. The
// mDNS-browse fallback keeps ".local" names working on those platforms (issue
// #1155). A bare IP literal is returned unchanged; "" is returned when the
// name cannot be resolved and no advertised mDNS device matches.
func resolveHostMDNSFallback(ctx context.Context, host string) string {
	if net.ParseIP(host) != nil {
		return host
	}
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	ips, err := osLookupHostFn(rctx, host)
	cancel()
	if err == nil && len(ips) > 0 {
		for _, ip := range ips { // prefer IPv4
			if net.ParseIP(ip).To4() != nil {
				return ip
			}
		}
		return ips[0]
	}
	return resolveMDNSHost(ctx, host) // "" for non-".local" names or no match
}

// resolveAddrOnce resolves a host:port whose host is a DNS/mDNS name to an
// IPv4-preferred IP:port, so the dials below target a literal IP. gRPC
// otherwise resolves the name separately for every ClientConn we open (mTLS
// port, mTLS port+1, plaintext), and an mDNS ".local" name that resolves to
// both IPv6 and IPv4 can cost a multi-second IPv6 connect timeout per dial on
// networks without IPv6 routing. Preferring IPv4 and resolving once removes
// both costs. On any resolution failure it returns addr unchanged so gRPC's
// own resolver remains the fallback.
func resolveAddrOnce(ctx context.Context, addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) != nil {
		return addr // not host:port, or already a literal IP
	}
	if ip := resolveHostMDNSFallback(ctx, host); ip != "" {
		return net.JoinHostPort(ip, port)
	}
	return addr
}

// resolveMDNSHost browses the LAN via mDNS and returns the IP address advertised
// by a device whose hostname matches host. It is the fallback used when the OS
// resolver cannot resolve an mDNS ".local" name — mirroring the discover/picker
// path, which already prefers discovered IPs for the same reason. Returns "" for
// non-".local" hosts or when no advertised device matches.
func resolveMDNSHost(ctx context.Context, host string) string {
	normalized := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if !strings.HasSuffix(normalized, ".local") {
		return ""
	}
	timeout := mdnsBrowseTimeoutValue()
	bctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	devices, err := lanBrowseFn(bctx, timeout)
	if err != nil {
		return ""
	}
	want := normalizeMDNSHost(host)
	for _, dev := range devices {
		if dev.IPAddress == "" {
			continue
		}
		if normalizeMDNSHost(dev.Hostname) == want {
			return dev.IPAddress
		}
	}
	return ""
}

// normalizeMDNSHost lowercases a hostname and strips a trailing dot and ".local"
// suffix so "Wendy-Thor.local." and "wendy-thor" compare equal.
func normalizeMDNSHost(host string) string {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	return strings.TrimSuffix(h, ".local")
}

// mdnsLocalHint returns guidance for ".local" mDNS resolution failures. The
// shipped CLI is built CGO_ENABLED=0, so it can't see ".local" names via the OS
// resolver (nss-mdns) and relies on an mDNS browse (avahi/raw multicast)
// instead; that browse needs multicast on the path. Returns "" for
// non-".local" hosts.
func mdnsLocalHint(host string) string {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if !strings.HasSuffix(h, ".local") {
		return ""
	}
	return "\n  Resolving a .local name needs mDNS: ensure avahi-daemon is running and" +
		" UDP 5353 isn't firewalled (e.g. 'sudo ufw allow 5353/udp'), or connect by IP."
}

// defaultDeviceUnreachableError wraps a connection failure for a saved default
// device so the message makes clear the default IS persisted but could not be
// reached — rather than letting the failure read as if set-default never took
// effect (issue #1155).
func defaultDeviceUnreachableError(hostname string, err error) error {
	return fmt.Errorf("default device %q is set but could not be reached: %w\n"+
		"  Confirm it with 'wendy device get-default'; change it with 'wendy device set-default' or clear it with 'wendy device unset-default'.%s",
		hostname, err, mdnsLocalHint(hostname))
}

func connectWithAutoTLSDiagnostics(ctx context.Context, plaintextAddr string) (*grpcclient.AgentConnection, error, error) {
	plaintextAddr = resolveAddrOnce(ctx, plaintextAddr)
	tlsDebug := os.Getenv("WENDY_TLS_DEBUG") != ""
	allCerts := loadAllCLICerts()
	var lastMTLSErr error
	recordMTLSErr := func(addr string, err error) {
		if err != nil {
			lastMTLSErr = fmt.Errorf("%s: %w", addr, err)
		}
	}
	if len(allCerts) > 0 {
		pins := openPinStore()
		host, portStr, _ := net.SplitHostPort(plaintextAddr)
		if port, err := strconv.Atoi(portStr); err == nil {
			// Try the given port first (covers explicit tunnel ports that already
			// point at the mTLS port), then fall back to port+1 (the normal case
			// where discovery returns the plaintext port and mTLS is port+1).
			mtlsAddrs := []string{plaintextAddr, hostPort(host, port+1)}
			// Two-bucket tracking per address index:
			//
			//   plaintextAddrCertReject — plaintextAddr itself was a TLS endpoint that
			//   rejected our cert (tunnel/mTLS-only-discovery case where index 0 IS
			//   already the mTLS port). isCertRejectionError only fires on server-sent
			//   TLS alerts, not on "server sent non-TLS preface" errors from plaintext ports.
			//
			//   mtlsPortCertFails / mtlsPortNonCertFails — cert-rejection vs. other
			//   failures at port+1 (the dedicated mTLS port in the normal case).
			//
			// Suppress the plaintext fallback if plaintextAddr itself was rejected, OR if
			// all port+1 probe failures were cert rejections (none were just "unreachable").
			var plaintextAddrCertReject bool
			var mtlsPortCertFails, mtlsPortNonCertFails int
			for addrIdx, mtlsAddr := range mtlsAddrs {
				for i := range allCerts {
					conn, tlsErr := grpcclient.ConnectWithTLSAndPins(ctx, mtlsAddr, &allCerts[i], pins)
					if tlsErr != nil {
						recordMTLSErr(mtlsAddr, tlsErr)
						if tlsDebug {
							fmt.Fprintf(os.Stderr, "[tls-debug] ConnectWithTLS(%s) error: %v\n", mtlsAddr, tlsErr)
						}
						continue
					}
					// grpc.NewClient is lazy — verify the connection actually
					// works with a fast probe before committing to mTLS.
					// The address is already resolved to an IP by resolveAddrOnce,
					// so this only needs to cover TCP + the TLS handshake; the old
					// 8s budget (which also covered .local mDNS resolution) made an
					// unreachable mTLS port cost 8s before the plaintext fallback.
					probeCtx, cancel := context.WithTimeout(ctx, mtlsProbeTimeout)
					_, probeErr := conn.AgentService.GetAgentVersion(probeCtx, &agentpb.GetAgentVersionRequest{})
					cancel()
					if probeErr == nil {
						return conn, nil, nil
					}
					recordMTLSErr(mtlsAddr, probeErr)
					if tlsDebug {
						fmt.Fprintf(os.Stderr, "[tls-debug] GetAgentVersion(%s) error: %v\n", mtlsAddr, probeErr)
					}
					conn.Close()
					if addrIdx == 0 {
						if isCertRejectionError(probeErr) {
							plaintextAddrCertReject = true
						}
					} else {
						if isCertRejectionError(probeErr) {
							mtlsPortCertFails++
						} else {
							mtlsPortNonCertFails++
						}
					}
				}
			}
			if plaintextAddrCertReject || (mtlsPortCertFails > 0 && mtlsPortNonCertFails == 0) {
				return nil, lastMTLSErr, newTLSHandshakeRejectedError(lastMTLSErr)
			}
		}
	}
	conn, err := grpcclient.Connect(ctx, plaintextAddr)
	return conn, lastMTLSErr, err
}

// isCertRejectionError reports whether a gRPC probe error is a server-sent TLS
// alert rejecting the client certificate, as distinct from the client failing to
// complete the handshake because the server isn't a TLS endpoint at all.
// Matches "remote error: tls:" (server sent an alert) and other cert-specific
// signals; deliberately excludes "tls: first record does not look like a TLS
// handshake" (plaintext server probed with TLS) and plain transport errors.
func isCertRejectionError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// A plaintext (unprovisioned) agent probed with TLS reports "first record
	// does not look like a TLS handshake", which gRPC wraps inside its
	// "authentication handshake failed" envelope. That is NOT a cert rejection —
	// the server simply isn't a TLS endpoint — so it must not suppress the
	// plaintext fallback. Exclude it explicitly before the broad substring match
	// below would otherwise catch the "authentication handshake failed" wrapper.
	if strings.Contains(msg, "first record does not look like a TLS handshake") {
		return false
	}
	return strings.Contains(msg, "remote error: tls:") ||
		strings.Contains(msg, "authentication handshake failed") ||
		strings.Contains(msg, "certificate required")
}

// provisionedAgentAdvertisedMTLS takes a short pre-connection LAN discovery
// snapshot and checks whether the target address was already advertised as an
// mTLS-only agent. Callers pass this result into the dial path; it is not
// refreshed after a failed connection attempt.
func provisionedAgentAdvertisedMTLS(ctx context.Context, plaintextAddr string) bool {
	devices, err := discoverLANDevices(ctx, provisionedAgentMetadataDiscoveryTimeout)
	if err != nil {
		return false
	}
	return provisionedAgentAdvertisedMTLSInSnapshot(plaintextAddr, devices)
}

func provisionedAgentAdvertisedMTLSInSnapshot(plaintextAddr string, devices []models.LANDevice) bool {
	for _, dev := range devices {
		if !dev.IsMTLS {
			continue
		}
		for _, candidate := range lanAgentAddresses(dev) {
			if sameAgentAddress(plaintextAddr, candidate) {
				return true
			}
		}
	}
	return false
}

func sameAgentAddress(a, b string) bool {
	aHost, aPort, aErr := net.SplitHostPort(a)
	bHost, bPort, bErr := net.SplitHostPort(b)
	if aErr != nil || bErr != nil {
		return a == b
	}
	return aPort == bPort && normalizeAgentHost(aHost) == normalizeAgentHost(bHost)
}

func normalizeAgentHost(host string) string {
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.Unmap().String()
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

// suggestProvisioning prints a hint when the connection is not using mTLS,
// nudging the user to provision the device.
func suggestProvisioning(conn *grpcclient.AgentConnection) {
	if conn.IsMTLS || jsonOutput {
		return
	}
	fmt.Fprintf(os.Stderr, "Hint: connected without mTLS. Run 'wendy device setup' to provision this device.\n")
}

// updateCheckTTL bounds how often checkAndOfferUpdate probes the agent. Within
// this window of a prior "agent is current" result, the probe (a gRPC
// round-trip that otherwise sits on the deploy hot path) is skipped entirely.
const updateCheckTTL = 6 * time.Hour

// updateCheckMarkerPath returns the per-host marker file recording the last time
// the agent was confirmed current. The CLI version is part of the key so that
// upgrading the CLI forces a fresh check immediately.
func updateCheckMarkerPath(host string) string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	key := sha256.Sum256([]byte(host + "\x00" + version.Version))
	return filepath.Join(cacheDir, "wendy", "update-check", hex.EncodeToString(key[:])+".json")
}

// updateCheckRecentlyPassed reports whether the agent at host was confirmed
// current within updateCheckTTL, in which case the version probe can be skipped.
func updateCheckRecentlyPassed(host string) bool {
	path := updateCheckMarkerPath(host)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) < updateCheckTTL
}

// markUpdateCheckPassed records that the agent at host is current as of now.
func markUpdateCheckPassed(host string) {
	path := updateCheckMarkerPath(host)
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte("{}"), 0o644)
}

// checkAndOfferUpdate probes the agent version and, when the agent is behind
// the CLI, either warns (non-interactive) or prompts [Y/n] (interactive). If
// the user accepts, it downloads the latest release, uploads it, and waits for
// the agent to restart, returning a fresh connection. On decline, or if the
// upload fails, the original conn is returned unchanged. If the upload succeeds
// but the agent does not come back, conn is closed and an error is returned.
func checkAndOfferUpdate(ctx context.Context, conn *grpcclient.AgentConnection) (*grpcclient.AgentConnection, error) {
	if jsonOutput {
		return conn, nil
	}
	// Skip the probe when this agent was confirmed current within updateCheckTTL.
	// This keeps the gRPC round-trip off the deploy hot path on repeat runs.
	if updateCheckRecentlyPassed(conn.Host) {
		return conn, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	resp, err := conn.AgentService.GetAgentVersion(probeCtx, &agentpb.GetAgentVersionRequest{})
	cancel()
	if err != nil {
		return conn, nil
	}

	agentVer := resp.GetVersion()
	// Dev CLI builds skip the update check entirely.
	if version.IsDev(version.Version) {
		markUpdateCheckPassed(conn.Host)
		return conn, nil
	}
	// A dev agent build is running intentionally — never offer to replace it
	// with a stable release (dev is treated as the latest version).
	if version.IsDev(agentVer) {
		markUpdateCheckPassed(conn.Host)
		return conn, nil
	}
	// Unknown agent version — skip to avoid spurious update prompts.
	if agentVer == "" {
		markUpdateCheckPassed(conn.Host)
		return conn, nil
	}
	if version.CompareVersions(version.Version, agentVer) <= 0 {
		markUpdateCheckPassed(conn.Host)
		return conn, nil
	}

	if !isInteractiveTerminal() {
		fmt.Fprintf(os.Stderr, "Warning: agent is behind the CLI (agent: %s, CLI: %s). Run 'wendy device update' to update.\n", agentVer, version.Version)
		return conn, nil
	}
	warn := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))

	fmt.Fprintf(os.Stderr, warn.Render("Agent is behind the CLI (agent: %s, CLI: %s). Update now? [Y/n] "), agentVer, version.Version)
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "" && answer != "y" && answer != "yes" {
		return conn, nil
	}

	arch := resp.GetCpuArchitecture()
	addr := hostPort(conn.Host, defaultAgentPort)

	if err := performAgentUpdate(ctx, conn, arch, false); err != nil {
		fmt.Fprintf(os.Stderr, "Update failed: %v\nContinuing with existing connection.\n", err)
		return conn, nil
	}

	conn.Close()

	fmt.Fprintf(os.Stderr, "Waiting for agent to restart...")
	newConn, err := waitForAgentRestart(ctx, addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, " failed.\n")
		return nil, fmt.Errorf("agent did not come back after update: %w", err)
	}
	fmt.Fprintf(os.Stderr, " ready.\n")
	return newConn, nil
}

// performAgentUpdate downloads the latest release for the given arch and uploads
// it to conn. Pass nightly=true to fetch the latest prerelease instead of stable.
// The agent will restart after this returns successfully.
func performAgentUpdate(ctx context.Context, conn *grpcclient.AgentConnection, arch string, nightly bool) error {
	if arch == "" {
		return fmt.Errorf("device did not report CPU architecture")
	}
	fmt.Fprintf(os.Stderr, "Fetching latest release...\n")
	release, err := fetchAgentRelease(nightly)
	if err != nil {
		return fmt.Errorf("fetching release: %w", err)
	}

	assetPrefix := fmt.Sprintf("wendy-agent-linux-%s-", arch)
	var matchedAsset *githubReleaseAsset
	for _, a := range release.Assets {
		if strings.HasPrefix(a.Name, assetPrefix) && strings.HasSuffix(a.Name, ".tar.gz") {
			matchedAsset = &a
			break
		}
	}
	if matchedAsset == nil {
		return fmt.Errorf("no asset for linux/%s in release %s", arch, release.TagName)
	}

	fmt.Fprintf(os.Stderr, "Downloading %s...\n", matchedAsset.Name)
	binaryData, err := downloadAgentBinary(*matchedAsset)
	if err != nil {
		return fmt.Errorf("downloading binary: %w", err)
	}

	h := sha256.Sum256(binaryData)
	sha256Hash := hex.EncodeToString(h[:])

	fmt.Fprintf(os.Stderr, "Uploading to device...\n")
	return deviceUpdateUpload(ctx, conn.AgentService, binaryData, sha256Hash)
}

// waitForAgentRestart polls addr with connectWithAutoTLS until the agent answers
// GetAgentVersion or 60 s elapse. Returns a fresh connection on success.
func waitForAgentRestart(ctx context.Context, addr string) (*grpcclient.AgentConnection, error) {
	deadline := time.Now().Add(60 * time.Second)
	time.Sleep(time.Second) // give the agent a moment to begin shutdown
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		conn, err := connectWithAutoTLS(ctx, addr)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, probeErr := conn.AgentService.GetAgentVersion(probeCtx, &agentpb.GetAgentVersionRequest{})
		cancel()
		if probeErr == nil {
			return conn, nil
		}
		conn.Close()
		time.Sleep(time.Second)
	}
	return nil, fmt.Errorf("timed out waiting for agent to restart")
}

func loadCLICert() *config.CertificateInfo {
	auth := loadCLIAuth()
	if auth == nil {
		return nil
	}
	cert := auth.Certificates[0]
	return &cert
}

func loadAllCLICerts() []config.CertificateInfo {
	cfg, err := config.Load()
	if err != nil || len(cfg.Auth) == 0 {
		return nil
	}
	var out []config.CertificateInfo
	for _, auth := range cfg.Auth {
		if len(auth.Certificates) > 0 {
			out = append(out, auth.Certificates[0])
		}
	}
	return out
}

func loadCLIAuth() *config.AuthConfig {
	cfg, err := config.Load()
	if err != nil || len(cfg.Auth) == 0 {
		return nil
	}
	for _, auth := range cfg.Auth {
		if len(auth.Certificates) > 0 {
			return &auth
		}
	}
	return nil
}

// openPinStore loads the device pin store from the wendy config directory.
// Returns nil (without error) if the store cannot be opened, so callers can
// treat nil PinChecker as "pinning disabled" without failing the connection.
func openPinStore() certs.PinChecker {
	dir, err := config.ConfigDir()
	if err != nil {
		return nil
	}
	store, err := devicepin.Open(dir)
	if err != nil {
		return nil
	}
	return store
}

// findCertByOrgID returns the first CertificateInfo across all auth entries
// whose OrganizationID matches orgID, or nil if none is found.
func findCertByOrgID(authEntries []config.AuthConfig, orgID int) *config.CertificateInfo {
	for i := range authEntries {
		for j := range authEntries[i].Certificates {
			if authEntries[i].Certificates[j].OrganizationID == orgID {
				return &authEntries[i].Certificates[j]
			}
		}
	}
	return nil
}

// attemptBLEConnect builds a TLS config and connects to device using the
// given certificate info and pin store.
func attemptBLEConnect(device *models.BluetoothDevice, cert config.CertificateInfo, pins certs.PinChecker) (*ble.AgentClient, error) {
	tlsCfg, err := ble.NewClientTLSConfig(cert.PemCertificate, cert.PemPrivateKey, certs.ServerVerifyOpts{
		ChainPEM:      cert.PemCertificateChain,
		ExpectedOrgID: int32(cert.OrganizationID),
		PinStore:      pins,
	})
	if err != nil {
		return nil, fmt.Errorf("building BLE TLS config: %w", err)
	}
	return ble.ConnectAgent(device, tlsCfg)
}

// connectBLEAgent connects to device via BLE mTLS, automatically retrying
// with the matching cert if the device belongs to a different org than the
// default auth session.
func connectBLEAgent(device *models.BluetoothDevice) (*ble.AgentClient, error) {
	auth := loadCLIAuth()
	if auth == nil || len(auth.Certificates) == 0 {
		return nil, fmt.Errorf("not logged in; run 'wendy auth login' to authenticate")
	}
	pins := openPinStore()
	cert := auth.Certificates[0]

	// Best-effort time sync before mTLS handshake — gives the device a chance
	// to advance its clock before we attempt the TLS handshake.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	clitimesync.BroadcastTime(ctx) //nolint:errcheck
	cancel()

	client, err := attemptBLEConnect(device, cert, pins)
	if err == nil {
		return client, nil
	}

	var mismatch *certs.OrgMismatchError
	if !errors.As(err, &mismatch) {
		return nil, err
	}

	// The device belongs to a different org. Search all auth entries.
	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		return nil, fmt.Errorf("device belongs to org %d but could not load config to find matching certificate: %w", mismatch.Got, cfgErr)
	}
	alt := findCertByOrgID(cfg.Auth, int(mismatch.Got))
	if alt == nil {
		return nil, fmt.Errorf("device belongs to org %d; authenticate for that org with 'wendy auth login'", mismatch.Got)
	}
	return attemptBLEConnect(device, *alt, pins)
}

// resolveOption configures resolveTarget behaviour.
type resolveOption func(*resolveConfig)

type resolveConfig struct {
	excludeProviderKeys      map[string]bool
	excludeBluetooth         bool
	suppressProvisioningHint bool
	suppressUpdateCheck      bool
	nonInteractive           bool
}

// SuppressUpdateCheck prevents connectToAgent from running the automatic
// agent-version check. Use this for commands that manage updates explicitly
// (e.g. "wendy device update") to avoid a double-prompt.
func SuppressUpdateCheck() resolveOption {
	return func(c *resolveConfig) {
		c.suppressUpdateCheck = true
	}
}

// SuppressProvisioningHint prevents connectToAgent from printing the
// "run 'wendy device setup'" hint when connected without mTLS.
func SuppressProvisioningHint() resolveOption {
	return func(c *resolveConfig) {
		c.suppressProvisioningHint = true
	}
}

// NonInteractive prevents resolveTarget from opening an interactive device
// picker. When no device is specified in non-interactive mode, a clear error
// is returned instead of attempting to open a TTY.
func NonInteractive() resolveOption {
	return func(c *resolveConfig) {
		c.nonInteractive = true
	}
}

// ExcludeBluetooth skips the BLE scan and filters out BLE-only devices
// (those with no LAN or external endpoint) from the interactive device picker.
func ExcludeBluetooth() resolveOption {
	return func(c *resolveConfig) {
		c.excludeBluetooth = true
	}
}

// ExcludeProviders prevents the named provider keys from appearing in the
// interactive device picker.
func ExcludeProviders(keys ...string) resolveOption {
	return func(c *resolveConfig) {
		for _, k := range keys {
			c.excludeProviderKeys[k] = true
		}
	}
}

// resolveTarget resolves the target device and, for agent connections,
// best-effort corrects a lagging device clock before the caller operates on it
// (issue #1171). This is the single funnel every command uses to obtain a
// device, so the clock fix applies to info, run, deploy, ros2 bag, etc.
func resolveTarget(ctx context.Context, opts ...resolveOption) (*SelectedDevice, error) {
	sel, err := resolveTargetInner(ctx, opts...)
	if err != nil {
		return nil, err
	}
	if sel != nil && sel.Agent != nil {
		maybeFixClock(ctx, sel.Agent)
	}
	return sel, nil
}

// resolveTargetInner inspects the --device flag and returns either an external
// provider device or falls back to the gRPC agent connection. If no device
// is specified and no default is configured, an interactive device picker
// is presented.
func resolveTargetInner(ctx context.Context, opts ...resolveOption) (*SelectedDevice, error) {
	cfg := resolveConfig{excludeProviderKeys: make(map[string]bool)}
	for _, o := range opts {
		o(&cfg)
	}

	if cloudCfg, ok := cloudDeviceConfigFromContext(ctx); ok {
		conn, err := connectToCloudAgent(ctx, cloudCfg.CloudGRPC, cloudCfg.DeviceName, cloudCfg.BrokerURL)
		if err != nil {
			return nil, err
		}
		return &SelectedDevice{Agent: conn}, nil
	}

	device := deviceFlag
	isDefault := false
	if device == "" {
		loadedCfg, err := config.Load()
		if err != nil {
			return nil, fmt.Errorf("loading config: %w", err)
		}
		device = loadedCfg.DefaultDevice
		isDefault = device != ""
	}

	rt := phaseTimer()

	// Check if the device flag matches a known provider key.
	if device != "" {
		if p := providers.ProviderForKey(device); p != nil {
			devices, err := p.DiscoverDevices(ctx)
			if err != nil {
				return nil, fmt.Errorf("discovering %s devices: %w", p.DisplayName(), err)
			}
			if len(devices) == 0 {
				return nil, fmt.Errorf("no %s devices found", p.DisplayName())
			}
			return &SelectedDevice{
				External: &devices[0],
				Provider: p,
			}, nil
		}
	}

	// Check if the device flag matches a discovered device ID (e.g.
	// "adb:emulator-5554"). Skip this for anything that looks like a network
	// address — a ".local" mDNS name, hostname, or IP all contain a "." (or
	// "[" for IPv6) — because provider IDs are short dotless tokens and the
	// discovery loop here spins up every provider (e.g. the adb server), costing
	// seconds. A WendyOS agent address falls through to the gRPC connect below.
	if device != "" && !strings.Contains(device, ".") && !strings.HasPrefix(device, "[") {
		if sel := findDeviceByID(ctx, device); sel != nil {
			return sel, nil
		}
	}
	rt("  ↳ findDeviceByID (provider discovery)")

	// If a device hostname was given, connect via gRPC (with mTLS if authenticated).
	if device != "" {
		addr := device
		if _, _, splitErr := net.SplitHostPort(device); splitErr != nil {
			addr = hostPort(device, defaultAgentPort)
		}
		startedAt := time.Now()
		provisionedMTLS := deferProvisionedMTLSCheck(ctx, addr)
		conn, err := connectResolvedAgentWithProvisionedHint(ctx, device, addr, isDefault, provisionedMTLS)
		rt("  ↳ connectResolvedAgent (dial+probe)")
		if err != nil {
			if errors.Is(err, ErrUserCancelled) {
				return nil, err
			}
			if syncedConn, ok := autoSyncTimeAndRetry(ctx, err, func() (*grpcclient.AgentConnection, error) {
				return connectResolvedAgentWithProvisionedHint(ctx, device, addr, isDefault, provisionedMTLS)
			}); ok {
				conn = syncedConn
			} else if errors.Is(err, errProvisionedAgentUnauthorized) {
				refreshedConn, ok := offerCertRefreshAndRetry(ctx, err, func() (*grpcclient.AgentConnection, error) {
					return connectResolvedAgentWithProvisionedHint(ctx, device, addr, isDefault, provisionedMTLS)
				})
				if !ok {
					return nil, err
				}
				conn = refreshedConn
			} else if isDefault && !jsonOutput && !cfg.nonInteractive && isInteractiveTerminal() {
				// Default device is unreachable — offer interactive recovery.
				return handleDefaultDeviceRecovery(ctx, device, time.Since(startedAt), err, cfg.excludeProviderKeys, cfg.excludeBluetooth, cfg.suppressUpdateCheck)
			} else if isDefault {
				return nil, defaultDeviceUnreachableError(device, err)
			} else {
				return nil, err
			}
		}
		if !cfg.suppressUpdateCheck {
			var updateErr error
			conn, updateErr = checkAndOfferUpdate(ctx, conn)
			if updateErr != nil {
				return nil, updateErr
			}
		}
		rt("  ↳ checkAndOfferUpdate")
		return &SelectedDevice{Agent: conn}, nil
	}

	// No device specified — run interactive picker if we have a TTY.
	if jsonOutput || cfg.nonInteractive {
		return nil, fmt.Errorf("no device specified; use --device flag or set a default with 'wendy device set-default'")
	}

	return pickDevice(ctx, cfg.excludeProviderKeys, cfg.excludeBluetooth, cfg.suppressUpdateCheck)
}

// findDeviceByID searches all available providers for a device whose ID
// matches the given string (e.g. "adb:emulator-5554").
func findDeviceByID(ctx context.Context, id string) *SelectedDevice {
	for _, p := range providers.AvailableProviders() {
		devices, err := p.DiscoverDevices(ctx)
		if err != nil {
			continue
		}
		for _, d := range devices {
			if d.ID == id {
				d := d // copy for stable pointer
				return &SelectedDevice{
					External: &d,
					Provider: p,
				}
			}
		}
	}
	return nil
}

// ensureAppConfig loads wendy.json from cfgPath. If the file does not exist
// and stdin is a TTY (or autoAccept is true), a default config is created automatically.
func ensureAppConfig(cfgPath string, autoAccept bool) (*appconfig.AppConfig, error) {
	cfg, err := appconfig.LoadFromFile(cfgPath)
	if err == nil {
		return cfg, nil
	}

	// If the error is anything other than "file not found", return it as-is
	// (e.g. a JSON parse error).
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	dir := filepath.Dir(cfgPath)
	dirName := filepath.Base(dir)

	if !autoAccept {
		// File doesn't exist. If we're not in an interactive terminal, give a
		// helpful error message.
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return nil, fmt.Errorf("wendy.json not found; run 'wendy init <app-id>' to create one")
		}

		fmt.Println("No wendy.json found in current directory.")
		fmt.Printf("Create one with app ID %q? [Y/n] ", dirName)

		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))

		if answer != "" && answer != "y" && answer != "yes" {
			return nil, fmt.Errorf("wendy.json is required; run 'wendy init <app-id>' to create one")
		}
	}

	// Detect language from the project files on disk.
	language := ""
	projectType, _ := detectProjectType(dir) // ignore multiple-xcodeproj error for config init
	switch projectType {
	case "python":
		language = "python"
	case "swift":
		language = "swift"
	case "xcode":
		language = "swift"
	}

	entitlements := defaultEntitlements(language, "")

	newCfg := &appconfig.AppConfig{
		AppID:        dirName,
		Version:      "0.1.0",
		Language:     language,
		Entitlements: entitlements,
	}

	data, marshalErr := json.MarshalIndent(newCfg, "", "  ")
	if marshalErr != nil {
		return nil, fmt.Errorf("marshaling config: %w", marshalErr)
	}

	if writeErr := os.WriteFile(cfgPath, data, 0o644); writeErr != nil {
		return nil, fmt.Errorf("writing wendy.json: %w", writeErr)
	}

	fmt.Printf("Created wendy.json for %s\n", dirName)
	return newCfg, nil
}

// pickerItemDeviceID extracts a hostname or provider key from a picker item,
// suitable for storing as the default device (must be resolvable by resolveDeviceAddress).
func pickerItemDeviceID(item tui.PickerItem) string {
	entry, ok := item.Value.(*pickerEntry)
	if !ok {
		return ""
	}
	// For LAN devices, use the mDNS hostname (matches what pickDeviceForDefault returns).
	if entry.mergedDevice != nil && entry.mergedDevice.LAN != nil {
		addr := entry.mergedDevice.LAN.Hostname
		if addr == "" {
			addr = entry.mergedDevice.LAN.IPAddress
		}
		return addr
	}
	if entry.externalDevice != nil {
		return entry.externalDevice.ProviderKey
	}
	if entry.mergedDevice != nil && entry.mergedDevice.External != nil {
		return entry.mergedDevice.External.ProviderKey
	}
	return ""
}

// pickerEntry is the value stored in each PickerItem.
type pickerEntry struct {
	mergedDevice   *models.DiscoveredDevice
	externalDevice *models.ExternalDevice
	provider       providers.DeviceProvider
}

// mergePickerItem merges a newly discovered transport into an existing picker
// item for the same physical device. It combines connection types, prefers
// LAN addresses, and merges the underlying DiscoveredDevice fields.
// nextProbeState resolves the probe state for a merged picker row. A succeeded
// probe (ProbeOK) is sticky: it survives a later transient failure and is never
// reset to the spinner when the device is rediscovered. A failed probe stays
// failed until a retry succeeds, and is not flipped back to the spinner on
// rediscovery. ProbeNone (non-LAN transports) never overrides a real state.
func nextProbeState(existing, incoming tui.ProbeState) tui.ProbeState {
	switch incoming {
	case tui.ProbeOK:
		return tui.ProbeOK
	case tui.ProbeFailed:
		if existing == tui.ProbeOK {
			return tui.ProbeOK
		}
		return tui.ProbeFailed
	case tui.ProbePending:
		if existing == tui.ProbeNone {
			return tui.ProbePending
		}
		return existing
	default:
		return existing
	}
}

func mergePickerItem(existing *tui.PickerItem, incoming tui.PickerItem) {
	e, eOK := existing.Value.(*pickerEntry)
	n, nOK := incoming.Value.(*pickerEntry)
	if !eOK || !nOK || e.mergedDevice == nil || n.mergedDevice == nil {
		return
	}

	md := e.mergedDevice
	nd := n.mergedDevice

	if nd.LAN != nil && md.LAN == nil {
		md.LAN = nd.LAN
		existing.Address = incoming.Address
	}
	if nd.LAN != nil && md.LAN != nil && nd.LAN.USB != "" && md.LAN.USB == "" {
		md.LAN.USB = nd.LAN.USB
		md.LAN.NetworkInterface = nd.LAN.NetworkInterface
	}
	if nd.LAN != nil && nd.LAN.USB != "" && existing.USB == "" {
		existing.USB = nd.LAN.USB
		key := existing.DedupKey
		if key == "" {
			key = existing.Name
		}
		existing.SortKey = usbFirstSortKey(key, nd.LAN.USB)
	}
	if nd.Bluetooth != nil && md.Bluetooth == nil {
		md.Bluetooth = nd.Bluetooth
	}
	if nd.External != nil && md.External == nil {
		md.External = nd.External
		if md.LAN == nil {
			existing.Address = incoming.Address
		}
	}

	if md.AgentVersion == "" {
		md.AgentVersion = nd.AgentVersion
		existing.AgentVersion = incoming.AgentVersion
	}
	if md.OS == "" {
		md.OS = nd.OS
	}
	if md.OSVersion == "" {
		md.OSVersion = nd.OSVersion
		existing.OSVersion = incoming.OSVersion
	}
	if md.CPUArchitecture == "" {
		md.CPUArchitecture = nd.CPUArchitecture
	}

	if existing.AgentVersion == "" {
		existing.AgentVersion = incoming.AgentVersion
	}
	if existing.OSVersion == "" {
		existing.OSVersion = incoming.OSVersion
	}

	if existing.Name == "" {
		existing.Name = incoming.Name
	}

	// Rebuild the type string from the merged transports.
	existing.Type = md.ConnectionTypes()

	// Propagate security status: LAN probes determine mTLS, BLE doesn't. Once
	// we know a device is insecure (or secure), update the existing item.
	// The same goes for the provisioned state and the no-access hint, which
	// clears once a probe succeeds.
	if nd.LAN != nil {
		existing.Insecure = incoming.Insecure
		existing.Provisioned = incoming.Provisioned
		existing.Hint = incoming.Hint
	}
	// The no-access hint must stay consistent with the version cell no matter
	// which transport supplied the version: AgentVersion is carried over from
	// earlier LAN probes or backfilled from BLE above, and a hint claiming
	// agent details are unreadable must not accompany a displayed version.
	if existing.AgentVersion != "" && existing.Hint == discoverNoAccessHint {
		existing.Hint = ""
	}

	existing.Probe = nextProbeState(existing.Probe, incoming.Probe)
}

func usbFirstSortKey(name, usb string) string {
	if usb == "" {
		return ""
	}
	return "0_" + strings.ToLower(name)
}

// hideLocalProviders returns a copy of excludes that additionally hides the
// local run targets (this machine, Docker/OrbStack, Apple Container) unless the
// user opts them back in via providers.ShowLocalDevices. The input map is left
// untouched (and may be nil); the picker lists separate WendyOS devices by
// default so local runtimes don't crowd out real hardware.
func hideLocalProviders(excludes map[string]bool) map[string]bool {
	merged := make(map[string]bool, len(excludes)+len(providers.LocalProviderKeys()))
	for k, v := range excludes {
		merged[k] = v
	}
	if !providers.ShowLocalDevices() {
		for _, k := range providers.LocalProviderKeys() {
			merged[k] = true
		}
	}
	return merged
}

// pickDevice runs an interactive TUI that discovers devices across all
// transports and providers, then lets the user select one.
// LAN discovery runs continuously so devices that come online after the
// initial scan still appear in the picker.
// excludeProviders hides the named provider keys from the picker. Local run
// targets are hidden on top of these unless providers.ShowLocalDevices is set.
func pickDevice(ctx context.Context, excludeProviders map[string]bool, excludeBluetooth bool, suppressUpdateCheck bool) (*SelectedDevice, error) {
	excludeProviders = hideLocalProviders(excludeProviders)

	picker := tui.NewPicker()
	picker.MergeItem = mergePickerItem

	// Load current default device to show ✦ indicator.
	if loadedCfg, err := config.Load(); err == nil && loadedCfg.DefaultDevice != "" {
		picker.DefaultKey = strings.ToLower(loadedCfg.DefaultDevice)
	}

	// Allow 'd' to set default and 'x' to unset default from the picker.
	picker.OnSetDefault = func(item tui.PickerItem) string {
		deviceID := pickerItemDeviceID(item)
		if deviceID == "" {
			return ""
		}
		if cfg, err := config.Load(); err == nil {
			cfg.DefaultDevice = deviceID
			_ = config.Save(cfg)
		}
		return fmt.Sprintf("Default device set to %s.", item.Name)
	}
	picker.OnUnsetDefault = func() string {
		if cfg, err := config.Load(); err == nil {
			cfg.DefaultDevice = ""
			_ = config.Save(cfg)
		}
		return "Default device cleared."
	}

	p := tea.NewProgram(picker)

	// Cancel continuous discovery when the picker exits.
	discoverCtx, discoverCancel := context.WithCancel(ctx)

	// Continuous LAN discovery — devices appear as they're found.
	lanCh := make(chan models.LANDevice, 16)
	go discovery.DiscoverLANContinuous(discoverCtx, lanCh)
	sendLANItem := func(dev models.LANDevice, insecure bool, probe tui.ProbeState) {
		devCopy := dev
		// While the probe is still in flight the Agent/OS columns show a
		// spinner, so suppress the no-access hint until we actually know the
		// probe failed.
		hint := ""
		if probe != tui.ProbePending {
			hint = lanNoAccessHint(&devCopy, dev.AgentVersion)
		}
		p.Send(tui.PickerAddMsg{Items: []tui.PickerItem{{
			Name:         dev.DisplayName,
			Type:         "LAN",
			USB:          dev.USB,
			Address:      preferredLANAddress(dev),
			AgentVersion: dev.AgentVersion,
			OS:           dev.OS,
			OSVersion:    dev.OSVersion,
			Provisioned:  lanProvisionedDisplay(&devCopy),
			Hint:         hint,
			Probe:        probe,
			DedupKey:     dev.DisplayName,
			SortKey:      usbFirstSortKey(dev.DisplayName, dev.USB),
			Insecure:     insecure,
			Value: &pickerEntry{mergedDevice: &models.DiscoveredDevice{
				DisplayName:     dev.DisplayName,
				AgentVersion:    dev.AgentVersion,
				OS:              dev.OS,
				OSVersion:       dev.OSVersion,
				CPUArchitecture: dev.CPUArchitecture,
				LAN:             &devCopy,
			}},
		}}})
	}
	go func() {
		for rawDev := range lanCh {
			// Show the device immediately with a "connecting" spinner, then
			// resolve its version/OS and update the row in place.
			sendLANItem(rawDev, false, tui.ProbePending)
			resolved, isMTLS, err := resolveLANVersion(discoverCtx, rawDev)
			if err == nil {
				sendLANItem(resolved, !isMTLS, tui.ProbeOK)
				continue
			}
			// Version probe failed on first attempt: mark the row failed (red
			// triangle) and retry in the background so the version appears once
			// the device becomes responsive, without requiring rediscovery.
			sendLANItem(rawDev, false, tui.ProbeFailed)
			go func(d models.LANDevice) {
				for attempt := 0; attempt < 5; attempt++ {
					select {
					case <-discoverCtx.Done():
						return
					case <-time.After(2 * time.Second):
					}
					if updated, isMTLS, retryErr := resolveLANVersion(discoverCtx, d); retryErr == nil {
						sendLANItem(updated, !isMTLS, tui.ProbeOK)
						return
					}
				}
			}(rawDev)
		}
	}()

	// Continuous provider discovery — re-scan every 3 seconds.
	for _, prov := range providers.AvailableProviders() {
		if excludeProviders[prov.Key()] {
			continue
		}
		prov := prov
		go func() {
			for {
				devices, err := prov.DiscoverDevices(discoverCtx)
				if err == nil && len(devices) > 0 {
					var items []tui.PickerItem
					for i := range devices {
						if prov.Key() == "wendy-lite" {
							items = append(items, tui.PickerItem{
								Name:         devices[i].DisplayName,
								DedupKey:     devices[i].DisplayName,
								Type:         "LAN (Lite)",
								Address:      devices[i].ConnectionInfo["ip"],
								AgentVersion: devices[i].AgentVersion,
								OS:           devices[i].OS,
								OSVersion:    devices[i].OSVersion,
								Value: &pickerEntry{mergedDevice: &models.DiscoveredDevice{
									DisplayName:     devices[i].DisplayName,
									AgentVersion:    devices[i].AgentVersion,
									OSVersion:       devices[i].OSVersion,
									CPUArchitecture: devices[i].CPUArchitecture,
									External:        &devices[i],
								}},
							})
						} else {
							items = append(items, tui.PickerItem{
								Name:         devices[i].DisplayName,
								Type:         prov.DisplayName(),
								Address:      externalProviderAddress(devices[i].ProviderKey, devices[i].ID),
								AgentVersion: devices[i].AgentVersion,
								OS:           devices[i].OS,
								OSVersion:    devices[i].OSVersion,
								DedupKey:     devices[i].DisplayName,
								SortKey:      externalProviderSortKey(prov.Key(), devices[i].DisplayName),
								Hint:         externalProviderPickerHint(prov.Key()),
								Value:        &pickerEntry{externalDevice: &devices[i], provider: prov},
							})
						}
					}
					if len(items) > 0 {
						p.Send(tui.PickerAddMsg{Items: items})
					}
				}

				select {
				case <-discoverCtx.Done():
					return
				case <-time.After(3 * time.Second):
				}
			}
		}()
	}

	// Continuous Bluetooth discovery — re-scan every 5 seconds.
	if !excludeBluetooth {
		go func() {
			for {
				bleDevices, err := discovery.DiscoverBluetooth(discoverCtx, true)
				if err == nil && len(bleDevices) > 0 {
					var items []tui.PickerItem
					for i := range bleDevices {
						connType := "BLE"
						if !bleDevices[i].IsWendyAgent() {
							connType = "BLE (Lite)"
						}
						items = append(items, tui.PickerItem{
							Name:         bleDevices[i].DisplayName,
							DedupKey:     bleDevices[i].DisplayName,
							Type:         connType,
							Address:      bleDevices[i].Address,
							AgentVersion: bleDevices[i].AgentVersion,
							OS:           bleDevices[i].OS,
							OSVersion:    bleDevices[i].OSVersion,
							Value: &pickerEntry{mergedDevice: &models.DiscoveredDevice{
								DisplayName:     bleDevices[i].DisplayName,
								AgentVersion:    bleDevices[i].AgentVersion,
								OS:              bleDevices[i].OS,
								OSVersion:       bleDevices[i].OSVersion,
								CPUArchitecture: bleDevices[i].CPUArchitecture,
								Bluetooth:       &bleDevices[i],
							}},
						})
					}
					p.Send(tui.PickerAddMsg{Items: items})
				}

				select {
				case <-discoverCtx.Done():
					return
				case <-time.After(5 * time.Second):
				}
			}
		}()
	}

	finalModel, err := p.Run()
	discoverCancel() // stop all background discovery
	if err != nil {
		return nil, fmt.Errorf("device picker: %w", err)
	}

	pm := finalModel.(tui.PickerModel)
	if pm.Cancelled() {
		return nil, ErrUserCancelled
	}
	sel := pm.Selected()
	if sel == nil {
		return nil, fmt.Errorf("no device selected")
	}

	entry, ok := sel.Value.(*pickerEntry)
	if !ok {
		return nil, fmt.Errorf("invalid picker selection")
	}

	// Merged LAN/Bluetooth/External device — prefer LAN (gRPC), fall back to BLE/External.
	if entry.mergedDevice != nil {
		d := entry.mergedDevice
		if d.LAN != nil {
			addr, _, _, err := resolveLANAgentVersion(ctx, *d.LAN)
			if err != nil {
				// LAN metadata lookups can fail on provisioned devices without CLI certs.
				// In that case, still try the preferred address once before falling back.
				addr = preferredLANAddress(*d.LAN)
			}
			if addr == "" {
				if d.Bluetooth != nil {
					return &SelectedDevice{Bluetooth: d.Bluetooth}, nil
				}
				if err != nil {
					return nil, err
				}
				return nil, fmt.Errorf("selected LAN device has no usable address")
			}
			mtls := d.LAN.IsMTLS
			conn, err := connectAgentAtAddressWithProvisionedHint(ctx, addr, func() bool { return mtls })
			if err == nil {
				if !suppressUpdateCheck {
					var updateErr error
					conn, updateErr = checkAndOfferUpdate(ctx, conn)
					if updateErr != nil {
						return nil, updateErr
					}
				}
				return &SelectedDevice{Agent: conn}, nil
			}
			// LAN failed — fall back to BLE if available.
			if d.Bluetooth != nil {
				return &SelectedDevice{Bluetooth: d.Bluetooth}, nil
			}
			return nil, err
		}

		// Wendy Lite device — set both BLE and External+Provider when
		// available so callers can pick the right transport.
		sel := &SelectedDevice{}
		if d.Bluetooth != nil {
			sel.Bluetooth = d.Bluetooth
		}
		if d.External != nil {
			sel.External = d.External
			sel.Provider = providers.ProviderForKey(d.External.ProviderKey)
		}
		if sel.Bluetooth != nil || sel.External != nil {
			return sel, nil
		}
	}

	// External provider device.
	if entry.externalDevice != nil && entry.provider != nil {
		return &SelectedDevice{
			External: entry.externalDevice,
			Provider: entry.provider,
		}, nil
	}

	return nil, fmt.Errorf("selected device type is not yet supported")
}

// resolveAgentPlatform determines the target platform string from the user's
// wendy.json platform field, the agent's OS, and the agent's CPU architecture.
//
// Rules:
//   - If cfgPlatform is a full "os/arch" string, use it as-is.
//   - If cfgPlatform is OS-only (e.g., "linux" or "darwin"), append the agent arch.
//   - If cfgPlatform is empty, default to Linux with the agent architecture.
//   - "wendyos" is a compatibility alias for "linux" and is normalized before
//     passing the platform to container builders.
func resolveAgentPlatform(cfgPlatform, agentOS, agentArch string) string {
	if cfgPlatform == "" {
		return appconfig.PlatformLinux + "/" + agentArch
	}
	if i := strings.IndexByte(cfgPlatform, '/'); i >= 0 {
		return normalizePlatformOS(cfgPlatform[:i]) + cfgPlatform[i:]
	}
	// OS-only: append agent architecture.
	return normalizePlatformOS(cfgPlatform) + "/" + agentArch
}

func normalizePlatformOS(os string) string {
	if strings.EqualFold(os, appconfig.PlatformWendyOS) {
		return appconfig.PlatformLinux
	}
	return os
}

func registryPort(agentOS string) int {
	if agentOS == "darwin" {
		return 5555
	}
	return 5000
}

// platformOS extracts the OS component from a platform string like "linux/arm64".
func platformOS(platform string) string {
	if i := strings.IndexByte(platform, '/'); i >= 0 {
		return platform[:i]
	}
	return platform
}
