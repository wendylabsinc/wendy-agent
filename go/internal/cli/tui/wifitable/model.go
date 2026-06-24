package wifitable

import (
	"fmt"
	"strings"
	"time"

	bubbleTable "github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc/status"
)

// mode tracks which sub-view the model is showing.
type mode int

const (
	modeBrowsing mode = iota
	modeFiltering
	modeRanking
	modeUnlisted
	modePassword
)

// Action is the intent the user picked. When a Handler is attached the model
// dispatches Actions as async tea.Cmds and stays open; with no Handler set it
// falls back to recording the Action on Result and quitting (used by tests).
type Action int

const (
	ActionNone Action = iota
	ActionQuit
	ActionConnect
	ActionReorder
	ActionForget
	ActionConnectUnlisted
)

// Result is what the caller reads after the TUI exits. Only populated for the
// no-Handler code path.
type Result struct {
	Action         Action
	SSID           string
	Password       string
	Security       agentpb.WiFiSecurityType
	Hidden         bool
	Order          []string // for ActionReorder
	PromptPassword bool     // the caller should prompt out-of-band
}

// Handler performs WiFi operations on behalf of the Model so the TUI can stay
// open between actions (mirroring the `wendy discover` screen). Each method
// returns a tea.Cmd that must eventually emit an OpResultMsg.
type Handler interface {
	Connect(ssid, password string, security agentpb.WiFiSecurityType, hidden bool) tea.Cmd
	Forget(ssid string) tea.Cmd
	Reorder(order []string) tea.Cmd
	Refresh() tea.Cmd
}

// RefreshMsg replaces the visible networks. The caller fires one of these on a
// timer — or as the result of a Handler.Refresh() call — to keep the list
// fresh while the user browses.
type RefreshMsg struct {
	Networks []Network
}

// OpResultMsg is sent by Handler commands to report the outcome of an async
// operation. The Model uses it to render a flash message and refresh the list.
type OpResultMsg struct {
	Action Action
	SSID   string
	Count  int // e.g. number of networks reordered
	Err    error
}

// flashClearMsg clears the current flash message after a delay.
type flashClearMsg struct{}

// refreshTickMsg drives the periodic background refresh that keeps the table
// populating while the device-side scan fills its cache.
type refreshTickMsg struct{}

const (
	flashDuration = 4 * time.Second
	// refreshInterval is how often the model re-polls the device. The first
	// device-side rescan blocks for several seconds, but repeated list calls
	// return the partially-filled scan cache immediately — so polling makes
	// SSIDs stream into the table instead of arriving all at once.
	refreshInterval = 2500 * time.Millisecond
)

func refreshTick() tea.Cmd {
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg { return refreshTickMsg{} })
}

// Model is the Bubble Tea model for the interactive WiFi table.
type Model struct {
	networks []Network
	table    tui.BubbleTable
	mode     mode

	handler Handler

	// ranking state
	origOrder []string

	// unlisted-network modal state
	ssidInput     textinput.Model
	passwordInput textinput.Model
	secIndex      int
	modalFocus    int // 0=ssid, 1=password, 2=security

	// per-row password prompt
	pwFor string

	// find-as-you-type filter over SSIDs (modeFiltering)
	filterInput textinput.Model

	flashMessage string
	flashIsError bool
	busy         bool // true while an op is in-flight
	// scanning is true until the first RefreshMsg arrives, so an empty table
	// reads as "scan in progress" rather than "no networks".
	scanning bool
	// refreshInFlight guards against stacking refresh RPCs when ticks fire
	// faster than the device answers (BLE transports can't multiplex).
	refreshInFlight bool
	// stickyConnectedSSID is the SSID of the most recent successful connect.
	// It survives RefreshMsgs that haven't yet reflected the new state
	// (nmcli rescan can lag behind association by a few seconds). Cleared
	// once a RefreshMsg confirms the SSID as known+connected, or on a
	// subsequent Forget/Connect.
	stickyConnectedSSID string
	result              Result
	done                bool
	width               int
	height              int
}

