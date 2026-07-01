package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// StepStatus is the lifecycle state of a step in a StepsModel.
type StepStatus int

const (
	StepRunning StepStatus = iota
	StepCached
	StepDone
	StepFailed
)

// Step lifecycle messages, sent to a running StepsModel via (*tea.Program).Send.
type (
	// StepStartMsg adds a step (or moves an existing one back to running).
	StepStartMsg struct {
		ID    int
		Label string
	}
	// StepDetailMsg sets the trailing detail of a running step (e.g. a byte
	// count like "1.2/3.0 GiB"). An empty Detail restores the auto elapsed timer.
	StepDetailMsg struct {
		ID     int
		Detail string
	}
	// StepDoneMsg marks a step finished. Cached renders "⚡ cached" instead of a
	// duration (for work that was skipped because a cache was warm).
	StepDoneMsg struct {
		ID     int
		Cached bool
	}
	// StepFailMsg marks a step failed (✗).
	StepFailMsg struct{ ID int }
	// StepsDoneMsg ends the program; Err is nil on success.
	StepsDoneMsg struct{ Err error }
)

type stepRow struct {
	id     int
	label  string
	status StepStatus
	start  time.Time
	dur    time.Duration
	detail string
}

// StepsModel renders a live, Docker BuildKit-style list of named steps: a spinner
// while running, a per-step elapsed timer (or a caller-supplied detail such as a
// byte count), and ✓/⚡/✗ terminal states with durations. Unlike BuildStepsModel
// it is driven by explicit Step* messages rather than a buildx log parser, for
// flows (e.g. Thor flashing) that have no buildkit vertices to parse.
type StepsModel struct {
	title   string
	rows    []stepRow
	byID    map[int]int
	spinner spinner.Model
	done    bool
	err     error
}

// NewStepsModel returns a model with the given title (the spinner line shown
// above the steps, e.g. "Flashing WendyOS nightly-…").
func NewStepsModel(title string) StepsModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(ColorPrimary)
	return StepsModel{
		title:   title,
		byID:    map[int]int{},
		spinner: s,
	}
}

// Init implements tea.Model.
func (m StepsModel) Init() tea.Cmd { return m.spinner.Tick }

// Update implements tea.Model.
func (m StepsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
	case StepStartMsg:
		if i, ok := m.byID[msg.ID]; ok {
			m.rows[i].status = StepRunning
			m.rows[i].start = time.Now()
			m.rows[i].detail = ""
			return m, nil
		}
		m.rows = append(m.rows, stepRow{id: msg.ID, label: msg.Label, status: StepRunning, start: time.Now()})
		m.byID[msg.ID] = len(m.rows) - 1
	case StepDetailMsg:
		if i, ok := m.byID[msg.ID]; ok {
			m.rows[i].detail = msg.Detail
		}
	case StepDoneMsg:
		if i, ok := m.byID[msg.ID]; ok {
			m.rows[i].dur = time.Since(m.rows[i].start)
			m.rows[i].detail = ""
			if msg.Cached {
				m.rows[i].status = StepCached
			} else {
				m.rows[i].status = StepDone
			}
		}
	case StepFailMsg:
		if i, ok := m.byID[msg.ID]; ok {
			m.rows[i].dur = time.Since(m.rows[i].start)
			m.rows[i].status = StepFailed
		}
	case StepsDoneMsg:
		m.done = true
		m.err = msg.Err
		return m, tea.Quit
	}
	return m, nil
}

var (
	stepCheck = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	stepCache = lipgloss.NewStyle().Foreground(ColorPrimary)
	stepCross = lipgloss.NewStyle().Foreground(ColorError).Bold(true)
	stepDim   = lipgloss.NewStyle().Foreground(ColorDim)
	stepTitle = lipgloss.NewStyle().Foreground(ColorPrimary)
)

const stepsLabelWidth = 26

// View implements tea.Model.
func (m StepsModel) View() string {
	if m.done {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s %s\n", m.spinner.View(), stepTitle.Render(m.title)))
	for _, r := range m.rows {
		label := padLabel(r.label, stepsLabelWidth)
		switch r.status {
		case StepRunning:
			trail := time.Since(r.start).Round(time.Second).String()
			if r.detail != "" {
				trail += " · " + r.detail
			}
			sb.WriteString(fmt.Sprintf("  %s %s %s\n", m.spinner.View(), label, stepDim.Render(trail)))
		case StepCached:
			sb.WriteString(fmt.Sprintf("  %s %s %s\n", stepCache.Render("⚡"), label, stepDim.Render("cached")))
		case StepDone:
			sb.WriteString(fmt.Sprintf("  %s %s %s\n", stepCheck.Render("✓"), label, stepDim.Render(r.dur.Round(time.Second).String())))
		case StepFailed:
			sb.WriteString(fmt.Sprintf("  %s %s\n", stepCross.Render("✗"), label))
		}
	}
	return sb.String()
}

// Err returns the terminal error (ErrCancelled on ctrl+c, the error from
// StepsDoneMsg, or nil).
func (m StepsModel) Err() error { return m.err }

// padLabel left-aligns s in a field of width runes, truncating with an ellipsis
// when too long so trailing detail columns line up.
func padLabel(s string, width int) string {
	r := []rune(s)
	if len(r) > width {
		if width <= 1 {
			return string(r[:width])
		}
		return string(r[:width-1]) + "…"
	}
	return s + strings.Repeat(" ", width-len(r))
}
