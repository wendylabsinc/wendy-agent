package tui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	bubbleTable "github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// newProbeSpinner builds the spinner that animates the Agent/OS columns while a
// device probe is in flight. Its Style is intentionally empty so View() returns
// a bare frame rune: the frame is written into plain table cells, which the
// bubbles table truncates without ANSI awareness, so it must carry no escapes.
func newProbeSpinner() spinner.Model {
	return spinner.New(spinner.WithSpinner(spinner.Dot))
}

// ProbeState describes whether an agent version/OS probe for a device row is in
// flight, succeeded, or failed. The zero value (ProbeNone) renders the version
// columns exactly as before, so non-probing pickers are unaffected.
type ProbeState int

const (
	ProbeNone    ProbeState = iota // no probe semantics: show version text as-is
	ProbePending                   // probe in flight: show an animated spinner
	ProbeOK                        // probe succeeded: show the version
	ProbeFailed                    // probe failed: show the error glyph
)

// ProbeFailedGlyph marks a row whose agent probe failed and for which no version
// is cached. It is rendered red by the view layer (see ColorizeProbeGlyphs); the
// cell value itself is kept as a plain glyph so the table's non-ANSI-aware
// truncation stays correct.
const ProbeFailedGlyph = "▲"

// probeFailedColored wraps the failure glyph in a red foreground that resets
// only the foreground (\x1b[39m), not the whole SGR state. A full reset would
// clear a selected row's background for the rest of the line; resetting just the
// foreground keeps the highlight intact. 196 matches the red used elsewhere in
// the device tables.
const probeFailedColored = "\x1b[38;5;196m" + ProbeFailedGlyph + "\x1b[39m"

// ColorizeProbeGlyphs colors the (plain-text) failure glyph red in an
// already-rendered table view. The glyph is kept plain inside table cells so the
// table's non-ANSI-aware truncation stays correct; color is applied here, after
// layout, so it never affects column widths.
func ColorizeProbeGlyphs(view string) string {
	return strings.ReplaceAll(view, ProbeFailedGlyph, probeFailedColored)
}

// probeColumnValue renders an Agent/OS cell for the given probe state. While
// pending it shows the current spinner frame; on failure with no cached version
// it shows the error glyph; otherwise it shows the version text (a cached
// version is preserved even after a later transient failure).
func probeColumnValue(state ProbeState, version, frame string) string {
	switch state {
	case ProbePending:
		return frame
	case ProbeFailed:
		if version == "" {
			return ProbeFailedGlyph
		}
	}
	return version
}

// formatOSNameVersion joins an OS/distro name and its version into a single
// display string (e.g. "ubuntu" + "24.04" -> "ubuntu 24.04"). Either part may
// be empty; the result never has leading or trailing space.
func formatOSNameVersion(os, version string) string {
	return strings.TrimSpace(strings.TrimSpace(os) + " " + strings.TrimSpace(version))
}

// PickerItem represents a selectable row in the device picker.
type PickerItem struct {
	// Display columns rendered in the table.
	Name         string
	Description  string // optional secondary text rendered dimmed
	Type         string // "LAN", "BLE", "External", etc.
	Size         string // optional picker-specific metadata column
	Parameters   string // optional picker-specific metadata column
	Comments     string // optional picker-specific metadata column
	USB          string // non-empty when the device is connected over USB
	Address      string
	AgentVersion string
	OS           string // distro/OS name (e.g. "ubuntu"); shown alongside OSVersion
	OSVersion    string
	Provisioned  string // "Provisioned" or "Unprovisioned" when known, empty otherwise
	Hint         string // optional footer text shown when this item is highlighted

	// Probe reflects whether the agent version/OS probe for this row is in
	// flight (ProbePending), done (ProbeOK), or failed (ProbeFailed). The zero
	// value (ProbeNone) renders the version columns verbatim.
	Probe ProbeState
	// ProbeFrame is the spinner frame shown in the Agent/OS columns while
	// Probe == ProbePending. The owning model refreshes it on every spinner
	// tick; it is ignored for every other probe state.
	ProbeFrame string

	// Section, when non-empty, groups this item under a non-selectable header
	// row bearing the section name. Sections are rendered in the order they
	// first appear after sorting, so callers control grouping order via SortKey.
	// When no visible item sets Section, the picker renders and navigates
	// exactly as it does without sections.
	Section string

	// DedupKey is used for deduplication. If empty, Name is used.
	// Items with the same DedupKey (case-insensitive) are merged via MergeItem.
	DedupKey string

	// DefaultKeys are optional alternate identities compared against
	// PickerModel.DefaultKey when rendering the default marker.
	DefaultKeys []string

	// SortKey overrides the sort order when set. Items are sorted by SortKey
	// first (when non-empty), then by name. Use this to pin items to a specific
	// position (e.g. "0_first", "z_last") without affecting the display name.
	SortKey string

	// Insecure is true when the device is reachable but the connection is not
	// secured with mTLS. A warning is shown in the picker when this item is highlighted.
	Insecure bool

	// Value is the opaque payload returned when this item is selected.
	Value interface{}
}

