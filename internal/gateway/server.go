package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bberka/agentpassage/internal/alerts"
	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/backend/acpbackend"
	"github.com/bberka/agentpassage/internal/backend/clibackend"
	"github.com/bberka/agentpassage/internal/backend/httpbackend"
	"github.com/bberka/agentpassage/internal/cache"
	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/descriptors"
	"github.com/bberka/agentpassage/internal/health"
	"github.com/bberka/agentpassage/internal/logbuf"
	"github.com/bberka/agentpassage/internal/pool"
	"github.com/bberka/agentpassage/internal/privacy"
	"github.com/bberka/agentpassage/internal/quota"
	"github.com/bberka/agentpassage/internal/router"
	"github.com/bberka/agentpassage/internal/secrets"
	"github.com/bberka/agentpassage/internal/usage"
)

const Version = "0.1.0"

// Commit is the source commit of this build, set with -ldflags -X. It is empty
// for plain `go build` and `go run`.
var Commit string

// BuildVersion is Version plus the build's commit when it is known.
func BuildVersion() string {
	if Commit == "" {
		return Version
	}
	return Version + " (" + Commit + ")"
}

// Server is the local OpenAI & Anthropic compatible HTTP gateway.
type Server struct {
	mu       sync.RWMutex
	dataDir  string
	cfg      *config.Config
	secrets  *secrets.Store
	registry *descriptors.Registry
	router   *router.Router
	pool     *pool.Manager
	httpBE   *httpbackend.Client
	acpBE    *acpbackend.Manager
	cliBE    *clibackend.Manager
	logs     *logbuf.Buffer
	usage    *usage.Tracker
	health   *health.Checker
	quota    *quota.Fetcher
	privacy  *privacy.Filter
	alerts   *alerts.Notifier
	// quotaAlerts tracks which limit windows already sent a low-quota alert.
	quotaAlerts quotaAlertState
	// balanceAlerts tracks which accounts have an open low-balance alert.
	balanceAlerts balanceAlertState
	cache         *cache.Store

	srv          *http.Server
	listener     net.Listener
	running      bool
	attached     bool
	listenOn     string
	remoteSnaps  []pool.AccountSnapshot
	remoteBans   []pool.HealthBanner
	remoteLogs   []logbuf.Entry
	remoteUsage  *usage.Summary
	remoteHealth *health.Report
	remoteQuota  *quota.Report
}

func New(dataDir string, cfg *config.Config, sec *secrets.Store, reg *descriptors.Registry, logs *logbuf.Buffer) *Server {
	rtr := router.New(cfg)
	pm := pool.NewManager(cfg.Accounts)
	acpMgr := acpbackend.NewManager(dataDir, reg, logs, cfg.ACP.ToolActivity)
	cliMgr := clibackend.NewManager(dataDir, reg, logs)
	usageTrk := usage.New(dataDir)
	s := &Server{
		dataDir:  dataDir,
		cfg:      cfg,
		secrets:  sec,
		registry: reg,
		router:   rtr,
		pool:     pm,
		httpBE:   httpbackend.New(),
		acpBE:    acpMgr,
		cliBE:    cliMgr,
		logs:     logs,
		usage:    usageTrk,
		privacy:  privacy.NewFilter(cfg.Privacy),
		alerts:   alerts.New(cfg.Alerts),
		cache:    cache.New(cfg.Cache.MaxEntries, time.Duration(cfg.Cache.TTLSeconds)*time.Second),
	}
	s.health = health.New(
		func() []config.AccountConfig {
			s.mu.RLock()
			defer s.mu.RUnlock()
			return append([]config.AccountConfig(nil), s.cfg.Accounts...)
		},
		func(ctx context.Context, acct config.AccountConfig) ([]string, error) {
			models, labels, efforts, err := s.TestAccountDiscovery(ctx, acct)
			if err == nil && (len(models) > 0 || len(labels) > 0 || len(efforts) > 0) {
				s.persistAccountMetadata(acct.ID, models, labels, efforts)
			}
			return models, err
		},
		func(acct config.AccountConfig, _ []string, err error) {
			if err == nil {
				s.pool.RecordSuccess(acct.ID)
			} else if be, ok := backend.AsBackendError(err); ok {
				if be.Kind == backend.ErrKindAuth || be.Kind == backend.ErrKindRestricted || be.Kind == backend.ErrKindRateLimit {
					s.pool.RecordError(acct.ID, err, config.RoutingRule{Strategy: config.StrategyManual})
				}
			}
		},
		logs,
		cfg.Health,
	)
	s.quota = quota.New(
		dataDir,
		func() []config.AccountConfig {
			s.mu.RLock()
			defer s.mu.RUnlock()
			return append([]config.AccountConfig(nil), s.cfg.Accounts...)
		},
		func() usage.Summary {
			return s.LiveUsage()
		},
		func(accountID string) string {
			if sec == nil {
				return ""
			}
			k, _ := sec.Get(accountID)
			return k
		},
	)
	s.quota.SetOnRefresh(s.checkQuotaAlerts)
	if logs != nil {
		s.alerts.SetErrorHook(func(err error) { logs.Warn("system", err.Error()) })
	}
	s.httpBE.SetHeaderHook(s.quota.RecordHTTPHeaders)
	if logs != nil {
		logs.RegisterSecret(cfg.Server.LocalKey)
		for _, k := range cfg.Server.APIKeys {
			logs.RegisterSecret(k.Key)
		}
	}
	pm.SetAutoDisableHook(func(accountID, reason string) {
		s.mu.Lock()
		for i := range s.cfg.Accounts {
			if s.cfg.Accounts[i].ID == accountID {
				s.cfg.Accounts[i].Enabled = false
				s.cfg.Accounts[i].DisabledReason = reason
				break
			}
		}
		_ = config.Save(s.dataDir, s.cfg)
		s.mu.Unlock()
		if s.logs != nil {
			s.logs.Warn("health", fmt.Sprintf("account %s auto-disabled: %s", accountID, reason))
		}
		if s.alerts != nil {
			s.alerts.Notify("disable:"+accountID, alerts.Event{
				Kind:     "auth_disabled",
				Severity: "critical",
				Account:  accountID,
				Title:    accountID + " disabled",
				Message:  shortText(reason, 100),
			})
		}
	})
	return s
}

// shortText cuts s to at most n characters for a notification body.
func shortText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func (s *Server) Config() *config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Server) Pool() *pool.Manager             { return s.pool }
func (s *Server) Router() *router.Router          { return s.router }
func (s *Server) Secrets() *secrets.Store         { return s.secrets }
func (s *Server) Registry() *descriptors.Registry { return s.registry }
func (s *Server) ACP() *acpbackend.Manager        { return s.acpBE }
func (s *Server) CLI() *clibackend.Manager        { return s.cliBE }
func (s *Server) Logs() *logbuf.Buffer            { return s.logs }
func (s *Server) Usage() *usage.Tracker           { return s.usage }
func (s *Server) Health() *health.Checker         { return s.health }
func (s *Server) Quota() *quota.Fetcher           { return s.quota }
func (s *Server) DataDir() string                 { return s.dataDir }

