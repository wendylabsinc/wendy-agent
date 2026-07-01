package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// topSample is a normalized snapshot used to compute CPU% from deltas.
type topSample struct {
	host         *agentpb.HostStats
	containers   map[string]uint64 // container ID -> cumulative cpu nanos
	mem          map[string]int64  // container ID -> memory bytes
	takenAtNanos int64
}

func newTopSample(resp *agentpb.GetResourceStatsResponse, atNanos int64) topSample {
	s := topSample{
		host:         resp.GetHost(),
		containers:   make(map[string]uint64),
		mem:          make(map[string]int64),
		takenAtNanos: atNanos,
	}
	for _, c := range resp.GetContainers() {
		s.containers[c.GetAppName()] = c.GetCpuUsageNanos()
		s.mem[c.GetAppName()] = c.GetMemoryBytes()
	}
	return s
}

// hostCPUPercent returns busy CPU percentage (0-100) across the whole machine,
// computed from the idle/total jiffy deltas between two samples.
func hostCPUPercent(prev, cur topSample) float64 {
	if prev.host == nil || cur.host == nil {
		return 0
	}
	totalΔ := int64(cur.host.GetCpuTotalJiffies()) - int64(prev.host.GetCpuTotalJiffies())
	idleΔ := int64(cur.host.GetCpuIdleJiffies()) - int64(prev.host.GetCpuIdleJiffies())
	if totalΔ <= 0 {
		return 0
	}
	busy := (1 - float64(idleΔ)/float64(totalΔ)) * 100
	if busy < 0 {
		return 0
	}
	return busy
}

// containerCPUPercent returns a container's CPU usage as a percentage of the
// whole machine (0-100 across all cores), from the CPU-nanos delta over elapsed
// wall time. cpuCount normalizes to "share of total machine".
func containerCPUPercent(prev, cur topSample, id string, cpuCount uint32) float64 {
	wallΔ := cur.takenAtNanos - prev.takenAtNanos
	if wallΔ <= 0 || cpuCount == 0 {
		return 0
	}
	prevNanos, ok := prev.containers[id]
	if !ok {
		return 0
	}
	curNanos := cur.containers[id]
	if curNanos < prevNanos {
		return 0 // counter reset / container restarted
	}
	pct := float64(curNanos-prevNanos) / (float64(wallΔ) * float64(cpuCount)) * 100
	if pct < 0 {
		return 0
	}
	return pct
}

// topRow is one display row (app, group header, or service subrow).
type topRow struct {
	name          string // app ID; "" for subrows
	displayName   string
	cpuPercent    float64
	memBytes      int64
	state         string // "running", "stopped", …
	hasCPU        bool
	isGroupHeader bool
	isSubrow      bool
}

// topStateLabel renders an app/service running state as a short lowercase label.
func topStateLabel(s agentpb.AppRunningState) string {
	switch s {
	case agentpb.AppRunningState_RUNNING:
		return "running"
	case agentpb.AppRunningState_STOPPED:
		return "stopped"
	default:
		return strings.ToLower(strings.TrimPrefix(s.String(), "APP_RUNNING_STATE_"))
	}
}

