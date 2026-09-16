package ui

// Neon cabinet theme ids. Pages set html[data-theme]. Token values live in live.css.
const (
	// ThemeNeonLight is the host default (CORE-HOST-GM-074).
	ThemeNeonLight = "neon-light"
	// ThemeNeonDark is the same shapes with the dark token set.
	ThemeNeonDark = "neon-dark"
)

// DefaultTheme is the theme new pages render until /settings can flip it.
const DefaultTheme = ThemeNeonLight

// Chrome is the shared document shell. Pages pass it into ui-start templates.
type Chrome struct {
	Title string
	Theme string
}

// Page returns light neon chrome, the v1 default.
func Page(title string) Chrome {
	return Chrome{Title: title, Theme: DefaultTheme}
}

// NormalizeTheme maps unknown values to the light default.
func NormalizeTheme(value string) string {
	if value == ThemeNeonDark {
		return ThemeNeonDark
	}
	return ThemeNeonLight
}
