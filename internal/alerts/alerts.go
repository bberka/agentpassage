package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	neturl "net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/bberka/agentpassage/internal/config"
)

type Event struct {
	Time     time.Time `json:"time"`
	Kind     string    `json:"kind"` // "auth_disabled", "rate_limit", "quota_threshold", "balance_threshold"
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
	onError  func(error)
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

// SetErrorHook registers fn to receive delivery failures.
func (n *Notifier) SetErrorHook(fn func(error)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.onError = fn
}

// Config returns the alert settings Notify currently applies.
func (n *Notifier) Config() config.AlertsConfig {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.cfg
}

func (n *Notifier) report(err error) {
	n.mu.Lock()
	fn := n.onError
	n.mu.Unlock()
	if fn != nil && err != nil {
		fn(err)
	}
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
		go func() { n.report(sendDesktopNotification(ev.Title, ev.Message)) }()
	}
	if url := strings.TrimSpace(cfg.WebhookURL); url != "" {
		go n.sendWebhook(url, ev)
	}
}

func (n *Notifier) sendWebhook(url string, ev Event) {
	body, err := json.Marshal(ev)
	if err != nil {
		n.report(fmt.Errorf("webhook alert: %w", err))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		n.report(errors.New("webhook alert: invalid webhook_url"))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		// Drop the URL from the error: webhook URLs often embed a token.
		var ue *neturl.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		n.report(fmt.Errorf("webhook alert: %w", err))
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		n.report(fmt.Errorf("webhook alert: HTTP %d", resp.StatusCode))
	}
}

func sendDesktopNotification(title, body string) error {
	if title == "" {
		title = "apx"
	}
	var args []string
	switch runtime.GOOS {
	case "linux":
		args = []string{"notify-send", "--app-name=apx", title, body}
	case "darwin":
		esc := func(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }
		script := `display notification "` + esc(body) + `" with title "` + esc(title) + `"`
		args = []string{"osascript", "-e", script}
	default:
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.WaitDelay = time.Second
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("desktop alert: %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}
