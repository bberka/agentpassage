package tui

import "github.com/charmbracelet/lipgloss"

type Styles struct {
	// Chrome
	TopBar        lipgloss.Style
	Brand         lipgloss.Style
	TabActive     lipgloss.Style
	TabInactive   lipgloss.Style
	PillRunning   lipgloss.Style
	PillStopped   lipgloss.Style
	FooterBar     lipgloss.Style
	KeyCap        lipgloss.Style
	KeyDesc       lipgloss.Style
	Divider       lipgloss.Style

	// Surfaces & Cards
	Card          lipgloss.Style
	CardAccent    lipgloss.Style
	Modal         lipgloss.Style
	ModalWarn     lipgloss.Style
	ModalDanger   lipgloss.Style

	// Typography & Lists
	Title         lipgloss.Style
	Subtitle      lipgloss.Style
	TableHeader   lipgloss.Style
	Muted         lipgloss.Style
	Selected      lipgloss.Style
	Highlight     lipgloss.Style

	// States & Banners
	StateReady    lipgloss.Style
	StateBusy     lipgloss.Style
	StateCool     lipgloss.Style
	StateWarn     lipgloss.Style
	StateErr      lipgloss.Style
	BannerCrit    lipgloss.Style
	BannerWarn    lipgloss.Style
}

func NewStyles(theme string) Styles {
	// Adaptive ANSI / 256-safe palette. "ansi16" strictly stays within ANSI 0-15.
	var (
		accent     lipgloss.TerminalColor = lipgloss.Color("6")  // cyan
		accentFg   lipgloss.TerminalColor = lipgloss.Color("0")
		ok         lipgloss.TerminalColor = lipgloss.Color("2")  // green
		warn       lipgloss.TerminalColor = lipgloss.Color("3")  // yellow
		errCol     lipgloss.TerminalColor = lipgloss.Color("1")  // red
		muted      lipgloss.TerminalColor = lipgloss.Color("8")  // gray
		border     lipgloss.TerminalColor = lipgloss.Color("8")
		selBg      lipgloss.TerminalColor = lipgloss.Color("4")  // blue
		selFg      lipgloss.TerminalColor = lipgloss.Color("15") // bright white
		barBg      lipgloss.TerminalColor = lipgloss.Color("0")
		keyCapFg   lipgloss.TerminalColor = lipgloss.Color("14")
	)

	switch theme {
	case "ansi16":
		// strictly ANSI 0-15 defaults above
	case "light":
		accent = lipgloss.Color("25")
		accentFg = lipgloss.Color("255")
		ok = lipgloss.Color("28")
		warn = lipgloss.Color("130")
		errCol = lipgloss.Color("160")
		muted = lipgloss.Color("243")
		border = lipgloss.Color("249")
		selBg = lipgloss.Color("25")
		selFg = lipgloss.Color("255")
		barBg = lipgloss.Color("254")
		keyCapFg = lipgloss.Color("25")
	case "dark":
		accent = lipgloss.Color("81")
		accentFg = lipgloss.Color("16")
		ok = lipgloss.Color("78")
		warn = lipgloss.Color("221")
		errCol = lipgloss.Color("203")
		muted = lipgloss.Color("244")
		border = lipgloss.Color("239")
		selBg = lipgloss.Color("237")
		selFg = lipgloss.Color("255")
		barBg = lipgloss.Color("234")
		keyCapFg = lipgloss.Color("81")
	default: // "auto" — OpenCode-inspired clean adaptive palette
		accent = lipgloss.AdaptiveColor{Light: "25", Dark: "81"}
		accentFg = lipgloss.AdaptiveColor{Light: "255", Dark: "16"}
		ok = lipgloss.AdaptiveColor{Light: "28", Dark: "78"}
		warn = lipgloss.AdaptiveColor{Light: "130", Dark: "221"}
		errCol = lipgloss.AdaptiveColor{Light: "160", Dark: "203"}
		muted = lipgloss.AdaptiveColor{Light: "243", Dark: "244"}
		border = lipgloss.AdaptiveColor{Light: "250", Dark: "239"}
		selBg = lipgloss.AdaptiveColor{Light: "253", Dark: "236"}
		selFg = lipgloss.AdaptiveColor{Light: "16", Dark: "255"}
		barBg = lipgloss.AdaptiveColor{Light: "254", Dark: "234"}
		keyCapFg = lipgloss.AdaptiveColor{Light: "25", Dark: "81"}
	}

	return Styles{
		TopBar:      lipgloss.NewStyle().Background(barBg).Padding(0, 1),
		Brand:       lipgloss.NewStyle().Bold(true).Foreground(accentFg).Background(accent).Padding(0, 1),
		TabActive:   lipgloss.NewStyle().Bold(true).Foreground(accent).Underline(true).Padding(0, 1),
		TabInactive: lipgloss.NewStyle().Foreground(muted).Padding(0, 1),
		PillRunning: lipgloss.NewStyle().Bold(true).Foreground(ok),
		PillStopped: lipgloss.NewStyle().Bold(true).Foreground(errCol),
		FooterBar:   lipgloss.NewStyle().Background(barBg).Padding(0, 1),
		KeyCap:      lipgloss.NewStyle().Bold(true).Foreground(keyCapFg),
		KeyDesc:     lipgloss.NewStyle().Foreground(muted),
		Divider:     lipgloss.NewStyle().Foreground(border),

		Card: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(border).
			Padding(0, 1),
		CardAccent: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(accent).
			Padding(1, 2),
		Modal: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(accent).
			Padding(1, 2),
		ModalWarn: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(warn).
			Padding(1, 2),
		ModalDanger: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(errCol).
			Padding(1, 2),

		Title:       lipgloss.NewStyle().Bold(true),
		Subtitle:    lipgloss.NewStyle().Bold(true).Foreground(accent),
		TableHeader: lipgloss.NewStyle().Bold(true).Foreground(muted),
		Muted:       lipgloss.NewStyle().Foreground(muted),
		Selected:    lipgloss.NewStyle().Bold(true).Foreground(selFg).Background(selBg),
		Highlight:   lipgloss.NewStyle().Bold(true).Foreground(accent),

		StateReady: lipgloss.NewStyle().Bold(true).Foreground(ok),
		StateBusy:  lipgloss.NewStyle().Bold(true).Foreground(accent),
		StateCool:  lipgloss.NewStyle().Bold(true).Foreground(warn),
		StateWarn:  lipgloss.NewStyle().Bold(true).Foreground(warn),
		StateErr:   lipgloss.NewStyle().Bold(true).Foreground(errCol),
		BannerCrit: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(errCol).Padding(0, 1),
		BannerWarn: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(warn).Padding(0, 1),
	}
}

func (s Styles) KeyHint(key, desc string) string {
	return s.KeyCap.Render(key) + " " + s.KeyDesc.Render(desc)
}