// PickerAddMsg adds new items to the picker. Duplicates (by DedupKey, or Name
// if DedupKey is empty) are merged via MergeItem or silently dropped.
type PickerAddMsg struct {
	Items []PickerItem
}

// PickerDoneMsg signals that discovery has finished. The picker remains
// interactive so the user can still select from the collected items.
type PickerDoneMsg struct{}

// PickerSetMsg replaces all items in the picker. It is intended for
// authoritative refreshes where missing items should disappear from the list.
type PickerSetMsg struct {
	Items []PickerItem
}

// PickerModel is a Bubble Tea model that presents a live-updating list of
// items and lets the user select one with arrow keys + Enter.
type PickerModel struct {
	Title string // header line, e.g. "Select a device"

	// MergeItem is called when a new item shares a DedupKey with an existing
	// item. The caller can update existing in place (type, address, value, ...).
	// If nil, duplicate items are silently dropped.
	MergeItem func(existing *PickerItem, incoming PickerItem)

	// OnSetDefault is called when the user presses 'd' on the highlighted item.
	// If nil, 'd' is ignored.
	OnSetDefault func(item PickerItem)

	// OnUnsetDefault is called when the user presses 'x'.
	// If nil, 'x' is ignored.
	OnUnsetDefault func()

	// DefaultKey is compared case-insensitively against each item's DedupKey
	// (or Name if DedupKey is empty). Should be stored lowercase for consistency.
	// Shown with a ✦ indicator in the table.
	DefaultKey string

	// Filterable enables find-as-you-type filtering: printable keys narrow
	// the list to items whose Name contains the typed query
	// (case-insensitive), backspace edits the query, and esc clears it (esc
	// and ctrl+c quit when the query is empty). Matched characters are
	// highlighted in the Name column. Enabling this retargets the plain
	// 'q'/'d'/'x' hotkeys into the filter query, so only use it for pickers
	// whose items are free-form text (e.g. WiFi SSIDs).
	Filterable bool

	filter  string
	items   []PickerItem
	seenIdx map[string]int // dedup key -> index in items
	// rowItem maps each table row to its index in the visible-items slice, or
	// -1 when the row is a non-selectable section header. When no item sets a
	// Section this is the identity mapping, so behavior matches the headerless
	// picker exactly.
	rowItem      []int
	table        BubbleTable
	spinner      spinner.Model // animates Agent/OS cells while probes are pending
	columns      []pickerColumnDef
	fixedColumns bool
	legend       string // optional glyph legend rendered under the table
	selected     *PickerItem
	scanning     bool
	quitting     bool
	width        int
	height       int
}

// PickerColumn defines a caller-provided picker table column.
type PickerColumn struct {
	Title    string
	MinWidth int
	Required bool
	Value    func(PickerItem) string
}

func NewPicker() PickerModel {
	m := PickerModel{
		Title:        "Select a device",
		seenIdx:      make(map[string]int),
		table:        newPickerTable(),
		spinner:      newProbeSpinner(),
		columns:      pickerDeviceColumnDefs,
		fixedColumns: true,
		legend:       DeviceTableLegend,
		scanning:     true,
	}
	m.refreshTable()
	return m
}

