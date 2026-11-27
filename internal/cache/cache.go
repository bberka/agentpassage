package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

type entry struct {
	value     []byte
	expiresAt time.Time
	lastUsed  time.Time
}

// Store is a lightweight, thread-safe in-memory TTL LRU cache for gateway responses
// (embeddings, rerank, and opt-in deterministic chat completions).
type Store struct {
	mu         sync.Mutex
	maxEntries int
	defaultTTL time.Duration
	items      map[string]*entry
}

func New(maxEntries int, defaultTTL time.Duration) *Store {
	if maxEntries <= 0 {
		maxEntries = 512
	}
	if defaultTTL <= 0 {
		defaultTTL = 10 * time.Minute
	}
	return &Store{
		maxEntries: maxEntries,
		defaultTTL: defaultTTL,
		items:      make(map[string]*entry, maxEntries),
	}
}

// Key computes a deterministic SHA-256 cache key from parts.
func Key(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Store) Get(key string) ([]byte, bool) {
	if s == nil || key == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	e, ok := s.items[key]
	if !ok {
		return nil, false
	}
	if now.After(e.expiresAt) {
		delete(s.items, key)
		return nil, false
	}
	e.lastUsed = now
	out := make([]byte, len(e.value))
	copy(out, e.value)
	return out, true
}

func (s *Store) Set(key string, val []byte, ttl time.Duration) {
	if s == nil || key == "" || len(val) == 0 {
		return
	}
	if ttl <= 0 {
		ttl = s.defaultTTL
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if len(s.items) >= s.maxEntries {
		if _, exists := s.items[key]; !exists {
			s.evictOneLocked(now)
		}
	}
	cp := make([]byte, len(val))
	copy(cp, val)
	s.items[key] = &entry{
		value:     cp,
		expiresAt: now.Add(ttl),
		lastUsed:  now,
	}
}

func (s *Store) evictOneLocked(now time.Time) {
	var oldestKey string
	var oldestTime time.Time
	for k, e := range s.items {
		if now.After(e.expiresAt) {
			delete(s.items, k)
			return
		}
		if oldestKey == "" || e.lastUsed.Before(oldestTime) {
			oldestKey = k
			oldestTime = e.lastUsed
		}
	}
	if oldestKey != "" {
		delete(s.items, oldestKey)
	}
}