var securityOptions = []agentpb.WiFiSecurityType{
	agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_OPEN,
	agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_WEP,
	agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_WPA_PSK,
	agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_WPA2_PSK,
	agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_WPA3_SAE,
	agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_WPA2_ENTERPRISE,
}

// NewModel constructs the initial model with the given network list.
func NewModel(networks []Network) Model {
	Sort(networks)

	ti := textinput.New()
	ti.Placeholder = "Network name"
	ti.CharLimit = 64
	ti.Width = 32

	pw := textinput.New()
	pw.Placeholder = "Password"
	pw.EchoMode = textinput.EchoPassword
	pw.EchoCharacter = '•'
	pw.CharLimit = 128
	pw.Width = 32

	fi := textinput.New()
	fi.Prompt = "Filter: "
	fi.CharLimit = 64
	fi.Width = 32

	m := Model{
		networks:      networks,
		table:         tui.NewBubbleTable(true, wifiColumns()),
		mode:          modeBrowsing,
		ssidInput:     ti,
		passwordInput: pw,
		filterInput:   fi,
		secIndex:      3, // WPA2-PSK default
		scanning:      len(networks) == 0,
	}
	m.refreshRows()
	return m
}

// WithHandler attaches a Handler, enabling inline async execution of actions
// so the TUI stays open between edits.
func (m Model) WithHandler(h Handler) Model {
	m.handler = h
	// Init dispatches the first Refresh; mark it in flight so the first tick
	// doesn't stack a second one on top.
	m.refreshInFlight = true
	return m
}

// Init kicks off the first refresh and the polling loop. The caller starts
// the TUI without waiting for an initial list, so the table appears
// immediately and rows stream in as the device scan progresses.
func (m Model) Init() tea.Cmd {
	if m.handler == nil {
		return nil
	}
	return tea.Batch(m.handler.Refresh(), refreshTick())
}

func wifiColumns() []bubbleTable.Column {
	return []bubbleTable.Column{
		{Title: "SSID", Width: 28},
		{Title: "Known", Width: 6},
		{Title: "Status", Width: 10},
		{Title: "Security", Width: 10},
		{Title: "Signal", Width: 8},
	}
}

// filteredNetworks returns the networks whose SSID contains the filter query
// (case-insensitive). With no active query it returns the full list, so table
// cursor indexes always address this slice in every mode.
func (m Model) filteredNetworks() []Network {
	query := strings.ToLower(m.filterInput.Value())
	if query == "" {
		return m.networks
	}
	out := make([]Network, 0, len(m.networks))
	for _, n := range m.networks {
		if strings.Contains(strings.ToLower(n.SSID), query) {
			out = append(out, n)
		}
	}
	return out
}

