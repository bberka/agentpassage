package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

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

	StrategySticky     = "sticky"
	StrategyManual     = "manual"
	StrategyFailover   = "failover"
	StrategyRoundRobin = "round-robin"
	StrategyLeastBusy  = "least-busy"

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
	Accounts []AccountConfig `toml:"accounts"`
	Models   []ModelOverride `toml:"models"`
	Routing  []RoutingRule   `toml:"routing"`
}

type CatalogConfig struct {
	ExposeDirectModels     bool `toml:"expose_direct_models"`     // show <backend>/<account>/<model> in /v1/models (default false)
	HideBackendQualified   bool `toml:"hide_backend_qualified"`   // hide <backend>/<model> when bare <model> exists (default false)
}

type ServerConfig struct {
	Bind        string `toml:"bind"`
	Port        int    `toml:"port"`
	LocalKey    string `toml:"local_key"`
	AllowRemote bool   `toml:"allow_remote"`
}

type UIConfig struct {
	Theme              string `toml:"theme"`
	OnboardingComplete bool   `toml:"onboarding_complete"`
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
	ExposeMode      string            `toml:"expose_mode,omitempty"`      // "pool" (default), "prefix", "both"
	ModelPrefix     string            `toml:"model_prefix,omitempty"`     // e.g. "a-", "b-" (defaults to "<name>-")
	MaxConcurrency  int               `toml:"max_concurrency"`
	HourlyLimit     int               `toml:"hourly_limit,omitempty"`
	DailyLimit      int               `toml:"daily_limit,omitempty"`
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
	Pattern                     string   `toml:"pattern"` // exact model id, prefix glob like "agy/*", or "*"
	Accounts                    []string `toml:"accounts"`
	Strategy                    string   `toml:"strategy"`
	AllowCrossAccountRetryOn429 bool     `toml:"allow_cross_account_retry_on_429,omitempty"`
	SubscriptionWarningAcked    bool     `toml:"subscription_warning_acked,omitempty"`
}

// DataDir resolves the acpx storage directory.
func DataDir() (string, error) {
	if env := strings.TrimSpace(os.Getenv("ACPX_HOME")); env != "" {
		return filepath.Clean(env), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "acpx"), nil
	case "windows":
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			return filepath.Join(appData, "acpx"), nil
		}
		return filepath.Join(home, "AppData", "Roaming", "acpx"), nil
	default:
		if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
			return filepath.Join(xdg, "acpx"), nil
		}
		return filepath.Join(home, ".config", "acpx"), nil
	}
}

func GenerateLocalKey() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "acpx_fallback_local_key_change_me"
	}
	return "acpx_" + hex.EncodeToString(b)
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
				a.PermissionMode = PermDeny
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
		if c.Routing[i].Strategy == "" {
			c.Routing[i].Strategy = StrategySticky
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