func NewPickerWithTitle(title string) PickerModel {
	m := PickerModel{
		Title:    title,
		seenIdx:  make(map[string]int),
		table:    newPickerTable(),
		spinner:  newProbeSpinner(),
		scanning: true,
	}
	m.refreshTable()
	return m
}

// NewPickerWithTitleAndColumns creates a picker with a custom title and stable
// caller-provided table columns.
func NewPickerWithTitleAndColumns(title string, columns []PickerColumn) PickerModel {
	if len(columns) == 0 {
		return NewPickerWithTitle(title)
	}
	m := NewPickerWithTitle(title)
	m.columns = pickerColumnDefsFromColumns(columns)
	m.fixedColumns = true
	m.refreshTable()
	return m
}

func (m PickerModel) Init() tea.Cmd { return m.spinner.Tick }

// anyProbePending reports whether any item still has a probe in flight, i.e.
// whether the spinner has anything to animate.
func (m PickerModel) anyProbePending() bool {
	for i := range m.items {
		if m.items[i].Probe == ProbePending {
			return true
		}
	}
	return false
}

func (m PickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		// Re-render so pending rows pick up the new frame; skip the rebuild
		// when nothing is animating, but keep the tick loop alive cheaply.
		if m.anyProbePending() {
			m.refreshTable()
		}
		return m, cmd
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		m.refreshTable()
		return m, cmd
	case tea.KeyMsg:
		key := msg.String()
		switch {
		case key == "enter":
			visible := m.visibleItems()
			if idx := m.itemIndexForRow(m.table.Cursor()); idx >= 0 && idx < len(visible) {
				item := visible[idx]
				m.selected = &item
				return m, tea.Quit
			}
		case key == "d" && !m.Filterable:
			if m.OnSetDefault != nil {
				visible := m.visibleItems()
				if idx := m.itemIndexForRow(m.table.Cursor()); idx >= 0 && idx < len(visible) {
					item := visible[idx]
					key := strings.ToLower(item.DedupKey)
					if key == "" {
						key = strings.ToLower(item.Name)
					}
					m.DefaultKey = key
					m.OnSetDefault(item)
					m.refreshTable()
				}
			}
			return m, nil
		case key == "x" && !m.Filterable:
			if m.OnUnsetDefault != nil {
				m.DefaultKey = ""
				m.OnUnsetDefault()
				m.refreshTable()
			}
			return m, nil
		case key == "esc" && m.Filterable && m.filter != "":
			m.filter = ""
			m.table.SetCursor(0)
			m.refreshTable()
			return m, nil
		case key == "ctrl+c", key == "esc", key == "q" && !m.Filterable:
			m.quitting = true
			return m, tea.Quit
		case key == "backspace" && m.Filterable:
			if m.filter != "" {
				runes := []rune(m.filter)
				m.filter = string(runes[:len(runes)-1])
				m.table.SetCursor(0)
				m.refreshTable()
			}
			return m, nil
		case (msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace) && m.Filterable:
			// Pasted input can carry control characters; keep them out of
			// the query (it is echoed back to the terminal verbatim), and
			// cap its length so a large paste can't distort the layout.
			if typed := StripControl(string(msg.Runes)); typed != "" {
				const maxFilterLen = 64
				if room := maxFilterLen - len([]rune(m.filter)); room > 0 {
					runes := []rune(typed)
					if len(runes) > room {
						runes = runes[:room]
					}
					m.filter += string(runes)
					m.table.SetCursor(0)
					m.refreshTable()
				}
			}
			return m, nil
		default:
			prev := m.table.Cursor()
			var cmd tea.Cmd
			m.table, cmd = m.table.Update(msg)
			// Skip over non-selectable section headers in the direction of
			// travel so the cursor never rests on one.
			dir := 1
			if m.table.Cursor() < prev {
				dir = -1
			}
			m.snapCursorToSelectable(dir)
			return m, cmd
		}

	case PickerAddMsg:
		changed := false
		for _, item := range msg.Items {
			key := strings.ToLower(pickerItemKey(item))
			if idx, ok := m.seenIdx[key]; ok {
				if m.MergeItem != nil {
					m.MergeItem(&m.items[idx], item)
					changed = true
				}
				continue
			}
			m.seenIdx[key] = len(m.items)
			m.items = append(m.items, item)
			changed = true
		}
		if changed {
			m.refreshTable()
		}

	case PickerDoneMsg:
		m.scanning = false

	case PickerSetMsg:
		cursorKey := m.currentCursorKey()
		m.items = nil
		m.seenIdx = make(map[string]int, len(msg.Items))
		for _, item := range msg.Items {
			key := strings.ToLower(pickerItemKey(item))
			if idx, ok := m.seenIdx[key]; ok {
				if m.MergeItem != nil {
					m.MergeItem(&m.items[idx], item)
				}
				continue
			}
			m.seenIdx[key] = len(m.items)
			m.items = append(m.items, item)
		}
		m.refreshTableWithCursorKey(cursorKey)
	}

	return m, nil
}

