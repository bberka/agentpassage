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
	Status           int       `json:"status"`
	DurationMs       int64     `json:"duration_ms"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
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
	AvgLatencyMs     int64     `json:"avg_latency_ms"`
	LastUsed         time.Time `json:"last_used"`
}

type ModelUsage struct {
	Model        string `json:"model"`
	Requests     int    `json:"requests"`
	Errors       int    `json:"errors"`
	TotalTokens  int    `json:"total_tokens"`
	AvgLatencyMs int64  `json:"avg_latency_ms"`
}

type Summary struct {
	GeneratedAt      time.Time      `json:"generated_at"`
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
	AvgLatencyMs     int64          `json:"avg_latency_ms"`
	Hourly24h        []Bucket       `json:"hourly_24h"`
	Daily14d         []Bucket       `json:"daily_14d"`
	ByAccount        []AccountUsage `json:"by_account"`
	ByModel          []ModelUsage   `json:"by_model"`
	Recent           []Record       `json:"recent"`
}

const maxInMemoryRecords = 20000

// Tracker records per-request usage metrics in memory and persists them to <dataDir>/usage.jsonl.
type Tracker struct {
	mu      sync.RWMutex
	file    *os.File
	path    string
	records []Record
}

func New(dataDir string) *Tracker {
	t := &Tracker{
		records: make([]Record, 0, 256),
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

	t.mu.Lock()
	defer t.mu.Unlock()
	t.records = append(t.records, rec)
	if len(t.records) > maxInMemoryRecords {
		t.records = t.records[len(t.records)-maxInMemoryRecords:]
	}
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
	tok := (n + 3) / 4
	if tok < 1 {
		return 1
	}
	return tok
}

func (t *Tracker) Summary() Summary {
	return t.SummaryAt(time.Now())
}

func (t *Tracker) SummaryAt(now time.Time) Summary {
	t.mu.RLock()
	recs := append([]Record(nil), t.records...)
	t.mu.RUnlock()

	s := Summary{
		GeneratedAt: now,
		Hourly24h:   make([]Bucket, 24),
		Daily14d:    make([]Bucket, 14),
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
		u         AccountUsage
		latSumMs  int64
	}
	type modelAgg struct {
		u        ModelUsage
		latSumMs int64
	}
	acctMap := make(map[string]*acctAgg)
	modelMap := make(map[string]*modelAgg)
	var totalLatMs int64

	for _, r := range recs {
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
		totalLatMs += r.DurationMs

		if !r.Time.Before(cutoff24h) {
			s.Requests24h++
			s.Tokens24h += r.TotalTokens
		}
		if !r.Time.Before(cutoff7d) {
			s.Requests7d++
		}

		// Hourly bucket
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

		// Daily bucket
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

		// Per-account
		acctKey := r.Account
		if acctKey == "" {
			acctKey = "(unrouted)"
		}
		aa, ok := acctMap[acctKey]
		if !ok {
			aa = &acctAgg{u: AccountUsage{Account: acctKey, Backend: r.Backend}}
			acctMap[acctKey] = aa
		}
		aa.u.Requests++
		if isErr {
			aa.u.Errors++
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
		}
		if !r.Time.Before(cutoff7d) {
			aa.u.Reqs7d++
			aa.u.Tokens7d += r.TotalTokens
		}
		aa.u.PromptTokens += r.PromptTokens
		aa.u.CompletionTokens += r.CompletionTokens
		aa.u.TotalTokens += r.TotalTokens
		aa.latSumMs += r.DurationMs
		if r.Time.After(aa.u.LastUsed) {
			aa.u.LastUsed = r.Time
		}

		// Per-model
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
		ma.latSumMs += r.DurationMs
	}

	if s.TotalRequests > 0 {
		s.SuccessRate = float64(s.SuccessRequests) * 100.0 / float64(s.TotalRequests)
		s.AvgLatencyMs = totalLatMs / int64(s.TotalRequests)
	}

	for _, aa := range acctMap {
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

	// Most recent 50 requests (newest first)
	recentCount := min(len(recs), 50)
	s.Recent = make([]Record, recentCount)
	for i := range recentCount {
		s.Recent[i] = recs[len(recs)-1-i]
	}

	return s
}
