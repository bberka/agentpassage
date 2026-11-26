package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	KindACP    = "acp"
	KindCLI    = "cli"
	KindAPIKey = "api_key"

	AuthBearer    = "bearer"
	AuthAnthropic = "anthropic"
	AuthNone      = "none"

	PermDeny        = "deny"
	PermAutoApprove = "auto-approve"
	PermAsk         = "ask"

	StrategyFailover   = "failover"
	StrategySticky     = "sticky"
	StrategyManual     = "manual"
	StrategyRoundRobin = "round-robin"
	StrategyLeastBusy  = "least-busy"
	StrategyMostQuota  = "most-quota"
	StrategyFastest    = "fastest"

	ToolActivityDrop = "drop"
	ToolActivityText = "text"

	ExposePool   = "pool"
	ExposePrefix = "prefix"
	ExposeBoth   = "both"

	DefaultBind = "127.0.0.1"
	DefaultPort = 8787
)

var validNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

type Config struct {
	Server   ServerConfig    `toml:"server"`
	UI       UIConfig        `toml:"ui"`
	ACP      ACPConfig       `toml:"acp"`
	Catalog  CatalogConfig   `toml:"catalog"`
	Health   HealthConfig    `toml:"health"`
	Privacy  PrivacyConfig   `toml:"privacy"`
	Alerts   AlertsConfig    `toml:"alerts"`
	Cache    CacheConfig     `toml:"cache"`
	Decision DecisionConfig  `toml:"decision"`
	Sessions SessionsConfig  `toml:"sessions"`
	Accounts []AccountConfig `toml:"accounts"`
	Models   []ModelOverride `toml:"models"`
	Routing  []RoutingRule   `toml:"routing"`
}

// SessionsConfig controls how a conversation (requests sharing a session key)
// is routed once it has run on an account.
type SessionsConfig struct {
	// SwitchAccountWhenExhausted moves a conversation to another account when
	// its account runs out of quota. Off by default: the conversation keeps its
	// account, and its requests fail with 429 until the quota resets.
	SwitchAccountWhenExhausted bool `toml:"switch_account_when_exhausted" json:"switch_account_when_exhausted"`
}

type CacheConfig struct {
	Enabled    bool `toml:"enabled" json:"enabled"`
	MaxEntries int  `toml:"max_entries,omitempty" json:"max_entries,omitempty"`
	TTLSeconds int  `toml:"ttl_seconds,omitempty" json:"ttl_seconds,omitempty"`
}

type DecisionConfig struct {
	Enabled        bool   `toml:"enabled" json:"enabled"`
	ModelID        string `toml:"model_id,omitempty" json:"model_id,omitempty"`               // virtual model ID (default "apx/auto")
	DefaultModel   string `toml:"default_model,omitempty" json:"default_model,omitempty"`     // fallback target model
	FastModel      string `toml:"fast_model,omitempty" json:"fast_model,omitempty"`           // short/simple queries
	CodingModel    string `toml:"coding_model,omitempty" json:"coding_model,omitempty"`       // code/debugging queries
	ReasoningModel string `toml:"reasoning_model,omitempty" json:"reasoning_model,omitempty"` // deep math/architecture/planning queries
	VisionModel    string `toml:"vision_model,omitempty" json:"vision_model,omitempty"`       // image/multimodal queries
}

type AlertsConfig struct {
	Enabled           bool   `toml:"enabled" json:"enabled"`
	Desktop           bool   `toml:"desktop" json:"desktop"`
	WebhookURL        string `toml:"webhook_url,omitempty" json:"webhook_url,omitempty"`
	QuotaThresholdPct int    `toml:"quota_threshold_pct,omitempty" json:"quota_threshold_pct,omitempty"`
}

type PrivacyPattern struct {
	Name    string `toml:"name" json:"name"`
	Pattern string `toml:"pattern" json:"pattern"`
}

