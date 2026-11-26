package cache

import (
	"testing"
	"time"
)

func TestCacheGetSetEvictAndTTL(t *testing.T) {
	c := New(2, 50*time.Millisecond)
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

	time.Sleep(70 * time.Millisecond)
	if _, ok := c.Get(k1); ok {
		t.Fatal("expected k1 expired")
	}
}
