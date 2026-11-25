package privacy

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
)

const placeholderPrefix = "{{APX_"
const placeholderSuffix = "}}"
const maxPlaceholderLen = 48

var (
	// High-confidence API key / secret token patterns.
	apiKeyPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\bsk-(?:proj-|ant-[A-Za-z0-9_-]+-|live-|test-)?[A-Za-z0-9_-]{16,}\b`),
		regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}\b`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
		regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{16,}\b`),
		regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{12,}\b`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}\b`),
		regexp.MustCompile(`\b(?:apx|acpx)_[a-fA-F0-9]{16,}\b`),
		regexp.MustCompile(`\b(?:gsk|hf)_[A-Za-z0-9]{20,}\b`),
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
	}
	// Key-value assignment patterns where only group 1 (the secret value) is replaced.
	assignedKeyPattern = regexp.MustCompile(`(?i)\b(?:api[_-]?key|secret[_-]?key|access[_-]?token|auth[_-]?token)\s*[:=]\s*["']?([A-Za-z0-9_\-.]{16,})["']?`)

	emailPattern = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)

	// Phone patterns (US/international formatted numbers; checked for 10-15 digits and non-date).
	phonePattern = regexp.MustCompile(`(?:\+\d{1,3}[\s.\-]?)?(?:\(\d{2,4}\)[\s.\-]?|\d{3}[\s.\-])\d{3}[\s.\-]\d{4}\b|\+\d{1,3}[\s.\-]\d{2,4}[\s.\-]\d{3,4}[\s.\-]\d{3,4}\b`)
	isoDateRe    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// RedactionItem records one redacted value safely (never storing raw secrets in reports).
type RedactionItem struct {
	Category      string `json:"category"`
	Placeholder   string `json:"placeholder"`
	MaskedPreview string `json:"masked_preview"`
}

// Summary describes what was redacted during a request.
type Summary struct {
	Total      int             `json:"total"`
	ByCategory map[string]int  `json:"by_category,omitempty"`
	Items      []RedactionItem `json:"items,omitempty"`
}

// Filter compiles privacy rules from config and spawns per-request reversible sessions.
type Filter struct {
	cfg            config.PrivacyConfig
	customCompiled []compiledCustom
}

type compiledCustom struct {
	name string
	re   *regexp.Regexp
}

func NewFilter(cfg config.PrivacyConfig) *Filter {
	f := &Filter{cfg: cfg}
	for _, cp := range cfg.CustomPatterns {
		pat := strings.TrimSpace(cp.Pattern)
		if pat == "" {
			continue
		}
		if re, err := regexp.Compile(pat); err == nil {
			name := strings.TrimSpace(cp.Name)
			if name == "" {
				name = "custom"
			}
			f.customCompiled = append(f.customCompiled, compiledCustom{name: name, re: re})
		}
	}
	return f
}

func (f *Filter) Enabled() bool {
	return f != nil && f.cfg.Enabled
}

// Session holds reversible placeholder mappings for one request/response exchange.
type Session struct {
	mu      sync.Mutex
	filter  *Filter
	valToPH map[string]string
	phToVal map[string]string
	counts  map[string]int
	items   []RedactionItem
}

func (f *Filter) NewSession() *Session {
	if !f.Enabled() {
		return nil
	}
	return &Session{
		filter:  f,
		valToPH: make(map[string]string),
		phToVal: make(map[string]string),
		counts:  make(map[string]int),
	}
}

func (s *Session) placeholderFor(category, tag, raw string) string {
	if ph, ok := s.valToPH[raw]; ok {
		return ph
	}
	s.counts[category]++
	ph := fmt.Sprintf("%s%s_%d%s", placeholderPrefix, tag, s.counts[category], placeholderSuffix)
	s.valToPH[raw] = ph
	s.phToVal[ph] = raw
	s.items = append(s.items, RedactionItem{
		Category:      category,
		Placeholder:   ph,
		MaskedPreview: maskValue(category, raw),
	})
	return ph
}