type PrivacyConfig struct {
	Enabled        bool             `toml:"enabled" json:"enabled"`
	RedactAPIKeys  bool             `toml:"redact_api_keys" json:"redact_api_keys"`
	RedactEmails   bool             `toml:"redact_emails" json:"redact_emails"`
	RedactPhones   bool             `toml:"redact_phones" json:"redact_phones"`
	CustomPatterns []PrivacyPattern `toml:"custom_patterns,omitempty" json:"custom_patterns,omitempty"`
}

type HealthConfig struct {
	Enabled         bool `toml:"enabled"`
	IntervalSeconds int  `toml:"interval_seconds"` // 0 = manual only, default 300 (5m)
}

const (
	CatalogModeProvider        = "provider"         // <provider>/<model> only (default; never merges across providers, no bare trimmed names)
	CatalogModeProviderAccount = "provider+account" // <provider>/<model> + <provider>/<account>/<model>
	CatalogModeAccount         = "account"          // <provider>/<account>/<model> only (every provider & account individually)
	CatalogModeBare            = "bare"             // legacy unambiguous bare names
)

type CatalogConfig struct {
	Mode                 string `toml:"mode,omitempty"`               // "provider" (default), "provider+account", "account", or "bare"
	ExposeDirectModels   bool   `toml:"expose_direct_models"`         // legacy: show <backend>/<account>/<model> in /v1/models
	HideBackendQualified bool   `toml:"hide_backend_qualified"`       // legacy: hide <backend>/<model> when bare <model> exists
	ShowEffortVariants   bool   `toml:"show_effort_variants,omitempty"` // expand -low/-medium/-high/[xhigh] as separate models (default false = grouped into thinking effort levels)
}

// EffectiveMode returns the normalized catalog listing mode, preserving legacy boolean flags when Mode is unset.
func (c CatalogConfig) EffectiveMode() string {
	switch strings.ToLower(strings.TrimSpace(c.Mode)) {
	case CatalogModeProvider:
		return CatalogModeProvider
	case CatalogModeProviderAccount:
		return CatalogModeProviderAccount
	case CatalogModeAccount:
		return CatalogModeAccount
	case CatalogModeBare:
		return CatalogModeBare
	}
	if c.HideBackendQualified {
		return CatalogModeBare
	}
	if c.ExposeDirectModels {
		return CatalogModeProviderAccount
	}
	return CatalogModeProvider
}

const (
	KeyRoleClient = "client" // /v1/* inference endpoints only (recommended for remote devices/tunnels)
	KeyRoleAdmin  = "admin"  // full access including /admin/* and Usage Hub
)

type ClientKey struct {
	ID        string   `toml:"id" json:"id"`
	Name      string   `toml:"name" json:"name"`
	Key       string   `toml:"key" json:"key"`
	Role      string   `toml:"role,omitempty" json:"role,omitempty"`       // "client" (default) or "admin"
	Project   string   `toml:"project,omitempty" json:"project,omitempty"` // optional project scope for project-based routing & usage
	Models           []string `toml:"models,omitempty" json:"models,omitempty"`                       // optional glob allowlist (empty = all)
	DailyBudgetUSD   float64  `toml:"daily_budget_usd,omitempty" json:"daily_budget_usd,omitempty"`   // optional daily USD spend cap (0 = unlimited)
	MonthlyBudgetUSD float64  `toml:"monthly_budget_usd,omitempty" json:"monthly_budget_usd,omitempty"` // optional 30d USD spend cap (0 = unlimited)
	Disabled         bool     `toml:"disabled,omitempty" json:"disabled,omitempty"`
	CreatedAt        string   `toml:"created_at,omitempty" json:"created_at,omitempty"`
}

func (k ClientKey) EffectiveRole() string {
	if strings.EqualFold(k.Role, KeyRoleAdmin) {
		return KeyRoleAdmin
	}
	return KeyRoleClient
}

func (k ClientKey) AllowsModel(modelID string) bool {
	if len(k.Models) == 0 {
		return true
	}
	for _, pat := range k.Models {
		pat = strings.TrimSpace(pat)
		if pat == "" || pat == "*" {
			return true
		}
		if pat == modelID {
			return true
		}
		if ok, _ := path.Match(pat, modelID); ok {
			return true
		}
	}
	return false
}