func (s *Server) ReloadConfig(cfg *config.Config) error {
	s.mu.Lock()
	prevKey := s.cfg.Server.LocalKey
	s.cfg = cfg
	s.privacy = privacy.NewFilter(cfg.Privacy)
	attached := s.attached
	addr := s.listenOn
	if s.logs != nil {
		s.logs.RegisterSecret(cfg.Server.LocalKey)
		for _, k := range cfg.Server.APIKeys {
			s.logs.RegisterSecret(k.Key)
		}
	}
	s.mu.Unlock()

	s.router.UpdateConfig(cfg)
	s.pool.SyncAccounts(cfg.Accounts)
	s.acpBE.SetToolActivity(cfg.ACP.ToolActivity)
	if s.health != nil {
		s.health.UpdateConfig(cfg.Health)
	}
	if s.alerts != nil {
		s.alerts.UpdateConfig(cfg.Alerts)
	}
	if err := config.Save(s.dataDir, cfg); err != nil {
		return err
	}
	// An attached daemon must reload the saved file before its state is re-read.
	if attached && addr != "" {
		key := prevKey
		if key == "" {
			key = cfg.Server.LocalKey
		}
		if err := s.notifyRemoteReload(addr, key); err == nil {
			s.SyncFromDaemon()
		}
	}
	return nil
}

// NotifyRunningDaemon asks an already-running apx daemon/service to hot-reload config & secrets.
func NotifyRunningDaemon(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	addr := net.JoinHostPort(cfg.Server.Bind, fmt.Sprintf("%d", cfg.Server.Port))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/admin/reload", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Server.LocalKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// DetachAndStopLocal stops only the local embedded HTTP listener (used when handing off to an OS background service).
func (s *Server) DetachAndStopLocal(ctx context.Context) error {
	s.mu.Lock()
	srv := s.srv
	s.srv = nil
	s.running = false
	s.attached = false
	s.mu.Unlock()
	if srv != nil {
		return srv.Shutdown(ctx)
	}
	return nil
}

func (s *Server) notifyRemoteReload(addr, key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/admin/reload", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("remote reload HTTP %d", resp.StatusCode)
	}
	return nil
}

// AttachIfRunning checks if an apx daemon is already listening on the configured host:port.
// If so, switches this Server into attached client mode instead of failing with address-in-use.
func (s *Server) AttachIfRunning() (bool, error) {
	s.mu.RLock()
	addr := net.JoinHostPort(s.cfg.Server.Bind, fmt.Sprintf("%d", s.cfg.Server.Port))
	s.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		return false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("unexpected healthz status %d", resp.StatusCode)
	}
	var hz struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&hz); err != nil || hz.Status != "ok" || hz.Version == "" {
		return false, fmt.Errorf("listener on %s is not apx", addr)
	}

	s.mu.Lock()
	s.attached = true
	s.running = true
	s.listenOn = addr
	s.mu.Unlock()

	s.SyncFromDaemon()
	return true, nil
}

// Attached reports whether this Server is attached to an already-running apx daemon/service.
func (s *Server) Attached() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.attached
}

// SyncFromDaemon refreshes live account snapshots, banners, and logs when attached to a background daemon.
func (s *Server) SyncFromDaemon() {
	s.mu.RLock()
	attached := s.attached
	addr := s.listenOn
	key := s.cfg.Server.LocalKey
	s.mu.RUnlock()
	if !attached || addr == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	base := "http://" + addr + "/admin/"
	if st, ok := fetchDaemon[struct {
		Accounts []pool.AccountSnapshot `json:"accounts"`
		Banners  []pool.HealthBanner    `json:"banners"`
	}](ctx, base+"state", key); ok {
		s.mu.Lock()
		s.remoteSnaps = st.Accounts
		s.remoteBans = st.Banners
		s.mu.Unlock()
	}
	if lg, ok := fetchDaemon[struct {
		Entries []logbuf.Entry `json:"entries"`
	}](ctx, base+"logs", key); ok {
		s.mu.Lock()
		s.remoteLogs = lg.Entries
		s.mu.Unlock()
	}
	if sum, ok := fetchDaemon[usage.Summary](ctx, base+"usage", key); ok {
		s.mu.Lock()
		s.remoteUsage = &sum
		s.mu.Unlock()
	}
	if rep, ok := fetchDaemon[health.Report](ctx, base+"health", key); ok {
		s.mu.Lock()
		s.remoteHealth = &rep
		s.mu.Unlock()
	}
	if rep, ok := fetchDaemon[quota.Report](ctx, base+"quota", key); ok {
		s.mu.Lock()
		s.remoteQuota = &rep
		s.mu.Unlock()
	}
}

// fetchDaemon GETs and decodes a daemon admin endpoint; ok is false on any failure.
func fetchDaemon[T any](ctx context.Context, url, key string) (out T, ok bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return out, false
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out, false
	}
	defer resp.Body.Close()
	ok = resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&out) == nil
	return out, ok
}

// postDaemon POSTs to a daemon admin endpoint and decodes the reply.
func postDaemon[T any](ctx context.Context, url, key string) (out T, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("daemon returned HTTP %d", resp.StatusCode)
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out, err
}

// LiveQuota returns the latest subscription quota & rolling usage report.
func (s *Server) LiveQuota() quota.Report {
	s.mu.RLock()
	attached := s.attached
	rq := s.remoteQuota
	s.mu.RUnlock()
	if attached && rq != nil {
		return *rq
	}
	if s.quota == nil {
		return quota.Report{}
	}
	return s.quota.Report()
}

// RefreshQuotaNow fetches fresh subscription limits (via daemon if attached, or locally).
func (s *Server) RefreshQuotaNow(ctx context.Context, accountID string) (quota.Report, error) {
	s.mu.RLock()
	attached := s.attached
	addr := s.listenOn
	key := s.cfg.Server.LocalKey
	s.mu.RUnlock()

	if attached && addr != "" {
		url := "http://" + addr + "/admin/quota/refresh"
		if accountID != "" {
			url += "?account=" + neturl.QueryEscape(accountID)
		}
		rep, err := postDaemon[quota.Report](ctx, url, key)
		if err != nil {
			return quota.Report{}, err
		}
		s.mu.Lock()
		s.remoteQuota = &rep
		s.mu.Unlock()
		return rep, nil
	}

	if accountID != "" {
		if acct := s.Config().FindAccount(accountID); acct != nil {
			s.quota.RefreshOne(ctx, *acct)
			return s.quota.Report(), nil
		}
	}
	return s.quota.RefreshAll(ctx), nil
}