// RedactText replaces detected sensitive values with reversible placeholders.
func (s *Session) RedactText(text string) string {
	if s == nil || text == "" {
		return text
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	out := text
	cfg := s.filter.cfg

	if cfg.RedactAPIKeys {
		for _, re := range apiKeyPatterns {
			out = re.ReplaceAllStringFunc(out, func(m string) string {
				if isPlaceholder(m) {
					return m
				}
				return s.placeholderFor("api_key", "KEY", m)
			})
		}
		out = replaceSubmatchGroup1(assignedKeyPattern, out, func(val string) string {
			if isPlaceholder(val) {
				return val
			}
			return s.placeholderFor("api_key", "KEY", val)
		})
	}

	if cfg.RedactEmails {
		out = emailPattern.ReplaceAllStringFunc(out, func(m string) string {
			if isPlaceholder(m) {
				return m
			}
			return s.placeholderFor("email", "EMAIL", m)
		})
	}

	if cfg.RedactPhones {
		out = phonePattern.ReplaceAllStringFunc(out, func(m string) string {
			if isPlaceholder(m) || !looksLikePhone(m) {
				return m
			}
			return s.placeholderFor("phone", "PHONE", m)
		})
	}

	for _, cp := range s.filter.customCompiled {
		tag := sanitizeTag(cp.name)
		cat := "custom:" + cp.name
		out = cp.re.ReplaceAllStringFunc(out, func(m string) string {
			if isPlaceholder(m) || m == "" {
				return m
			}
			return s.placeholderFor(cat, tag, m)
		})
	}

	return out
}

// RedactMessages returns a redacted copy of chat messages without mutating the input slice.
func (s *Session) RedactMessages(msgs []backend.Message) []backend.Message {
	if s == nil || len(msgs) == 0 {
		return msgs
	}
	out := make([]backend.Message, len(msgs))
	for i, m := range msgs {
		m.Content = s.RedactText(m.Content)
		out[i] = m
	}
	return out
}

// RedactRawOpenAIBody redacts message text inside a raw OpenAI JSON request payload
// while preserving extra fields such as tool definitions and response_format.
func (s *Session) RedactRawOpenAIBody(raw []byte) []byte {
	if s == nil || len(raw) == 0 {
		return raw
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return raw
	}
	if msgs, ok := payload["messages"]; ok {
		payload["messages"] = s.redactJSONValue(msgs)
	}
	if input, ok := payload["input"]; ok {
		payload["input"] = s.redactJSONValue(input)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return raw
	}
	return encoded
}

func (s *Session) redactJSONValue(v any) any {
	switch x := v.(type) {
	case string:
		return s.RedactText(x)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = s.redactJSONValue(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if k == "role" || k == "type" || k == "model" {
				out[k] = val
			} else {
				out[k] = s.redactJSONValue(val)
			}
		}
		return out
	default:
		return v
	}
}

// RestoreText replaces reversible placeholders with their original sensitive values.
func (s *Session) RestoreText(text string) string {
	if s == nil || text == "" {
		return text
	}
	s.mu.Lock()
	if len(s.phToVal) == 0 {
		s.mu.Unlock()
		return text
	}
	pairs := make([]string, 0, len(s.phToVal)*2)
	for ph, orig := range s.phToVal {
		pairs = append(pairs, ph, orig)
	}
	s.mu.Unlock()
	return strings.NewReplacer(pairs...).Replace(text)
}

// Summary returns counts by category and masked previews of everything redacted in this session.
func (s *Session) Summary() Summary {
	if s == nil {
		return Summary{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) == 0 {
		return Summary{}
	}
	byCat := make(map[string]int, len(s.counts))
	for k, v := range s.counts {
		byCat[k] = v
	}
	items := append([]RedactionItem(nil), s.items...)
	return Summary{
		Total:      len(items),
		ByCategory: byCat,
		Items:      items,
	}
}

// HeaderValue formats a compact header value like "api_key=1,email=2" or "" if nothing was redacted.
func (s *Session) HeaderValue() string {
	sum := s.Summary()
	if sum.Total == 0 {
		return ""
	}
	keys := make([]string, 0, len(sum.ByCategory))
	for k := range sum.ByCategory {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, sum.ByCategory[k]))
	}
	return strings.Join(parts, ",")
}