type ServerConfig struct {
	Bind        string      `toml:"bind"`
	Port        int         `toml:"port"`
	LocalKey    string      `toml:"local_key"`
	APIKeys     []ClientKey `toml:"api_keys,omitempty"`
	AllowRemote bool        `toml:"allow_remote"`
}

type UIConfig struct {
	Theme                   string `toml:"theme"`
	OnboardingComplete      bool   `toml:"onboarding_complete"`
	HostAuthPromptDismissed bool   `toml:"host_auth_prompt_dismissed,omitempty"`
}

type ACPConfig struct {
	ToolActivity string `toml:"tool_activity"` // "drop" or "text"
}

type AccountConfig struct {
	ID             string            `toml:"id"`
	Name           string            `toml:"name"`
	Kind           string            `toml:"kind"`    // "acp" or "api_key"
	Backend        string            `toml:"backend"` // descriptor id or preset id
	Enabled        bool              `toml:"enabled"`
	DisabledReason string            `toml:"disabled_reason,omitempty"`
	BaseURL        string            `toml:"base_url,omitempty"`
	AuthStyle      string            `toml:"auth_style,omitempty"` // bearer, anthropic, none
	DefaultCwd      string            `toml:"default_cwd,omitempty"`
	PermissionMode  string            `toml:"permission_mode,omitempty"` // deny, auto-approve, ask
	UseHostProfile  bool              `toml:"use_host_profile,omitempty"`
	CustomCommand   []string          `toml:"custom_command,omitempty"`
	CustomLogin    []string          `toml:"custom_login,omitempty"`
	CustomEnv       map[string]string `toml:"custom_env,omitempty"`
	Models          []string          `toml:"models,omitempty"`           // enabled models served by this account
	AvailableModels []string          `toml:"available_models,omitempty"` // full discovered catalog for enable/disable toggling
	DisabledModels  []string          `toml:"disabled_models,omitempty"`  // explicitly disabled models from the catalog
	ModelLabels     map[string]string   `toml:"model_labels,omitempty"`     // upstream model id -> human display name (e.g. "fable" -> "Fable 5.2")
	ModelEfforts    map[string][]string `toml:"model_efforts,omitempty"`    // upstream model id (or "*" default) -> ACP thought_level options (e.g. ["low","medium","high"])
	ExposeMode      string            `toml:"expose_mode,omitempty"`      // "pool" (default), "prefix", "both"
	ModelPrefix     string            `toml:"model_prefix,omitempty"`     // e.g. "a-", "b-" (defaults to "<name>-")
	MaxConcurrency  int               `toml:"max_concurrency"`
	HourlyLimit      int     `toml:"hourly_limit,omitempty"`
	DailyLimit       int     `toml:"daily_limit,omitempty"`
	WeeklyLimit      int     `toml:"weekly_limit,omitempty"`
	DailyBudgetUSD   float64 `toml:"daily_budget_usd,omitempty"`
	MonthlyBudgetUSD float64 `toml:"monthly_budget_usd,omitempty"`
}

// EffectivePrefix returns the model prefix used when ExposeMode is "prefix" or "both".
func (a AccountConfig) EffectivePrefix() string {
	if strings.TrimSpace(a.ModelPrefix) != "" {
		return a.ModelPrefix
	}
	return a.Name + "-"
}

