package usage

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Record struct {
	Time             time.Time `json:"time"`
	Account          string    `json:"account"`
	Backend          string    `json:"backend"`
	Model            string    `json:"model"`
	Endpoint         string    `json:"endpoint"`
	APIKey           string    `json:"api_key,omitempty"`
	Project          string    `json:"project,omitempty"`
	Status           int       `json:"status"`
	DurationMs       int64     `json:"duration_ms"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	EstimatedCostUSD float64   `json:"estimated_cost_usd,omitempty"`
	Estimated        bool      `json:"estimated,omitempty"`
}

type Bucket struct {
	Label    string `json:"label"`
	Requests int    `json:"requests"`
	Errors   int    `json:"errors"`
	Tokens   int    `json:"tokens"`
}

type AccountUsage struct {
	Account          string    `json:"account"`
	Backend          string    `json:"backend"`
	Requests         int       `json:"requests"`
	Errors           int       `json:"errors"`
	Reqs1h           int       `json:"reqs_1h"`
	Reqs5h           int       `json:"reqs_5h"`
	Reqs24h          int       `json:"reqs_24h"`
	Reqs7d           int       `json:"reqs_7d"`
	Tokens5h         int       `json:"tokens_5h"`
	Tokens7d         int       `json:"tokens_7d"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	EstimatedCostUSD float64   `json:"estimated_cost_usd,omitempty"`
	AvgLatencyMs     int64     `json:"avg_latency_ms"`
	LastUsed         time.Time `json:"last_used"`
}

type ModelUsage struct {
	Model            string  `json:"model"`
	Requests         int     `json:"requests"`
	Errors           int     `json:"errors"`
	TotalTokens      int     `json:"total_tokens"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd,omitempty"`
	AvgLatencyMs     int64   `json:"avg_latency_ms"`
}

type KeyUsage struct {
	Key              string  `json:"key"`
	Requests         int     `json:"requests"`
	Errors           int     `json:"errors"`
	TotalTokens      int     `json:"total_tokens"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd,omitempty"`
	AvgLatencyMs     int64   `json:"avg_latency_ms"`
}

type ProjectUsage struct {
	Project          string  `json:"project"`
	Requests         int     `json:"requests"`
	Errors           int     `json:"errors"`
	TotalTokens      int     `json:"total_tokens"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd,omitempty"`
	AvgLatencyMs     int64   `json:"avg_latency_ms"`
}

type Summary struct {
	GeneratedAt      time.Time      `json:"generated_at"`
	Period           string         `json:"period"`
	TotalRequests    int            `json:"total_requests"`
	SuccessRequests  int            `json:"success_requests"`
	ErrorRequests    int            `json:"error_requests"`
	SuccessRate      float64        `json:"success_rate"`
	Requests24h      int            `json:"requests_24h"`
	Requests7d       int            `json:"requests_7d"`
	PromptTokens     int            `json:"prompt_tokens"`
	CompletionTokens int            `json:"completion_tokens"`
	TotalTokens      int            `json:"total_tokens"`
	Tokens24h        int            `json:"tokens_24h"`
	EstimatedCostUSD float64        `json:"estimated_cost_usd"`
	CostEstimated    bool           `json:"cost_estimated"`
	AvgLatencyMs     int64          `json:"avg_latency_ms"`
	Hourly24h        []Bucket       `json:"hourly_24h"`
	Daily14d         []Bucket       `json:"daily_14d"`
	ByAccount        []AccountUsage `json:"by_account"`
	ByModel          []ModelUsage   `json:"by_model"`
	ByKey            []KeyUsage     `json:"by_key,omitempty"`
	ByProject        []ProjectUsage `json:"by_project,omitempty"`
	Recent           []Record       `json:"recent"`
}

const maxInMemoryRecords = 20000

type cachedSummary struct {
	version uint64
	at      time.Time
	sum     Summary
}

// Tracker records per-request usage metrics in memory and persists them to <dataDir>/usage.jsonl.
type Tracker struct {
	mu      sync.RWMutex
	file    *os.File
	path    string
	records []Record
	version uint64
	cache   map[string]cachedSummary
}