var (
	pickerTitle    = lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	pickerHint     = lipgloss.NewStyle().Foreground(ColorDim)
	pickerScanning = lipgloss.NewStyle().Foreground(ColorPrimary)
	pickerInsecure = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ef4444"))
)

func (m PickerModel) View() string {
	if m.quitting || m.selected != nil {
		return ""
	}

	var sb strings.Builder

	scrollHint := ""
	if m.canScrollTable() {
		scrollHint = ", ←/→ scroll"
	}
	hint := " (↑/↓ navigate" + scrollHint + ", enter select, q quit)"
	if m.Filterable {
		hint = " (type to filter, ↑/↓ navigate" + scrollHint + ", enter select, esc quit)"
	}
	if m.OnSetDefault != nil || m.OnUnsetDefault != nil {
		extras := ""
		if m.OnSetDefault != nil {
			extras += ", d set default"
		}
		if m.OnUnsetDefault != nil {
			extras += ", x unset default"
		}
		hint = " (↑/↓ navigate" + scrollHint + ", enter select" + extras + ", q quit)"
	}
	sb.WriteString(m.viewLine(pickerTitle.Render(m.Title)+pickerHint.Render(hint)) + "\n\n")

	if m.Filterable && m.filter != "" {
		// The query is sanitized as it is typed; strip again here so this
		// render site is safe in isolation.
		sb.WriteString(m.viewLine("  Filter: "+StripControl(m.filter)+pickerHint.Render("  (esc to clear)")) + "\n\n")
	}

	if len(m.items) == 0 {
		if m.scanning {
			sb.WriteString(m.viewLine(pickerScanning.Render("  Scanning...")) + "\n")
		} else {
			sb.WriteString(m.viewLine(pickerHint.Render("  No options found.")) + "\n")
		}
		return sb.String()
	}

	visible := m.visibleItems()
	if len(visible) == 0 {
		sb.WriteString(m.viewLine(pickerHint.Render("  No matches — esc to clear the filter.")) + "\n")
		return sb.String()
	}

	sb.WriteString(ColorizeProbeGlyphs(m.tableView()) + "\n")

	if m.legend != "" {
		sb.WriteString(m.viewLine(pickerHint.Render("  "+m.legend)) + "\n")
	}

	if idx := m.itemIndexForRow(m.table.Cursor()); idx >= 0 && idx < len(visible) && visible[idx].Insecure {
		sb.WriteString(m.viewLine(pickerInsecure.Render("  ⚠  Connection is not secured with mTLS. PKI support is coming soon.")) + "\n")
	}

	if m.scanning {
		sb.WriteString("\n" + m.viewLine(pickerScanning.Render("  Scanning for more results...")) + "\n")
	}

	if hint := m.selectedHint(); hint != "" {
		if !m.scanning {
			sb.WriteString("\n")
		}
		sb.WriteString(m.viewLine(pickerHint.Render("  "+hint)) + "\n")
	}

	return sb.String()
}