func (m *Model) refreshRows() {
	visible := m.filteredNetworks()
	rows := make([]bubbleTable.Row, 0, len(visible))
	for _, n := range visible {
		known := ""
		if n.Known {
			known = "★"
		}
		status := ""
		if n.Connected {
			status = "Connected"
		}
		signal := ""
		if n.Signal > 0 {
			signal = fmt.Sprintf("%d%%", n.Signal)
		}
		rows = append(rows, bubbleTable.Row{n.SSID, known, status, SecurityLabel(n.Security), signal})
	}
	m.table.SetRows(rows)
	if cur := m.table.Cursor(); cur >= len(rows) && len(rows) > 0 {
		m.table.SetCursor(len(rows) - 1)
	}
	h := len(rows) + 2
	if h < 6 {
		h = 6
	}
	if m.height > 0 && h > m.height-6 {
		h = m.height - 6
	}
	m.table.SetHeight(h)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		m.refreshRows()
		return m, cmd

	case RefreshMsg:
		m.refreshInFlight = false
		m.scanning = false
		if m.mode == modeBrowsing || m.mode == modeFiltering {
			m.networks = m.reconcileRefresh(msg.Networks)
			Sort(m.networks)
			m.refreshRows()
		}
		return m, nil

	case refreshTickMsg:
		cmds := []tea.Cmd{refreshTick()}
		// Re-poll only while the user is looking at the list; skip when an
		// op or refresh is already talking to the device.
		if m.handler != nil && !m.refreshInFlight && !m.busy &&
			(m.mode == modeBrowsing || m.mode == modeFiltering) {
			m.refreshInFlight = true
			cmds = append(cmds, m.handler.Refresh())
		}
		return m, tea.Batch(cmds...)

	case OpResultMsg:
		m.busy = false
		if msg.Action == ActionNone {
			// A refresh (not a user op) failed; the next tick retries.
			m.refreshInFlight = false
		}
		m.flashMessage, m.flashIsError = flashFor(msg)
		if msg.Err != nil {
			return m, clearFlashAfter(flashDuration)
		}
		// Apply an optimistic update so the table reflects the change
		// immediately — the async Refresh that follows will reconcile with
		// the authoritative state from the device.
		m.applyOptimisticUpdate(msg)
		m.refreshRows()
		if m.handler != nil {
			m.refreshInFlight = true
			return m, tea.Batch(m.handler.Refresh(), clearFlashAfter(flashDuration))
		}
		return m, clearFlashAfter(flashDuration)

	case flashClearMsg:
		m.flashMessage = ""
		m.flashIsError = false
		return m, nil
	}

	switch m.mode {
	case modeBrowsing:
		return m.updateBrowsing(msg)
	case modeFiltering:
		return m.updateFiltering(msg)
	case modeRanking:
		return m.updateRanking(msg)
	case modeUnlisted:
		return m.updateUnlisted(msg)
	case modePassword:
		return m.updatePassword(msg)
	}
	return m, nil
}