// LiveHealth returns the latest health check report (from daemon when attached, or local checker).
func (s *Server) LiveHealth() health.Report {
	s.mu.RLock()
	attached := s.attached
	rh := s.remoteHealth
	s.mu.RUnlock()
	if attached && rh != nil {
		return *rh
	}
	if s.health == nil {
		return health.Report{Overall: health.StatusUnknown}
	}
	return s.health.Report()
}

// CheckHealthNow runs an immediate health sweep (on the attached daemon if attached, or locally).
func (s *Server) CheckHealthNow(ctx context.Context, accountID string) (health.Report, error) {
	s.mu.RLock()
	attached := s.attached
	addr := s.listenOn
	key := s.cfg.Server.LocalKey
	s.mu.RUnlock()

	if attached && addr != "" {
		url := "http://" + addr + "/admin/health/check"
		if accountID != "" {
			url += "?account=" + neturl.QueryEscape(accountID)
		}
		rep, err := postDaemon[health.Report](ctx, url, key)
		if err != nil {
			return health.Report{}, err
		}
		s.mu.Lock()
		s.remoteHealth = &rep
		s.mu.Unlock()
		return rep, nil
	}

	if accountID != "" {
		if acct := s.Config().FindAccount(accountID); acct != nil {
			s.health.CheckOne(ctx, *acct)
			return s.health.Report(), nil
		}
	}
	return s.health.CheckAll(ctx), nil
}

// LiveUsage returns usage summary from the background daemon when attached, or local tracker.
func (s *Server) LiveUsage() usage.Summary {
	return s.LiveUsagePeriod("all")
}

// LiveUsagePeriod returns a period-filtered usage summary ("1h", "24h", "7d", "30d", "all").
func (s *Server) LiveUsagePeriod(period string) usage.Summary {
	if period == "" {
		period = "all"
	}
	s.mu.RLock()
	attached := s.attached
	ru := s.remoteUsage
	addr := s.listenOn
	key := s.cfg.Server.LocalKey
	s.mu.RUnlock()
	if attached {
		if period != "all" && addr != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if sum, ok := fetchDaemon[usage.Summary](ctx, "http://"+addr+"/admin/usage?period="+period, key); ok {
				return sum
			}
		}
		if ru != nil {
			return *ru
		}
	}
	if s.usage == nil {
		return usage.Summary{}
	}
	return s.usage.SummaryPeriod(period)
}

// UsageHubURL returns the localhost URL for the web Usage Report Hub.
func (s *Server) UsageHubURL(withKey bool) string {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	base := strings.TrimSuffix(cfg.BaseURL(), "/v1") + "/usage"
	if withKey && cfg.Server.LocalKey != "" {
		return base + "?key=" + cfg.Server.LocalKey
	}
	return base
}

// LiveSnapshots returns live account snapshots (from the remote daemon when attached, or local pool).
func (s *Server) LiveSnapshots() []pool.AccountSnapshot {
	s.mu.RLock()
	attached := s.attached
	snaps := s.remoteSnaps
	s.mu.RUnlock()
	if attached && len(snaps) > 0 {
		return snaps
	}
	return s.pool.Snapshots()
}

// LiveBanners returns live health banners (from the remote daemon when attached, or local pool).
func (s *Server) LiveBanners() []pool.HealthBanner {
	s.mu.RLock()
	attached := s.attached
	bans := s.remoteBans
	s.mu.RUnlock()
	if attached {
		return bans
	}
	return s.pool.Banners()
}

// LiveLogEntries returns recent log entries (from the remote daemon when attached, or local buffer).
func (s *Server) LiveLogEntries() []logbuf.Entry {
	s.mu.RLock()
	attached := s.attached
	logs := s.remoteLogs
	s.mu.RUnlock()
	if attached && len(logs) > 0 {
		return logs
	}
	if s.logs == nil {
		return nil
	}
	return s.logs.Entries()
}

func (s *Server) Addr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.listenOn != "" {
		return s.listenOn
	}
	return fmt.Sprintf("%s:%d", s.cfg.Server.Bind, s.cfg.Server.Port)
}

func (s *Server) Running() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /usage", s.handleUsageHub)
	mux.HandleFunc("GET /admin/state", s.withAdminAuth(s.handleAdminState))
	mux.HandleFunc("GET /admin/logs", s.withAdminAuth(s.handleAdminLogs))
	mux.HandleFunc("GET /admin/usage", s.withAdminAuth(s.handleAdminUsage))
	mux.HandleFunc("GET /admin/quota", s.withAdminAuth(s.handleAdminQuota))
	mux.HandleFunc("GET /admin/route/explain", s.withAdminAuth(s.handleAdminRouteExplain))
	mux.HandleFunc("POST /admin/quota/refresh", s.withAdminAuth(s.handleAdminQuotaRefresh))
	mux.HandleFunc("GET /admin/health", s.withAdminAuth(s.handleAdminHealth))
	mux.HandleFunc("POST /admin/health/check", s.withAdminAuth(s.handleAdminHealthCheck))
	mux.HandleFunc("POST /admin/reload", s.withAdminAuth(s.handleAdminReload))
	mux.HandleFunc("GET /v1/models", s.withAuth(s.handleListModels))
	mux.HandleFunc("POST /v1/chat/completions", s.withAuth(s.handleChatCompletions))
	mux.HandleFunc("POST /v1/responses", s.withAuth(s.handleOpenAIResponses))
	mux.HandleFunc("POST /v1/messages", s.withAuth(s.handleAnthropicMessages))
	mux.HandleFunc("POST /v1/embeddings", s.withAuth(s.handleEmbeddings))
	mux.HandleFunc("POST /v1/rerank", s.withAuth(s.handleRerank))
	mux.HandleFunc("POST /v1/images/generations", s.withAuth(s.handleOpenAIImages))
	mux.HandleFunc("POST /v1/audio/speech", s.withAuth(s.handleOpenAIAudioSpeech))
	mux.HandleFunc("POST /v1/audio/transcriptions", s.withAuth(s.handleOpenAIAudioTranscriptions))
	s.registerCLIProxyCompatRoutes(mux)
	return withBarePaths(withCORS(mux))
}

