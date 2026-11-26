package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/bberka/agentpassage/internal/config"
)

type Event struct {
	Time     time.Time `json:"time"`
	Kind     string    `json:"kind"` // "auth_disabled", "rate_limit", "quota_threshold"
	Severity string    `json:"severity"`
	Account  string    `json:"account,omitempty"`
	Title    string    `json:"title"`
	Message  string    `json:"message"`
}

// Notifier dispatches deduplicated desktop notifications and/or webhook POSTs.
type Notifier struct {
	mu       sync.Mutex
	cfg      config.AlertsConfig
	lastSent map[string]time.Time
	client   *http.Client
}

func New(cfg config.AlertsConfig) *Notifier {
	return &Notifier{
		cfg:      cfg,
		lastSent: make(map[string]time.Time),
		client:   &http.Client{Timeout: 8 * time.Second},
	}
}

func (n *Notifier) UpdateConfig(cfg config.AlertsConfig) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.cfg = cfg
}

// Notify sends an alert if enabled and not recently sent for the same dedupKey (5 min window).
func (n *Notifier) Notify(dedupKey string, ev Event) {
	n.mu.Lock()
	cfg := n.cfg
	if !cfg.Enabled {
		n.mu.Unlock()
		return
	}
	now := time.Now()
	if ev.Time.IsZero() {
		ev.Time = now
	}
	if dedupKey != "" {
		if prev, ok := n.lastSent[dedupKey]; ok && now.Sub(prev) < 5*time.Minute {
			n.mu.Unlock()
			return
		}
		n.lastSent[dedupKey] = now
	}
	n.mu.Unlock()

	if cfg.Desktop {
		go sendDesktopNotification(ev.Title, ev.Message)
	}
	if url := strings.TrimSpace(cfg.WebhookURL); url != "" {
		go n.sendWebhook(url, ev)
	}
}

func (n *Notifier) sendWebhook(url string, ev Event) {
	body, err := json.Marshal(ev)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err == nil && resp != nil {
		_ = resp.Body.Close()
	}
}

func sendDesktopNotification(title, body string) {
	if title == "" {
		title = "AgentPassage (apx)"
	}
	switch runtime.GOOS {
	case "linux":
		if _, err := exec.LookPath("notify-send"); err == nil {
			_ = exec.Command("notify-send", title, body).Run()
		}
	case "darwin":
		if _, err := exec.LookPath("osascript"); err == nil {
			esc := func(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }
			script := `display notification "` + esc(body) + `" with title "` + esc(title) + `"`
			_ = exec.Command("osascript", "-e", script).Run()
		}
	}
}
