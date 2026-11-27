package cache

import (
	"testing"
	"time"
)

func TestCacheGetSetEvictAndTTL(t *testing.T) {
	c := New(2, time.Hour)
	k1 := Key([]byte("m1"), []byte("hello"))
	k2 := Key([]byte("m1"), []byte("world"))
	k3 := Key([]byte("m1"), []byte("third"))

	if _, ok := c.Get(k1); ok {
		t.Fatal("expected cache miss")
	}
	c.Set(k1, []byte("resp1"), 0)
	c.Set(k2, []byte("resp2"), 0)

	if v, ok := c.Get(k1); !ok || string(v) != "resp1" {
		t.Fatalf("expected hit resp1, got ok=%v v=%q", ok, v)
	}
	// k1 was just touched, so inserting k3 should evict k2.
	c.Set(k3, []byte("resp3"), 0)
	if _, ok := c.Get(k2); ok {
		t.Fatal("expected k2 evicted")
	}
	if _, ok := c.Get(k3); !ok {
		t.Fatal("expected k3 present")
	}

	// A zero TTL on Set takes the store default, which must expire the entry.
	short := New(2, time.Millisecond)
	short.Set(k1, []byte("resp1"), 0)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := short.Get(k1); !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("expected k1 expired")
		}
		time.Sleep(time.Millisecond)
	}
}
