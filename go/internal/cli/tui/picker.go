package tui

import (
	"sort"
	"strings"

	bubbleTable "github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// PickerItem represents a selectable row in the device picker.
type PickerItem struct {
	// Display columns rendered in the table.
	Name         string
	Description  string // optional secondary text rendered dimmed
	Type         string // "LAN", "Bluetooth", "External", etc.
	Size         string // optional picker-specific metadata column
	Parameters   string // optional picker-specific metadata column
	Comments     string // optional picker-specific metadata column
	USB          string // non-empty when the device is connected over USB
	Address      string
	AgentVersion string
	OSVersion    string
	Hint         string // optional footer text shown when this item is highlighted

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
	// Shown with a ★ indicator in the table.
	DefaultKey string

	items        []PickerItem
	seenIdx      map[string]int // dedup key -> index in items
	table        BubbleTable
	columns      []pickerColumnDef
	fixedColumns bool
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
		columns:      pickerDeviceColumnDefs,
		fixedColumns: true,
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

func (m PickerModel) Init() tea.Cmd { return nil }

func (m PickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		m.refreshTable()
		return m, cmd
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			cursor := m.table.Cursor()
			if len(m.items) > 0 && cursor >= 0 && cursor < len(m.items) {
				item := m.items[cursor]
				m.selected = &item
				return m, tea.Quit
			}
		case "d":
			if m.OnSetDefault != nil {
				cursor := m.table.Cursor()
				if len(m.items) > 0 && cursor >= 0 && cursor < len(m.items) {
					item := m.items[cursor]
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
		case "x":
			if m.OnUnsetDefault != nil {
				m.DefaultKey = ""
				m.OnUnsetDefault()
				m.refreshTable()
			}
			return m, nil
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		default:
			var cmd tea.Cmd
			m.table, cmd = m.table.Update(msg)
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

	if len(m.items) == 0 {
		if m.scanning {
			sb.WriteString(m.viewLine(pickerScanning.Render("  Scanning...")) + "\n")
		} else {
			sb.WriteString(m.viewLine(pickerHint.Render("  No options found.")) + "\n")
		}
		return sb.String()
	}

	sb.WriteString(m.tableView() + "\n")

	cursor := m.table.Cursor()
	if cursor >= 0 && cursor < len(m.items) && m.items[cursor].Insecure {
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
	cursor := m.table.Cursor()
	if cursor < 0 || cursor >= len(m.items) {
		return ""
	}
	return strings.TrimSpace(m.items[cursor].Hint)
}

func (m PickerModel) viewLine(line string) string {
	if m.width <= 0 {
		return line
	}
	return CropANSIView(line, 0, m.width)
}

func (m PickerModel) tableView() string {
	return m.table.View()
}

// Cancelled returns true if the user quit the picker without selecting (e.g. Ctrl+C).
func (m PickerModel) Cancelled() bool {
	return m.quitting
}

func (m PickerModel) Selected() *PickerItem {
	return m.selected
}

type pickerColumnDef struct {
	title    string
	minWidth int
	value    func(PickerItem) string
	required bool
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
		title:    "wendy-agent version",
		minWidth: 20,
		value: func(item PickerItem) string {
			return item.AgentVersion
		},
	},
	{
		title:    "WendyOS Version",
		minWidth: 16,
		value: func(item PickerItem) string {
			return item.OSVersion
		},
	},
	pickerColumnDefs[3],
}

func newPickerTable() BubbleTable {
	return NewBubbleTable(true, nil)
}

func (m *PickerModel) currentCursorKey() string {
	if cursor := m.table.Cursor(); cursor >= 0 && cursor < len(m.items) {
		return strings.ToLower(pickerItemKey(m.items[cursor]))
	}
	return ""
}

func (m *PickerModel) refreshTable() {
	m.refreshTableWithCursorKey(m.currentCursorKey())
}

func (m *PickerModel) refreshTableWithCursorKey(cursorKey string) {
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

	hasDefaultCol := m.OnSetDefault != nil
	cols, rows := pickerTableDataForColumns(m.items, m.DefaultKey, hasDefaultCol, m.columns, m.fixedColumns)
	m.table.SetRows(nil)
	m.table.SetColumns(cols)
	m.table.SetRows(rows)

	// Restore cursor to the same item when possible. If that item disappeared,
	// keep the cursor near its previous position and clamp it into range.
	if len(rows) > 0 {
		if cursorKey != "" {
			if idx, ok := m.seenIdx[cursorKey]; ok {
				m.table.SetCursor(idx)
			} else if m.table.Cursor() < 0 || m.table.Cursor() >= len(rows) {
				m.table.SetCursor(len(rows) - 1)
			}
		} else if m.table.Cursor() < 0 {
			m.table.SetCursor(0)
		} else if m.table.Cursor() >= len(rows) {
			m.table.SetCursor(len(rows) - 1)
		}
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

func pickerActiveColumnsForDefs(items []PickerItem, defs []pickerColumnDef, fixed bool) []pickerColumnDef {
	if len(defs) == 0 {
		defs = pickerColumnDefs
	}
	if fixed {
		return defs
	}
	var active []pickerColumnDef
	for _, def := range defs {
		if def.required {
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
	rows := pickerRows(items, activeCols, defaultKey, hasDefaultCol)
	return pickerColumns(rows, activeCols, hasDefaultCol), rows
}

func pickerRows(items []PickerItem, cols []pickerColumnDef, defaultKey string, hasDefaultCol bool) []bubbleTable.Row {
	rows := make([]bubbleTable.Row, 0, len(items))
	for _, item := range items {
		var row bubbleTable.Row
		// Always add the ★ column when default tracking is enabled.
		if hasDefaultCol {
			if pickerItemMatchesDefaultKey(item, defaultKey) {
				row = append(row, "★")
			} else {
				row = append(row, "")
			}
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

func pickerColumns(rows []bubbleTable.Row, defs []pickerColumnDef, hasDefaultCol bool) []bubbleTable.Column {
	cols := make([]bubbleTable.Column, 0, len(defs)+1)
	offset := 0
	if hasDefaultCol {
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
