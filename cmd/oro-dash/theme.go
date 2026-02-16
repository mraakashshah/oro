package main

import "github.com/charmbracelet/lipgloss"

// Theme defines the visual styling for the oro dashboard.
type Theme struct {
	// Legacy fields (backward compatibility)
	Primary   lipgloss.Color
	Secondary lipgloss.Color
	Success   lipgloss.Color
	Warning   lipgloss.Color
	Error     lipgloss.Color
	Muted     lipgloss.Color

	// Status colors
	ColorReady      lipgloss.Color
	ColorInProgress lipgloss.Color
	ColorBlocked    lipgloss.Color
	ColorDone       lipgloss.Color

	// Priority colors
	ColorP0 lipgloss.Color
	ColorP1 lipgloss.Color
	ColorP2 lipgloss.Color
	ColorP3 lipgloss.Color
	ColorP4 lipgloss.Color

	// Chrome colors
	ColorBorder lipgloss.Color
	ColorBg     lipgloss.Color
	ColorFg     lipgloss.Color

	// Heartbeat health colors
	ColorHealthy lipgloss.Color
	ColorWarn    lipgloss.Color
	ColorStale   lipgloss.Color
}

// Styles holds pre-computed lipgloss styles to avoid allocations during rendering.
type Styles struct {
	// Status bar styles
	DaemonOnline  lipgloss.Style
	DaemonOffline lipgloss.Style
	StatusLabel   lipgloss.Style
	StatusPrimary lipgloss.Style
	StatusWarning lipgloss.Style
	StatusSuccess lipgloss.Style

	// Common text styles
	Muted     lipgloss.Style
	Dim       lipgloss.Style
	Error     lipgloss.Style
	Bold      lipgloss.Style
	Primary   lipgloss.Style
	Success   lipgloss.Style
	Secondary lipgloss.Style

	// Board styles
	Card       lipgloss.Style
	ActiveCard lipgloss.Style
	Column     lipgloss.Style
	Header     lipgloss.Style
	IDMuted    lipgloss.Style

	// Search styles
	SearchTitle   lipgloss.Style
	SearchInput   lipgloss.Style
	SearchHelp    lipgloss.Style
	SearchResults lipgloss.Style
	NoResults     lipgloss.Style
	Highlight     lipgloss.Style

	// Help overlay styles
	HelpTitle   lipgloss.Style
	HelpKey     lipgloss.Style
	HelpDesc    lipgloss.Style
	HelpContent lipgloss.Style
	HelpFooter  lipgloss.Style

	// Detail view styles
	DetailTitle     lipgloss.Style
	DetailDimItalic lipgloss.Style
	DetailError     lipgloss.Style
	DetailBold      lipgloss.Style

	// Priority badge styles
	BadgeP0 lipgloss.Style
	BadgeP1 lipgloss.Style
	BadgeP2 lipgloss.Style
	BadgeP3 lipgloss.Style
	BadgeP4 lipgloss.Style

	// Status badge styles
	BadgeReady      lipgloss.Style
	BadgeInProgress lipgloss.Style
	BadgeBlocked    lipgloss.Style

	// Health badge styles
	HealthGreen  lipgloss.Style
	HealthAmber  lipgloss.Style
	HealthRed    lipgloss.Style
	WorkerStyle  lipgloss.Style
	BlockerStyle lipgloss.Style
}

// DefaultTheme returns the default theme for oro dash.
func DefaultTheme() Theme {
	return Theme{
		// Legacy fields (backward compatibility) - map to sensible new colors
		Primary:   lipgloss.Color("#6E56CF"), // Purple (maps to ColorP2)
		Secondary: lipgloss.Color("#E5A836"), // Amber (maps to ColorInProgress)
		Success:   lipgloss.Color("#30A46C"), // Green (maps to ColorDone)
		Warning:   lipgloss.Color("#E5A836"), // Amber (maps to ColorWarn)
		Error:     lipgloss.Color("#E5484D"), // Red (maps to ColorBlocked)
		Muted:     lipgloss.Color("#687076"), // Dim gray (maps to ColorP4)

		// Status colors
		ColorReady:      lipgloss.Color("#6E56CF"), // Purple
		ColorInProgress: lipgloss.Color("#E5A836"), // Amber
		ColorBlocked:    lipgloss.Color("#E5484D"), // Red
		ColorDone:       lipgloss.Color("#30A46C"), // Green

		// Priority colors
		ColorP0: lipgloss.Color("#E5484D"), // Critical — red
		ColorP1: lipgloss.Color("#E5A836"), // High — amber
		ColorP2: lipgloss.Color("#6E56CF"), // Medium — purple
		ColorP3: lipgloss.Color("#889096"), // Low — gray
		ColorP4: lipgloss.Color("#687076"), // Backlog — dim gray

		// Chrome colors
		ColorBorder: lipgloss.Color("#3E4347"),
		ColorBg:     lipgloss.Color("#111113"),
		ColorFg:     lipgloss.Color("#EDEEF0"),

		// Heartbeat health colors
		ColorHealthy: lipgloss.Color("#30A46C"), // Green — recent heartbeat
		ColorWarn:    lipgloss.Color("#E5A836"), // Amber — aging heartbeat
		ColorStale:   lipgloss.Color("#E5484D"), // Red — stale/dead worker
	}
}