// buildTopRows groups containers by app (mirroring buildDashboardRows) with CPU%
// and memory columns. Top-level apps are sorted by the active key descending;
// service subrows stay under their group header.
func buildTopRows(containers []*agentpb.AppContainer, cpuByID map[string]float64, memByID map[string]int64, sortByCPU bool) []topRow {
	type appAgg struct {
		container *agentpb.AppContainer
		cpu       float64
		mem       int64
	}
	aggs := make([]appAgg, 0, len(containers))
	for _, c := range containers {
		appName := c.GetAppName()
		var cpu float64
		var mem int64
		if len(c.GetServices()) > 1 {
			for _, svc := range c.GetServices() {
				key := appName + "_" + svc.GetName()
				cpu += cpuByID[key]
				mem += memByID[key]
			}
		} else {
			cpu = cpuByID[appName]
			mem = memByID[appName]
		}
		aggs = append(aggs, appAgg{container: c, cpu: cpu, mem: mem})
	}

	sort.SliceStable(aggs, func(i, j int) bool {
		if sortByCPU {
			return aggs[i].cpu > aggs[j].cpu
		}
		return aggs[i].mem > aggs[j].mem
	})

	var rows []topRow
	for _, a := range aggs {
		c := a.container
		appName := c.GetAppName()
		if len(c.GetServices()) > 1 {
			rows = append(rows, topRow{
				name:          appName,
				displayName:   appName + " [group]",
				cpuPercent:    a.cpu,
				memBytes:      a.mem,
				state:         topStateLabel(c.GetRunningState()),
				hasCPU:        true,
				isGroupHeader: true,
			})
			for _, svc := range c.GetServices() {
				key := appName + "_" + svc.GetName()
				rows = append(rows, topRow{
					displayName: "  ↳ " + svc.GetName(),
					cpuPercent:  cpuByID[key],
					memBytes:    memByID[key],
					state:       topStateLabel(svc.GetRunningState()),
					hasCPU:      true,
					isSubrow:    true,
				})
			}
		} else {
			rows = append(rows, topRow{
				name:        appName,
				displayName: appName,
				cpuPercent:  a.cpu,
				memBytes:    a.mem,
				state:       topStateLabel(c.GetRunningState()),
				hasCPU:      true,
			})
		}
	}
	return rows
}

// errResourceStatsUnimplemented marks an agent too old to support device top.
var errResourceStatsUnimplemented = fmt.Errorf("the device's agent does not support resource stats; update it with 'wendy device update'")

func sampleResourceStats(ctx context.Context, conn *grpcclient.AgentConnection) (*agentpb.GetResourceStatsResponse, error) {
	resp, err := conn.ContainerService.GetResourceStats(ctx, &agentpb.GetResourceStatsRequest{})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return nil, errResourceStatsUnimplemented
		}
		return nil, err
	}
	return resp, nil
}

func listAppContainers(ctx context.Context, conn *grpcclient.AgentConnection) ([]*agentpb.AppContainer, error) {
	stream, err := conn.ContainerService.ListContainers(ctx, &agentpb.ListContainersRequest{})
	if err != nil {
		return nil, err
	}
	var out []*agentpb.AppContainer
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if c := resp.GetContainer(); c != nil {
			out = append(out, c)
		}
	}
	return out, nil
}

type topJSONHost struct {
	CPUPercent    float64          `json:"cpuPercent"`
	CPUCount      uint32           `json:"cpuCount"`
	MemUsedBytes  int64            `json:"memUsedBytes"`
	MemTotalBytes int64            `json:"memTotalBytes"`
	GPUs          []topJSONGPU     `json:"gpus,omitempty"`
	ThermalZones  []topJSONThermal `json:"thermalZones,omitempty"`
}

type topJSONThermal struct {
	Name  string  `json:"name"`
	TempC float64 `json:"tempC"`
}

type topJSONGPU struct {
	Index         uint32   `json:"index"`
	Name          string   `json:"name"`
	UtilPercent   float64  `json:"utilPercent"`
	MemUsedBytes  int64    `json:"memUsedBytes"`
	MemTotalBytes int64    `json:"memTotalBytes"`
	TempC         *float64 `json:"tempC,omitempty"`
	PowerW        *float64 `json:"powerW,omitempty"`
}

type topJSONContainer struct {
	Name       string  `json:"name"`
	State      string  `json:"state"`
	CPUPercent float64 `json:"cpuPercent"`
	MemBytes   int64   `json:"memBytes"`
}

type topJSONOutput struct {
	Host       topJSONHost        `json:"host"`
	Containers []topJSONContainer `json:"containers"`
}