// MergeDiscoveredModels updates AvailableModels with newly fetched models and enables
// any model that has not been explicitly disabled.
func (a *AccountConfig) MergeDiscoveredModels(fetched []string) {
	disabled := make(map[string]bool, len(a.DisabledModels))
	for _, m := range a.DisabledModels {
		disabled[m] = true
	}
	seen := make(map[string]bool)
	var catalog []string
	addCatalog := func(list []string) {
		for _, m := range list {
			m = strings.TrimSpace(m)
			if m == "" || seen[m] {
				continue
			}
			seen[m] = true
			catalog = append(catalog, m)
		}
	}
	addCatalog(fetched)
	addCatalog(a.AvailableModels)
	addCatalog(a.Models)

	var enabled []string
	for _, m := range catalog {
		if !disabled[m] {
			enabled = append(enabled, m)
		}
	}
	// If real models were fetched from the provider, drop placeholder "default" unless it was returned by the provider.
	if len(fetched) > 0 {
		fetchedHasDefault := false
		for _, m := range fetched {
			if strings.TrimSpace(m) == "default" {
				fetchedHasDefault = true
				break
			}
		}
		if !fetchedHasDefault && len(catalog) > 1 {
			cleanCat := catalog[:0]
			for _, m := range catalog {
				if m != "default" {
					cleanCat = append(cleanCat, m)
				}
			}
			catalog = cleanCat
			cleanEn := enabled[:0]
			for _, m := range enabled {
				if m != "default" {
					cleanEn = append(cleanEn, m)
				}
			}
			enabled = cleanEn
		}
	}
	a.AvailableModels = catalog
	a.Models = enabled
}

// MergeModelLabels merges upstream display names (e.g. "fable" -> "Fable 5.2") onto the account.
// Returns true if any label was added or updated.
func (a *AccountConfig) MergeModelLabels(labels map[string]string) bool {
	if len(labels) == 0 {
		return false
	}
	if a.ModelLabels == nil {
		a.ModelLabels = make(map[string]string, len(labels))
	}
	changed := false
	for k, v := range labels {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || v == "" || v == k {
			continue
		}
		if a.ModelLabels[k] != v {
			a.ModelLabels[k] = v
			changed = true
		}
	}
	return changed
}

// IsLabelAlias reports whether model is a short alias that shares its display
// name with a longer model ID on the same account (e.g. "fable" and
// "claude-fable-5-2" are both "Fable 5.2"). Catalogs hide such aliases so the
// model appears once; requests for the alias still route.
func (a AccountConfig) IsLabelAlias(model string) bool {
	lbl := strings.TrimSpace(a.ModelLabels[model])
	if lbl == "" {
		return false
	}
	for _, other := range a.Models {
		if other != model && len(other) > len(model) && strings.TrimSpace(a.ModelLabels[other]) == lbl {
			return true
		}
	}
	return false
}

// MergeModelEfforts merges ACP-discovered thought_level/effort options onto the account.
// Key "*" represents account-wide effort options for backends that expose a shared thought_level
// configOption instead of suffixed model IDs (e.g. claude-agent-acp).
func (a *AccountConfig) MergeModelEfforts(efforts map[string][]string) bool {
	if len(efforts) == 0 {
		return false
	}
	if a.ModelEfforts == nil {
		a.ModelEfforts = make(map[string][]string, len(efforts))
	}
	changed := false
	for k, vals := range efforts {
		k = strings.TrimSpace(k)
		if k == "" || len(vals) == 0 {
			continue
		}
		clean := make([]string, 0, len(vals))
		seen := make(map[string]bool, len(vals))
		for _, v := range vals {
			v = strings.ToLower(strings.TrimSpace(v))
			if v == "" || v == "default" || v == "auto" || v == "none" || v == "off" || seen[v] {
				continue
			}
			seen[v] = true
			clean = append(clean, v)
		}
		if len(clean) == 0 {
			continue
		}
		prev := a.ModelEfforts[k]
		same := len(prev) == len(clean)
		if same {
			for i := range prev {
				if prev[i] != clean[i] {
					same = false
					break
				}
			}
		}
		if !same {
			a.ModelEfforts[k] = clean
			changed = true
		}
	}
	return changed
}

