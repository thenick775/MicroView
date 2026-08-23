package theming

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

const (
	ModeSystem = "System"
	ModeDark   = "Dark"
	ModeLight  = "Light"
)

type forcedVariant struct {
	fyne.Theme
	variant fyne.ThemeVariant
}

func ThemeForMode(mode string) fyne.Theme {
	switch mode {
	case ModeDark:
		return &forcedVariant{Theme: theme.DefaultTheme(), variant: theme.VariantDark}
	case ModeLight:
		return &forcedVariant{Theme: theme.DefaultTheme(), variant: theme.VariantLight}
	default:
		return theme.DefaultTheme()
	}
}

func (f *forcedVariant) Color(name fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	return f.Theme.Color(name, f.variant)
}