func buildTopJSON(prev, cur topSample, containers []*agentpb.AppContainer) topJSONOutput {
	out := topJSONOutput{}
	if cur.host != nil {
		out.Host.CPUPercent = hostCPUPercent(prev, cur)
		out.Host.CPUCount = cur.host.GetCpuCount()
		out.Host.MemTotalBytes = cur.host.GetMemTotalBytes()
		out.Host.MemUsedBytes = cur.host.GetMemTotalBytes() - cur.host.GetMemAvailableBytes()
		for _, g := range cur.host.GetGpus() {
			out.Host.GPUs = append(out.Host.GPUs, topJSONGPU{
				Index:         g.GetIndex(),
				Name:          g.GetName(),
				UtilPercent:   g.GetUtilPercent(),
				MemUsedBytes:  g.GetMemUsedBytes(),
				MemTotalBytes: g.GetMemTotalBytes(),
				TempC:         g.TempC,
				PowerW:        g.PowerW,
			})
		}
		for _, z := range cur.host.GetThermalZones() {
			out.Host.ThermalZones = append(out.Host.ThermalZones, topJSONThermal{
				Name:  z.GetName(),
				TempC: z.GetTempC(),
			})
		}
	}
	cpuCount := uint32(1)
	if cur.host != nil && cur.host.GetCpuCount() > 0 {
		cpuCount = cur.host.GetCpuCount()
	}
	cpuByID := map[string]float64{}
	for id := range cur.containers {
		cpuByID[id] = containerCPUPercent(prev, cur, id, cpuCount)
	}
	rows := buildTopRows(containers, cpuByID, cur.mem, false)
	for _, r := range rows {
		if r.isSubrow {
			continue
		}
		out.Containers = append(out.Containers, topJSONContainer{
			Name:       r.displayName,
			CPUPercent: r.cpuPercent,
			MemBytes:   r.memBytes,
		})
	}
	return out
}

