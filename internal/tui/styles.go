package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// catppuccin holds the full named palette for a Catppuccin flavor.
type catppuccin struct {
	Rosewater color.Color
	Flamingo  color.Color
	Pink      color.Color
	Mauve     color.Color
	Red       color.Color
	Maroon    color.Color
	Peach     color.Color
	Yellow    color.Color
	Green     color.Color
	Teal      color.Color
	Sky       color.Color
	Sapphire  color.Color
	Blue      color.Color
	Lavender  color.Color
	Text      color.Color
	Subtext1  color.Color
	Subtext0  color.Color
	Overlay2  color.Color
	Overlay1  color.Color
	Overlay0  color.Color
	Surface2  color.Color
	Surface1  color.Color
	Surface0  color.Color
	Base      color.Color
	Mantle    color.Color
	Crust     color.Color
}

var mochaPalette = catppuccin{
	Rosewater: lipgloss.Color("#f5e0dc"),
	Flamingo:  lipgloss.Color("#f2cdcd"),
	Pink:      lipgloss.Color("#f5c2e7"),
	Mauve:     lipgloss.Color("#cba6f7"),
	Red:       lipgloss.Color("#f38ba8"),
	Maroon:    lipgloss.Color("#eba0ac"),
	Peach:     lipgloss.Color("#fab387"),
	Yellow:    lipgloss.Color("#f9e2af"),
	Green:     lipgloss.Color("#a6e3a1"),
	Teal:      lipgloss.Color("#94e2d5"),
	Sky:       lipgloss.Color("#89dceb"),
	Sapphire:  lipgloss.Color("#74c7ec"),
	Blue:      lipgloss.Color("#89b4fa"),
	Lavender:  lipgloss.Color("#b4befe"),
	Text:      lipgloss.Color("#cdd6f4"),
	Subtext1:  lipgloss.Color("#bac2de"),
	Subtext0:  lipgloss.Color("#a6adc8"),
	Overlay2:  lipgloss.Color("#9399b2"),
	Overlay1:  lipgloss.Color("#7f849c"),
	Overlay0:  lipgloss.Color("#6c7086"),
	Surface2:  lipgloss.Color("#585b70"),
	Surface1:  lipgloss.Color("#45475a"),
	Surface0:  lipgloss.Color("#313244"),
	Base:      lipgloss.Color("#1e1e2e"),
	Mantle:    lipgloss.Color("#181825"),
	Crust:     lipgloss.Color("#11111b"),
}

var lattePalette = catppuccin{
	Rosewater: lipgloss.Color("#dc8a78"),
	Flamingo:  lipgloss.Color("#dd7878"),
	Pink:      lipgloss.Color("#ea76cb"),
	Mauve:     lipgloss.Color("#8839ef"),
	Red:       lipgloss.Color("#d20f39"),
	Maroon:    lipgloss.Color("#e64553"),
	Peach:     lipgloss.Color("#fe640b"),
	Yellow:    lipgloss.Color("#df8e1d"),
	Green:     lipgloss.Color("#40a02b"),
	Teal:      lipgloss.Color("#179299"),
	Sky:       lipgloss.Color("#04a5e5"),
	Sapphire:  lipgloss.Color("#209fb5"),
	Blue:      lipgloss.Color("#1e66f5"),
	Lavender:  lipgloss.Color("#7287fd"),
	Text:      lipgloss.Color("#4c4f69"),
	Subtext1:  lipgloss.Color("#5c5f77"),
	Subtext0:  lipgloss.Color("#6c6f85"),
	Overlay2:  lipgloss.Color("#7c7f93"),
	Overlay1:  lipgloss.Color("#8c8fa1"),
	Overlay0:  lipgloss.Color("#9ca0b0"),
	Surface2:  lipgloss.Color("#acb0be"),
	Surface1:  lipgloss.Color("#bcc0cc"),
	Surface0:  lipgloss.Color("#ccd0da"),
	Base:      lipgloss.Color("#eff1f5"),
	Mantle:    lipgloss.Color("#e6e9ef"),
	Crust:     lipgloss.Color("#dce0e8"),
}

// Theme holds all semantic colors for a theme.
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
	// Tab bar colors.
	TabActiveFg color.Color
	TabActiveBg color.Color
	TabInactive color.Color
}

func themeFromPalette(name string, p catppuccin) Theme {
	return Theme{
		Name:        name,
		Primary:     p.Blue,
		Secondary:   p.Teal,
		Success:     p.Green,
		Warning:     p.Yellow,
		Error:       p.Red,
		Muted:       p.Overlay1,
		Text:        p.Text,
		LLMText:     p.Subtext1,
		Border:      p.Surface2,
		TabActiveFg: p.Blue,
		TabActiveBg: p.Surface0,
		TabInactive: p.Overlay0,
	}
}

var (
	darkTheme  = themeFromPalette("mocha", mochaPalette)
	lightTheme = themeFromPalette("latte", lattePalette)

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

func warningStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(currentTheme().Warning).Bold(true)
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

func tabActiveStyle() lipgloss.Style {
	t := currentTheme()
	return lipgloss.NewStyle().Bold(true).Foreground(t.TabActiveFg).Background(t.TabActiveBg).Padding(0, 1)
}

func tabInactiveStyle() lipgloss.Style {
	t := currentTheme()
	return lipgloss.NewStyle().Foreground(t.TabInactive).Padding(0, 1)
}

// statusDot returns a colored status indicator for tracked items.
// Colors: Open=cyan, InProgress=amber, Closed=green, Blocked=red, Merged=green, Review=magenta, Bailed=red.
func statusDot(status string) string {
	t := currentTheme()
	switch status {
	case "Revew":
		return lipgloss.NewStyle().Foreground(t.Primary).Render("\u25cf")
	case "InProg":
		return lipgloss.NewStyle().Foreground(t.Warning).Render("\u25cf")
	case "Blckd":
		return lipgloss.NewStyle().Foreground(t.Error).Render("\u25cf")
	case "Baild", "Faild":
		return lipgloss.NewStyle().Foreground(t.Error).Render("\u2718")
	case "Mrgd":
		return lipgloss.NewStyle().Foreground(t.Success).Render("\u2713")
	case "Closd":
		return lipgloss.NewStyle().Foreground(t.Success).Render("\u2715")
	default: // Open
		return lipgloss.NewStyle().Foreground(t.Secondary).Render("\u25cb")
	}
}