func (m Model) updateBrowsing(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		return m, cmd
	}
	switch km.String() {
	case "q", "ctrl+c", "esc":
		m.result.Action = ActionQuit
		m.done = true
		return m, tea.Quit
	case "enter":
		if m.busy {
			return m, nil
		}
		idx := m.table.Cursor()
		if idx < 0 || idx >= len(m.networks) {
			return m, nil
		}
		return m.connectTo(m.networks[idx])
	case "/":
		return m.startFiltering("")
	case "r":
		if m.busy {
			return m, nil
		}
		if !m.hasKnown() {
			m.flashMessage = "No known networks to rank."
			m.flashIsError = true
			return m, clearFlashAfter(flashDuration)
		}
		idx := m.table.Cursor()
		if idx < 0 || idx >= len(m.networks) || !m.networks[idx].Known {
			m.flashMessage = "Move the cursor to a known (★) network to start ranking."
			m.flashIsError = true
			return m, clearFlashAfter(flashDuration)
		}
		m.origOrder = snapshotSSIDs(m.networks)
		m.mode = modeRanking
		m.flashMessage = ""
		m.flashIsError = false
		return m, nil
	case "n":
		if m.busy {
			return m, nil
		}
		m.mode = modeUnlisted
		m.modalFocus = 0
		m.ssidInput.SetValue("")
		m.passwordInput.SetValue("")
		m.ssidInput.Focus()
		m.passwordInput.Blur()
		return m, textinput.Blink
	case "f":
		if m.busy {
			return m, nil
		}
		idx := m.table.Cursor()
		if idx < 0 || idx >= len(m.networks) {
			return m, nil
		}
		if !m.networks[idx].Known {
			m.flashMessage = "Only known networks can be forgotten."
			m.flashIsError = true
			return m, clearFlashAfter(flashDuration)
		}
		return m.dispatchForget(m.networks[idx].SSID)
	}

	// Any other printable key starts find-as-you-type filtering, seeded with
	// that key — except j/k/h/l, which stay table navigation (their letters
	// can still be matched after entering filter mode via another key or /).
	if km.Type == tea.KeyRunes || km.Type == tea.KeySpace {
		switch km.String() {
		case "j", "k", "h", "l":
		default:
			return m.startFiltering(string(km.Runes))
		}
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

// connectTo runs the shared connect flow for a row picked in browse or filter
// mode: known profiles and explicitly-open networks connect directly,
// everything else prompts for a password. Treating UNSPECIFIED as open would
// be unsafe — many drivers omit the security field in scan output — so the
// prompt appears for ambiguous networks too (it accepts an empty password for
// networks that turn out to be open).
func (m Model) connectTo(n Network) (tea.Model, tea.Cmd) {
	if n.Known {
		// nmcli will reuse the saved profile (and its stored password).
		return m.dispatchConnect(n.SSID, "", n.Security, false)
	}
	if n.Security == agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_OPEN {
		return m.dispatchConnect(n.SSID, "", n.Security, false)
	}
	m.pwFor = n.SSID
	m.passwordInput.SetValue("")
	m.passwordInput.Focus()
	m.mode = modePassword
	return m, textinput.Blink
}

func (m Model) startFiltering(seed string) (tea.Model, tea.Cmd) {
	m.mode = modeFiltering
	m.filterInput.SetValue(seed)
	m.filterInput.CursorEnd()
	m.filterInput.Focus()
	m.table.SetCursor(0)
	m.refreshRows()
	return m, textinput.Blink
}

// stopFiltering clears the query and returns to browse mode, keeping the
// cursor on the network that was highlighted in the filtered view.
func (m Model) stopFiltering() (tea.Model, tea.Cmd) {
	var selectedSSID string
	if visible := m.filteredNetworks(); len(visible) > 0 {
		if idx := m.table.Cursor(); idx >= 0 && idx < len(visible) {
			selectedSSID = visible[idx].SSID
		}
	}
	m.filterInput.SetValue("")
	m.filterInput.Blur()
	m.mode = modeBrowsing
	m.refreshRows()
	for i, n := range m.networks {
		if n.SSID == selectedSSID {
			m.table.SetCursor(i)
			break
		}
	}
	return m, nil
}

func (m Model) updateFiltering(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		m.filterInput, cmd = m.filterInput.Update(msg)
		return m, cmd
	}
	switch km.String() {
	case "ctrl+c":
		m.result.Action = ActionQuit
		m.done = true
		return m, tea.Quit
	case "esc":
		return m.stopFiltering()
	case "up", "down":
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		return m, cmd
	case "enter":
		if m.busy {
			return m, nil
		}
		visible := m.filteredNetworks()
		idx := m.table.Cursor()
		if idx < 0 || idx >= len(visible) {
			return m, nil
		}
		n := visible[idx]
		// Leave filter mode before dispatching so the table under the
		// password prompt (and after the op) shows the full list again.
		next, _ := m.stopFiltering()
		return next.(Model).connectTo(n)
	case "backspace":
		if m.filterInput.Value() == "" {
			return m.stopFiltering()
		}
	}

	before := m.filterInput.Value()
	var cmd tea.Cmd
	m.filterInput, cmd = m.filterInput.Update(msg)
	if m.filterInput.Value() != before {
		m.table.SetCursor(0)
		m.refreshRows()
	}
	return m, cmd
}