// StreamRestorer restores placeholders across streaming text chunks without breaking
// placeholders that are split across chunk boundaries (e.g. "{{APX_" + "EMAIL_1}}").
type StreamRestorer struct {
	session *Session
	pending string
}

func (s *Session) NewStreamRestorer() *StreamRestorer {
	return &StreamRestorer{session: s}
}

// Push feeds one streamed delta and returns the safe restored prefix ready to emit.
func (sr *StreamRestorer) Push(chunk string) string {
	if sr == nil || sr.session == nil {
		return chunk
	}
	if chunk == "" && sr.pending == "" {
		return ""
	}
	buf := sr.pending + chunk
	sr.pending = ""

	// 1. If there's an unclosed "{{APX_" near the end, hold from that index until "}}" arrives.
	if idx := strings.LastIndex(buf, placeholderPrefix); idx >= 0 {
		tail := buf[idx:]
		if !strings.Contains(tail, placeholderSuffix) && len(tail) < maxPlaceholderLen {
			sr.pending = tail
			return sr.session.RestoreText(buf[:idx])
		}
	}

	// 2. If the end of buf is a partial prefix of "{{APX_" (e.g. "{", "{{", "{{A", "{{APX"), hold it.
	maxHold := min(len(buf), len(placeholderPrefix)-1)
	for hold := maxHold; hold >= 1; hold-- {
		suffix := buf[len(buf)-hold:]
		if strings.HasPrefix(placeholderPrefix, suffix) {
			sr.pending = suffix
			return sr.session.RestoreText(buf[:len(buf)-hold])
		}
	}

	return sr.session.RestoreText(buf)
}

// Flush restores and returns any buffered tail at end-of-stream.
func (sr *StreamRestorer) Flush() string {
	if sr == nil || sr.pending == "" {
		return ""
	}
	tail := sr.pending
	sr.pending = ""
	if sr.session == nil {
		return tail
	}
	return sr.session.RestoreText(tail)
}

func isPlaceholder(s string) bool {
	return strings.HasPrefix(s, placeholderPrefix) && strings.HasSuffix(s, placeholderSuffix)
}

func looksLikePhone(s string) bool {
	trimmed := strings.TrimSpace(s)
	if isoDateRe.MatchString(trimmed) {
		return false
	}
	digits := 0
	for _, r := range trimmed {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	return digits >= 10 && digits <= 15
}

func replaceSubmatchGroup1(re *regexp.Regexp, s string, fn func(string) string) string {
	matches := re.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s
	}
	var sb strings.Builder
	last := 0
	for _, m := range matches {
		if len(m) < 4 || m[2] < 0 || m[3] < 0 {
			continue
		}
		sb.WriteString(s[last:m[2]])
		sb.WriteString(fn(s[m[2]:m[3]]))
		last = m[3]
	}
	sb.WriteString(s[last:])
	return sb.String()
}

func sanitizeTag(name string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('_')
		}
	}
	out := strings.Trim(sb.String(), "_")
	if out == "" {
		return "CUSTOM"
	}
	if len(out) > 16 {
		return out[:16]
	}
	return out
}

func maskValue(category, raw string) string {
	switch category {
	case "email":
		if at := strings.IndexByte(raw, '@'); at > 0 {
			return raw[:1] + "***" + raw[at:]
		}
	case "phone":
		digits := make([]byte, 0, len(raw))
		for i := 0; i < len(raw); i++ {
			if raw[i] >= '0' && raw[i] <= '9' {
				digits = append(digits, raw[i])
			}
		}
		if len(digits) >= 4 {
			return "***-" + string(digits[len(digits)-4:])
		}
	}
	if len(raw) <= 8 {
		return "****"
	}
	return raw[:4] + "..." + raw[len(raw)-4:]
}
