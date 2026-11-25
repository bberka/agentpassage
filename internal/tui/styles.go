package tui

import "github.com/charmbracelet/lipgloss"

type Styles struct {
	Header      lipgloss.Style
	TabActive   lipgloss.Style
	TabInactive lipgloss.Style
	Title       lipgloss.Style
	Subtitle    lipgloss.Style
	Muted       lipgloss.Style
	Box         lipgloss.Style
	Modal       lipgloss.Style
	Selected    lipgloss.Style
	StateReady  lipgloss.Style
	StateBusy   lipgloss.Style
	StateCool   lipgloss.Style
	StateWarn   lipgloss.Style
	StateErr    lipgloss.Style
	BannerCrit  lipgloss.Style
	BannerWarn  lipgloss.Style
	StatusBar   lipgloss.Style
	KeyHint     lipgloss.Style
}

func NewStyles(theme string) Styles {
	// Default uses ANSI 16-color palette indices (0-15) so it degrades cleanly over SSH and 16-color terminals.
	accent := lipgloss.Color("6")  // cyan
	ok := lipgloss.Color("2")      // green
	warn := lipgloss.Color("3")    // yellow
	errCol := lipgloss.Color("1")  // red
	muted := lipgloss.Color("8")   // bright black / gray
	border := lipgloss.Color("8")
	selBg := lipgloss.Color("4")   // blue
	selFg := lipgloss.Color("15")  // bright white

	switch theme {
	case "light":
		accent = lipgloss.Color("4")
		muted = lipgloss.Color("8")
		selBg = lipgloss.Color("12")
		selFg = lipgloss.Color("0")
	case "dark":
		accent = lipgloss.Color("14")
		border = lipgloss.Color("8")
	case "ansi16":
		// strictly base 16 ANSI colors (already default)
	}

	return Styles{
		Header:      lipgloss.NewStyle().Bold(true).Foreground(accent),
		TabActive:   lipgloss.NewStyle().Bold(true).Foreground(selFg).Background(accent).Padding(0, 1),
		TabInactive: lipgloss.NewStyle().Foreground(muted).Padding(0, 1),
		Title:       lipgloss.NewStyle().Bold(true),
		Subtitle:    lipgloss.NewStyle().Bold(true).Foreground(accent),
		Muted:       lipgloss.NewStyle().Foreground(muted),
		Box:         lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(border).Padding(0, 1),
		Modal:       lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).BorderForeground(warn).Padding(1, 2),
		Selected:    lipgloss.NewStyle().Bold(true).Foreground(selFg).Background(selBg),
		StateReady:  lipgloss.NewStyle().Bold(true).Foreground(ok),
		StateBusy:   lipgloss.NewStyle().Bold(true).Foreground(accent),
		StateCool:   lipgloss.NewStyle().Bold(true).Foreground(warn),
		StateWarn:   lipgloss.NewStyle().Bold(true).Foreground(warn),
		StateErr:    lipgloss.NewStyle().Bold(true).Foreground(errCol),
		BannerCrit:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(errCol).Padding(0, 1),
		BannerWarn:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(warn).Padding(0, 1),
		StatusBar:   lipgloss.NewStyle().Foreground(muted),
		KeyHint:     lipgloss.NewStyle().Bold(true).Foreground(accent),
	}
}