func (m PickerModel) selectedHint() string {
	visible := m.visibleItems()
	idx := m.itemIndexForRow(m.table.Cursor())
	if idx < 0 || idx >= len(visible) {
		return ""
	}
	return strings.TrimSpace(visible[idx].Hint)
}

func (m PickerModel) viewLine(line string) string {
	if m.width <= 0 {
		return line
	}
	return CropANSIView(line, 0, m.width)
}

func (m PickerModel) tableView() string {
	if !m.Filterable || m.filter == "" {
		return m.table.View()
	}
	// Highlight filter matches in the Name column. The cell layout is
	// 1 padding + content + 1 padding per column; the Name column is first
	// unless the ✦ default column (width 3 + padding) precedes it.
	cols := m.table.Columns()
	nameIdx := 0
	start := 1
	if m.OnSetDefault != nil && len(cols) > 1 {
		nameIdx = 1
		start += cols[0].Width + 2
	}
	if nameIdx >= len(cols) {
		return m.table.View()
	}
	view := HighlightMatches(m.table.FullView(), m.filter, start, start+cols[nameIdx].Width)
	if w := m.table.ViewportWidth(); w > 0 {
		view = CropANSIView(view, m.table.ScrollOffset(), w)
	}
	return view
}

// Cancelled returns true if the user quit the picker without selecting (e.g. Ctrl+C).
func (m PickerModel) Cancelled() bool {
	return m.quitting
}

func (m PickerModel) Selected() *PickerItem {
	return m.selected
}

// DeviceTableLegend explains the glyphs used in the compact device table.
const DeviceTableLegend = "● provisioned  ○ unprovisioned  ✦ default  ⚠ agent older than CLI"

type pickerColumnDef struct {
	title    string
	minWidth int
	value    func(PickerItem) string
	required bool
	optional bool // hidden when no item has a value, even with fixed columns
}

// provisionedGlyph maps the PickerItem.Provisioned state to the 1-char glyph
// rendered in the leading marker column. See DeviceTableLegend.
func provisionedGlyph(provisioned string) string {
	switch provisioned {
	case "Provisioned":
		return "●"
	case "Unprovisioned":
		return "○"
	}
	return ""
}

func pickerColumnDefsFromColumns(columns []PickerColumn) []pickerColumnDef {
	if len(columns) == 0 {
		return nil
	}
	defs := make([]pickerColumnDef, 0, len(columns))
	for _, col := range columns {
		value := col.Value
		if value == nil {
			value = func(PickerItem) string { return "" }
		}
		defs = append(defs, pickerColumnDef{
			title:    col.Title,
			minWidth: col.MinWidth,
			value:    value,
			required: col.Required,
		})
	}
	return defs
}

var pickerColumnDefs = []pickerColumnDef{
	{
		title:    "Name",
		minWidth: 18,
		value: func(item PickerItem) string {
			return item.Name
		},
		required: true,
	},
	{
		title:    "Type",
		minWidth: 12,
		value: func(item PickerItem) string {
			if item.USB != "" && !strings.Contains(item.Type, "USB") {
				return "USB, " + item.Type
			}
			return item.Type
		},
	},
	{
		title:    "Address",
		minWidth: 14,
		value: func(item PickerItem) string {
			return item.Address
		},
	},
	{
		title:    "Description",
		minWidth: 20,
		value: func(item PickerItem) string {
			return item.Description
		},
	},
}

var pickerDeviceColumnDefs = []pickerColumnDef{
	pickerColumnDefs[0],
	pickerColumnDefs[1],
	pickerColumnDefs[2],
	{
		title:    "Agent",
		minWidth: 7,
		value: func(item PickerItem) string {
			return probeColumnValue(item.Probe, item.AgentVersion, item.ProbeFrame)
		},
	},
	{
		title:    "OS",
		minWidth: 4,
		value: func(item PickerItem) string {
			return probeColumnValue(item.Probe, formatOSNameVersion(item.OS, item.OSVersion), item.ProbeFrame)
		},
	},
	{
		title:    "Description",
		minWidth: 20,
		value: func(item PickerItem) string {
			return item.Description
		},
		optional: true,
	},
}

