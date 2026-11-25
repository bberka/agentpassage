package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/acpx/internal/config"
)

var strategyOrder = []string{
	config.StrategySticky,
	config.StrategyRoundRobin,
	config.StrategyLeastBusy,
	config.StrategyFailover,
	config.StrategyManual,
}

func (a *App) ruleHasSubscriptionAccounts(rule config.RoutingRule) bool {
	cfg := a.srv.Config()
	if len(rule.Accounts) == 0 {
		for _, acct := range cfg.Accounts {
			if acct.Kind == config.KindACP && (rule.Pattern == "*" || strings.HasPrefix(rule.Pattern, acct.Backend+"/")) {
				return true
			}
		}
		return false
	}
	for _, id := range rule.Accounts {
		if acct := cfg.FindAccount(id); acct != nil && acct.Kind == config.KindACP {
			return true
		}
	}
	return false
}

func (a *App) updateRouting(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cfg := a.srv.Config()
	rules := cfg.Routing
	if a.routingCursor >= len(rules) && len(rules) > 0 {
		a.routingCursor = len(rules) - 1
	}

	switch msg.String() {
	case "up", "k":
		if a.routingCursor > 0 {
			a.routingCursor--
		}
	case "down", "j":
		if a.routingCursor < len(rules)-1 {
			a.routingCursor++
		}
	case "a":
		return a, a.openEditModal("Add Routing Rule", "Model pattern (e.g. agy/*, gemini-4-neon, deepseek/*, or *)", "agy/*", func(val string) error {
			if val == "" {
				return fmt.Errorf("pattern is required")
			}
			cfg := a.srv.Config()
			cfg.Routing = append(cfg.Routing, config.RoutingRule{
				Pattern:  val,
				Strategy: config.StrategySticky,
			})
			_ = a.srv.ReloadConfig(cfg)
			a.setStatus("Added routing rule for "+val, false)
			return nil
		})
	}

	if len(rules) == 0 {
		return a, nil
	}

	switch msg.String() {
	case "s":
		rule := cfg.Routing[a.routingCursor]
		idx := 0
		for i, st := range strategyOrder {
			if st == rule.Strategy {
				idx = i
				break
			}
		}
		next := strategyOrder[(idx+1)%len(strategyOrder)]
		if (next == config.StrategyRoundRobin || next == config.StrategyLeastBusy) &&
			a.ruleHasSubscriptionAccounts(rule) && !rule.SubscriptionWarningAcked {
			a.warnRuleIdx = a.routingCursor
			a.warnStrategy = next
			a.modal = modalSubStrategyWarn
			return a, nil
		}
		cfg.Routing[a.routingCursor].Strategy = next
		_ = a.srv.ReloadConfig(cfg)
		a.setStatus(fmt.Sprintf("Rule %s strategy -> %s", rule.Pattern, next), false)

	case "e":
		rule := cfg.Routing[a.routingCursor]
		return a, a.openEditModal("Rule Accounts", "Comma-separated account IDs/names (blank = all matching)", strings.Join(rule.Accounts, ","), func(val string) error {
			cfg := a.srv.Config()
			var list []string
			for _, part := range strings.Split(val, ",") {
				if p := strings.TrimSpace(part); p != "" {
					list = append(list, p)
				}
			}
			cfg.Routing[a.routingCursor].Accounts = list
			_ = a.srv.ReloadConfig(cfg)
			a.setStatus("Updated accounts for rule "+rule.Pattern, false)
			return nil
		})

	case "4":
		cfg.Routing[a.routingCursor].AllowCrossAccountRetryOn429 = !cfg.Routing[a.routingCursor].AllowCrossAccountRetryOn429
		_ = a.srv.ReloadConfig(cfg)
		a.setStatus(fmt.Sprintf("Rule %s cross-account 429 retry = %v",
			cfg.Routing[a.routingCursor].Pattern, cfg.Routing[a.routingCursor].AllowCrossAccountRetryOn429), false)

	case "x", "delete":
		pat := cfg.Routing[a.routingCursor].Pattern
		cfg.Routing = append(cfg.Routing[:a.routingCursor], cfg.Routing[a.routingCursor+1:]...)
		_ = a.srv.ReloadConfig(cfg)
		a.setStatus("Deleted routing rule "+pat, false)
	}

	return a, nil
}

func (a *App) viewRouting(bodyHeight int) string {
	cfg := a.srv.Config()
	fullW := max(a.width-2, 40)
	innerW := fullW - 4

	var lines []string
	lines = append(lines, a.styles.Title.Render(fmt.Sprintf("Routing Rules (%d)", len(cfg.Routing)))+"   "+
		a.styles.Muted.Render("a:add  s:cycle strategy  e:accounts  4:429-retry  x:del"))
	lines = append(lines, a.styles.Muted.Render("Default without a rule: sticky (LRU per thread). Use round-robin to rotate threads across accounts, or Accounts→m/P for prefixed models (a-model, b-model)."))
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))

	if len(cfg.Routing) == 0 {
		lines = append(lines, "")
		lines = append(lines, a.styles.Muted.Render("  No custom rules yet. Press 'a' to add a rule (e.g. 'agy/*' or '*') and press 's' to pick round-robin/failover/sticky."))
		return a.styles.Card.Width(fullW).Height(bodyHeight - 2).Render(strings.Join(lines, "\n"))
	}

	lines = append(lines, a.styles.TableHeader.Render(fmt.Sprintf("  %-24s %-14s %-11s %s", "PATTERN", "STRATEGY", "429 RETRY", "ACCOUNTS")))

	for i, r := range cfg.Routing {
		if a.filterText != "" && !strings.Contains(strings.ToLower(r.Pattern), strings.ToLower(a.filterText)) {
			continue
		}
		accts := "(all matching)"
		if len(r.Accounts) > 0 {
			accts = strings.Join(r.Accounts, ", ")
		}
		retry429 := "off"
		if r.AllowCrossAccountRetryOn429 {
			retry429 = "on"
		}
		row := fmt.Sprintf("%-24s %-14s %-11s %s",
			truncate(r.Pattern, 24), r.Strategy, retry429, truncate(accts, max(innerW-54, 12)))
		if i == a.routingCursor {
			lines = append(lines, a.styles.Selected.Width(innerW).Render("> "+row))
		} else {
			lines = append(lines, "  "+row)
		}
	}
	return a.styles.Card.Width(fullW).Height(bodyHeight - 2).Render(strings.Join(lines, "\n"))
}
