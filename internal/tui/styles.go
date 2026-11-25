package tui

import "github.com/charmbracelet/lipgloss"

type Styles struct {
	// Background tokens
	HasCanvasBg bool
	CanvasBg    lipgloss.TerminalColor
	SurfaceBg   lipgloss.TerminalColor
	ModalBg     lipgloss.TerminalColor

	// Chrome
	TopBar      lipgloss.Style
	Brand       lipgloss.Style
	TabActive   lipgloss.Style
	TabInactive lipgloss.Style
	PillRunning lipgloss.Style
	PillStopped lipgloss.Style
	FooterBar   lipgloss.Style
	KeyCap      lipgloss.Style
	KeyDesc     lipgloss.Style
	Divider     lipgloss.Style

	// Surfaces & Cards
	Card        lipgloss.Style
	CardAccent  lipgloss.Style
	Modal       lipgloss.Style
	ModalWarn   lipgloss.Style
	ModalDanger lipgloss.Style

	// Typography & Lists
	Title       lipgloss.Style
	Subtitle    lipgloss.Style
	TableHeader lipgloss.Style
	Muted       lipgloss.Style
	Selected    lipgloss.Style
	Highlight   lipgloss.Style

	// States & Banners
	StateReady lipgloss.Style
	StateBusy  lipgloss.Style
	StateCool  lipgloss.Style
	StateWarn  lipgloss.Style
	StateErr   lipgloss.Style
	BannerCrit lipgloss.Style
	BannerWarn lipgloss.Style
}