func newPickerTable() BubbleTable {
	return NewBubbleTable(true, nil)
}

// visibleItems returns the items that pass the active filter, in display
// order. With no filter (or filtering disabled) it returns the full list, so
// table cursor indexes always address this slice.
func (m PickerModel) visibleItems() []PickerItem {
	if !m.Filterable || m.filter == "" {
		return m.items
	}
	query := strings.ToLower(m.filter)
	out := make([]PickerItem, 0, len(m.items))
	for _, item := range m.items {
		if strings.Contains(strings.ToLower(item.Name), query) {
			out = append(out, item)
		}
	}
	return out
}

func (m *PickerModel) currentCursorKey() string {
	visible := m.visibleItems()
	if idx := m.itemIndexForRow(m.table.Cursor()); idx >= 0 && idx < len(visible) {
		return strings.ToLower(pickerItemKey(visible[idx]))
	}
	return ""
}

// itemIndexForRow maps a table row index to its index in the visible-items
// slice, or returns -1 when the row is a section header or out of range.
func (m PickerModel) itemIndexForRow(row int) int {
	if row < 0 || row >= len(m.rowItem) {
		return -1
	}
	return m.rowItem[row]
}

// rowForItemIndex maps a visible-items index back to its table row, or -1 if
// the item has no row (should not happen for in-range indices).
func (m PickerModel) rowForItemIndex(itemIdx int) int {
	for row, idx := range m.rowItem {
		if idx == itemIdx {
			return row
		}
	}
	return -1
}

// snapCursorToSelectable nudges the cursor off a section-header row onto the
// nearest selectable row, searching first in dir (+1 down, -1 up) and then the
// other way when the list edge is reached. It is a no-op when the cursor is
// already on a selectable row, which is always the case for headerless pickers.
func (m *PickerModel) snapCursorToSelectable(dir int) {
	n := len(m.rowItem)
	if n == 0 {
		return
	}
	cursor := m.table.Cursor()
	if cursor >= 0 && cursor < n && m.rowItem[cursor] >= 0 {
		return
	}
	for i := cursor; i >= 0 && i < n; i += dir {
		if m.rowItem[i] >= 0 {
			m.table.SetCursor(i)
			return
		}
	}
	for i := cursor; i >= 0 && i < n; i -= dir {
		if m.rowItem[i] >= 0 {
			m.table.SetCursor(i)
			return
		}
	}
}

func (m *PickerModel) refreshTable() {
	m.refreshTableWithCursorKey(m.currentCursorKey())
}

func (m *PickerModel) refreshTableWithCursorKey(cursorKey string) {
	// Stamp the current spinner frame onto pending rows so the Agent/OS columns
	// animate as the table is rebuilt.
	frame := m.spinner.View()
	for i := range m.items {
		if m.items[i].Probe == ProbePending {
			m.items[i].ProbeFrame = frame
		}
	}

	// Sort items for a stable, predictable display order. When SortKey is set,
	// it takes precedence; otherwise sort by name (using DedupKey if present).
	sort.SliceStable(m.items, func(i, j int) bool {
		ki := m.items[i].SortKey
		if ki == "" {
			ki = strings.ToLower(pickerItemKey(m.items[i]))
		}
		kj := m.items[j].SortKey
		if kj == "" {
			kj = strings.ToLower(pickerItemKey(m.items[j]))
		}
		return ki < kj
	})

	// Rebuild seenIdx to reflect the new positions after sorting.
	// Keys are always stored lowercase to match the lookup in Update.
	for k := range m.seenIdx {
		delete(m.seenIdx, k)
	}
	for i, item := range m.items {
		m.seenIdx[strings.ToLower(pickerItemKey(item))] = i
	}

	visible := m.visibleItems()
	hasDefaultCol := m.OnSetDefault != nil
	cols, itemRows := pickerTableDataForColumns(visible, m.DefaultKey, hasDefaultCol, m.columns, m.fixedColumns)
	rows, rowItem := withSectionHeaders(visible, itemRows, len(cols))
	m.rowItem = rowItem
	m.table.SetRows(nil)
	m.table.SetColumns(cols)
	m.table.SetRows(rows)

	// Restore cursor to the same item when possible. If that item disappeared,
	// keep the cursor near its previous position and clamp it into range.
	if len(rows) > 0 {
		visIdx := -1
		if cursorKey != "" {
			for i, item := range visible {
				if strings.ToLower(pickerItemKey(item)) == cursorKey {
					visIdx = i
					break
				}
			}
		}
		if visIdx >= 0 {
			m.table.SetCursor(m.rowForItemIndex(visIdx))
		} else if m.table.Cursor() < 0 {
			m.table.SetCursor(0)
		} else if m.table.Cursor() >= len(rows) {
			m.table.SetCursor(len(rows) - 1)
		}
		// Keep the cursor off a leading/inherited section header.
		m.snapCursorToSelectable(1)
	}

	m.table.SetWidth(PickerTableWidth(m.table.Columns()))
	m.table.SetHeight(PickerTableHeight(len(rows), m.height))
}