func New(dataDir string) *Tracker {
	t := &Tracker{
		records: make([]Record, 0, 256),
		cache:   make(map[string]cachedSummary),
	}
	if dataDir != "" {
		t.path = filepath.Join(dataDir, "usage.jsonl")
		t.loadExisting()
		if err := os.MkdirAll(dataDir, 0o700); err == nil {
			if f, err := os.OpenFile(t.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
				t.file = f
			}
		}
	}
	return t
}

func (t *Tracker) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file != nil {
		err := t.file.Close()
		t.file = nil
		return err
	}
	return nil
}

func (t *Tracker) loadExisting() {
	f, err := os.Open(t.path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 32*1024), 1024*1024)
	for sc.Scan() {
		var rec Record
		if err := json.Unmarshal(sc.Bytes(), &rec); err == nil && !rec.Time.IsZero() {
			t.records = append(t.records, rec)
		}
	}
	if len(t.records) > maxInMemoryRecords {
		t.records = t.records[len(t.records)-maxInMemoryRecords:]
	}
}

func (t *Tracker) Record(rec Record) {
	if rec.Time.IsZero() {
		rec.Time = time.Now()
	}
	if rec.TotalTokens == 0 && (rec.PromptTokens > 0 || rec.CompletionTokens > 0) {
		rec.TotalTokens = rec.PromptTokens + rec.CompletionTokens
	}
	if rec.EstimatedCostUSD == 0 && rec.TotalTokens > 0 {
		if usd, ok := EstimateCostUSD(rec.Model, rec.PromptTokens, rec.CompletionTokens); ok {
			rec.EstimatedCostUSD = usd
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.records = append(t.records, rec)
	if len(t.records) > maxInMemoryRecords {
		t.records = t.records[len(t.records)-maxInMemoryRecords:]
	}
	t.version++
	if t.file != nil {
		if raw, err := json.Marshal(rec); err == nil {
			_, _ = t.file.Write(append(raw, '\n'))
		}
	}
}

// EstimateTokens provides a lightweight ~4 chars/token estimate when streaming ACP/CLI backends omit usage counts.
func EstimateTokens(text string) int {
	n := len(text)
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}

func (t *Tracker) Summary() Summary {
	return t.SummaryPeriodAt(time.Now(), "all")
}

func (t *Tracker) SummaryAt(now time.Time) Summary {
	return t.SummaryPeriodAt(now, "all")
}

func (t *Tracker) SummaryPeriod(period string) Summary {
	now := time.Now()
	normPeriod, _ := periodCutoff(now, period)
	t.mu.RLock()
	if c, ok := t.cache[normPeriod]; ok && c.version == t.version && now.Sub(c.at) < 15*time.Second {
		sum := c.sum
		t.mu.RUnlock()
		return sum
	}
	ver := t.version
	t.mu.RUnlock()

	sum := t.SummaryPeriodAt(now, normPeriod)
	t.mu.Lock()
	if t.version == ver {
		if t.cache == nil {
			t.cache = make(map[string]cachedSummary)
		}
		t.cache[normPeriod] = cachedSummary{version: ver, at: now, sum: sum}
	}
	t.mu.Unlock()
	return sum
}

func periodCutoff(now time.Time, period string) (string, time.Time) {
	switch period {
	case "1h":
		return "1h", now.Add(-1 * time.Hour)
	case "24h", "1d":
		return "24h", now.Add(-24 * time.Hour)
	case "7d":
		return "7d", now.Add(-7 * 24 * time.Hour)
	case "30d":
		return "30d", now.Add(-30 * 24 * time.Hour)
	default:
		return "all", time.Time{}
	}
}

func (t *Tracker) SummaryPeriodAt(now time.Time, period string) Summary {
	t.mu.RLock()
	recs := append([]Record(nil), t.records...)
	t.mu.RUnlock()

	normPeriod, cutoffPeriod := periodCutoff(now, period)
	s := Summary{
		GeneratedAt:   now,
		Period:        normPeriod,
		CostEstimated: true,
		Hourly24h:     make([]Bucket, 24),
		Daily14d:      make([]Bucket, 14),
	}

	// Initialize 24 hourly buckets (oldest -> newest)
	hourStart := now.Truncate(time.Hour).Add(-23 * time.Hour)
	for i := range 24 {
		s.Hourly24h[i].Label = hourStart.Add(time.Duration(i) * time.Hour).Format("15:00")
	}

	// Initialize 14 daily buckets (oldest -> newest)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	dayStart := today.AddDate(0, 0, -13)
	for i := range 14 {
		s.Daily14d[i].Label = dayStart.AddDate(0, 0, i).Format("01-02")
	}

	cutoff1h := now.Add(-1 * time.Hour)
	cutoff5h := now.Add(-5 * time.Hour)
	cutoff24h := now.Add(-24 * time.Hour)
	cutoff7d := now.Add(-7 * 24 * time.Hour)

	type acctAgg struct {
		u        AccountUsage
		latSumMs int64
	}
	type modelAgg struct {
		u        ModelUsage
		latSumMs int64
	}
	type keyAgg struct {
		u        KeyUsage
		latSumMs int64
	}
	type projAgg struct {
		u        ProjectUsage
		latSumMs int64
	}
	acctMap := make(map[string]*acctAgg)
	modelMap := make(map[string]*modelAgg)
	keyMap := make(map[string]*keyAgg)
	projMap := make(map[string]*projAgg)
	var totalLatMs int64
	var filtered []Record

	for _, r := range recs {
		// Always compute rolling 1h/5h/24h/7d windows per account for quota/budget tracking.
		acctKey := r.Account
		if acctKey == "" {
			acctKey = "(unrouted)"
		}
		aa, ok := acctMap[acctKey]
		if !ok {
			aa = &acctAgg{u: AccountUsage{Account: acctKey, Backend: r.Backend}}
			acctMap[acctKey] = aa
		}
		if !r.Time.Before(cutoff1h) {
			aa.u.Reqs1h++
		}
		if !r.Time.Before(cutoff5h) {
			aa.u.Reqs5h++
			aa.u.Tokens5h += r.TotalTokens
		}
		if !r.Time.Before(cutoff24h) {
			aa.u.Reqs24h++
			s.Requests24h++
			s.Tokens24h += r.TotalTokens
		}
		if !r.Time.Before(cutoff7d) {
			aa.u.Reqs7d++
			aa.u.Tokens7d += r.TotalTokens
			s.Requests7d++
		}

		if !cutoffPeriod.IsZero() && r.Time.Before(cutoffPeriod) {
			continue
		}
		filtered = append(filtered, r)

		cost := r.EstimatedCostUSD
		if cost == 0 && r.TotalTokens > 0 {
			if usd, okC := EstimateCostUSD(r.Model, r.PromptTokens, r.CompletionTokens); okC {
				cost = usd
			}
		}

		s.TotalRequests++
		isErr := r.Status >= 400 || r.Status == 0
		if isErr {
			s.ErrorRequests++
		} else {
			s.SuccessRequests++
		}
		s.PromptTokens += r.PromptTokens
		s.CompletionTokens += r.CompletionTokens
		s.TotalTokens += r.TotalTokens
		s.EstimatedCostUSD += cost
		totalLatMs += r.DurationMs

		if !r.Time.Before(hourStart) {
			idx := int(r.Time.Truncate(time.Hour).Sub(hourStart) / time.Hour)
			if idx >= 0 && idx < 24 {
				s.Hourly24h[idx].Requests++
				s.Hourly24h[idx].Tokens += r.TotalTokens
				if isErr {
					s.Hourly24h[idx].Errors++
				}
			}
		}

		rDay := time.Date(r.Time.Year(), r.Time.Month(), r.Time.Day(), 0, 0, 0, 0, now.Location())
		if !rDay.Before(dayStart) {
			idx := int(rDay.Sub(dayStart) / (24 * time.Hour))
			if idx >= 0 && idx < 14 {
				s.Daily14d[idx].Requests++
				s.Daily14d[idx].Tokens += r.TotalTokens
				if isErr {
					s.Daily14d[idx].Errors++
				}
			}
		}

		// Per-account (period-filtered)
		aa.u.Requests++
		if isErr {
			aa.u.Errors++
		}
		aa.u.PromptTokens += r.PromptTokens
		aa.u.CompletionTokens += r.CompletionTokens
		aa.u.TotalTokens += r.TotalTokens
		aa.u.EstimatedCostUSD += cost
		aa.latSumMs += r.DurationMs
		if r.Time.After(aa.u.LastUsed) {
			aa.u.LastUsed = r.Time
		}

		mKey := r.Model
		if mKey == "" {
			mKey = "(unknown)"
		}
		ma, ok := modelMap[mKey]
		if !ok {
			ma = &modelAgg{u: ModelUsage{Model: mKey}}
			modelMap[mKey] = ma
		}
		ma.u.Requests++
		if isErr {
			ma.u.Errors++
		}
		ma.u.TotalTokens += r.TotalTokens
		ma.u.EstimatedCostUSD += cost
		ma.latSumMs += r.DurationMs

		kName := r.APIKey
		if kName == "" {
			kName = "local-admin"
		}
		ka, ok := keyMap[kName]
		if !ok {
			ka = &keyAgg{u: KeyUsage{Key: kName}}
			keyMap[kName] = ka
		}
		ka.u.Requests++
		if isErr {
			ka.u.Errors++
		}
		ka.u.TotalTokens += r.TotalTokens
		ka.u.EstimatedCostUSD += cost
		ka.latSumMs += r.DurationMs

		pName := r.Project
		if pName == "" {
			pName = "(default)"
		}
		pa, ok := projMap[pName]
		if !ok {
			pa = &projAgg{u: ProjectUsage{Project: pName}}
			projMap[pName] = pa
		}
		pa.u.Requests++
		if isErr {
			pa.u.Errors++
		}
		pa.u.TotalTokens += r.TotalTokens
		pa.u.EstimatedCostUSD += cost
		pa.latSumMs += r.DurationMs
	}

	if s.TotalRequests > 0 {
		s.SuccessRate = float64(s.SuccessRequests) * 100.0 / float64(s.TotalRequests)
		s.AvgLatencyMs = totalLatMs / int64(s.TotalRequests)
	}

	for _, aa := range acctMap {
		if aa.u.Requests == 0 && aa.u.Reqs7d == 0 {
			continue
		}
		if aa.u.Requests > 0 {
			aa.u.AvgLatencyMs = aa.latSumMs / int64(aa.u.Requests)
		}
		s.ByAccount = append(s.ByAccount, aa.u)
	}
	sort.Slice(s.ByAccount, func(i, j int) bool {
		if s.ByAccount[i].Requests != s.ByAccount[j].Requests {
			return s.ByAccount[i].Requests > s.ByAccount[j].Requests
		}
		return s.ByAccount[i].Account < s.ByAccount[j].Account
	})

	for _, ma := range modelMap {
		if ma.u.Requests > 0 {
			ma.u.AvgLatencyMs = ma.latSumMs / int64(ma.u.Requests)
		}
		s.ByModel = append(s.ByModel, ma.u)
	}
	sort.Slice(s.ByModel, func(i, j int) bool {
		if s.ByModel[i].Requests != s.ByModel[j].Requests {
			return s.ByModel[i].Requests > s.ByModel[j].Requests
		}
		return s.ByModel[i].Model < s.ByModel[j].Model
	})

	for _, ka := range keyMap {
		if ka.u.Requests > 0 {
			ka.u.AvgLatencyMs = ka.latSumMs / int64(ka.u.Requests)
		}
		s.ByKey = append(s.ByKey, ka.u)
	}
	sort.Slice(s.ByKey, func(i, j int) bool {
		if s.ByKey[i].Requests != s.ByKey[j].Requests {
			return s.ByKey[i].Requests > s.ByKey[j].Requests
		}
		return s.ByKey[i].Key < s.ByKey[j].Key
	})

	for _, pa := range projMap {
		if pa.u.Requests > 0 {
			pa.u.AvgLatencyMs = pa.latSumMs / int64(pa.u.Requests)
		}
		s.ByProject = append(s.ByProject, pa.u)
	}
	sort.Slice(s.ByProject, func(i, j int) bool {
		if s.ByProject[i].Requests != s.ByProject[j].Requests {
			return s.ByProject[i].Requests > s.ByProject[j].Requests
		}
		return s.ByProject[i].Project < s.ByProject[j].Project
	})

	// Most recent 50 requests (newest first)
	recentCount := min(len(filtered), 50)
	s.Recent = make([]Record, recentCount)
	for i := range recentCount {
		s.Recent[i] = filtered[len(filtered)-1-i]
	}

	return s
}