func (s *Server) Start() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	cfg := s.cfg
	if !cfg.IsLoopbackBind() && !cfg.Server.AllowRemote {
		s.mu.Unlock()
		return fmt.Errorf("refusing to bind to non-loopback address %q without --allow-remote", cfg.Server.Bind)
	}
	addr := net.JoinHostPort(cfg.Server.Bind, fmt.Sprintf("%d", cfg.Server.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	s.listener = ln
	s.srv = srv
	s.running = true
	s.listenOn = ln.Addr().String()
	s.mu.Unlock()

	if !cfg.IsLoopbackBind() && s.logs != nil {
		s.logs.Warn("system", fmt.Sprintf("WARNING: gateway bound to non-loopback address %s", ln.Addr().String()))
	} else if s.logs != nil {
		s.logs.Info("system", fmt.Sprintf("gateway listening on http://%s/v1", ln.Addr().String()))
	}

	if s.health != nil {
		s.health.Start()
	}
	if s.quota != nil {
		s.quota.Start()
	}

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			if s.logs != nil {
				s.logs.Error("system", "http server error", err)
			}
		}
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	srv := s.srv
	s.running = false
	s.mu.Unlock()
	s.acpBE.Close()
	if s.health != nil {
		s.health.Stop()
	}
	if s.quota != nil {
		s.quota.Stop()
	}
	if s.usage != nil {
		_ = s.usage.Close()
	}
	if srv != nil {
		return srv.Shutdown(ctx)
	}
	return nil
}

// TestAccount runs a live connectivity test against an API-key, ACP, or direct CLI account and returns discovered models.
func (s *Server) TestAccount(ctx context.Context, acct config.AccountConfig) ([]string, error) {
	models, _, err := s.TestAccountWithLabels(ctx, acct)
	return models, err
}

// TestAccountWithLabels runs a live connectivity test and also returns upstream display names
// (e.g. "fable" -> "Fable 5.2") when the backend provides them.
func (s *Server) TestAccountWithLabels(ctx context.Context, acct config.AccountConfig) ([]string, map[string]string, error) {
	models, labels, _, err := s.TestAccountDiscovery(ctx, acct)
	return models, labels, err
}

// TestAccountDiscovery runs a live connectivity test and returns discovered model IDs,
// upstream display names, and ACP-discovered thought_level/effort options.
func (s *Server) TestAccountDiscovery(ctx context.Context, acct config.AccountConfig) ([]string, map[string]string, map[string][]string, error) {
	switch acct.Kind {
	case config.KindACP:
		return s.acpBE.TestAccountDiscovery(ctx, acct)
	case config.KindCLI:
		models, err := s.cliBE.TestAccount(ctx, acct)
		return models, nil, nil, err
	default:
		key, _ := s.secrets.Get(acct.ID)
		models, err := s.httpBE.TestAccount(ctx, acct, key)
		return models, nil, nil, err
	}
}

func (s *Server) persistAccountMetadata(accountID string, models []string, labels map[string]string, efforts map[string][]string) {
	if len(models) == 0 && len(labels) == 0 && len(efforts) == 0 {
		return
	}
	s.mu.Lock()
	changed := false
	for i := range s.cfg.Accounts {
		if s.cfg.Accounts[i].ID == accountID {
			if len(models) > 0 {
				prevAvail := strings.Join(s.cfg.Accounts[i].AvailableModels, "\x00")
				prevModels := strings.Join(s.cfg.Accounts[i].Models, "\x00")
				s.cfg.Accounts[i].MergeDiscoveredModels(models)
				if strings.Join(s.cfg.Accounts[i].AvailableModels, "\x00") != prevAvail ||
					strings.Join(s.cfg.Accounts[i].Models, "\x00") != prevModels {
					changed = true
				}
			}
			if s.cfg.Accounts[i].MergeModelLabels(labels) {
				changed = true
			}
			if s.cfg.Accounts[i].MergeModelEfforts(efforts) {
				changed = true
			}
			break
		}
	}
	cfg := s.cfg
	s.mu.Unlock()
	if changed {
		s.router.UpdateConfig(cfg)
		_ = config.Save(s.dataDir, cfg)
	}
}

// FetchModelsWithKey fetches all available models from an API-key, ACP, or direct CLI backend.
func (s *Server) FetchModelsWithKey(ctx context.Context, acct config.AccountConfig, apiKey string) ([]string, error) {
	switch acct.Kind {
	case config.KindACP:
		return s.acpBE.TestAccount(ctx, acct)
	case config.KindCLI:
		return s.cliBE.TestAccount(ctx, acct)
	default:
		if apiKey == "" {
			apiKey, _ = s.secrets.Get(acct.ID)
		}
		return s.httpBE.TestAccount(ctx, acct, apiKey)
	}
}

type authCtxKey struct{}

type authPrincipal struct {
	KeyName string
	Role    string
	Client  *config.ClientKey // nil for primary LocalKey (admin)
}

func (s *Server) authenticateRequest(r *http.Request) (*authPrincipal, bool) {
	s.mu.RLock()
	primary := s.cfg.Server.LocalKey
	extra := append([]config.ClientKey(nil), s.cfg.Server.APIKeys...)
	s.mu.RUnlock()

	var token string
	if auth := r.Header.Get("Authorization"); auth != "" {
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			token = strings.TrimSpace(auth[7:])
		}
	}
	if token == "" {
		token = strings.TrimSpace(r.Header.Get("x-api-key"))
	}
	if token == "" {
		token = strings.TrimSpace(r.Header.Get("X-Management-Key"))
	}
	if token == "" {
		return nil, false
	}
	if primary != "" && subtle.ConstantTimeCompare([]byte(token), []byte(primary)) == 1 {
		return &authPrincipal{KeyName: "primary", Role: config.KeyRoleAdmin}, true
	}
	for i := range extra {
		k := extra[i]
		if k.Disabled || k.Key == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(k.Key)) == 1 {
			cp := k
			return &authPrincipal{KeyName: k.Name, Role: k.EffectiveRole(), Client: &cp}, true
		}
	}
	return nil, false
}

func principalFromContext(ctx context.Context) *authPrincipal {
	p, _ := ctx.Value(authCtxKey{}).(*authPrincipal)
	return p
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.authenticateRequest(r)
		if !ok {
			writeOpenAIError(w, http.StatusUnauthorized, "invalid_api_key", "Invalid or missing apx API key.")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), authCtxKey{}, p)))
	}
}

func (s *Server) withAdminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.authenticateRequest(r)
		if !ok {
			writeOpenAIError(w, http.StatusUnauthorized, "invalid_api_key", "Invalid or missing apx API key.")
			return
		}
		if p.Role != config.KeyRoleAdmin {
			writeOpenAIError(w, http.StatusForbidden, "insufficient_scope", "This API key has 'client' role; admin endpoints require an 'admin' key.")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), authCtxKey{}, p)))
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	rep := s.LiveHealth()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":             "ok",
		"version":            Version,
		"commit":             Commit,
		"overall_health":     rep.Overall,
		"accounts_total":     rep.TotalCount,
		"accounts_healthy":   rep.HealthyCount,
		"accounts_unhealthy": rep.UnhealthyCount,
	})
}

func (s *Server) handleAdminHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.LiveHealth())
}