func (m Model) updateRanking(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch km.String() {
	case "esc":
		// Restore order.
		m.networks = restoreOrder(m.networks, m.origOrder)
		m.mode = modeBrowsing
		m.flashMessage = "Cancelled rank edit."
		m.flashIsError = false
		m.refreshRows()
		return m, clearFlashAfter(flashDuration)
	case "enter":
		order := KnownSSIDsInOrder(m.networks)
		return m.dispatchReorder(order)
	case "up", "k":
		idx := m.table.Cursor()
		newIdx := MoveUp(m.networks, idx)
		if newIdx == idx {
			m.flashMessage = "Already at the top of the ranking."
			m.flashIsError = false
			return m, clearFlashAfter(flashDuration)
		}
		m.table.SetCursor(newIdx)
		m.refreshRows()
		m.table.SetCursor(newIdx)
		m.flashMessage = ""
		m.flashIsError = false
		return m, nil
	case "down", "j":
		idx := m.table.Cursor()
		newIdx := MoveDown(m.networks, idx)
		if newIdx == idx {
			m.flashMessage = "Already at the bottom of the ranking."
			m.flashIsError = false
			return m, clearFlashAfter(flashDuration)
		}
		m.table.SetCursor(newIdx)
		m.refreshRows()
		m.table.SetCursor(newIdx)
		m.flashMessage = ""
		m.flashIsError = false
		return m, nil
	}
	return m, nil
}

func (m Model) updateUnlisted(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch km.String() {
	case "esc":
		m.mode = modeBrowsing
		return m, nil
	case "tab":
		m.modalFocus = (m.modalFocus + 1) % 3
		m.syncModalFocus()
		return m, nil
	case "shift+tab":
		m.modalFocus = (m.modalFocus + 2) % 3
		m.syncModalFocus()
		return m, nil
	case "left":
		if m.modalFocus == 2 {
			m.secIndex = (m.secIndex - 1 + len(securityOptions)) % len(securityOptions)
		}
	case "right":
		if m.modalFocus == 2 {
			m.secIndex = (m.secIndex + 1) % len(securityOptions)
		}
	case "enter":
		ssid := strings.TrimSpace(m.ssidInput.Value())
		if ssid == "" {
			m.flashMessage = "Network name is required."
			m.flashIsError = true
			return m, clearFlashAfter(flashDuration)
		}
		return m.dispatchConnectUnlisted(ssid, m.passwordInput.Value(), securityOptions[m.secIndex])
	}

	var cmd tea.Cmd
	switch m.modalFocus {
	case 0:
		m.ssidInput, cmd = m.ssidInput.Update(msg)
	case 1:
		m.passwordInput, cmd = m.passwordInput.Update(msg)
	}
	return m, cmd
}

func (m *Model) syncModalFocus() {
	if m.modalFocus == 0 {
		m.ssidInput.Focus()
		m.passwordInput.Blur()
	} else if m.modalFocus == 1 {
		m.ssidInput.Blur()
		m.passwordInput.Focus()
	} else {
		m.ssidInput.Blur()
		m.passwordInput.Blur()
	}
}

func (m Model) updatePassword(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		m.passwordInput, cmd = m.passwordInput.Update(msg)
		return m, cmd
	}
	switch km.String() {
	case "esc":
		m.mode = modeBrowsing
		return m, nil
	case "enter":
		ssid := m.pwFor
		password := m.passwordInput.Value()
		return m.dispatchConnect(ssid, password, agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_UNSPECIFIED, false)
	}
	var cmd tea.Cmd
	m.passwordInput, cmd = m.passwordInput.Update(msg)
	return m, cmd
}

// dispatchConnect either fires the Handler (staying open) or records the
// action on Result and quits (legacy test path).
func (m Model) dispatchConnect(ssid, password string, sec agentpb.WiFiSecurityType, hidden bool) (tea.Model, tea.Cmd) {
	if m.handler == nil {
		m.result.Action = ActionConnect
		m.result.SSID = ssid
		m.result.Password = password
		m.result.Security = sec
		m.result.Hidden = hidden
		m.done = true
		return m, tea.Quit
	}
	m.mode = modeBrowsing
	m.busy = true
	m.flashMessage = "Connecting to " + ssid + "..."
	m.flashIsError = false
	return m, m.handler.Connect(ssid, password, sec, hidden)
}

