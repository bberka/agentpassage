package tui

import (
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/limits"
)

type limitsEditorState struct {
	accountID string // "" edits the default for every account
	title     string
	every     string
	focus     int // 0 = period, then one input per remaining limits.Fields
	inputs    []textinput.Model
	used      []string
}

var limitPeriods = []string{"1d", "2d", "3d", "4d", "5d", "6d", "7d", "14d", "30d"}

// openLimitsModal edits the usage limits of one account, or the default ones
// when acct is nil.
func (a *App) openLimitsModal(acct *config.AccountConfig) tea.Cmd {
	cfg := a.srv.Config()
	ed := &limitsEditorState{title: "all accounts"}
	own := cfg.Limits
	if acct != nil {
		ed.accountID, ed.title = acct.ID, acct.Backend+"/"+acct.Name
		own = config.LimitsConfig{}
		if acct.Limits != nil {
			own = *acct.Limits
		}
		list, err := a.srv.UsageLimits()
		if err != nil {
			a.setStatus(err.Error(), true)
		}
		ed.used = limits.Describe(cfg, *acct, list)
	}
	ed.every = own.Every
	for _, field := range limits.Fields[1:] {
		in := textinput.New()
		in.Prompt = ""
		in.CharLimit = 12
		in.Placeholder = "no limit"
		if acct != nil {
			in.Placeholder = "default"
			if v := limits.Value(cfg.Limits, field); v != "" {
				in.Placeholder = "default (" + v + ")"
			}
		}
		in.SetValue(limits.Value(own, field))
		ed.inputs = append(ed.inputs, in)
	}
	a.limitsEditor = ed
	a.modal = modalLimits
	return textinput.Blink
}

// periods lists the choices of the period field; "" is unset.
func (ed *limitsEditorState) periods() []string {
	out := append([]string{""}, limitPeriods...)
	if !slices.Contains(out, ed.every) {
		out = append(out, ed.every)
	}
	return out
}

func (a *App) updateLimitsModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	ed := a.limitsEditor
	move := func(delta int) {
		if ed.focus > 0 {
			ed.inputs[ed.focus-1].Blur()
		}
		total := len(ed.inputs) + 1
		ed.focus = (ed.focus + delta + total) % total
		if ed.focus > 0 {
			ed.inputs[ed.focus-1].Focus()
		}
	}
	switch msg.String() {
	case "esc":
		a.modal, a.limitsEditor = modalNone, nil
		return a, nil
	case "tab", "down":
		move(1)
		return a, nil
	case "shift+tab", "up":
		move(-1)
		return a, nil
	case "ctrl+a":
		if ed.accountID == "" {
			return a, nil
		}
		n, err := a.srv.AllowMore(ed.accountID)
		switch {
		case err != nil:
			a.setStatus(err.Error(), true)
		case n == 0:
			a.setStatus("No limit is reached", false)
		default:
			cfg := a.srv.Config()
			if acct := cfg.FindAccount(ed.accountID); acct != nil {
				list, _ := a.srv.UsageLimits()
				ed.used = limits.Describe(cfg, *acct, list)
			}
			a.setStatus("Allowed more for this period", false)
		}
		return a, nil
	case "enter":
		a.saveLimits()
		return a, nil
	}
	if ed.focus == 0 {
		choices := ed.periods()
		i := slices.Index(choices, ed.every)
		switch msg.String() {
		case "right", " ":
			ed.every = choices[(i+1)%len(choices)]
		case "left":
			ed.every = choices[(i+len(choices)-1)%len(choices)]
		}
		return a, nil
	}
	var cmd tea.Cmd
	ed.inputs[ed.focus-1], cmd = ed.inputs[ed.focus-1].Update(msg)
	return a, cmd
}

func (a *App) saveLimits() {
	ed := a.limitsEditor
	forAccount := ed.accountID != ""
	next := config.LimitsConfig{Every: ed.every}
	for i, field := range limits.Fields[1:] {
		if err := limits.SetValue(&next, field, ed.inputs[i].Value(), forAccount); err != nil {
			a.setStatus(err.Error(), true)
			return
		}
	}
	cfg := a.srv.Config()
	if !forAccount {
		cfg.Limits = next
	} else if acct := cfg.FindAccount(ed.accountID); acct != nil {
		acct.Limits = nil
		if next != (config.LimitsConfig{}) {
			acct.Limits = &next
		}
	}
	if err := a.srv.ReloadConfig(cfg); err != nil {
		a.setStatus("Couldn't save: "+err.Error(), true)
		return
	}
	a.modal, a.limitsEditor = modalNone, nil
	a.setStatus("Saved usage limits of "+ed.title, false)
}

// limitsSummary is the Settings row for the default limits.
func limitsSummary(c config.LimitsConfig) string {
	var parts []string
	for _, field := range limits.Fields[1:] {
		if v := limits.Value(c, field); v != "" && v != "off" {
			if field != "balance" {
				v += "%"
			}
			parts = append(parts, field+" "+v)
		}
	}
	if len(parts) == 0 {
		return "off"
	}
	every := c.Every
	if every == "" {
		every = "1d"
	}
	return strings.Join(parts, ", ") + " per " + every
}

func (a *App) viewLimitsModal() string {
	ed := a.limitsEditor
	cardW := clamp(a.width-8, 60, 92)
	innerW := cardW - 6
	lines := []string{
		a.styles.Title.Render("◆ Usage limits: " + ed.title),
		a.styles.Muted.Render("How much of a long limit may be used per period. Blank keeps the default; off removes."),
		a.styles.Divider.Render(strings.Repeat("─", innerW)),
	}
	every := ed.every
	if every == "" {
		every = "default (1d)"
		if d := a.srv.Config().Limits.Every; ed.accountID != "" && d != "" {
			every = "default (" + d + ")"
		}
	}
	rows := []struct{ label, value string }{
		{"Period", "‹ " + every + " ›"},
		{"Weekly %", ed.inputs[0].View()},
		{"Monthly %", ed.inputs[1].View()},
		{"Balance amount", ed.inputs[2].View()},
	}
	for i, r := range rows {
		mark := "  "
		if i == ed.focus {
			mark = "> "
		}
		lines = append(lines, mark+a.styles.PadCell(r.label, 18)+r.value)
	}
	if len(ed.used) > 0 {
		lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
		for _, u := range ed.used[:min(len(ed.used), max(a.height-16, 2))] {
			lines = append(lines, a.styles.Muted.Render(truncate(u, innerW)))
		}
	}
	return a.styles.Modal.Width(cardW).Render(strings.Join(lines, "\n"))
}