func runTopSnapshot(ctx context.Context, conn *grpcclient.AgentConnection, asJSON bool) error {
	containers, err := listAppContainers(ctx, conn)
	if err != nil {
		return fmt.Errorf("listing containers: %w", err)
	}
	first, err := sampleResourceStats(ctx, conn)
	if err != nil {
		return err
	}
	prev := newTopSample(first, time.Now().UnixNano())
	time.Sleep(250 * time.Millisecond)
	second, err := sampleResourceStats(ctx, conn)
	if err != nil {
		return err
	}
	cur := newTopSample(second, time.Now().UnixNano())

	if asJSON {
		data, err := json.MarshalIndent(buildTopJSON(prev, cur, containers), "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	// Plain table.
	cpuCount := uint32(1)
	if cur.host != nil && cur.host.GetCpuCount() > 0 {
		cpuCount = cur.host.GetCpuCount()
	}
	if cur.host != nil {
		fmt.Printf("CPU: %.1f%%  MEM: %s / %s\n",
			hostCPUPercent(prev, cur),
			formatBytes(cur.host.GetMemTotalBytes()-cur.host.GetMemAvailableBytes()),
			formatBytes(cur.host.GetMemTotalBytes()))
		for _, g := range cur.host.GetGpus() {
			fmt.Printf("GPU%d %s: %.0f%%  %s / %s\n", g.GetIndex(), g.GetName(),
				g.GetUtilPercent(), formatBytes(g.GetMemUsedBytes()), formatBytes(g.GetMemTotalBytes()))
		}
		if zones := cur.host.GetThermalZones(); len(zones) > 0 {
			fmt.Printf("TEMP: %s\n", formatThermalZones(zones))
		}
	}
	cpuByID := map[string]float64{}
	for id := range cur.containers {
		cpuByID[id] = containerCPUPercent(prev, cur, id, cpuCount)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "APP\tCPU%\tMEM")
	for _, r := range buildTopRows(containers, cpuByID, cur.mem, false) {
		fmt.Fprintf(tw, "%s\t%.1f\t%s\n", r.displayName, r.cpuPercent, formatBytes(r.memBytes))
	}
	return tw.Flush()
}

func newTopCmd() *cobra.Command {
	var interval time.Duration
	cmd := &cobra.Command{
		Use:   "top",
		Short: "Live CPU, memory, and GPU usage for the device and its containers",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			if jsonOutput || !isInteractiveTerminal() {
				return runTopSnapshot(ctx, conn, jsonOutput)
			}
			return runTopDashboard(ctx, conn, interval)
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "Refresh interval for the live view")
	return cmd
}

type topStatsMsg struct {
	resp *agentpb.GetResourceStatsResponse
	err  error
}

type topContainersMsg struct {
	containers []*agentpb.AppContainer
	err        error
}

type topModel struct {
	conn     *grpcclient.AgentConnection
	ctx      context.Context
	interval time.Duration

	statsCh      chan topStatsMsg
	containersCh chan topContainersMsg

	prev, cur        topSample
	havePrev         bool
	cachedContainers []*agentpb.AppContainer

	rows      []topRow
	cursor    int
	sortByCPU bool
	width     int
	height    int
	flash     string

	// Ports for the currently selected app (always-on side panel).
	portsApp string
	ports    []*agentpb.PortEntry
	portsErr error
}

type topPortsMsg struct {
	app   string
	ports []*agentpb.PortEntry
	err   error
}

func newTopModel(ctx context.Context, conn *grpcclient.AgentConnection, interval time.Duration) topModel {
	return topModel{
		conn:         conn,
		ctx:          ctx,
		interval:     interval,
		statsCh:      make(chan topStatsMsg, 2),
		containersCh: make(chan topContainersMsg, 2),
	}
}

func (m topModel) Init() tea.Cmd {
	go m.runStatsPoll()
	go m.runContainersPoll()
	return tea.Batch(waitForTopStats(m.statsCh), waitForTopContainers(m.containersCh))
}

func waitForTopStats(ch chan topStatsMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}
func waitForTopContainers(ch chan topContainersMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// selectedAppName returns the app the cursor is on, walking up from a service
// subrow to its group header.
func (m topModel) selectedAppName() string {
	for i := m.cursor; i >= 0 && i < len(m.rows); i-- {
		if m.rows[i].name != "" {
			return m.rows[i].name
		}
	}
	return ""
}

// fetchPortsCmd fetches the listening ports for app. The result carries the app
// name so a stale response for a no-longer-selected app can be ignored.
func (m topModel) fetchPortsCmd(app string) tea.Cmd {
	conn, ctx := m.conn, m.ctx
	return func() tea.Msg {
		if app == "" {
			return topPortsMsg{app: app}
		}
		resp, err := conn.ContainerService.GetContainerPorts(ctx, &agentpb.GetContainerPortsRequest{AppName: app})
		if err != nil {
			return topPortsMsg{app: app, err: err}
		}
		return topPortsMsg{app: app, ports: resp.GetPorts()}
	}
}

// maybeFetchPorts issues a ports fetch when the selected app has changed,
// clearing the now-stale panel contents. Returns nil when the selection is
// unchanged. The pointer receiver mutates the addressable model copy held by
// Update.
func (m *topModel) maybeFetchPorts() tea.Cmd {
	sel := m.selectedAppName()
	if sel == m.portsApp {
		return nil
	}
	m.portsApp = sel
	m.ports = nil
	m.portsErr = nil
	return m.fetchPortsCmd(sel)
}

func (m topModel) runStatsPoll() {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	// fetch keeps polling on transient errors (a hiccup recovers on the next
	// tick); it only stops when the agent does not implement the RPC at all,
	// since that will never succeed.
	fetch := func() bool {
		resp, err := sampleResourceStats(m.ctx, m.conn)
		select {
		case m.statsCh <- topStatsMsg{resp: resp, err: err}:
		case <-m.ctx.Done():
			return false
		}
		return !errors.Is(err, errResourceStatsUnimplemented)
	}
	if !fetch() {
		return
	}
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			if !fetch() {
				return
			}
		}
	}
}

func (m topModel) runContainersPoll() {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	fetch := func() {
		containers, err := listAppContainers(m.ctx, m.conn)
		select {
		case m.containersCh <- topContainersMsg{containers: containers, err: err}:
		case <-m.ctx.Done():
		}
	}
	fetch()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			fetch()
		}
	}
}