func (m Model) dispatchConnectUnlisted(ssid, password string, sec agentpb.WiFiSecurityType) (tea.Model, tea.Cmd) {
	if m.handler == nil {
		m.result.Action = ActionConnectUnlisted
		m.result.SSID = ssid
		m.result.Password = password
		m.result.Security = sec
		m.result.Hidden = true
		m.done = true
		return m, tea.Quit
	}
	m.mode = modeBrowsing
	m.busy = true
	m.flashMessage = "Connecting to " + ssid + "..."
	m.flashIsError = false
	return m, m.handler.Connect(ssid, password, sec, true)
}

func (m Model) dispatchForget(ssid string) (tea.Model, tea.Cmd) {
	if m.handler == nil {
		m.result.Action = ActionForget
		m.result.SSID = ssid
		m.done = true
		return m, tea.Quit
	}
	m.busy = true
	m.flashMessage = "Forgetting " + ssid + "..."
	m.flashIsError = false
	return m, m.handler.Forget(ssid)
}

func (m Model) dispatchReorder(order []string) (tea.Model, tea.Cmd) {
	if m.handler == nil {
		m.result.Action = ActionReorder
		m.result.Order = order
		m.done = true
		return m, tea.Quit
	}
	m.mode = modeBrowsing
	m.busy = true
	m.flashMessage = "Updating ranking..."
	m.flashIsError = false
	return m, m.handler.Reorder(order)
}

// applyOptimisticUpdate mutates the local network list to reflect a completed
// operation, so the table updates immediately without waiting for the async
// refresh (which rescans WiFi on the device and can take several seconds).
func (m *Model) applyOptimisticUpdate(msg OpResultMsg) {
	switch msg.Action {
	case ActionConnect, ActionConnectUnlisted:
		m.stickyConnectedSSID = msg.SSID
		found := false
		for i := range m.networks {
			if m.networks[i].SSID == msg.SSID {
				m.networks[i].Known = true
				m.networks[i].Connected = true
				found = true
			} else {
				m.networks[i].Connected = false
			}
		}
		if !found {
			// Unlisted/hidden network — add a row so it shows up until the
			// refresh fills in the scan details.
			m.networks = append(m.networks, Network{
				SSID:      msg.SSID,
				Known:     true,
				Connected: true,
			})
		}
		Sort(m.networks)

	case ActionForget:
		if m.stickyConnectedSSID == msg.SSID {
			m.stickyConnectedSSID = ""
		}
		out := make([]Network, 0, len(m.networks))
		for _, n := range m.networks {
			if n.SSID == msg.SSID {
				// If not currently visible in the scan, drop the row; it was
				// only shown because it was a saved profile.
				if n.Signal == 0 {
					continue
				}
				n.Known = false
				n.Priority = 0
				n.Connected = false
			}
			out = append(out, n)
		}
		m.networks = out
		Sort(m.networks)
	}
}

// reconcileRefresh merges an authoritative RefreshMsg with the Model's sticky
// state. nmcli can briefly return a scan that's missing IsConnected/IsKnown
// for the network we just connected to (rescan lags activation). When that
// happens we keep the optimistic flags so the user doesn't see the row flap
// back to unknown/disconnected. The sticky SSID is cleared once the refresh
// confirms it as known+connected.
func (m *Model) reconcileRefresh(incoming []Network) []Network {
	if m.stickyConnectedSSID == "" {
		return incoming
	}
	confirmed := false
	foundRow := false
	for i := range incoming {
		if incoming[i].SSID == m.stickyConnectedSSID {
			foundRow = true
			if incoming[i].Known && incoming[i].Connected {
				confirmed = true
			} else {
				incoming[i].Known = true
				incoming[i].Connected = true
			}
		} else if incoming[i].Connected {
			// Only one network is connected at a time; the refresh may
			// legitimately disagree, but trust our recent successful connect
			// over the scan.
			incoming[i].Connected = false
		}
	}
	if !foundRow {
		// Sticky SSID dropped out of the scan — keep a placeholder row.
		incoming = append(incoming, Network{
			SSID:      m.stickyConnectedSSID,
			Known:     true,
			Connected: true,
		})
	}
	if confirmed {
		m.stickyConnectedSSID = ""
	}
	return incoming
}