func (s *Server) handleAdminHealthCheck(w http.ResponseWriter, r *http.Request) {
	acctID := strings.TrimSpace(r.URL.Query().Get("account"))
	rep, err := s.CheckHealthNow(r.Context(), acctID)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "health_check_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) handleAdminQuota(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.LiveQuota())
}

func (s *Server) handleAdminQuotaRefresh(w http.ResponseWriter, r *http.Request) {
	acctID := strings.TrimSpace(r.URL.Query().Get("account"))
	rep, err := s.RefreshQuotaNow(r.Context(), acctID)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "quota_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) handleAdminState(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	cfg := s.cfg
	listenOn := s.listenOn
	running := s.running
	s.mu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"running":         running,
		"address":         listenOn,
		"bind":            cfg.Server.Bind,
		"port":            cfg.Server.Port,
		"secrets_backend": s.secrets.Backend(),
		"accounts":        s.pool.Snapshots(),
		"banners":         s.pool.Banners(),
		"models_count":    len(s.router.ListModels(false)),
	})
}

func (s *Server) handleAdminLogs(w http.ResponseWriter, r *http.Request) {
	var entries []logbuf.Entry
	if s.logs != nil {
		entries = s.logs.Entries()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
	})
}

func (s *Server) handleAdminReload(w http.ResponseWriter, r *http.Request) {
	cfg, _, err := config.Load(s.dataDir)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "reload_error", err.Error())
		return
	}
	s.mu.Lock()
	prevAccounts := make(map[string]config.AccountConfig, len(s.cfg.Accounts))
	for _, a := range s.cfg.Accounts {
		prevAccounts[a.ID] = a
	}
	s.cfg = cfg
	s.privacy = privacy.NewFilter(cfg.Privacy)
	if s.logs != nil {
		s.logs.RegisterSecret(cfg.Server.LocalKey)
		for _, k := range cfg.Server.APIKeys {
			s.logs.RegisterSecret(k.Key)
		}
	}
	s.mu.Unlock()

	if s.secrets != nil {
		s.secrets.Reload()
	}
	s.router.UpdateConfig(cfg)
	s.pool.SyncAccounts(cfg.Accounts)
	// Stop stale ACP workers when an account is re-enabled or its profile mode changed so it picks up fresh auth.
	for _, a := range cfg.Accounts {
		if a.Kind != config.KindACP {
			continue
		}
		prev, existed := prevAccounts[a.ID]
		if !existed || (!prev.Enabled && a.Enabled) || prev.UseHostProfile != a.UseHostProfile || (prev.DisabledReason != "" && a.DisabledReason == "") {
			s.acpBE.StopAccount(a.ID)
		}
	}
	s.acpBE.SetToolActivity(cfg.ACP.ToolActivity)
	if s.health != nil {
		s.health.UpdateConfig(cfg.Health)
	}
	if s.alerts != nil {
		s.alerts.UpdateConfig(cfg.Alerts)
	}
	if s.logs != nil {
		s.logs.Info("system", fmt.Sprintf("hot-reloaded config (%d accounts, %d models)", len(cfg.Accounts), len(s.router.ListModels(false))))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "reloaded",
		"accounts": len(cfg.Accounts),
		"models":   len(s.router.ListModels(false)),
	})
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	models := s.router.ListModels(false)
	p := principalFromContext(r.Context())
	now := time.Now().Unix()
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		if p != nil && p.Client != nil && !p.Client.AllowsModel(m.ID) {
			continue
		}
		if plan, err := s.router.Resolve(m.ID); err == nil && s.agentAccessError(p, plan) != nil {
			continue
		}
		owner := m.Backend
		if m.AccountName != "" {
			owner = m.Backend + "/" + m.AccountName
		}
		entry := map[string]any{
			"id":       m.ID,
			"object":   "model",
			"created":  now,
			"owned_by": owner,
		}
		entry["display_name"] = router.Label(m)
		if len(m.Efforts) > 0 {
			entry["reasoning_efforts"] = m.Efforts
		}
		data = append(data, entry)
	}
	if decID := EffectiveDecisionModelID(s.Config().Decision); decID != "" {
		if p == nil || p.Client == nil || p.Client.AllowsModel(decID) {
			data = append(data, map[string]any{
				"id":           decID,
				"object":       "model",
				"created":      now,
				"owned_by":     "apx-decision",
				"display_name": "AgentPassage Auto Router",
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
}

type openAIChatRequest struct {
	Model           string            `json:"model"`
	Messages        []openAIMessage   `json:"messages"`
	Stream          bool              `json:"stream"`
	Temperature     *float64          `json:"temperature,omitempty"`
	MaxTokens       int               `json:"max_tokens,omitempty"`
	MaxCompTok      int               `json:"max_completion_tokens,omitempty"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
	User            string            `json:"user,omitempty"`
	ToolChoice      json.RawMessage   `json:"tool_choice,omitempty"`
	Tools           []json.RawMessage `json:"tools,omitempty"`
	Functions       []json.RawMessage `json:"functions,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    json.RawMessage  `json:"content"`
	ToolCalls  []openAIToolCall `json:"tool_calls"`
	ToolCallID string           `json:"tool_call_id"`
}

func extractMessageContent(raw json.RawMessage) string {
	text, _ := parseContent(raw, "")
	return text
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rawBody, err := io.ReadAll(io.LimitReader(r.Body, maxChatBodyBytes))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "failed to read request body")
		return
	}
	var req openAIChatRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON request body: "+err.Error())
		return
	}

	msgs := make([]backend.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		text, atts := parseContent(m.Content, "")
		msgs = append(msgs, backend.Message{
			Role:        m.Role,
			Content:     text,
			Attachments: atts,
			ToolCalls:   parseOpenAIToolCalls(m.ToolCalls),
			ToolCallID:  m.ToolCallID,
		})
	}
	tools, toolChoice := parseOpenAITools(req.Tools, req.ToolChoice)
	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = req.MaxCompTok
	}
	sessionKey := headerFirst(r, "X-Apx-Session", "X-Acpx-Session")
	sharedSession := false
	if sessionKey == "" {
		sessionKey = strings.TrimSpace(req.User)
		sharedSession = true
	}
	project := projectFromRequest(r)
	sessionKey = sessionScope(principalFromContext(r.Context()), sessionKey)

	chatReq := backend.ChatRequest{
		Model:           req.Model,
		ReasoningEffort: router.NormalizeEffort(req.ReasoningEffort),
		Messages:        msgs,
		Stream:          req.Stream,
		Temperature:     req.Temperature,
		MaxTokens:       maxTok,
		SessionKey:      sessionKey,
		SharedSession:   sharedSession,
		Cwd:             headerFirst(r, "X-Apx-Cwd", "X-Acpx-Cwd"),
		HasTools:        len(req.Tools) > 0 || len(req.Functions) > 0,
		Tools:           tools,
		ToolChoice:      toolChoice,
		RawOpenAIBody:   rawBody,
	}

	if resolved, reason, ok := s.ResolveDecisionModel(req.Model, chatReq); ok && resolved != "" {
		req.Model = resolved
		chatReq.Model = resolved
		w.Header().Set("X-Apx-Decision-Model", resolved)
		if reason != "" {
			w.Header().Set("X-Apx-Decision-Reason", reason)
		}
	}

	principal := principalFromContext(r.Context())
	if principal != nil && principal.Client != nil && !principal.Client.AllowsModel(req.Model) {
		writeOpenAIError(w, http.StatusForbidden, "model_not_allowed", fmt.Sprintf("API key %q is not allowed to access model %q.", principal.KeyName, req.Model))
		return
	}
	plan, err := s.router.ResolveForProject(req.Model, project)
	if err != nil {
		s.logRequest(r, req.Model, "", http.StatusNotFound, start, err)
		writeOpenAIError(w, http.StatusNotFound, "model_not_found", err.Error())
		return
	}
	if loopErr := checkLoopGuard(r, plan); loopErr != nil {
		s.logRequest(r, req.Model, plan.Backend, http.StatusLoopDetected, start, loopErr)
		writeOpenAIError(w, http.StatusLoopDetected, "loop_detected", loopErr.Error())
		return
	}
	if accessErr := s.agentAccessError(principal, plan); accessErr != nil {
		s.logRequest(r, req.Model, "", http.StatusForbidden, start, accessErr)
		writeOpenAIError(w, http.StatusForbidden, "model_not_allowed", accessErr.Error())
		return
	}
	chatReq.UpstreamModel = plan.UpstreamModel

	// Opt-in local LRU TTL response cache for non-streaming completions.
	useCache := !req.Stream && s.cache != nil && (strings.EqualFold(r.Header.Get("X-Apx-Cache"), "true") ||
		plan.Rule.Cache ||
		(s.Config().Cache.Enabled && req.Temperature != nil && *req.Temperature == 0))
	cacheKey := ""
	if useCache {
		cacheKey = cache.Key([]byte(req.Model), []byte(chatReq.ReasoningEffort), rawBody)
		if cached, hit := s.cache.Get(cacheKey); hit {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Apx-Cache", "HIT")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(cached)
			return
		}
		w.Header().Set("X-Apx-Cache", "MISS")
	}

	privSess := s.newPrivacySession()
	chatReq = applyPrivacyToRequest(privSess, chatReq)

	if req.Stream {
		s.executeOpenAIStream(w, r, plan, project, principal, chatReq, privSess, start)
		return
	}

	res := s.executeNonStreamWithFallback(r.Context(), plan, project, principal, chatReq)
	resp, acct, status, execErr := res.Resp, res.Account, res.Status, res.Err
	if execErr != nil {
		acctName := ""
		backendID := plan.Backend
		if acct != nil {
			acctName = acct.Backend + "/" + acct.Name
			backendID = acct.Backend
		}
		s.logRequest(r, req.Model, acctName, status, start, execErr)
		s.recordUsageForRequest(r, req.Model, acctName, backendID, status, start, backend.Usage{}, false)
		writeOpenAIError(w, status, "backend_error", execErr.Error())
		return
	}
	if privSess != nil {
		resp.Content = privSess.RestoreText(resp.Content)
		s.recordPrivacyHeader(w, privSess, res.UsedModel)
	}

	acctName := acct.Backend + "/" + acct.Name
	setRouteHeaders(w, acctName, res.UsedModel, res.FallbackFrom, project, resp.ToolsIgnored, resp.SessionID)

	u := resp.Usage
	est := false
	if u.TotalTokens == 0 {
		u.PromptTokens = estimateMessagesTokens(chatReq.Messages)
		u.CompletionTokens = usage.EstimateTokens(resp.Content)
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
		est = true
	}
	s.recordUsageForRequest(r, res.UsedModel, acctName, acct.Backend, http.StatusOK, start, u, est)

	message := map[string]any{"role": "assistant", "content": resp.Content}
	if len(resp.ToolCalls) > 0 {
		message["tool_calls"] = openAIToolCallsOut(resp.ToolCalls, privSess)
		if resp.Content == "" {
			message["content"] = nil
		}
	}

	id := resp.ID
	if id == "" {
		id = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	out := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       message,
				"finish_reason": resp.FinishReason,
			},
		},
		"usage": resp.Usage,
	}
	s.logRequest(r, req.Model, acctName, http.StatusOK, start, nil)
	if useCache && cacheKey != "" {
		if rawOut, err := json.Marshal(out); err == nil {
			s.cache.Set(cacheKey, rawOut, 0)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) newPrivacySession() *privacy.Session {
	s.mu.RLock()
	f := s.privacy
	s.mu.RUnlock()
	if f == nil {
		return nil
	}
	return f.NewSession()
}

func applyPrivacyToRequest(sess *privacy.Session, req backend.ChatRequest) backend.ChatRequest {
	if sess == nil {
		return req
	}
	req.Messages = sess.RedactMessages(req.Messages)
	if len(req.RawOpenAIBody) > 0 {
		req.RawOpenAIBody = sess.RedactRawOpenAIBody(req.RawOpenAIBody)
	}
	return req
}

func (s *Server) recordPrivacyHeader(w http.ResponseWriter, sess *privacy.Session, model string) {
	if sess == nil {
		return
	}
	if hdr := sess.HeaderValue(); hdr != "" {
		if w != nil {
			w.Header().Set("X-Apx-Redacted", hdr)
		}
		if s.logs != nil {
			s.logs.Info("privacy", fmt.Sprintf("redacted & restored (%s) for %s", hdr, model))
		}
	}
}

func (s *Server) executeNonStream(ctx context.Context, plan *router.Plan, req backend.ChatRequest) (*backend.ChatResponse, *config.AccountConfig, int, error) {
	tried := make(map[string]bool)
	var lastAcct *config.AccountConfig
	var lastErr error

	for attempt := 0; attempt < max(len(plan.CandidateIDs), 1); attempt++ {
		lease, err := s.pool.Acquire(s.buildPoolSelectRequest(ctx, plan, req.SessionKey, tried))
		if err != nil {
			if lastErr != nil {
				return nil, lastAcct, httpStatusFromError(lastErr), lastErr
			}
			return nil, nil, httpStatusFromPoolError(err), err
		}

		acct := lease.Account
		lastAcct = &acct
		tried[acct.ID] = true

		acctReq := applyRouteDefaults(req, plan.Rule)
		acctReq.UpstreamModel = router.ResolveAccountEffortModel(acct, req.UpstreamModel, acctReq.ReasoningEffort)

		callStart := time.Now()
		var resp *backend.ChatResponse
		var callErr error
		switch acct.Kind {
		case config.KindACP:
			resp, callErr = s.acpBE.Complete(ctx, acct, acctReq)
		case config.KindCLI:
			resp, callErr = s.cliBE.Complete(ctx, acct, acctReq)
		default:
			apiKey, _ := s.secrets.Get(acct.ID)
			resp, callErr = s.httpBE.Complete(ctx, acct, apiKey, acctReq)
		}
		lease.Release()

		if callErr == nil {
			s.pool.RecordSuccess(acct.ID)
			s.pool.PinSession(req.SessionKey, acct.ID)
			s.pool.RecordLatency(acct.ID, time.Since(callStart).Milliseconds())
			return resp, &acct, http.StatusOK, nil
		}

		lastErr = callErr
		allowFailover := s.recordFailure(ctx, acct.ID, plan, callErr)
		if !allowFailover {
			return nil, &acct, httpStatusFromError(callErr), callErr
		}
	}
	if lastErr == nil {
		lastErr = pool.ErrNoAvailableAccount
	}
	return nil, lastAcct, httpStatusFromError(lastErr), lastErr
}

func (s *Server) executeOpenAIStream(w http.ResponseWriter, r *http.Request, primaryPlan *router.Plan, project string, principal *authPrincipal, req backend.ChatRequest, privSess *privacy.Session, start time.Time) {
	chain := s.buildModelChain(primaryPlan, project, principal)
	var lastErr error
	var lastAcct string

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "streaming_unsupported", "streaming unsupported")
		return
	}

	for planIdx, plan := range chain {
		if planIdx > 0 {
			if loopErr := checkLoopGuard(r, plan); loopErr != nil {
				continue
			}
		}
		tried := make(map[string]bool)
		var planErr error

		for attempt := 0; attempt < max(len(plan.CandidateIDs), 1); attempt++ {
			lease, err := s.pool.Acquire(s.buildPoolSelectRequest(r.Context(), plan, req.SessionKey, tried))
			if err != nil {
				if planErr == nil {
					planErr = err
				}
				break
			}

			acct := lease.Account
			acctName := acct.Backend + "/" + acct.Name
			lastAcct = acctName
			tried[acct.ID] = true

			headersSent := false
			compID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
			created := time.Now().Unix()
			usedModel := plan.RequestedModel

			sendHeaders := func(toolsIgnored bool, sessionID string) {
				if headersSent {
					return
				}
				headersSent = true
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				fallbackFrom := ""
				if planIdx > 0 {
					fallbackFrom = primaryPlan.RequestedModel
				}
				setRouteHeaders(w, acctName, usedModel, fallbackFrom, project, toolsIgnored, sessionID)
				s.recordPrivacyHeader(w, privSess, usedModel)
				w.WriteHeader(http.StatusOK)
				writeSSE(w, map[string]any{
					"id":      compID,
					"object":  "chat.completion.chunk",
					"created": created,
					"model":   usedModel,
					"choices": []map[string]any{
						{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil},
					},
				})
				flusher.Flush()
			}

			var finishedSent bool
			var streamUsage backend.Usage
			var outTextLen int
			deltaRestorer := privSess.NewStreamRestorer()
			reasonRestorer := privSess.NewStreamRestorer()
			toolCalls := &toolStream{priv: privSess}
			cb := func(ch backend.StreamChunk) error {
				if ch.Delta == "" && ch.Reasoning == "" && ch.FinishReason == "" && ch.Usage == nil && len(ch.ToolCalls) == 0 {
					return nil
				}
				sendHeaders(ch.ToolsIgnored, ch.SessionID)
				restoredDelta := deltaRestorer.Push(ch.Delta)
				restoredReason := reasonRestorer.Push(ch.Reasoning)
				if ch.FinishReason != "" {
					restoredDelta += deltaRestorer.Flush()
					restoredReason += reasonRestorer.Flush()
				}
				delta := map[string]any{}
				calls := toolCalls.push(ch.ToolCalls)
				if ch.FinishReason != "" {
					calls = append(calls, toolCalls.flush()...)
				}
				if len(calls) > 0 {
					delta["tool_calls"] = calls
				}
				if restoredDelta != "" {
					delta["content"] = restoredDelta
					outTextLen += len(restoredDelta)
				}
				if restoredReason != "" {
					delta["reasoning_content"] = restoredReason
				}
				if len(delta) == 0 && ch.FinishReason == "" && ch.Usage == nil {
					return nil
				}
				var fr any = nil
				if ch.FinishReason != "" {
					fr = ch.FinishReason
					finishedSent = true
				}
				payload := map[string]any{
					"id":      compID,
					"object":  "chat.completion.chunk",
					"created": created,
					"model":   usedModel,
					"choices": []map[string]any{
						{"index": 0, "delta": delta, "finish_reason": fr},
					},
				}
				if ch.Usage != nil {
					payload["usage"] = ch.Usage
					streamUsage = *ch.Usage
				}
				writeSSE(w, payload)
				flusher.Flush()
				return nil
			}

			acctReq := applyRouteDefaults(req, plan.Rule)
			acctReq.Model = usedModel
			acctReq.UpstreamModel = router.ResolveAccountEffortModel(acct, plan.UpstreamModel, acctReq.ReasoningEffort)

			var streamErr error
			switch acct.Kind {
			case config.KindACP:
				streamErr = s.acpBE.Stream(r.Context(), acct, acctReq, cb)
			case config.KindCLI:
				streamErr = s.cliBE.Stream(r.Context(), acct, acctReq, cb)
			default:
				apiKey, _ := s.secrets.Get(acct.ID)
				streamErr = s.httpBE.Stream(r.Context(), acct, apiKey, acctReq, cb)
			}
			lease.Release()

			if streamErr == nil {
				s.pool.RecordSuccess(acct.ID)
				s.pool.PinSession(req.SessionKey, acct.ID)
				sendHeaders(acct.Kind == config.KindACP && req.HasTools, "")
				if tailDelta, tailReason := deltaRestorer.Flush(), reasonRestorer.Flush(); tailDelta != "" || tailReason != "" {
					delta := map[string]any{}
					if tailDelta != "" {
						delta["content"] = tailDelta
						outTextLen += len(tailDelta)
					}
					if tailReason != "" {
						delta["reasoning_content"] = tailReason
					}
					writeSSE(w, map[string]any{
						"id":      compID,
						"object":  "chat.completion.chunk",
						"created": created,
						"model":   usedModel,
						"choices": []map[string]any{
							{"index": 0, "delta": delta, "finish_reason": nil},
						},
					})
				}
				if !finishedSent {
					writeSSE(w, map[string]any{
						"id":      compID,
						"object":  "chat.completion.chunk",
						"created": created,
						"model":   usedModel,
						"choices": []map[string]any{
							{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"},
						},
					})
				}
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
				flusher.Flush()
				est := false
				if streamUsage.TotalTokens == 0 {
					streamUsage.PromptTokens = estimateMessagesTokens(req.Messages)
					streamUsage.CompletionTokens = (outTextLen + 3) / 4
					streamUsage.TotalTokens = streamUsage.PromptTokens + streamUsage.CompletionTokens
					est = true
				}
				s.recordUsageForRequest(r, usedModel, acctName, acct.Backend, http.StatusOK, start, streamUsage, est)
				s.logRequest(r, usedModel, acctName, http.StatusOK, start, nil)
				return
			}

			planErr = streamErr
			lastErr = streamErr
			allowFailover := s.recordFailure(r.Context(), acct.ID, plan, streamErr)
			if headersSent {
				// Cannot retry after streaming started; report the error and close.
				s.logRequest(r, usedModel, acctName, httpStatusFromError(streamErr), start, streamErr)
				writeSSE(w, map[string]any{
					"error": map[string]any{"type": "upstream_error", "message": streamErr.Error()},
				})
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
				flusher.Flush()
				return
			}
			if !allowFailover {
				break
			}
		}

		if planErr != nil {
			lastErr = planErr
		}
		if r.Context().Err() != nil || !shouldFallbackModel(lastErr, primaryPlan.Rule) {
			break
		}
		if planIdx+1 < len(chain) && s.logs != nil {
			s.logs.Warn("fallback", fmt.Sprintf("stream model %s failed (%s); falling back to %s", plan.RequestedModel, classifyFallbackKind(lastErr), chain[planIdx+1].RequestedModel))
		}
	}

	status, lastErr := finalFailure(lastErr)
	s.logRequest(r, req.Model, lastAcct, status, start, lastErr)
	writeOpenAIError(w, status, "backend_error", lastErr.Error())
}

