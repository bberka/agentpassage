package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/sharing"
)

const toolsUsage = "usage: apx tools [list] [--json] | apx tools on|off <name>... [--skill]"

// cmdTools lists the MCP servers and skills accounts get, and changes them.
func cmdTools(args []string) error {
	asJSON := slices.Contains(args, "--json")
	skill := slices.Contains(args, "--skill")
	args = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--json" || a == "--skill" })
	verb := "list"
	if len(args) > 0 {
		verb, args = args[0], args[1:]
	}

	dataDir, err := config.DataDir()
	if err != nil {
		return err
	}
	cfg, _, err := config.Load(dataDir)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	choices, err := sharing.Open(dataDir, home, cfg.Accounts)
	if err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	items, err := sharing.List(choices, home, cwd, sharing.Agents(cfg.Accounts))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Warning:", err)
	}

	switch verb {
	case "list", "ls":
		if asJSON {
			return json.NewEncoder(os.Stdout).Encode(items)
		}
		if len(items) == 0 {
			fmt.Println("No MCP servers or skills found for your agent accounts.")
			return nil
		}
		for _, i := range items {
			note := strings.Join(i.Agents, ", ")
			if i.Project {
				note = "project"
			}
			fmt.Printf("%-4s %-6s %-28s %-20s %s\n", i.State, i.Kind, i.Name, note, i.Detail)
		}
		if n := sharing.Pending(items); n > 0 {
			fmt.Printf("%s: apx tools on|off <name>\n", sharing.PendingText(n))
		}
		return nil

	case "on", "off":
		if len(args) == 0 {
			return errors.New(toolsUsage)
		}
		kind := sharing.KindMCP
		if skill {
			kind = sharing.KindSkill
		}
		for _, name := range args {
			found := false
			for _, i := range items {
				if i.Name != name || i.Kind != kind {
					continue
				}
				if i.Project {
					return fmt.Errorf("%q is a project skill, which can't be switched off", name)
				}
				choices.Set(i, verb == "on")
				found = true
			}
			if !found {
				hint := " (add --skill for a skill)"
				if skill {
					hint = ""
				}
				return fmt.Errorf("no %s named %q%s", kind, name, hint)
			}
		}
		if err := sharing.Save(dataDir, choices); err != nil {
			return err
		}
		fmt.Printf("Turned %s %s for all accounts. It applies to new sessions.\n", verb, strings.Join(args, ", "))
		return nil
	}
	return errors.New(toolsUsage)
}