// flashFor renders a user-facing message for a completed operation.
func flashFor(msg OpResultMsg) (string, bool) {
	if msg.Err != nil {
		reason := userFacingError(msg.Err)
		switch msg.Action {
		case ActionConnect, ActionConnectUnlisted:
			return fmt.Sprintf("Connect to %s failed: %s", msg.SSID, reason), true
		case ActionForget:
			return fmt.Sprintf("Forget %s failed: %s", msg.SSID, reason), true
		case ActionReorder:
			return fmt.Sprintf("Reorder failed: %s", reason), true
		default:
			return reason, true
		}
	}
	switch msg.Action {
	case ActionConnect, ActionConnectUnlisted:
		return "Connected to " + msg.SSID + ".", false
	case ActionForget:
		return "Forgot " + msg.SSID + ".", false
	case ActionReorder:
		return fmt.Sprintf("Updated priority for %d known networks.", msg.Count), false
	}
	return "", false
}

// userFacingError renders a remote error for display, preferring the gRPC
// status description so internal "rpc error: code = ..." framing is not shown
// in the table status line. Non-gRPC errors fall back to their plain text.
func userFacingError(err error) string {
	if err == nil {
		return ""
	}
	if _, ok := status.FromError(err); ok {
		if desc, descOK := grpcDescFromErrorString(err.Error()); descOK {
			return desc
		}
	}
	return err.Error()
}

func grpcDescFromErrorString(msg string) (string, bool) {
	idx := strings.Index(msg, "desc = ")
	if idx < 0 {
		return "", false
	}
	desc := strings.TrimSpace(msg[idx+len("desc = "):])
	return desc, desc != ""
}

func clearFlashAfter(d time.Duration) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(d)
		return flashClearMsg{}
	}
}

func (m Model) hasKnown() bool {
	for _, n := range m.networks {
		if n.Known {
			return true
		}
	}
	return false
}

// snapshotSSIDs captures the ordering of known SSIDs before a rank edit starts.
func snapshotSSIDs(networks []Network) []string {
	out := make([]string, 0, len(networks))
	for _, n := range networks {
		if n.Known {
			out = append(out, n.SSID)
		}
	}
	return out
}

// restoreOrder rearranges the slice so the known networks appear in origOrder,
// leaving unknown networks untouched.
func restoreOrder(networks []Network, origOrder []string) []Network {
	known := make(map[string]Network, len(origOrder))
	var unknown []Network
	for _, n := range networks {
		if n.Known {
			known[n.SSID] = n
		} else {
			unknown = append(unknown, n)
		}
	}
	out := make([]Network, 0, len(networks))
	for _, ssid := range origOrder {
		if n, ok := known[ssid]; ok {
			out = append(out, n)
		}
	}
	out = append(out, unknown...)
	return out
}

var (
	footerStyle     = lipgloss.NewStyle().Foreground(tui.ColorDim)
	scanningStyle   = lipgloss.NewStyle().Foreground(tui.ColorPrimary)
	titleStyle      = lipgloss.NewStyle().Bold(true).Foreground(tui.ColorPrimary)
	modalBorder     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(tui.ColorBorder).Padding(0, 1)
	modalSelected   = lipgloss.NewStyle().Bold(true).Foreground(tui.ColorPrimary)
	flashStyle      = lipgloss.NewStyle().Foreground(tui.ColorNotice)
	flashErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)