func (s *Server) recordUsageForRequest(r *http.Request, model, acct, backendID string, status int, start time.Time, u backend.Usage, estimated bool) {
	if s.usage == nil {
		return
	}
	keyName := "local-admin"
	if r != nil {
		if p := principalFromContext(r.Context()); p != nil && p.KeyName != "" {
			keyName = p.KeyName
		}
	}
	endpoint := ""
	if r != nil {
		endpoint = r.URL.Path
	}
	s.usage.Record(usage.Record{
		Time:             time.Now(),
		Account:          acct,
		Backend:          backendID,
		Model:            model,
		Endpoint:         endpoint,
		APIKey:           keyName,
		Project:          projectFromRequest(r),
		Status:           status,
		DurationMs:       time.Since(start).Milliseconds(),
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CacheReadTokens:  u.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens,
		Estimated:        estimated,
	})
}

func estimateMessagesTokens(msgs []backend.Message) int {
	total := 0
	for _, m := range msgs {
		total += usage.EstimateTokens(m.Content)
	}
	return total
}

func (s *Server) logRequest(r *http.Request, model, acct string, status int, start time.Time, err error) {
	if s.logs == nil {
		return
	}
	lvl := logbuf.LevelInfo
	errStr := ""
	if err != nil {
		lvl = logbuf.LevelError
		errStr = err.Error()
	} else if status >= 400 {
		lvl = logbuf.LevelWarn
	}
	s.logs.Add(logbuf.Entry{
		Time:       time.Now(),
		Level:      lvl,
		Kind:       "request",
		Method:     r.Method,
		Path:       r.URL.Path,
		Model:      model,
		Account:    acct,
		Status:     status,
		DurationMs: time.Since(start).Milliseconds(),
		Error:      errStr,
	})
}

