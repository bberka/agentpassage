package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/gateway"
	"github.com/bberka/agentpassage/internal/limits"
)

const limitsUsage = `usage: apx limits [list] [--json]
       apx limits set [account] [--every 1d] [--weekly 20] [--monthly 5] [--balance 1.50]
       apx limits allow <account>
Without an account, set changes the default for every account. "off" removes a
limit; an empty value ("") returns an account to the default.`

// cmdLimits shows and changes how much of the long provider limits each
// account may use per period.
func cmdLimits(args []string) error {
	dataDir, err := config.DataDir()
	if err != nil {
		return err
	}
	cfg, _, err := config.Load(dataDir)
	if err != nil {
		return err
	}
	tracker := limits.New(dataDir)
	verb := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		verb, args = args[0], args[1:]
	}
	account := func(ref string) (*config.AccountConfig, error) {
		for i := range cfg.Accounts {
			a := &cfg.Accounts[i]
			if a.ID == ref || a.Backend+"/"+a.Name == ref {
				return a, nil
			}
		}
		return nil, fmt.Errorf("account %q not found", ref)
	}

	switch verb {
	case "list", "ls":
		list, err := tracker.Statuses(cfg, time.Now())
		if err != nil {
			return err
		}
		if len(args) == 1 && args[0] == "--json" {
			return json.NewEncoder(os.Stdout).Encode(list)
		}
		any := false
		for _, a := range cfg.Accounts {
			for _, line := range limits.Describe(cfg, a, list) {
				fmt.Printf("%-24s %s\n", a.Backend+"/"+a.Name, line)
				any = true
			}
		}
		if !any {
			fmt.Println("No usage limits set. Example: apx limits set --every 1d --weekly 20")
		}
		return nil

	case "set":
		var target *config.AccountConfig
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			if target, err = account(args[0]); err != nil {
				return err
			}
			args = args[1:]
		}
		fs := flag.NewFlagSet("apx limits set", flag.ContinueOnError)
		values := map[string]*string{}
		for _, field := range limits.Fields {
			values[field] = fs.String(field, "", field)
		}
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NFlag() == 0 || fs.NArg() > 0 {
			return errors.New(limitsUsage)
		}
		dest := &cfg.Limits
		if target != nil {
			if target.Limits == nil {
				target.Limits = &config.LimitsConfig{}
			}
			dest = target.Limits
		}
		var failed error
		fs.Visit(func(f *flag.Flag) {
			failed = errors.Join(failed, limits.SetValue(dest, f.Name, *values[f.Name], target != nil))
		})
		if failed != nil {
			return failed
		}
		if target != nil && *target.Limits == (config.LimitsConfig{}) {
			target.Limits = nil
		}
		if err := config.Save(dataDir, cfg); err != nil {
			return err
		}
		gateway.NotifyRunningDaemon(cfg)
		name := "every account (unless it sets its own)"
		if target != nil {
			name = target.Backend + "/" + target.Name
		}
		fmt.Printf("Updated the usage limits of %s.\n", name)
		return nil

	case "allow":
		if len(args) != 1 {
			return errors.New(limitsUsage)
		}
		a, err := account(args[0])
		if err != nil {
			return err
		}
		n, err := tracker.Allow(cfg, a.ID, time.Now())
		if err != nil {
			return err
		}
		if n == 0 {
			fmt.Printf("%s/%s has no reached limit.\n", a.Backend, a.Name)
			return nil
		}
		fmt.Printf("Allowed one more allowance for %s/%s in this period.\n", a.Backend, a.Name)
		return nil
	}
	return errors.New(limitsUsage)
}
