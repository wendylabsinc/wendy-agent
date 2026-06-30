package tui

import "github.com/charmbracelet/lipgloss"

// Emerald color palette.
const (
	Emerald50  = lipgloss.Color("#ecfdf5")
	Emerald100 = lipgloss.Color("#d1fae5")
	Emerald200 = lipgloss.Color("#a7f3d0")
	Emerald300 = lipgloss.Color("#6ee7b7")
	Emerald400 = lipgloss.Color("#34d399")
	Emerald500 = lipgloss.Color("#10b981")
	Emerald600 = lipgloss.Color("#059669")
	Emerald700 = lipgloss.Color("#047857")
	Emerald800 = lipgloss.Color("#065f46")
	Emerald900 = lipgloss.Color("#064e3b")
	Emerald950 = lipgloss.Color("#022c22")

	// Amber color palette.
	Amber500 = lipgloss.Color("#f59e0b")

	// Red color palette.
	Red500 = lipgloss.Color("#ef4444")

	// Sky color palette.
	Sky500 = lipgloss.Color("#0ea5e9")

	// Semantic aliases used across TUI components.
	ColorPrimary    = Emerald400            // titles, spinners, scanning text
	ColorAccent     = Emerald500            // progress bar, active indicators
	ColorHeaderFg   = Emerald50             // table header foreground
	ColorHeaderBg   = Emerald800            // table header background
	ColorBorder     = Emerald600            // table borders
	ColorSelectedBg = Emerald900            // table selection background
	ColorSelectedFg = Emerald100            // table selection foreground
	ColorDim        = lipgloss.Color("240") // muted/hint text (neutral gray)
	ColorNotice     = Amber500              // informational notices
	ColorError      = Red500                // error messages
	ColorInfo       = Sky500                // informational status messages
)

var (
	successStyle    = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	errorStyle      = lipgloss.NewStyle().Foreground(ColorError).Bold(true)
	warningStyle    = lipgloss.NewStyle().Foreground(ColorNotice).Bold(true)
	infoStyle       = lipgloss.NewStyle().Foreground(ColorInfo).Bold(true)
	headerStyleTUI  = lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	deviceStyleTUI  = lipgloss.NewStyle().Foreground(Emerald300).Bold(true)
	valueStyleTUI   = lipgloss.NewStyle().Bold(true)
	commandStyleTUI = lipgloss.NewStyle().Foreground(Sky500)
	pathStyleTUI    = lipgloss.NewStyle().Foreground(ColorDim).Underline(true)
	dimStyleTUI     = lipgloss.NewStyle().Foreground(ColorDim)
)

func SuccessMessage(message string) string {
	return successStyle.Render("✓ " + message)
}

func ErrorMessage(message string) string {
	return errorStyle.Render("✗ " + message)
}

func WarningMessage(message string) string {
	return warningStyle.Render("⚠ " + message)
}

func InfoMessage(message string) string {
	return infoStyle.Render("› " + message)
}

// Header styles a section title in long output.
func Header(s string) string { return headerStyleTUI.Render(s) }

// Device styles a device name so it stands out as the subject of an action.
func Device(s string) string { return deviceStyleTUI.Render(s) }

// App styles an app name. Shares Device's style; named separately for clarity.
func App(s string) string { return deviceStyleTUI.Render(s) }

// Value styles a value such as an IP, version, count, or duration.
func Value(s string) string { return valueStyleTUI.Render(s) }

// Command styles a copyable next-step command.
func Command(s string) string { return commandStyleTUI.Render(s) }

// Path styles a file path or URL.
func Path(s string) string { return pathStyleTUI.Render(s) }

// Dim styles secondary/hint text so it recedes.
func Dim(s string) string { return dimStyleTUI.Render(s) }