// recordFailure notes a failed call against the account and reports whether the
// request may try another one. A caller that gave up costs the account nothing.
func (s *Server) recordFailure(ctx context.Context, accountID string, plan *router.Plan, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	return s.pool.RecordErrorForGroup(accountID, quota.GroupForModel(plan.Backend, plan.UpstreamModel), err, plan.Rule)
}

// finalFailure is the status and error to report once every candidate failed.
func finalFailure(lastErr error) (int, error) {
	if lastErr == nil {
		lastErr = pool.ErrNoAvailableAccount
	}
	if errors.Is(lastErr, pool.ErrNoAvailableAccount) || errors.Is(lastErr, pool.ErrStrategyOptInNeeded) {
		return httpStatusFromPoolError(lastErr), lastErr
	}
	return httpStatusFromError(lastErr), lastErr
}

func httpStatusFromPoolError(err error) int {
	if errors.Is(err, pool.ErrStrategyOptInNeeded) {
		return http.StatusForbidden
	}
	return http.StatusServiceUnavailable
}

func httpStatusFromError(err error) int {
	if be, ok := backend.AsBackendError(err); ok {
		if be.StatusCode >= 400 {
			return be.StatusCode
		}
		switch be.Kind {
		case backend.ErrKindRateLimit:
			return http.StatusTooManyRequests
		case backend.ErrKindAuth, backend.ErrKindRestricted:
			return http.StatusUnauthorized
		case backend.ErrKindClient:
			return http.StatusBadRequest
		}
	}
	return http.StatusBadGateway
}