// SetModelEnabled enables or disables one model ID on this account.
func (a *AccountConfig) SetModelEnabled(modelID string, enabled bool) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return
	}
	// Ensure present in AvailableModels
	inCatalog := false
	for _, m := range a.AvailableModels {
		if m == modelID {
			inCatalog = true
			break
		}
	}
	if !inCatalog {
		a.AvailableModels = append(a.AvailableModels, modelID)
	}

	disabled := make(map[string]bool, len(a.DisabledModels))
	for _, m := range a.DisabledModels {
		disabled[m] = true
	}
	if enabled {
		delete(disabled, modelID)
	} else {
		disabled[modelID] = true
	}

	var newDisabled []string
	var newEnabled []string
	seen := make(map[string]bool)
	for _, m := range a.AvailableModels {
		if seen[m] {
			continue
		}
		seen[m] = true
		if disabled[m] {
			newDisabled = append(newDisabled, m)
		} else {
			newEnabled = append(newEnabled, m)
		}
	}
	a.DisabledModels = newDisabled
	a.Models = newEnabled
}

// SetAllModelsEnabled enables or disables every model in AvailableModels.
func (a *AccountConfig) SetAllModelsEnabled(enabled bool) {
	if len(a.AvailableModels) == 0 {
		a.AvailableModels = append([]string(nil), a.Models...)
	}
	if enabled {
		a.DisabledModels = nil
		a.Models = append([]string(nil), a.AvailableModels...)
	} else {
		a.DisabledModels = append([]string(nil), a.AvailableModels...)
		a.Models = nil
	}
}

type ModelOverride struct {
	ID          string `toml:"id"`
	DisplayName string `toml:"display_name,omitempty"`
	Alias       string `toml:"alias,omitempty"`
	Hidden      bool   `toml:"hidden,omitempty"`
}

type RoutingRule struct {
	Pattern                     string   `toml:"pattern"`                                  // exact model id, prefix glob like "agy/*", or "*"
	Project                     string   `toml:"project,omitempty"`                        // optional project id or glob (matched from X-Apx-Project or API key)
	Accounts                    []string `toml:"accounts"`
	Strategy                    string   `toml:"strategy"`
	AllowCrossAccountRetryOn429 bool     `toml:"allow_cross_account_retry_on_429"`
	SubscriptionWarningAcked    bool     `toml:"subscription_warning_acked,omitempty"`
	QuotaThresholdPct           int      `toml:"quota_threshold_pct,omitempty"`            // avoid accounts above this used % (e.g. 90) when alternatives exist
	DefaultEffort               string   `toml:"default_effort,omitempty"`                 // default thinking effort ("off", "low", "medium", "high", "xhigh", "max")
	SystemPrompt                string   `toml:"system_prompt,omitempty"`                  // optional system prompt prepended for requests matching this rule
	Cache                       bool     `toml:"cache,omitempty"`                          // opt-in response caching for this route
	FallbackModels              []string `toml:"fallback_models,omitempty"`                // alternate models/providers tried after primary model fails
	FallbackOn                  []string `toml:"fallback_on,omitempty"`                    // eligible error kinds: rate_limit, transient, unavailable
}

// EffectiveFallbackOn returns the error kinds eligible to trigger model fallback.
// Defaults strictly to safe, recoverable error cases (never auth/restricted/client-400).
func (r RoutingRule) EffectiveFallbackOn() []string {
	if len(r.FallbackOn) > 0 {
		return r.FallbackOn
	}
	return []string{"rate_limit", "transient", "unavailable"}
}

// DataDir resolves the AgentPassage (apx) storage directory.
func DataDir() (string, error) {
	if env := strings.TrimSpace(os.Getenv("APX_HOME")); env != "" {
		return filepath.Clean(env), nil
	}
	if env := strings.TrimSpace(os.Getenv("ACPX_HOME")); env != "" {
		return filepath.Clean(env), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "agentpassage"), nil
	case "windows":
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			return filepath.Join(appData, "agentpassage"), nil
		}
		return filepath.Join(home, "AppData", "Roaming", "agentpassage"), nil
	default:
		if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
			return filepath.Join(xdg, "agentpassage"), nil
		}
		return filepath.Join(home, ".config", "agentpassage"), nil
	}
}

func GenerateLocalKey() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "apx_fallback_local_key_change_me"
	}
	return "apx_" + hex.EncodeToString(b)
}

