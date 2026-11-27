package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"
)

const (
	keyringService       = "agentpassage"
	legacyKeyringService = "acpx"
)

// Store stores API keys in the OS keychain when available, falling back to a 0600 file.
type Store struct {
	mu           sync.RWMutex
	filePath     string
	useKeyring   bool
	fallbackNote string
	cache        map[string]string
}

func New(dataDir string) *Store {
	s := &Store{
		filePath: filepath.Join(dataDir, "secrets.json"),
		cache:    make(map[string]string),
	}
	// Probe keyring with a non-destructive read.
	if os.Getenv("APX_NO_KEYRING") == "" && os.Getenv("ACPX_NO_KEYRING") == "" {
		_, err := keyring.Get(keyringService, "__apx_probe__")
		if err == nil || errors.Is(err, keyring.ErrNotFound) {
			s.useKeyring = true
		} else {
			s.fallbackNote = err.Error()
		}
	} else {
		s.fallbackNote = "disabled via APX_NO_KEYRING"
	}
	// Load any existing file secrets so keys written before/during fallback remain readable.
	_ = s.loadFileLocked()
	return s
}

// NewFileOnly creates a file-backed secret store (useful for tests or headless setups).
func NewFileOnly(dataDir string) *Store {
	s := &Store{
		filePath:     filepath.Join(dataDir, "secrets.json"),
		useKeyring:   false,
		fallbackNote: "file-only mode",
		cache:        make(map[string]string),
	}
	_ = s.loadFileLocked()
	return s
}

func (s *Store) UsingFileFallback() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.useKeyring
}

func (s *Store) Backend() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.useKeyring {
		return "OS keychain"
	}
	return "file (0600)"
}

func (s *Store) FallbackReason() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fallbackNote
}

func (s *Store) Set(accountID, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if accountID == "" {
		return fmt.Errorf("empty account id")
	}
	s.cache[accountID] = secret
	if s.useKeyring {
		if err := keyring.Set(keyringService, accountID, secret); err == nil {
			// An older file entry would win over the keychain on the next start.
			return s.updateFileLocked(accountID, "")
		} else {
			s.useKeyring = false
			s.fallbackNote = err.Error()
		}
	}
	return s.updateFileLocked(accountID, secret)
}

func (s *Store) Get(accountID string) (string, error) {
	s.mu.RLock()
	if v, ok := s.cache[accountID]; ok && v != "" {
		s.mu.RUnlock()
		return v, nil
	}
	useKR := s.useKeyring
	s.mu.RUnlock()

	if useKR {
		v, err := keyring.Get(keyringService, accountID)
		if err == nil {
			s.mu.Lock()
			s.cache[accountID] = v
			s.mu.Unlock()
			return v, nil
		}
		if errors.Is(err, keyring.ErrNotFound) {
			// Seamless fallback to legacy "acpx" keychain entries.
			if lv, lerr := keyring.Get(legacyKeyringService, accountID); lerr == nil && lv != "" {
				_ = keyring.Set(keyringService, accountID, lv)
				s.mu.Lock()
				s.cache[accountID] = lv
				s.mu.Unlock()
				return lv, nil
			}
		} else {
			s.mu.Lock()
			s.useKeyring = false
			s.fallbackNote = err.Error()
			s.mu.Unlock()
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.cache[accountID]; ok {
		return v, nil
	}
	return "", os.ErrNotExist
}

func (s *Store) Delete(accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cache, accountID)
	if s.useKeyring {
		_ = keyring.Delete(keyringService, accountID)
	}
	return s.updateFileLocked(accountID, "")
}

// KnownSecrets returns all cached non-empty secrets for log redaction.
func (s *Store) KnownSecrets() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.cache))
	for _, v := range s.cache {
		if len(v) >= 6 {
			out = append(out, v)
		}
	}
	return out
}

// Reload clears the in-memory secret cache and reloads file-backed secrets so a running
// daemon picks up API keys added or updated by another CLI/TUI process.
func (s *Store) Reload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = make(map[string]string)
	_ = s.loadFileLocked()
}

func (s *Store) loadFileLocked() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	for k, v := range m {
		s.cache[k] = v
	}
	return nil
}

// updateFileLocked writes one entry of the secrets file, or removes it when
// secret is empty. Other entries stay as they are on disk, so keys read from
// the keychain never reach the file.
func (s *Store) updateFileLocked(accountID, secret string) error {
	m := make(map[string]string)
	data, err := os.ReadFile(s.filePath)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &m); err != nil {
			return fmt.Errorf("read %s: %w", s.filePath, err)
		}
	}
	if secret != "" {
		m[accountID] = secret
	} else if _, ok := m[accountID]; !ok {
		return nil
	} else {
		delete(m, accountID)
	}
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0o700); err != nil {
		return err
	}
	data, err = json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.filePath)
}

// Mask returns a redacted version of a secret suitable for display in TUI/CLI.
func Mask(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "(none)"
	}
	if len(secret) <= 8 {
		return "••••••••"
	}
	return secret[:4] + "••••" + secret[len(secret)-4:]
}