// rebuildRows recomputes the displayed rows from the cached samples, keeping
// the cursor within bounds.
func (m *topModel) rebuildRows() {
	cpuCount := uint32(1)
	if m.cur.host != nil && m.cur.host.GetCpuCount() > 0 {
		cpuCount = m.cur.host.GetCpuCount()
	}
	cpuByID := map[string]float64{}
	if m.havePrev {
		for id := range m.cur.containers {
			cpuByID[id] = containerCPUPercent(m.prev, m.cur, id, cpuCount)
		}
	}
	m.rows = buildTopRows(m.cachedContainers, cpuByID, m.cur.mem, m.sortByCPU)
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m topModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case topStatsMsg:
		if msg.err != nil {
			m.flash = userFacingGRPCError(msg.err)
			return m, waitForTopStats(m.statsCh)
		}
		m.flash = ""
		if m.cur.host != nil || len(m.cur.containers) > 0 {
			m.prev = m.cur
			m.havePrev = true
		}
		m.cur = newTopSample(msg.resp, time.Now().UnixNano())
		m.rebuildRows()
		// Refresh ports for the selected app on every tick so they stay current.
		sel := m.selectedAppName()
		m.portsApp = sel
		return m, tea.Batch(waitForTopStats(m.statsCh), m.fetchPortsCmd(sel))

	case topContainersMsg:
		if msg.err != nil {
			m.flash = userFacingGRPCError(msg.err)
			return m, waitForTopContainers(m.containersCh)
		}
		m.cachedContainers = msg.containers
		m.rebuildRows()
		return m, tea.Batch(waitForTopContainers(m.containersCh), m.maybeFetchPorts())

	case topPortsMsg:
		// Ignore responses for an app that is no longer selected.
		if msg.app == m.selectedAppName() {
			m.ports = msg.ports
			m.portsErr = msg.err
			m.portsApp = msg.app
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, m.maybeFetchPorts()
		case "down", "j":
			if m.cursor < len(m.rows)-1 {
				m.cursor++
			}
			return m, m.maybeFetchPorts()
		case "c":
			m.sortByCPU = true
			m.rebuildRows()
			return m, m.maybeFetchPorts()
		case "m":
			m.sortByCPU = false
			m.rebuildRows()
			return m, m.maybeFetchPorts()
		}
		return m, nil
	}
	return m, nil
}

// --- htop-style rendering ---

var (
	topMeterLabel = lipgloss.NewStyle().Bold(true).Foreground(tui.Emerald400)
	topBracket    = lipgloss.NewStyle().Foreground(tui.ColorDim)
	topValDim     = lipgloss.NewStyle().Foreground(tui.ColorDim)
	topHeaderBar  = lipgloss.NewStyle().Bold(true).Background(tui.Emerald500).Foreground(lipgloss.Color("#02160f"))
	// Bright mint selection bar for strong contrast with the black row text.
	topSelRow   = lipgloss.NewStyle().Background(lipgloss.Color("#9FE2BF")).Foreground(lipgloss.Color("#000000"))
	topKeyCap   = lipgloss.NewStyle().Foreground(lipgloss.Color("#02160f")).Background(lipgloss.Color("#d0d0d0"))
	topKeyLabel = lipgloss.NewStyle().Foreground(lipgloss.Color("#02160f")).Background(tui.Emerald500)
)

// topMeter renders an htop-style bracketed meter: LABEL[|||||      value].
// The fill is colored green/amber/red by load, and value is right-aligned
// inside the bracket.
func topMeter(label string, ratio float64, value string, width int) string {
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	valW := lipgloss.Width(value)
	inner := width - lipgloss.Width(label) - 2 // 2 for the brackets
	if inner < valW+1 {
		inner = valW + 1
	}
	barArea := inner - valW
	if barArea < 0 {
		barArea = 0
	}
	filled := int(ratio * float64(barArea))
	if filled > barArea {
		filled = barArea
	}
	var c lipgloss.Color
	switch {
	case ratio < 0.5:
		c = tui.Emerald500
	case ratio < 0.85:
		c = tui.Amber500
	default:
		c = tui.Red500
	}
	bars := lipgloss.NewStyle().Foreground(c).Render(strings.Repeat("|", filled))
	gap := strings.Repeat(" ", barArea-filled)
	return topMeterLabel.Render(label) + topBracket.Render("[") + bars + gap + topValDim.Render(value) + topBracket.Render("]")
}

// topColWidths returns the fixed column widths and the flexible name width for
// a given total terminal width. Layout: " name  CPU%  MEM%  MEM  STATE".
func topNameWidth(width int) int {
	// 1 (lead) + name + 1 + 6 (cpu) + 1 + 6 (memp) + 1 + 10 (mem) + 1 + 8 (state)
	nameW := width - 36
	if nameW < 10 {
		nameW = 10
	}
	return nameW
}

