package health

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/logbuf"
)

type Status string

const (
	StatusHealthy   Status = "healthy"
	StatusDegraded  Status = "degraded"
	StatusUnhealthy Status = "unhealthy"
	StatusUnknown   Status = "unknown"
)

type Result struct {
	AccountID        string    `json:"account_id"`
	AccountName      string    `json:"account_name"`
	Backend          string    `json:"backend"`
	Kind             string    `json:"kind"`
	Status           Status    `json:"status"`
	LatencyMs        int64     `json:"latency_ms"`
	ModelsFound      int       `json:"models_found"`
	CheckedAt        time.Time `json:"checked_at"`
	Error            string    `json:"error,omitempty"`
	ConsecutiveFails int       `json:"consecutive_fails"`
}

type Report struct {
	Overall         Status    `json:"overall"`
	Enabled         bool      `json:"enabled"`
	IntervalSeconds int       `json:"interval_seconds"`
	LastSweepAt     time.Time `json:"last_sweep_at,omitempty"`
	HealthyCount    int       `json:"healthy_count"`
	UnhealthyCount  int       `json:"unhealthy_count"`
	TotalCount      int       `json:"total_count"`
	Accounts        []Result  `json:"accounts"`
}

type ProbeFunc func(ctx context.Context, acct config.AccountConfig) ([]string, error)
type ResultHook func(acct config.AccountConfig, models []string, err error)

// Checker performs periodic and on-demand non-destructive health probes across configured accounts.
type Checker struct {
	mu           sync.RWMutex
	getAccounts  func() []config.AccountConfig
	probe        ProbeFunc
	onResult     ResultHook
	logs         *logbuf.Buffer
	enabled      bool
	interval     time.Duration
	lastSweepAt  time.Time
	results      map[string]Result
	checking     map[string]bool
	stopCh       chan struct{}
	stopOnce     sync.Once
	runningLoop  bool
}

func New(getAccounts func() []config.AccountConfig, probe ProbeFunc, onResult ResultHook, logs *logbuf.Buffer, cfg config.HealthConfig) *Checker {
	interval := time.Duration(cfg.IntervalSeconds) * time.Second
	if cfg.Enabled && interval <= 0 {
		interval = 5 * time.Minute
	}
	return &Checker{
		getAccounts: getAccounts,
		probe:       probe,
		onResult:    onResult,
		logs:        logs,
		enabled:     cfg.Enabled,
		interval:    interval,
		results:     make(map[string]Result),
		checking:    make(map[string]bool),
		stopCh:      make(chan struct{}),
	}
}

func (c *Checker) UpdateConfig(cfg config.HealthConfig) {
	c.mu.Lock()
	c.enabled = cfg.Enabled
	if cfg.IntervalSeconds > 0 {
		c.interval = time.Duration(cfg.IntervalSeconds) * time.Second
	} else {
		c.interval = 0
	}
	c.mu.Unlock()
}

func (c *Checker) Start() {
	c.mu.Lock()
	if c.runningLoop {
		c.mu.Unlock()
		return
	}
	c.runningLoop = true
	c.mu.Unlock()

	go func() {
		// Short initial delay so startup stays <10ms and doesn't slow first boot.
		select {
		case <-time.After(10 * time.Second):
		case <-c.stopCh:
			return
		}

		for {
			c.mu.RLock()
			enabled := c.enabled
			interval := c.interval
			c.mu.RUnlock()

			if enabled && interval > 0 {
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				c.CheckAll(ctx)
				cancel()
			}

			wait := interval
			if wait <= 0 {
				wait = 30 * time.Second
			}
			select {
			case <-time.After(wait):
			case <-c.stopCh:
				return
			}
		}
	}()
}

func (c *Checker) Stop() {
	c.stopOnce.Do(func() {
		close(c.stopCh)
	})
}