func pickerItemKey(item PickerItem) string {
	if item.DedupKey != "" {
		return item.DedupKey
	}
	return item.Name
}

// withSectionHeaders interleaves non-selectable section-header rows ahead of
// the first item of each section. It returns the full row set (headers + item
// rows) and a parallel rowItem slice mapping each row to its visible-items
// index (-1 for headers). When no visible item sets a Section, the item rows
// are returned unchanged with an identity mapping, preserving the headerless
// picker's exact behavior.
func withSectionHeaders(visible []PickerItem, itemRows []bubbleTable.Row, ncols int) ([]bubbleTable.Row, []int) {
	hasSection := false
	for _, item := range visible {
		if item.Section != "" {
			hasSection = true
			break
		}
	}
	if !hasSection {
		rowItem := make([]int, len(itemRows))
		for i := range rowItem {
			rowItem[i] = i
		}
		return itemRows, rowItem
	}

	rows := make([]bubbleTable.Row, 0, len(itemRows)+2)
	rowItem := make([]int, 0, len(itemRows)+2)
	currentSection := ""
	for i, item := range visible {
		if i >= len(itemRows) {
			break
		}
		if item.Section != "" && item.Section != currentSection {
			currentSection = item.Section
			rows = append(rows, sectionHeaderRow(currentSection, ncols))
			rowItem = append(rowItem, -1)
		}
		rows = append(rows, itemRows[i])
		rowItem = append(rowItem, i)
	}
	return rows, rowItem
}

// sectionHeaderRow builds a non-selectable header row whose first column shows
// the section label and whose remaining columns are blank.
//
// The label is intentionally plain text, not a lipgloss-styled string: the
// underlying bubbles table truncates every cell with runewidth.Truncate, which
// is not ANSI-aware. A styled value's escape bytes count toward the column
// width, so truncation cuts inside the escape sequence and the terminal renders
// garbage. The "── " rule prefix keeps the row readable as a header without
// embedding color.
func sectionHeaderRow(label string, ncols int) bubbleTable.Row {
	row := make(bubbleTable.Row, max(ncols, 1))
	row[0] = "── " + label
	return row
}

func pickerActiveColumnsForDefs(items []PickerItem, defs []pickerColumnDef, fixed bool) []pickerColumnDef {
	if len(defs) == 0 {
		defs = pickerColumnDefs
	}
	var active []pickerColumnDef
	for _, def := range defs {
		if def.required || (fixed && !def.optional) {
			active = append(active, def)
			continue
		}
		for _, item := range items {
			if def.value(item) != "" {
				active = append(active, def)
				break
			}
		}
	}
	return active
}

// PickerTableData builds the shared picker table columns and rows for items.
func PickerTableData(items []PickerItem, defaultKey string, hasDefaultCol bool) ([]bubbleTable.Column, []bubbleTable.Row) {
	return pickerTableDataForColumns(items, defaultKey, hasDefaultCol, nil, false)
}