func NewStyles(theme string) Styles {
	var (
		hasBg     = true
		canvasBg  lipgloss.TerminalColor = lipgloss.Color("#0d1117")
		surfaceBg lipgloss.TerminalColor = lipgloss.Color("#161b22")
		modalBg   lipgloss.TerminalColor = lipgloss.Color("#1c2128")
		barBg     lipgloss.TerminalColor = lipgloss.Color("#161b22")
		fg        lipgloss.TerminalColor = lipgloss.Color("#e6edf3")
		accent    lipgloss.TerminalColor = lipgloss.Color("#58a6ff")
		accentFg  lipgloss.TerminalColor = lipgloss.Color("#0d1117")
		ok        lipgloss.TerminalColor = lipgloss.Color("#3fb950")
		warn      lipgloss.TerminalColor = lipgloss.Color("#d29922")
		errCol    lipgloss.TerminalColor = lipgloss.Color("#f85149")
		muted     lipgloss.TerminalColor = lipgloss.Color("#8b949e")
		border    lipgloss.TerminalColor = lipgloss.Color("#30363d")
		selBg     lipgloss.TerminalColor = lipgloss.Color("#21262d")
		selFg     lipgloss.TerminalColor = lipgloss.Color("#f0f6fc")
		keyCapFg  lipgloss.TerminalColor = lipgloss.Color("#79c0ff")
	)

	switch theme {
	case "ansi16":
		hasBg = false
		canvasBg = lipgloss.NoColor{}
		surfaceBg = lipgloss.NoColor{}
		modalBg = lipgloss.NoColor{}
		barBg = lipgloss.Color("0")
		fg = lipgloss.Color("15")
		accent = lipgloss.Color("6")
		accentFg = lipgloss.Color("0")
		ok = lipgloss.Color("2")
		warn = lipgloss.Color("3")
		errCol = lipgloss.Color("1")
		muted = lipgloss.Color("8")
		border = lipgloss.Color("8")
		selBg = lipgloss.Color("4")
		selFg = lipgloss.Color("15")
		keyCapFg = lipgloss.Color("14")

	case "transparent":
		hasBg = false
		canvasBg = lipgloss.NoColor{}
		surfaceBg = lipgloss.NoColor{}
		modalBg = lipgloss.NoColor{}
		barBg = lipgloss.AdaptiveColor{Light: "#eaeef2", Dark: "#161b22"}
		fg = lipgloss.AdaptiveColor{Light: "#1f2328", Dark: "#e6edf3"}
		accent = lipgloss.AdaptiveColor{Light: "#0969da", Dark: "#58a6ff"}
		accentFg = lipgloss.AdaptiveColor{Light: "#ffffff", Dark: "#0d1117"}
		ok = lipgloss.AdaptiveColor{Light: "#1a7f37", Dark: "#3fb950"}
		warn = lipgloss.AdaptiveColor{Light: "#9a6700", Dark: "#d29922"}
		errCol = lipgloss.AdaptiveColor{Light: "#cf222e", Dark: "#f85149"}
		muted = lipgloss.AdaptiveColor{Light: "#656d76", Dark: "#8b949e"}
		border = lipgloss.AdaptiveColor{Light: "#d0d7de", Dark: "#30363d"}
		selBg = lipgloss.AdaptiveColor{Light: "#ddf4ff", Dark: "#21262d"}
		selFg = lipgloss.AdaptiveColor{Light: "#1f2328", Dark: "#f0f6fc"}
		keyCapFg = lipgloss.AdaptiveColor{Light: "#0969da", Dark: "#79c0ff"}

	case "light":
		hasBg = true
		canvasBg = lipgloss.Color("#f6f8fa")
		surfaceBg = lipgloss.Color("#ffffff")
		modalBg = lipgloss.Color("#ffffff")
		barBg = lipgloss.Color("#eaeef2")
		fg = lipgloss.Color("#1f2328")
		accent = lipgloss.Color("#0969da")
		accentFg = lipgloss.Color("#ffffff")
		ok = lipgloss.Color("#1a7f37")
		warn = lipgloss.Color("#9a6700")
		errCol = lipgloss.Color("#cf222e")
		muted = lipgloss.Color("#656d76")
		border = lipgloss.Color("#d0d7de")
		selBg = lipgloss.Color("#ddf4ff")
		selFg = lipgloss.Color("#1f2328")
		keyCapFg = lipgloss.Color("#0969da")

	case "dark":
		// OpenCode-style deep dark palette (defaults above)

	default: // "auto" — adaptive dark/light canvas & surfaces
		hasBg = true
		canvasBg = lipgloss.AdaptiveColor{Light: "#f6f8fa", Dark: "#0d1117"}
		surfaceBg = lipgloss.AdaptiveColor{Light: "#ffffff", Dark: "#161b22"}
		modalBg = lipgloss.AdaptiveColor{Light: "#ffffff", Dark: "#1c2128"}
		barBg = lipgloss.AdaptiveColor{Light: "#eaeef2", Dark: "#161b22"}
		fg = lipgloss.AdaptiveColor{Light: "#1f2328", Dark: "#e6edf3"}
		accent = lipgloss.AdaptiveColor{Light: "#0969da", Dark: "#58a6ff"}
		accentFg = lipgloss.AdaptiveColor{Light: "#ffffff", Dark: "#0d1117"}
		ok = lipgloss.AdaptiveColor{Light: "#1a7f37", Dark: "#3fb950"}
		warn = lipgloss.AdaptiveColor{Light: "#9a6700", Dark: "#d29922"}
		errCol = lipgloss.AdaptiveColor{Light: "#cf222e", Dark: "#f85149"}
		muted = lipgloss.AdaptiveColor{Light: "#656d76", Dark: "#8b949e"}
		border = lipgloss.AdaptiveColor{Light: "#d0d7de", Dark: "#30363d"}
		selBg = lipgloss.AdaptiveColor{Light: "#ddf4ff", Dark: "#21262d"}
		selFg = lipgloss.AdaptiveColor{Light: "#1f2328", Dark: "#f0f6fc"}
		keyCapFg = lipgloss.AdaptiveColor{Light: "#0969da", Dark: "#79c0ff"}
	}

	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Foreground(fg).
		Padding(0, 1)
	cardAccent := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accent).
		Foreground(fg).
		Padding(1, 2)
	modal := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accent).
		Foreground(fg).
		Padding(1, 2)
	modalWarn := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(warn).
		Foreground(fg).
		Padding(1, 2)
	modalDanger := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(errCol).
		Foreground(fg).
		Padding(1, 2)

	if hasBg {
		card = card.Background(surfaceBg).BorderBackground(surfaceBg)
		cardAccent = cardAccent.Background(modalBg).BorderBackground(modalBg)
		modal = modal.Background(modalBg).BorderBackground(modalBg)
		modalWarn = modalWarn.Background(modalBg).BorderBackground(modalBg)
		modalDanger = modalDanger.Background(modalBg).BorderBackground(modalBg)
	}

	return Styles{
		HasCanvasBg: hasBg,
		CanvasBg:    canvasBg,
		SurfaceBg:   surfaceBg,
		ModalBg:     modalBg,

		TopBar:      lipgloss.NewStyle().Background(barBg).Foreground(fg).Padding(0, 1),
		Brand:       lipgloss.NewStyle().Bold(true).Foreground(accentFg).Background(accent).Padding(0, 1),
		TabActive:   lipgloss.NewStyle().Bold(true).Foreground(accent).Background(barBg).Underline(true).Padding(0, 1),
		TabInactive: lipgloss.NewStyle().Foreground(muted).Background(barBg).Padding(0, 1),
		PillRunning: lipgloss.NewStyle().Bold(true).Foreground(ok).Background(barBg),
		PillStopped: lipgloss.NewStyle().Bold(true).Foreground(errCol).Background(barBg),
		FooterBar:   lipgloss.NewStyle().Background(barBg).Foreground(fg).Padding(0, 1),
		KeyCap:      lipgloss.NewStyle().Bold(true).Foreground(keyCapFg),
		KeyDesc:     lipgloss.NewStyle().Foreground(muted),
		Divider:     lipgloss.NewStyle().Foreground(border),

		Card:        card,
		CardAccent:  cardAccent,
		Modal:       modal,
		ModalWarn:   modalWarn,
		ModalDanger: modalDanger,

		Title:       lipgloss.NewStyle().Bold(true).Foreground(fg),
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
		BannerCrit: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ffffff")).Background(errCol).Padding(0, 1),
		BannerWarn: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0d1117")).Background(warn).Padding(0, 1),
	}
}

func (s Styles) KeyHint(key, desc string) string {
	return s.KeyCap.Render(key) + " " + s.KeyDesc.Render(desc)
}

func (s Styles) Place(width, height int, hPos, vPos lipgloss.Position, content string) string {
	if s.HasCanvasBg {
		return lipgloss.Place(width, height, hPos, vPos, content, lipgloss.WithWhitespaceBackground(s.CanvasBg))
	}
	return lipgloss.Place(width, height, hPos, vPos, content)
}