// CheckOne probes a single account and updates its cached health status.
func (c *Checker) CheckOne(ctx context.Context, acct config.AccountConfig) Result {
	c.mu.Lock()
	if c.checking[acct.ID] {
		existing := c.results[acct.ID]
		c.mu.Unlock()
		return existing
	}
	c.checking[acct.ID] = true
	prev := c.results[acct.ID]
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.checking, acct.ID)
		c.mu.Unlock()
	}()

	start := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	models, err := c.probe(probeCtx, acct)
	cancel()
	latMs := time.Since(start).Milliseconds()

	res := Result{
		AccountID:   acct.ID,
		AccountName: acct.Backend + "/" + acct.Name,
		Backend:     acct.Backend,
		Kind:        acct.Kind,
		LatencyMs:   latMs,
		ModelsFound: len(models),
		CheckedAt:   time.Now(),
	}

	if err != nil {
		res.ConsecutiveFails = prev.ConsecutiveFails + 1
		res.Error = err.Error()
		if res.ConsecutiveFails >= 2 {
			res.Status = StatusUnhealthy
		} else {
			res.Status = StatusDegraded
		}
		if c.logs != nil {
			c.logs.Add(logbuf.Entry{
				Level:      logbuf.LevelWarn,
				Kind:       "health",
				Account:    res.AccountName,
				DurationMs: latMs,
				Message:    "health probe failed",
				Error:      err.Error(),
			})
		}
	} else {
		res.Status = StatusHealthy
		res.ConsecutiveFails = 0
		if c.logs != nil {
			c.logs.Add(logbuf.Entry{
				Level:      logbuf.LevelInfo,
				Kind:       "health",
				Account:    res.AccountName,
				DurationMs: latMs,
				Message:    "health probe ok",
			})
		}
	}

	c.mu.Lock()
	c.results[acct.ID] = res
	c.mu.Unlock()

	if c.onResult != nil {
		c.onResult(acct, models, err)
	}
	return res
}

// CheckAll probes all enabled accounts concurrently (bounded to 4 workers) and returns the updated Report.
func (c *Checker) CheckAll(ctx context.Context) Report {
	accts := c.getAccounts()
	var enabledAccts []config.AccountConfig
	for _, a := range accts {
		if a.Enabled {
			enabledAccts = append(enabledAccts, a)
		}
	}

	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, acct := range enabledAccts {
		wg.Add(1)
		go func(a config.AccountConfig) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c.CheckOne(ctx, a)
		}(acct)
	}
	wg.Wait()

	c.mu.Lock()
	c.lastSweepAt = time.Now()
	c.mu.Unlock()
	return c.Report()
}

// Report returns the latest health check summary across all configured accounts.
func (c *Checker) Report() Report {
	accts := c.getAccounts()
	c.mu.RLock()
	defer c.mu.RUnlock()

	rep := Report{
		Overall:         StatusHealthy,
		Enabled:         c.enabled,
		IntervalSeconds: int(c.interval.Seconds()),
		LastSweepAt:     c.lastSweepAt,
		TotalCount:      len(accts),
		Accounts:        make([]Result, 0, len(accts)),
	}

	seen := make(map[string]bool, len(accts))
	for _, a := range accts {
		seen[a.ID] = true
		r, ok := c.results[a.ID]
		if !ok {
			r = Result{
				AccountID:   a.ID,
				AccountName: a.Backend + "/" + a.Name,
				Backend:     a.Backend,
				Kind:        a.Kind,
				Status:      StatusUnknown,
			}
		} else {
			r.AccountName = a.Backend + "/" + a.Name
		}
		if r.Status == StatusHealthy {
			rep.HealthyCount++
		} else if r.Status == StatusUnhealthy || r.Status == StatusDegraded {
			rep.UnhealthyCount++
		}
		rep.Accounts = append(rep.Accounts, r)
	}

	sort.Slice(rep.Accounts, func(i, j int) bool {
		return rep.Accounts[i].AccountName < rep.Accounts[j].AccountName
	})

	if len(accts) == 0 {
		rep.Overall = StatusUnknown
	} else if rep.UnhealthyCount > 0 && rep.HealthyCount == 0 {
		rep.Overall = StatusUnhealthy
	} else if rep.UnhealthyCount > 0 {
		rep.Overall = StatusDegraded
	} else if rep.HealthyCount == 0 {
		rep.Overall = StatusUnknown
	}

	return rep
}