// PickerDeviceTableData builds the stable device table used by device pickers
// and discover output.
func PickerDeviceTableData(items []PickerItem, defaultKey string, hasDefaultCol bool) ([]bubbleTable.Column, []bubbleTable.Row) {
	return pickerTableDataForColumns(items, defaultKey, hasDefaultCol, pickerDeviceColumnDefs, true)
}

func pickerTableDataForColumns(items []PickerItem, defaultKey string, hasDefaultCol bool, defs []pickerColumnDef, fixed bool) ([]bubbleTable.Column, []bubbleTable.Row) {
	activeCols := pickerActiveColumnsForDefs(items, defs, fixed)
	hasMarkerCol := hasDefaultCol || anyProvisionedGlyph(items)
	rows := pickerRows(items, activeCols, defaultKey, hasDefaultCol, hasMarkerCol)
	return pickerColumns(rows, activeCols, hasMarkerCol), rows
}

func anyProvisionedGlyph(items []PickerItem) bool {
	for _, item := range items {
		if provisionedGlyph(item.Provisioned) != "" {
			return true
		}
	}
	return false
}

func pickerRows(items []PickerItem, cols []pickerColumnDef, defaultKey string, hasDefaultCol, hasMarkerCol bool) []bubbleTable.Row {
	rows := make([]bubbleTable.Row, 0, len(items))
	for _, item := range items {
		var row bubbleTable.Row
		// The marker column combines the provisioned-state glyph with the
		// ✦ default indicator (see DeviceTableLegend).
		if hasMarkerCol {
			cell := provisionedGlyph(item.Provisioned)
			if hasDefaultCol && pickerItemMatchesDefaultKey(item, defaultKey) {
				if cell != "" {
					cell += " "
				}
				cell += "✦"
			}
			row = append(row, cell)
		}
		for _, col := range cols {
			val := col.value(item)
			if col.required && item.Insecure {
				val += " ⚠"
			}
			row = append(row, val)
		}
		rows = append(rows, row)
	}
	return rows
}

func pickerItemMatchesDefaultKey(item PickerItem, defaultKey string) bool {
	defaultKey = strings.ToLower(strings.TrimSpace(defaultKey))
	if defaultKey == "" {
		return false
	}
	for _, key := range item.DefaultKeys {
		if strings.ToLower(strings.TrimSpace(key)) == defaultKey {
			return true
		}
	}
	key := strings.ToLower(strings.TrimSpace(item.DedupKey))
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(item.Name))
	}
	return key == defaultKey
}

func pickerColumns(rows []bubbleTable.Row, defs []pickerColumnDef, hasMarkerCol bool) []bubbleTable.Column {
	cols := make([]bubbleTable.Column, 0, len(defs)+1)
	offset := 0
	if hasMarkerCol {
		cols = append(cols, bubbleTable.Column{Title: "", Width: 3})
		offset = 1
	}
	for i, def := range defs {
		width := lipgloss.Width(def.title)
		for _, row := range rows {
			rowIdx := i + offset
			if rowIdx >= len(row) {
				continue
			}
			width = max(width, lipgloss.Width(row[rowIdx]))
		}
		width += 2
		width = max(width, def.minWidth)
		cols = append(cols, bubbleTable.Column{Title: def.title, Width: width})
	}
	return cols
}

func PickerTableWidth(cols []bubbleTable.Column) int {
	total := 0
	for _, col := range cols {
		total += col.Width + 2
	}
	return total
}

func PickerTableHeight(rowCount, windowHeight int) int {
	height := max(rowCount+1, 4)
	if windowHeight > 0 {
		return min(height, max(windowHeight-5, 4))
	}
	return min(height, 12)
}

func (m PickerModel) canScrollTable() bool {
	return m.table.CanScroll()
}

func (m PickerModel) tableViewportWidth() int {
	if width := m.table.ViewportWidth(); width > 0 {
		return width
	}
	return PickerTableWidth(m.table.Columns())
}
