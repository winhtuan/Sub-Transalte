package translator

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

/* ---------- normalize ---------- */

func TestNormalize(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Thank you", "thank you"},
		{"thank you", "thank you"},
		{"THANK  YOU", "thank you"},   // multiple spaces collapsed
		{"  Thank\tyou  ", "thank you"}, // tabs + leading/trailing
		{"Thankyou", "thankyou"},       // single word unchanged
		{"thank you\t", "thank you"},   // trailing tab
		// Verify OLD bug is fixed: "Thank you" and "Thankyou" must NOT collide.
		{"Thank  you", "thank you"}, // double space inside
	}
	for _, tt := range tests {
		got := normalize(tt.input)
		if got != tt.want {
			t.Errorf("normalize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeNoCollision(t *testing.T) {
	a := normalize("Thank you")
	b := normalize("Thankyou")
	if a == b {
		t.Errorf("normalize collision: %q == %q", a, b)
	}
}

/* ---------- TTL ---------- */

func TestCacheTTLHit(t *testing.T) {
	c := NewCacheWithTTL(100, 200*time.Millisecond)
	c.Put("hello", "xin chào")

	v, ok := c.Get("hello")
	if !ok || v != "xin chào" {
		t.Fatalf("expected cache hit before TTL expiry, got ok=%v v=%q", ok, v)
	}
}

func TestCacheTTLExpiry(t *testing.T) {
	c := NewCacheWithTTL(100, 50*time.Millisecond)
	c.Put("hello", "xin chào")

	time.Sleep(70 * time.Millisecond)

	_, ok := c.Get("hello")
	if ok {
		t.Fatal("expected cache miss after TTL expiry")
	}
}

func TestCacheTTLDisabled(t *testing.T) {
	c := NewCache(100) // TTL = 0
	c.Put("hello", "xin chào")

	time.Sleep(5 * time.Millisecond) // Would expire if TTL were 1ms
	v, ok := c.Get("hello")
	if !ok || v != "xin chào" {
		t.Fatal("expected permanent cache hit when TTL is disabled")
	}
}

/* ---------- Sharding / basic operations ---------- */

func TestCacheBasicGetPut(t *testing.T) {
	c := NewCache(1000)
	c.Put("hello world", "xin chào thế giới")

	v, ok := c.Get("hello world")
	if !ok || v != "xin chào thế giới" {
		t.Errorf("basic get/put failed: ok=%v v=%q", ok, v)
	}
	// Miss
	_, ok = c.Get("nonexistent")
	if ok {
		t.Error("expected miss for nonexistent key")
	}
}

func TestCacheEviction(t *testing.T) {
	// 16 shards, 1 entry per shard → total capacity = 16
	c := NewCache(16)
	for i := 0; i < 100; i++ {
		c.Put(fmt.Sprintf("key-%d", i), "v")
	}
	if c.Size() > 16 {
		t.Errorf("cache size %d exceeds capacity 16", c.Size())
	}
}

func TestCacheSharding(t *testing.T) {
	// Use a capacity well above the number of inserts so no shard exceeds its
	// per-shard limit regardless of hash distribution.
	c := NewCache(numShards * 20) // 20 per shard, inserting only 16
	for i := 0; i < 16; i++ {
		c.Put(fmt.Sprintf("key%d", i), "val")
	}
	if got := c.Size(); got != 16 {
		t.Errorf("expected 16 entries, got %d", got)
	}
}

/* ---------- WAL persistence ---------- */

func TestCacheWALReplayWithoutSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	// First cache instance: populate via Put (appends to WAL, no Save).
	c1 := NewCache(100)
	if err := c1.Load(path); err != nil {
		t.Fatal("Load c1:", err)
	}
	c1.Put("hello", "xin chào")
	c1.Put("world", "thế giới")
	c1.Close() // release WAL file handle before second Load

	// Second cache instance: Load should replay WAL and recover both entries.
	c2 := NewCache(100)
	if err := c2.Load(path); err != nil {
		t.Fatal("Load c2:", err)
	}
	t.Cleanup(c2.Close)

	if v, ok := c2.Get("hello"); !ok || v != "xin chào" {
		t.Errorf("WAL replay: expected 'xin chào', got %q (ok=%v)", v, ok)
	}
	if v, ok := c2.Get("world"); !ok || v != "thế giới" {
		t.Errorf("WAL replay: expected 'thế giới', got %q (ok=%v)", v, ok)
	}
}

func TestCacheSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	c1 := NewCache(100)
	if err := c1.Load(path); err != nil {
		t.Fatal(err)
	}
	c1.Put("a", "alpha")
	c1.Put("b", "beta")
	if err := c1.Save(path); err != nil {
		t.Fatal("Save:", err)
	}
	c1.Close() // release WAL handle after Save

	c2 := NewCache(100)
	if err := c2.Load(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c2.Close)

	if v, ok := c2.Get("a"); !ok || v != "alpha" {
		t.Errorf("after Save+Load: key 'a' = %q ok=%v", v, ok)
	}
	if v, ok := c2.Get("b"); !ok || v != "beta" {
		t.Errorf("after Save+Load: key 'b' = %q ok=%v", v, ok)
	}
}

/* ---------- Benchmarks ---------- */

func BenchmarkCacheGetConcurrent(b *testing.B) {
	c := NewCache(10000)
	for i := 0; i < 1000; i++ {
		c.Put(fmt.Sprintf("key%d", i), "translated value")
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c.Get(fmt.Sprintf("key%d", i%1000))
			i++
		}
	})
}

func BenchmarkCachePutConcurrent(b *testing.B) {
	c := NewCache(10000)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c.Put(fmt.Sprintf("key%d", i%1000), "translated")
			i++
		}
	})
}
