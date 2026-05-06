package components

import (
	"time"

	"oro/pkg/mg"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ToastLevel controls the style of a toast message.
type ToastLevel int

const (
	ToastInfo ToastLevel = iota
	ToastSuccess
	ToastWarn
	ToastError
)

// Toast is a transient notification message that auto-dismisses.
type Toast struct {
	Message   string
	Level     ToastLevel
	ExpiresAt time.Time
}

// ToastDismissMsg signals that the current toast should be cleared.
type ToastDismissMsg struct{}

// ShowToast creates a new toast and returns a dismiss command.
//
//oro:testonly
func ShowToast(message string, level ToastLevel, duration time.Duration) (Toast, tea.Cmd) {
	t := Toast{
		Message:   message,
		Level:     level,
		ExpiresAt: time.Now().Add(duration),
	}
	cmd := tea.Tick(duration, func(time.Time) tea.Msg {
		return ToastDismissMsg{}
	})
	return t, cmd
}

// View renders the toast bar.
func (t Toast) View(width int) string {
	if t.Message == "" {
		return ""
	}

	var style lipgloss.Style
	switch t.Level {
	case ToastSuccess:
		style = mg.ToastSuccess
	case ToastWarn:
		style = mg.ToastWarn
	case ToastError:
		style = mg.ToastError
	default:
		style = mg.ToastInfo
	}

	return style.Width(width).Render(t.Message)
}

// Active returns true if the toast has a message and hasn't expired.
func (t Toast) Active() bool {
	return t.Message != "" && time.Now().Before(t.ExpiresAt)
}
