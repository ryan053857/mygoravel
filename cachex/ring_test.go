package cachex_test

import (
	"crypto/rand"
	"net"
	"os"
	"strconv"
	"testing"

	"github.com/bradfitz/gomemcache/memcache"

	"github.com/ryan053857/mygoravel/cachex"
)

// pick 返回每个键所在节点的地址。
func pick(t *testing.T, r *cachex.Ring, keys []string) []string {
	t.Helper()
	got := make([]string, len(keys))
	for i, k := range keys {
		addr, err := r.PickServer(k)
		if err != nil {
			t.Fatal(err)
		}
		got[i] = addr.String()
	}
	return got
}

func mustRing(t *testing.T, servers ...string) *cachex.Ring {
	t.Helper()
	r, err := cachex.NewRing(servers...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func testKeys(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = "blog:article:v1:" + strconv.Itoa(i)
	}
	return keys
}

func TestRingOrderIndependent(t *testing.T) {
	keys := testKeys(10000)
	a := pick(t, mustRing(t, "10.0.0.1:11211", "10.0.0.2:11211", "10.0.0.3:11211"), keys)
	b := pick(t, mustRing(t, "10.0.0.3:11211", "10.0.0.1:11211", "10.0.0.2:11211"), keys)
	for i := range keys {
		if a[i] != b[i] {
			t.Fatalf("key %q: %s vs %s", keys[i], a[i], b[i])
		}
	}
}

func TestRingBalance(t *testing.T) {
	keys := testKeys(30000)
	counts := map[string]int{}
	for _, s := range pick(t, mustRing(t, "10.0.0.1:11211", "10.0.0.2:11211", "10.0.0.3:11211"), keys) {
		counts[s]++
	}
	if len(counts) != 3 {
		t.Fatalf("counts %v", counts)
	}
	for s, n := range counts {
		if share := float64(n) / float64(len(keys)); share < 0.25 || share > 0.42 {
			t.Errorf("%s has %.1f%% of keys", s, share*100)
		}
	}
}

func TestRingMinimalMovement(t *testing.T) {
	keys := testKeys(30000)
	three := pick(t, mustRing(t, "10.0.0.1:11211", "10.0.0.2:11211", "10.0.0.3:11211"), keys)
	four := pick(t, mustRing(t, "10.0.0.1:11211", "10.0.0.2:11211", "10.0.0.3:11211", "10.0.0.4:11211"), keys)
	moved := 0
	for i := range keys {
		if three[i] == four[i] {
			continue
		}
		// 增加节点时，换节点的键都换到新节点。
		if four[i] != "10.0.0.4:11211" {
			t.Fatalf("key %q moved from %s to %s", keys[i], three[i], four[i])
		}
		moved++
	}
	if share := float64(moved) / float64(len(keys)); share < 0.15 || share > 0.35 {
		t.Errorf("%.1f%% of keys moved, want about 25%%", share*100)
	}
}

func TestRingEach(t *testing.T) {
	r := mustRing(t, "cache-b:11211", "/run/memcached.sock", "[::1]:11211")
	var got []string
	if err := r.Each(func(a net.Addr) error {
		got = append(got, a.Network()+" "+a.String())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"tcp cache-b:11211", "unix /run/memcached.sock", "tcp [::1]:11211"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestRingInvalid(t *testing.T) {
	for _, servers := range [][]string{
		nil,
		{"10.0.0.1:11211", "10.0.0.1:11211"},
		{"10.0.0.1"},
		{":11211"},
		{"10.0.0.1:"},
	} {
		if _, err := cachex.NewRing(servers...); err == nil {
			t.Errorf("NewRing(%q) succeeded", servers)
		}
	}
}

func TestRingWithMemcached(t *testing.T) {
	addr := os.Getenv("MEMCACHED_TEST_ADDR")
	if addr == "" {
		t.Fatal("MEMCACHED_TEST_ADDR is required for this test")
	}
	mc := memcache.NewFromSelector(mustRing(t, addr))
	t.Cleanup(func() { _ = mc.Close() })
	key := "test:" + rand.Text() + ":ring"
	if err := mc.Set(&memcache.Item{Key: key, Value: []byte("v"), Expiration: 60}); err != nil {
		t.Fatal(err)
	}
	item, err := mc.Get(key)
	if err != nil || string(item.Value) != "v" {
		t.Fatalf("got %v err %v", item, err)
	}
	if err := mc.Ping(); err != nil {
		t.Fatal(err)
	}
}