func (m Model) View() string {
	if m.done {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(m.viewLine(titleStyle.Render("WiFi networks")) + "\n\n")
	if m.mode == modeFiltering {
		count := footerStyle.Render(fmt.Sprintf("  %d/%d", len(m.filteredNetworks()), len(m.networks)))
		sb.WriteString(m.viewLine(m.filterInput.View()+count) + "\n\n")
	}
	sb.WriteString(m.tableView())
	sb.WriteString("\n")

	if len(m.networks) == 0 {
		if m.scanning {
			sb.WriteString(m.viewLine(scanningStyle.Render("  Scanning for nearby networks...")) + "\n")
		} else {
			sb.WriteString(m.viewLine(footerStyle.Render("  No networks found.")) + "\n")
		}
	} else if m.mode == modeFiltering && len(m.filteredNetworks()) == 0 {
		sb.WriteString(m.viewLine(footerStyle.Render("  No SSIDs match the filter.")) + "\n")
	}

	if m.flashMessage != "" {
		style := flashStyle
		if m.flashIsError {
			style = flashErrorStyle
		}
		sb.WriteString(m.viewLine(style.Render(m.flashMessage)) + "\n")
	}

	switch m.mode {
	case modeBrowsing:
		hint := "↑/↓ move · enter connect · / filter · r rank · n new · f forget · q quit"
		if m.table.CanScroll() {
			hint = "↑/↓ move · ←/→ scroll · enter connect · / filter · r rank · n new · f forget · q quit"
		}
		sb.WriteString(m.viewLine(footerStyle.Render(hint)) + "\n")
	case modeFiltering:
		sb.WriteString(m.viewLine(footerStyle.Render("type to filter · ↑/↓ move · enter connect · esc cancel")) + "\n")
	case modeRanking:
		sb.WriteString(m.viewLine(footerStyle.Render("rank mode: ↑/↓ reorder · enter commit · esc cancel")) + "\n")
	case modePassword:
		sb.WriteString(m.viewLine(titleStyle.Render("Password for "+m.pwFor)) + "\n")
		sb.WriteString(m.viewLine(m.passwordInput.View()) + "\n")
		sb.WriteString(m.viewLine(footerStyle.Render("enter connect · esc cancel")) + "\n")
	case modeUnlisted:
		sb.WriteString(m.renderUnlistedModal() + "\n")
	}
	return sb.String()
}

func (m Model) viewLine(line string) string {
	if m.width <= 0 {
		return line
	}
	return tui.CropANSIView(line, 0, m.width)
}

// tableView renders the table, wrapping filter matches in the SSID column
// with the shared highlight style. Highlighting happens on the uncropped view
// so match positions line up, then the horizontal crop is reapplied.
func (m Model) tableView() string {
	query := m.filterInput.Value()
	if m.mode != modeFiltering || query == "" {
		return m.table.View()
	}
	ssidWidth := wifiColumns()[0].Width
	view := tui.HighlightMatches(m.table.FullView(), query, 1, 1+ssidWidth)
	if w := m.table.ViewportWidth(); w > 0 {
		view = tui.CropANSIView(view, m.table.ScrollOffset(), w)
	}
	return view
}

func (m Model) renderUnlistedModal() string {
	ssidLabel := "SSID"
	pwLabel := "Password"
	secLabel := "Security"
	if m.modalFocus == 0 {
		ssidLabel = modalSelected.Render("SSID")
	}
	if m.modalFocus == 1 {
		pwLabel = modalSelected.Render("Password")
	}
	if m.modalFocus == 2 {
		secLabel = modalSelected.Render("Security")
	}

	secValue := SecurityLabel(securityOptions[m.secIndex])
	if m.modalFocus == 2 {
		secValue = "← " + secValue + " →"
	}

	body := fmt.Sprintf("%s\n%s\n\n%s\n%s\n\n%s: %s",
		ssidLabel, m.ssidInput.View(),
		pwLabel, m.passwordInput.View(),
		secLabel, secValue,
	)

	return modalBorder.Render("Unlisted network\n\n"+body) + "\n" +
		footerStyle.Render("tab switch fields · ←/→ change security · enter submit · esc cancel")
}

func (m Model) Result() Result { return m.result }