// setRouteHeaders reports how a chat request was routed.
func setRouteHeaders(w http.ResponseWriter, account, model, fallbackFrom, project string, toolsIgnored bool, sessionID string) {
	h := w.Header()
	h.Set("X-Apx-Account", account)
	h.Set("X-Apx-Model", model)
	if fallbackFrom != "" {
		h.Set("X-Apx-Fallback-From", fallbackFrom)
	}
	if project != "" {
		h.Set("X-Apx-Project", project)
	}
	if toolsIgnored {
		h.Set("X-Apx-Tools-Ignored", "true")
	}
	if sessionID != "" {
		h.Set("X-Apx-Session-Id", sessionID)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeSSE(w io.Writer, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
}

func writeOpenAIError(w http.ResponseWriter, status int, errType, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    errType,
		},
	})
}

func headerFirst(r *http.Request, names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(r.Header.Get(n)); v != "" {
			return v
		}
	}
	return ""
}

func checkLoopGuard(r *http.Request, plan *router.Plan) error {
	if hopRaw := headerFirst(r, "X-Apx-Hop", "X-Acpx-Hop"); hopRaw != "" {
		if hop, err := strconv.Atoi(hopRaw); err == nil && hop >= 2 {
			return fmt.Errorf("recursive apx loop detected (X-Apx-Hop=%d)", hop)
		}
	}
	client := strings.ToLower(headerFirst(r, "X-Apx-Client", "X-Acpx-Client"))
	if client != "" && plan.Backend != "" && client == strings.ToLower(plan.Backend) {
		return fmt.Errorf("loop prevented: client %q cannot route back into backend %q", client, plan.Backend)
	}
	return nil
}
