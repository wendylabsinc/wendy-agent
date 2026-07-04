package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// BuildStepMsg delivers a parser event to the model.
type BuildStepMsg BuildStepEvent

// BuildAllDoneMsg signals the build finished (Err nil on success).
type BuildAllDoneMsg struct{ Err error }

type buildRow struct {
	id      int
	kind    BuildVertexKind
	display string
	status  BuildStepStatus
	dur     time.Duration
}

// BuildStepsModel renders a live, collapsing list of buildx steps for a single
// service build.
type BuildStepsModel struct {
	title   string
	rows    []buildRow
	byID    map[int]int
	spinner spinner.Model
	hints   hintRotator
	tally   BuildTally
	done    bool
	err     error
}

// NewBuildStepsModel returns a model with the given title (e.g. the
// "Building image for linux/amd64..." line).
func NewBuildStepsModel(title string) BuildStepsModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(ColorPrimary)
	return BuildStepsModel{
		title:   title,
		byID:    map[int]int{},
		spinner: s,
		hints:   newHintRotator(),
	}
}

// Init implements tea.Model.
func (m BuildStepsModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.hints.tick())
}

// Update implements tea.Model.
func (m BuildStepsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.done = true
			m.err = ErrCancelled
			return m, tea.Quit
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case hintTickMsg:
		m.hints.next()
		return m, m.hints.tick()
	case BuildStepMsg:
		m.applyEvent(BuildStepEvent(msg))
	case BuildAllDoneMsg:
		m.done = true
		m.err = msg.Err
		return m, tea.Quit
	}
	return m, nil
}

func (m *BuildStepsModel) applyEvent(e BuildStepEvent) {
	i, ok := m.byID[e.ID]
	if !ok {
		m.rows = append(m.rows, buildRow{id: e.ID, kind: e.Kind, display: e.Display, status: e.Status})
		m.byID[e.ID] = len(m.rows) - 1
		return
	}
	m.rows[i].status = e.Status
	m.rows[i].dur = e.Dur
	switch e.Status {
	case BuildStepCached:
		if e.Kind == BuildVertexStep {
			m.tally.Cached++
		}
	case BuildStepDone:
		if e.Kind == BuildVertexStep {
			m.tally.Rebuilt++
		}
	}
}

var (
	bsCheck = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	bsCache = lipgloss.NewStyle().Foreground(ColorPrimary)
	bsCross = lipgloss.NewStyle().Foreground(ColorError).Bold(true)
	bsDim   = lipgloss.NewStyle().Foreground(ColorDim)
	bsTitle = lipgloss.NewStyle().Foreground(ColorPrimary)
)

const buildStepLabelWidth = 34

// View implements tea.Model.
func (m BuildStepsModel) View() string {
	if m.done {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s %s\n", m.spinner.View(), bsTitle.Render(m.title)))
	for _, r := range m.rows {
		label := truncateLabel(r.display, buildStepLabelWidth)
		switch r.status {
		case BuildStepRunning:
			sb.WriteString(fmt.Sprintf("  %s %s\n", m.spinner.View(), label))
		case BuildStepCached:
			sb.WriteString(fmt.Sprintf("  %s %s %s\n", bsCache.Render("⚡"), label, bsDim.Render("cached")))
		case BuildStepDone:
			sb.WriteString(fmt.Sprintf("  %s %s %s\n", bsCheck.Render("✓"), label, bsDim.Render(r.dur.Round(time.Millisecond).String())))
		case BuildStepFailed:
			sb.WriteString(fmt.Sprintf("  %s %s\n", bsCross.Render("✗"), label))
		}
	}
	if hint := m.hints.view(); hint != "" {
		sb.WriteString(hint)
		sb.WriteString("\n")
	}
	return sb.String()
}

func truncateLabel(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}

// Err returns the terminal error (ErrCancelled on ctrl+c, the build error from
// BuildAllDoneMsg, or nil).
func (m BuildStepsModel) Err() error { return m.err }

// Tally returns the cached/rebuilt counts accumulated from step events.
func (m BuildStepsModel) Tally() BuildTally { return m.tally }