func topFormatRow(name, cpu, memp, mem, state string, nameW int) string {
	r := []rune(name)
	if len(r) > nameW {
		name = string(r[:nameW])
	}
	return fmt.Sprintf(" %-*s %6s %6s %10s %-8s", nameW, name, cpu, memp, mem, state)
}

func (m topModel) View() string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}

	// Reserve a right-hand ports panel when the terminal is wide enough.
	panelW := 0
	if width >= 70 {
		panelW = 34
	}
	listW := width
	if panelW > 0 {
		listW = width - panelW - 3 // " │ " separator
	}

	var top []string // full-width meters + summary

	if m.cur.host != nil {
		h := m.cur.host
		meterW := width - 2
		cpuRatio, cpuVal := 0.0, "—"
		if m.havePrev {
			pct := hostCPUPercent(m.prev, m.cur)
			cpuRatio, cpuVal = pct/100, fmt.Sprintf("%.1f%%", pct)
		}
		cpuLabel := "CPU"
		if h.GetCpuCount() > 0 {
			cpuLabel = fmt.Sprintf("CPU(%d)", h.GetCpuCount())
		}
		top = append(top, topMeter(cpuLabel, cpuRatio, cpuVal, meterW))

		used := h.GetMemTotalBytes() - h.GetMemAvailableBytes()
		memRatio := 0.0
		if h.GetMemTotalBytes() > 0 {
			memRatio = float64(used) / float64(h.GetMemTotalBytes())
		}
		top = append(top, topMeter("Mem", memRatio,
			fmt.Sprintf("%s/%s", formatBytes(used), formatBytes(h.GetMemTotalBytes())), meterW))

		for _, g := range h.GetGpus() {
			val := fmt.Sprintf("%.0f%%", g.GetUtilPercent())
			if g.GetMemTotalBytes() > 0 {
				val += fmt.Sprintf(" %s/%s", formatBytes(g.GetMemUsedBytes()), formatBytes(g.GetMemTotalBytes()))
			}
			if g.TempC != nil {
				val += fmt.Sprintf(" %.0f°C", *g.TempC)
			}
			top = append(top, topMeter("GPU", g.GetUtilPercent()/100, val, meterW))
		}

		if zones := h.GetThermalZones(); len(zones) > 0 {
			top = append(top, topValDim.Render(" Temp: "+formatThermalZones(zones)))
		}
	} else {
		top = append(top, topValDim.Render(" Connecting…"))
	}

	running := 0
	for _, c := range m.cachedContainers {
		if c.GetRunningState() == agentpb.AppRunningState_RUNNING {
			running++
		}
	}
	top = append(top, topValDim.Render(fmt.Sprintf(" Apps: %d, %d running", len(m.cachedContainers), running)))
	top = append(top, "")

	// Build the left (container list) and right (ports) columns of the body.
	left := m.listLines(listW)
	var right []string
	if panelW > 0 {
		right = m.portsPanelLines(panelW)
	}

	bodyHeight := height - len(top) - 1 // last line is the key bar
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	sep := topValDim.Render(" │ ")
	body := make([]string, bodyHeight)
	for i := 0; i < bodyHeight; i++ {
		l := ""
		if i < len(left) {
			l = left[i]
		}
		l = padOrCrop(l, listW)
		if panelW == 0 {
			body[i] = l
			continue
		}
		r := ""
		if i < len(right) {
			r = right[i]
		}
		body[i] = l + sep + padOrCrop(r, panelW)
	}

	out := append(top, body...)
	return strings.Join(out, "\n") + "\n" + m.topKeyBar(width)
}