func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Bind:     DefaultBind,
			Port:     DefaultPort,
			LocalKey: GenerateLocalKey(),
		},
		UI: UIConfig{
			Theme: "auto",
		},
		ACP: ACPConfig{
			ToolActivity: ToolActivityDrop,
		},
		Health: HealthConfig{
			Enabled:         true,
			IntervalSeconds: 300,
		},
		Privacy: PrivacyConfig{
			Enabled:       false,
			RedactAPIKeys: true,
			RedactEmails:  true,
			RedactPhones:  true,
		},
	}
}

func EnsureDirs(dataDir string) error {
	dirs := []string{
		dataDir,
		filepath.Join(dataDir, "profiles"),
		filepath.Join(dataDir, "backends.d"),
		filepath.Join(dataDir, "logs"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("create directory %s: %w", d, err)
		}
	}
	return nil
}

func ConfigPath(dataDir string) string {
	return filepath.Join(dataDir, "config.toml")
}

func Load(dataDir string) (*Config, bool, error) {
	if err := EnsureDirs(dataDir); err != nil {
		return nil, false, err
	}
	path := ConfigPath(dataDir)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg := Default()
		if err := Save(dataDir, cfg); err != nil {
			return nil, true, err
		}
		return cfg, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read config: %w", err)
	}
	cfg := Default()
	if _, err := toml.Decode(string(data), cfg); err != nil {
		return nil, false, fmt.Errorf("parse config.toml: %w", err)
	}
	cfg.Normalize()
	return cfg, false, nil
}

func Save(dataDir string, cfg *Config) error {
	if err := EnsureDirs(dataDir); err != nil {
		return err
	}
	cfg.Normalize()
	var buf strings.Builder
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	path := ConfigPath(dataDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(buf.String()), 0o600); err != nil {
		return fmt.Errorf("write config temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit config file: %w", err)
	}
	return nil
}

func (c *Config) Normalize() {
	if c.Server.Bind == "" {
		c.Server.Bind = DefaultBind
	}
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		c.Server.Port = DefaultPort
	}
	for i := range c.Server.APIKeys {
		if c.Server.APIKeys[i].ID == "" {
			c.Server.APIKeys[i].ID = SanitizeSlug(c.Server.APIKeys[i].Name)
		}
		if c.Server.APIKeys[i].Role == "" {
			c.Server.APIKeys[i].Role = KeyRoleClient
		}
	}
	if c.Server.LocalKey == "" {
		c.Server.LocalKey = GenerateLocalKey()
	}
	if c.UI.Theme == "" {
		c.UI.Theme = "auto"
	}
	if c.ACP.ToolActivity != ToolActivityText && c.ACP.ToolActivity != ToolActivityDrop {
		c.ACP.ToolActivity = ToolActivityDrop
	}
	for i := range c.Accounts {
		a := &c.Accounts[i]
		if a.ID == "" {
			a.ID = SanitizeSlug(a.Backend + "-" + a.Name)
		}
		if a.ExposeMode != ExposePool && a.ExposeMode != ExposePrefix && a.ExposeMode != ExposeBoth {
			a.ExposeMode = ExposePool
		}
		if len(a.AvailableModels) == 0 && len(a.Models) > 0 {
			a.AvailableModels = append([]string(nil), a.Models...)
		}
		if a.Kind == KindACP || a.Kind == KindCLI {
			if a.MaxConcurrency <= 0 {
				a.MaxConcurrency = 1
			}
			if a.PermissionMode == "" {
				a.PermissionMode = PermAutoApprove
			}
		} else {
			a.Kind = KindAPIKey
			if a.MaxConcurrency <= 0 {
				a.MaxConcurrency = 8
			}
			if a.AuthStyle == "" {
				a.AuthStyle = AuthBearer
			}
		}
	}
	for i := range c.Routing {
		switch c.Routing[i].Strategy {
		case StrategyFailover, StrategySticky, StrategyManual, StrategyRoundRobin, StrategyLeastBusy, StrategyMostQuota, StrategyFastest:
			// valid (round-robin and least-busy are intended for API keys; subscription accounts require opt-in)
		default:
			c.Routing[i].Strategy = StrategyFailover
		}
	}
}

