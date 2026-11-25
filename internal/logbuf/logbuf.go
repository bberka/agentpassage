package logbuf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Level string

const (
	LevelInfo  Level = "INFO"
	LevelWarn  Level = "WARN"
	LevelError Level = "ERROR"
)

type Entry struct {
	Time       time.Time `json:"time"`
	Level      Level     `json:"level"`
	Kind       string    `json:"kind"` // "request", "acp", "health", "system"
	Method     string    `json:"method,omitempty"`
	Path       string    `json:"path,omitempty"`
	Model      string    `json:"model,omitempty"`
	Account    string    `json:"account,omitempty"`
	Status     int       `json:"status,omitempty"`
	DurationMs int64     `json:"duration_ms,omitempty"`
	Message    string    `json:"message,omitempty"`
	Error      string    `json:"error,omitempty"`
}

var (
	tokenPattern = regexp.MustCompile(`(?i)(bearer\s+|x-api-key[:=]\s*|sk-[a-zA-Z0-9_-]{8,}|acpx_[a-fA-F0-9]{8,}|AIza[0-9A-Za-z_-]{16,})([^\s"']*)`)
)

// Buffer keeps recent log entries in memory, redacts secrets, optionally appends to disk, and notifies subscribers.
type Buffer struct {
	mu            sync.RWMutex
	entries       []Entry
	maxEntries    int
	secretSource  func() []string
	extraSecrets  []string
	file          *os.File
	subs          map[int]chan Entry
	nextSubID     int
}

func New(maxEntries int, dataDir string, secretSource func() []string) *Buffer {
	if maxEntries <= 0 {
		maxEntries = 1000
	}
	b := &Buffer{
		entries:      make([]Entry, 0, maxEntries),
		maxEntries:   maxEntries,
		secretSource: secretSource,
		subs:         make(map[int]chan Entry),
	}
	if dataDir != "" {
		logDir := filepath.Join(dataDir, "logs")
		if err := os.MkdirAll(logDir, 0o700); err == nil {
			f, err := os.OpenFile(filepath.Join(logDir, "acpx.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err == nil {
				b.file = f
			}
		}
	}
	return b
}

func (b *Buffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.file != nil {
		err := b.file.Close()
		b.file = nil
		return err
	}
	return nil
}

func (b *Buffer) RegisterSecret(s string) {
	if len(s) < 6 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.extraSecrets = append(b.extraSecrets, s)
}

func (b *Buffer) Redact(s string) string {
	if s == "" {
		return ""
	}
	out := s
	if b.secretSource != nil {
		for _, sec := range b.secretSource() {
			if len(sec) >= 6 {
				out = strings.ReplaceAll(out, sec, "[REDACTED]")
			}
		}
	}
	b.mu.RLock()
	extras := append([]string(nil), b.extraSecrets...)
	b.mu.RUnlock()
	for _, sec := range extras {
		if len(sec) >= 6 {
			out = strings.ReplaceAll(out, sec, "[REDACTED]")
		}
	}
	out = tokenPattern.ReplaceAllStringFunc(out, func(m string) string {
		lower := strings.ToLower(m)
		if strings.HasPrefix(lower, "bearer ") {
			return "Bearer [REDACTED]"
		}
		if strings.HasPrefix(lower, "x-api-key") {
			return "x-api-key: [REDACTED]"
		}
		return "[REDACTED]"
	})
	return out
}

func (b *Buffer) Add(e Entry) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if e.Level == "" {
		e.Level = LevelInfo
	}
	e.Message = b.Redact(e.Message)
	e.Error = b.Redact(e.Error)
	e.Path = b.Redact(e.Path)

	b.mu.Lock()
	if len(b.entries) >= b.maxEntries {
		copy(b.entries, b.entries[1:])
		b.entries[len(b.entries)-1] = e
	} else {
		b.entries = append(b.entries, e)
	}
	if b.file != nil {
		if raw, err := json.Marshal(e); err == nil {
			_, _ = b.file.Write(append(raw, '\n'))
		}
	}
	subs := make([]chan Entry, 0, len(b.subs))
	for _, ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- e:
		default:
		}
	}
}

func (b *Buffer) Info(kind, msg string) {
	b.Add(Entry{Level: LevelInfo, Kind: kind, Message: msg})
}

func (b *Buffer) Warn(kind, msg string) {
	b.Add(Entry{Level: LevelWarn, Kind: kind, Message: msg})
}

func (b *Buffer) Error(kind, msg string, err error) {
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	b.Add(Entry{Level: LevelError, Kind: kind, Message: msg, Error: errStr})
}

func (b *Buffer) Entries() []Entry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Entry, len(b.entries))
	copy(out, b.entries)
	return out
}

func (b *Buffer) Subscribe() (int, <-chan Entry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextSubID
	b.nextSubID++
	ch := make(chan Entry, 64)
	b.subs[id] = ch
	return id, ch
}

func (b *Buffer) Unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.subs[id]; ok {
		delete(b.subs, id)
		close(ch)
	}
}
