package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Theme holds all colors for a theme.
type Theme struct {
	Name      string
	Primary   color.Color
	Secondary color.Color
	Success   color.Color
	Warning   color.Color
	Error     color.Color
	Muted     color.Color
	Text      color.Color
	LLMText   color.Color
	Border    color.Color
}

var (
	darkTheme = Theme{
		Name:      "dark",
		Primary:   lipgloss.Color("#B794F4"), // lighter purple
		Secondary: lipgloss.Color("#63E6F0"), // brighter cyan
		Success:   lipgloss.Color("#68D391"), // brighter green
		Warning:   lipgloss.Color("#FBD38D"), // brighter amber
		Error:     lipgloss.Color("#FC8181"), // brighter red
		Muted:     lipgloss.Color("#A0AEC0"), // lighter gray
		Text:      lipgloss.Color("#F7FAFC"), // near-white
		LLMText:   lipgloss.Color("#E2E8F0"), // light gray
		Border:    lipgloss.Color("#718096"), // medium gray
	}

	lightTheme = Theme{
		Name:      "light",
		Primary:   lipgloss.Color("#6B21A8"), // deep purple
		Secondary: lipgloss.Color("#0E7490"), // dark cyan
		Success:   lipgloss.Color("#047857"), // dark green
		Warning:   lipgloss.Color("#B45309"), // dark amber
		Error:     lipgloss.Color("#B91C1C"), // dark red
		Muted:     lipgloss.Color("#4B5563"), // dark gray
		Text:      lipgloss.Color("#1F2937"), // near-black
		LLMText:   lipgloss.Color("#374151"), // dark gray
		Border:    lipgloss.Color("#9CA3AF"), // medium gray
	}

	themes     = []Theme{darkTheme, lightTheme}
	themeIndex = 0
)

func currentTheme() Theme {
	return themes[themeIndex]
}

func cycleTheme() Theme {
	themeIndex = (themeIndex + 1) % len(themes)
	return currentTheme()
}

func setThemeByName(name string) {
	for i, t := range themes {
		if t.Name == name {
			themeIndex = i
			return
		}
	}
}

// Style builders that read from current theme.

func titleStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(currentTheme().Primary).PaddingLeft(1)
}

func headerStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(currentTheme().Secondary).PaddingLeft(1)
}

func statusRunningStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(currentTheme().Success).Bold(true)
}

func statusPausedStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(currentTheme().Warning).Bold(true)
}

func concernWarningStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(currentTheme().Warning)
}

func concernDangerStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(currentTheme().Error).Bold(true)
}

func concernInfoStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(currentTheme().Muted)
}

func llmResponseStyle() lipgloss.Style {
	return lipgloss.NewStyle().PaddingLeft(2).Foreground(currentTheme().LLMText)
}

func mutedStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(currentTheme().Muted)
}

func textStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(currentTheme().Text)
}

func helpStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(currentTheme().Muted).PaddingLeft(1)
}

func helpKeyStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(currentTheme().Secondary).Bold(true).PaddingLeft(1)
}

func errorStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(currentTheme().Error)
}

func broadcastStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(currentTheme().Secondary).Italic(true)
}

func userMsgStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(currentTheme().Success)
}

func spinnerStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(currentTheme().Secondary)
}

// statusDot returns a colored status indicator for tracked items.
// Colors: Open=cyan, InProgress=amber, Closed=green, Blocked=red, Merged=green, Review=magenta.
func statusDot(status string) string {
	t := currentTheme()
	switch status {
	case "Revew":
		return lipgloss.NewStyle().Foreground(t.Primary).Render("\u25cf")
	case "InProg":
		return lipgloss.NewStyle().Foreground(t.Warning).Render("\u25cf")
	case "Blckd":
		return lipgloss.NewStyle().Foreground(t.Error).Render("\u25cf")
	case "Mrgd":
		return lipgloss.NewStyle().Foreground(t.Success).Render("\u2713")
	case "Closd":
		return lipgloss.NewStyle().Foreground(t.Success).Render("\u2715")
	default: // Open
		return lipgloss.NewStyle().Foreground(t.Secondary).Render("\u25cb")
	}
}