// NewStyles creates pre-computed styles from a theme to avoid allocations during rendering.
//
//nolint:funlen // Intentionally long - initializes all styles in one place for clarity
func NewStyles(theme Theme) Styles {
	s := Styles{}
	s.initStatusBarStyles(theme)
	s.initCommonStyles(theme)
	s.initBoardStyles(theme)
	s.initSearchStyles(theme)
	s.initHelpStyles(theme)
	s.initDetailStyles(theme)
	s.initBadgeStyles(theme)
	return s
}

func (s *Styles) initStatusBarStyles(theme Theme) {
	s.DaemonOnline = lipgloss.NewStyle().Foreground(theme.Success)
	s.DaemonOffline = lipgloss.NewStyle().Foreground(theme.Error)
	s.StatusLabel = lipgloss.NewStyle()
	s.StatusPrimary = lipgloss.NewStyle().Foreground(theme.Primary)
	s.StatusWarning = lipgloss.NewStyle().Foreground(theme.Warning)
	s.StatusSuccess = lipgloss.NewStyle().Foreground(theme.Success)
}

func (s *Styles) initCommonStyles(theme Theme) {
	s.Muted = lipgloss.NewStyle().Foreground(theme.Muted)
	s.Dim = lipgloss.NewStyle().Foreground(theme.Muted).Italic(true)
	s.Error = lipgloss.NewStyle().Foreground(theme.Error)
	s.Bold = lipgloss.NewStyle().Bold(true)
	s.Primary = lipgloss.NewStyle().Foreground(theme.Primary)
	s.Success = lipgloss.NewStyle().Foreground(theme.Success)
	s.Secondary = lipgloss.NewStyle().Foreground(theme.Secondary)
}

func (s *Styles) initBoardStyles(theme Theme) {
	s.Card = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(theme.ColorBorder).Padding(1, 2)
	s.ActiveCard = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(theme.Primary).Padding(1, 2)
	s.Column = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(theme.ColorBorder).Padding(1)
	s.Header = lipgloss.NewStyle().Bold(true).Foreground(theme.Primary).Padding(0, 1)
	s.IDMuted = lipgloss.NewStyle().Foreground(theme.Muted)
}

func (s *Styles) initSearchStyles(theme Theme) {
	s.SearchTitle = lipgloss.NewStyle().Bold(true).Foreground(theme.Primary).Padding(1, 0)
	s.SearchInput = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(theme.Primary).Padding(0, 1).Width(60)
	s.SearchHelp = lipgloss.NewStyle().Foreground(theme.Muted).Padding(1, 0)
	s.SearchResults = lipgloss.NewStyle().Padding(1, 0)
	s.NoResults = lipgloss.NewStyle().Foreground(theme.Muted)
	s.Highlight = lipgloss.NewStyle().Background(theme.Primary).Foreground(lipgloss.Color("#ffffff")).Bold(true).Padding(0, 1)
}

func (s *Styles) initHelpStyles(theme Theme) {
	s.HelpTitle = lipgloss.NewStyle().Bold(true).Foreground(theme.Primary).Padding(1, 0).MarginBottom(1)
	s.HelpKey = lipgloss.NewStyle().Bold(true).Foreground(theme.Primary).Width(10)
	s.HelpDesc = lipgloss.NewStyle().Foreground(theme.ColorFg)
	s.HelpContent = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(theme.ColorBorder).Padding(2)
	s.HelpFooter = lipgloss.NewStyle().Foreground(theme.Muted).Padding(1, 0).MarginTop(1)
}

func (s *Styles) initDetailStyles(theme Theme) {
	s.DetailTitle = lipgloss.NewStyle().Bold(true).Foreground(theme.Primary)
	s.DetailDimItalic = lipgloss.NewStyle().Foreground(theme.Muted).Italic(true)
	s.DetailError = lipgloss.NewStyle().Foreground(theme.Error)
	s.DetailBold = lipgloss.NewStyle().Bold(true)
}

func (s *Styles) initBadgeStyles(theme Theme) {
	s.BadgeP0 = lipgloss.NewStyle().Foreground(theme.ColorP0).Bold(true)
	s.BadgeP1 = lipgloss.NewStyle().Foreground(theme.ColorP1).Bold(true)
	s.BadgeP2 = lipgloss.NewStyle().Foreground(theme.ColorP2).Bold(true)
	s.BadgeP3 = lipgloss.NewStyle().Foreground(theme.ColorP3).Bold(true)
	s.BadgeP4 = lipgloss.NewStyle().Foreground(theme.ColorP4).Bold(true)
	s.BadgeReady = lipgloss.NewStyle().Foreground(theme.ColorReady).Bold(true)
	s.BadgeInProgress = lipgloss.NewStyle().Foreground(theme.ColorInProgress).Bold(true)
	s.BadgeBlocked = lipgloss.NewStyle().Foreground(theme.ColorBlocked).Bold(true)
	s.HealthGreen = lipgloss.NewStyle().Foreground(theme.ColorHealthy)
	s.HealthAmber = lipgloss.NewStyle().Foreground(theme.ColorWarn)
	s.HealthRed = lipgloss.NewStyle().Foreground(theme.ColorStale)
	s.WorkerStyle = lipgloss.NewStyle().Foreground(theme.ColorInProgress)
	s.BlockerStyle = lipgloss.NewStyle().Foreground(theme.ColorBlocked)
}