func ValidateName(name string) error {
	name = strings.TrimSpace(name)
	if !validNameRe.MatchString(name) {
		return fmt.Errorf("name %q must be 1-64 chars matching [a-zA-Z0-9._-]", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("name %q must not contain '..'", name)
	}
	return nil
}

func SanitizeSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else if r == ' ' || r == '/' || r == ':' {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-._")
	if out == "" {
		return "default"
	}
	if len(out) > 64 {
		return out[:64]
	}
	return out
}

func ProfileDir(dataDir, backend, accountName string) (string, error) {
	if err := ValidateName(backend); err != nil {
		return "", fmt.Errorf("invalid backend name: %w", err)
	}
	if err := ValidateName(accountName); err != nil {
		return "", fmt.Errorf("invalid account name: %w", err)
	}
	base := filepath.Join(dataDir, "profiles")
	target := filepath.Join(base, backend, accountName)
	rel, err := filepath.Rel(base, target)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("unsafe profile path")
	}
	return target, nil
}

func EnsureProfileDir(dataDir, backend, accountName string) (string, error) {
	dir, err := ProfileDir(dataDir, backend, accountName)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create profile dir: %w", err)
	}
	return dir, nil
}

func DeleteProfileDir(dataDir, backend, accountName string) error {
	dir, err := ProfileDir(dataDir, backend, accountName)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (c *Config) FindAccount(idOrKey string) *AccountConfig {
	for i := range c.Accounts {
		a := &c.Accounts[i]
		if a.ID == idOrKey || a.Backend+"/"+a.Name == idOrKey || a.Name == idOrKey {
			return a
		}
	}
	return nil
}

func (c *Config) IsLoopbackBind() bool {
	b := strings.TrimSpace(c.Server.Bind)
	return b == "" || b == "127.0.0.1" || b == "localhost" || b == "::1"
}

func (c *Config) BaseURL() string {
	host := c.Server.Bind
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%d/v1", host, c.Server.Port)
}

func (c *Config) AddAPIKey(name, role string, models []string) (ClientKey, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ClientKey{}, fmt.Errorf("key name is required")
	}
	if err := ValidateName(name); err != nil {
		return ClientKey{}, err
	}
	id := SanitizeSlug(name)
	for _, existing := range c.Server.APIKeys {
		if existing.ID == id || strings.EqualFold(existing.Name, name) {
			return ClientKey{}, fmt.Errorf("API key %q already exists", name)
		}
	}
	if strings.EqualFold(role, KeyRoleAdmin) {
		role = KeyRoleAdmin
	} else {
		role = KeyRoleClient
	}
	var cleanModels []string
	for _, m := range models {
		if m = strings.TrimSpace(m); m != "" {
			cleanModels = append(cleanModels, m)
		}
	}
	ck := ClientKey{
		ID:        id,
		Name:      name,
		Key:       GenerateLocalKey(),
		Role:      role,
		Models:    cleanModels,
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	c.Server.APIKeys = append(c.Server.APIKeys, ck)
	return ck, nil
}

func (c *Config) FindAPIKey(idOrName string) *ClientKey {
	q := strings.TrimSpace(idOrName)
	for i := range c.Server.APIKeys {
		if c.Server.APIKeys[i].ID == q || strings.EqualFold(c.Server.APIKeys[i].Name, q) {
			return &c.Server.APIKeys[i]
		}
	}
	return nil
}

func (c *Config) DeleteAPIKey(idOrName string) bool {
	q := strings.TrimSpace(idOrName)
	for i := range c.Server.APIKeys {
		if c.Server.APIKeys[i].ID == q || strings.EqualFold(c.Server.APIKeys[i].Name, q) {
			c.Server.APIKeys = append(c.Server.APIKeys[:i], c.Server.APIKeys[i+1:]...)
			return true
		}
	}
	return false
}
