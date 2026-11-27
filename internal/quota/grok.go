package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bberka/agentpassage/internal/config"
)

// GrokBillingURL is the Grok CLI's billing endpoint. It reports the plan's
// credit usage for the current period, read with the CLI's own login token.
const GrokBillingURL = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"

// grokCLIVersion is sent as the client version; the endpoint expects a Grok CLI client.
const grokCLIVersion = "1.0.77"

// grokHome returns the Grok CLI home (~/.grok) apx uses for this account.
func grokHome(acct config.AccountConfig, dataDir string) (string, error) {
	if h := strings.TrimSpace(acct.CustomEnv["GROK_HOME"]); h != "" {
		return h, nil
	}
	if acct.UseHostProfile {
		if h := strings.TrimSpace(os.Getenv("GROK_HOME")); h != "" {
			return h, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".grok"), nil
	}
	pdir, err := config.ProfileDir(dataDir, acct.Backend, acct.Name)
	if err != nil {
		return "", err
	}
	return filepath.Join(pdir, ".grok"), nil
}

type grokCredential struct {
	Key       string `json:"key"`
	ExpiresAt string `json:"expires_at"`
}

// readGrokToken returns the login token from <grok home>/auth.json and when it expires.
func readGrokToken(home string) (string, time.Time, error) {
	raw, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		return "", time.Time{}, err
	}
	var creds map[string]grokCredential
	if err := json.Unmarshal(raw, &creds); err != nil {
		return "", time.Time{}, err
	}
	for _, c := range creds {
		if key := strings.TrimSpace(c.Key); key != "" {
			exp, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(c.ExpiresAt))
			return key, exp, nil
		}
	}
	return "", time.Time{}, fmt.Errorf("no Grok login in %s", filepath.Join(home, "auth.json"))
}

// fetchGrokBilling reads the plan's usage from Grok's billing endpoint. When the
// stored token has expired it runs `grok models` once, which makes the CLI
// refresh its own login, so apx never writes the CLI's credential file itself.
func fetchGrokBilling(ctx context.Context, acct config.AccountConfig, dataDir string, env []string) (AccountQuota, error) {
	home, err := grokHome(acct, dataDir)
	if err != nil {
		return AccountQuota{}, err
	}
	token, exp, err := readGrokToken(home)
	if err != nil {
		return AccountQuota{}, err
	}
	refreshed := false
	if !exp.IsZero() && time.Until(exp) < time.Minute {
		if t, _, rerr := refreshGrokLogin(ctx, home, env); rerr == nil {
			token, refreshed = t, true
		}
	}
	q, status, err := getGrokBilling(ctx, token)
	if status == http.StatusUnauthorized && !refreshed {
		if t, _, rerr := refreshGrokLogin(ctx, home, env); rerr == nil {
			q, _, err = getGrokBilling(ctx, t)
		}
	}
	return q, err
}

func refreshGrokLogin(ctx context.Context, home string, env []string) (string, time.Time, error) {
	bin, err := exec.LookPath("grok")
	if err != nil {
		return "", time.Time{}, fmt.Errorf("grok binary not on PATH")
	}
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(rctx, bin, "models")
	cmd.Env = env
	_ = cmd.Run()
	return readGrokToken(home)
}

func getGrokBilling(ctx context.Context, token string) (AccountQuota, int, error) {
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, GrokBillingURL, nil)
	if err != nil {
		return AccountQuota{}, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-xai-token-auth", "xai-grok-cli")
	req.Header.Set("x-grok-client-version", grokCLIVersion)
	req.Header.Set("User-Agent", "grok-cli/"+grokCLIVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return AccountQuota{}, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if strings.Contains(msg, "WKE=privacy-mode:") {
			msg = "switch your xAI account to Privacy Mode to show Grok usage"
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return AccountQuota{}, resp.StatusCode, fmt.Errorf("grok billing: HTTP %d: %s", resp.StatusCode, msg)
	}
	q, err := ParseGrokBilling(body)
	return q, resp.StatusCode, err
}

// ParseGrokBilling turns a Grok billing response into a single usage window
// labeled by its period (Weekly, Monthly or Subscription).
func ParseGrokBilling(raw []byte) (AccountQuota, error) {
	var resp struct {
		Config *struct {
			CreditUsagePercent *float64 `json:"creditUsagePercent"`
			CurrentPeriod      *struct {
				Type  string `json:"type"`
				Start string `json:"start"`
				End   string `json:"end"`
			} `json:"currentPeriod"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return AccountQuota{}, err
	}
	if resp.Config == nil {
		return AccountQuota{}, fmt.Errorf("grok billing: no config in response")
	}
	label := "Subscription"
	var start, end time.Time
	if p := resp.Config.CurrentPeriod; p != nil {
		switch strings.TrimPrefix(p.Type, "USAGE_PERIOD_TYPE_") {
		case "WEEKLY":
			label = "Weekly"
		case "MONTHLY":
			label = "Monthly"
		}
		start, _ = time.Parse(time.RFC3339Nano, p.Start)
		end, _ = time.Parse(time.RFC3339Nano, p.End)
	}
	used := 0.0
	if resp.Config.CreditUsagePercent != nil {
		used = *resp.Config.CreditUsagePercent
	} else if label == "Subscription" || !start.Before(end) {
		return AccountQuota{}, fmt.Errorf("grok billing: no usage reported")
	}
	used = math.Max(0, math.Min(100, used))
	w := &Window{
		Label:       label,
		UsedPercent: math.Round(used*10) / 10,
		LeftPercent: math.Round((100-used)*10) / 10,
	}
	if !end.IsZero() {
		e := end.UTC()
		w.ResetsAt = &e
		if start.Before(end) {
			w.WindowMins = int(math.Round(end.Sub(start).Minutes()))
		}
	}
	return AccountQuota{Source: "grok-live", Primary: w}, nil
}