// listLines renders the container table (header + rows) at the given width.
func (m topModel) listLines(width int) []string {
	nameW := topNameWidth(width)
	cpuTitle, memTitle := "CPU%", "MEM"
	if m.sortByCPU {
		cpuTitle = "CPU%▾"
	} else {
		memTitle = "MEM▾"
	}
	var lines []string
	header := padOrCrop(topFormatRow("APP", cpuTitle, "MEM%", memTitle, "STATE", nameW), width)
	lines = append(lines, topHeaderBar.Render(header))

	if len(m.rows) == 0 {
		lines = append(lines, topValDim.Render(" Sampling…"))
		return lines
	}
	memTotal := int64(0)
	if m.cur.host != nil {
		memTotal = m.cur.host.GetMemTotalBytes()
	}
	for i, r := range m.rows {
		cpu := "-"
		if r.hasCPU && m.havePrev {
			cpu = fmt.Sprintf("%.1f", r.cpuPercent)
		}
		memp := "-"
		if memTotal > 0 {
			memp = fmt.Sprintf("%.1f", float64(r.memBytes)/float64(memTotal)*100)
		}
		row := padOrCrop(topFormatRow(r.displayName, cpu, memp, formatBytes(r.memBytes), r.state, nameW), width)
		switch {
		case i == m.cursor:
			lines = append(lines, topSelRow.Render(row))
		case r.isSubrow:
			lines = append(lines, topValDim.Render(row))
		default:
			lines = append(lines, row)
		}
	}
	return lines
}

// portsPanelLines renders the open-ports panel for the selected app.
func (m topModel) portsPanelLines(width int) []string {
	app := m.selectedAppName()
	title := "OPEN PORTS"
	if app != "" {
		title = "OPEN PORTS — " + app
	}
	lines := []string{topHeaderBar.Render(padOrCrop(" "+title, width))}

	switch {
	case app == "":
		lines = append(lines, topValDim.Render(" (no app selected)"))
	case m.portsErr != nil:
		if errors.Is(m.portsErr, errResourceStatsUnimplemented) || status.Code(m.portsErr) == codes.Unimplemented {
			lines = append(lines, topValDim.Render(" (agent too old)"))
		} else {
			lines = append(lines, topValDim.Render(" (unavailable)"))
		}
	case m.portsApp != app:
		lines = append(lines, topValDim.Render(" …"))
	case len(m.ports) == 0:
		lines = append(lines, topValDim.Render(" (none listening)"))
	default:
		for _, p := range m.ports {
			addr := p.GetAddress()
			if strings.Contains(addr, ":") { // IPv6 → bracket for clarity
				addr = "[" + addr + "]"
			}
			lines = append(lines, fmt.Sprintf(" %-4s %s:%d", p.GetProtocol(), addr, p.GetPort()))
		}
	}
	return lines
}

// padOrCrop pads a plain string with spaces to exactly width, or crops it.
func padOrCrop(s string, width int) string {
	n := lipgloss.Width(s)
	if n < width {
		return s + strings.Repeat(" ", width-n)
	}
	if n > width {
		return tui.CropANSIView(s, 0, width)
	}
	return s
}

func (m topModel) topKeyBar(width int) string {
	flash := m.flash
	segs := []struct{ key, label string }{
		{"↑↓", "Nav"},
		{"m", "Mem"},
		{"c", "CPU"},
		{"q", "Quit"},
	}
	var b strings.Builder
	plainLen := 0
	for _, s := range segs {
		b.WriteString(topKeyCap.Render(s.key))
		b.WriteString(topKeyLabel.Render(s.label + " "))
		plainLen += lipgloss.Width(s.key) + lipgloss.Width(s.label) + 1
	}
	if flash != "" && plainLen+2+len(flash) < width {
		msg := "  " + flash
		b.WriteString(topKeyLabel.Render(msg))
		plainLen += lipgloss.Width(msg)
	}
	if plainLen < width {
		b.WriteString(topKeyLabel.Render(strings.Repeat(" ", width-plainLen)))
	}
	return b.String()
}

func runTopDashboard(ctx context.Context, conn *grpcclient.AgentConnection, interval time.Duration) error {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newTopModel(cctx, conn, interval)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// formatThermalZones renders thermal zones (already sorted hottest-first by the
// agent) as a compact one-line summary, e.g. "cpu 49°C  gpu 48°C  soc0 47°C".
// Zone names are shortened by trimming the conventional "-thermal"/"-therm"
// suffix for readability.
func formatThermalZones(zones []*agentpb.ThermalZone) string {
	parts := make([]string, 0, len(zones))
	for _, z := range zones {
		name := z.GetName()
		name = strings.TrimSuffix(name, "-thermal")
		name = strings.TrimSuffix(name, "-therm")
		parts = append(parts, fmt.Sprintf("%s %.0f°C", name, z.GetTempC()))
	}
	return strings.Join(parts, "  ")
}
