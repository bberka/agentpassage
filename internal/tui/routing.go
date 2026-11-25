package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/acpx/internal/config"
)

var strategyOrder = []string{
	config.StrategySticky,
	config.StrategyManual,
	config.StrategyFailover,
	config.StrategyRoundRobin,
	config.StrategyLeastBusy,
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
		return a, a.openEditModal("Add Routing Rule", "Model pattern (e.g. agy/*, deepseek/*, or *)", "agy/*", func(val string) error {
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
	case "s": // cycle strategy
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

	case "e": // edit accounts list for rule
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

	case "4": // toggle allow cross-account retry on 429
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
	var lines []string
	lines = append(lines, a.styles.Title.Render(fmt.Sprintf("Routing Rules (%d)", len(cfg.Routing)))+"  "+
		a.styles.Muted.Render("a:add  s:strategy  e:accounts  4:429-retry  x:del"))
	lines = append(lines, a.styles.Muted.Render("Default strategy without a matching rule: sticky (LRU for new sessions)."))
	lines = append(lines, "")

	if len(cfg.Routing) == 0 {
		lines = append(lines, a.styles.Muted.Render("No custom rules yet. Press 'a' to create a per-model routing rule."))
		return strings.Join(lines, "\n")
	}

	for i, r := range cfg.Routing {
		if a.filterText != "" && !strings.Contains(strings.ToLower(r.Pattern), strings.ToLower(a.filterText)) {
			continue
		}
		accts := "(all matching)"
		if len(r.Accounts) > 0 {
			accts = strings.Join(r.Accounts, ",")
		}
		retry429 := "off"
		if r.AllowCrossAccountRetryOn429 {
			retry429 = "on"
		}
		row := fmt.Sprintf("%-22s %-12s 429-retry:%-3s accounts:%s",
			truncate(r.Pattern, 22), r.Strategy, retry429, truncate(accts, max(a.width-52, 12)))
		if i == a.routingCursor {
			lines = append(lines, a.styles.Selected.Render("> "+row))
		} else {
			lines = append(lines, "  "+row)
		}
	}
	return strings.Join(lines, "\n")
}
