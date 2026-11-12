package cachex

import (
	"cmp"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/cespare/xxhash/v2"
	"github.com/cockroachdb/errors"
)

// replicas 是每个节点在环上的虚拟节点数。
const replicas = 160

// Ring 用一致性哈希把键分到多个 Memcached 节点，实现 memcache.ServerSelector，传给 memcache.NewFromSelector：
//
//	ring, err := cachex.NewRing(cfg.Cache.Servers...)
//	mc := memcache.NewFromSelector(ring)
//
// 和 memcache.New 的取模分片相比，增加一个节点时只有约 1/(N+1) 的键换到新节点，去掉一个节点时只有它的键换到别的节点。
// 节点在环上的位置只由地址字符串决定，与顺序无关，所有进程的配置写法相同就会把同一个键分到同一个节点。
// 地址在建立连接时解析，节点换了 IP 不用重启。Ring 创建后不可修改，可以并发使用。
type Ring struct {
	servers []net.Addr
	points  []point
}

// point 是环上的一个虚拟节点。
type point struct {
	hash   uint64
	server int
}

// NewRing 创建 Ring。servers 是 "host:port" 形式的地址，含有 "/" 的是 Unix socket 路径。
// servers 为空、地址无法解析或有重复时返回错误。
func NewRing(servers ...string) (*Ring, error) {
	if len(servers) == 0 {
		return nil, errors.New("cachex: ring needs at least one server")
	}
	r := &Ring{servers: make([]net.Addr, len(servers)), points: make([]point, 0, len(servers)*replicas)}
	seen := make(map[string]bool, len(servers))
	for i, s := range servers {
		if seen[s] {
			return nil, errors.Newf("cachex: duplicate server %q", s)
		}
		seen[s] = true
		addr, err := parseAddr(s)
		if err != nil {
			return nil, err
		}
		r.servers[i] = addr
		for j := range replicas {
			r.points = append(r.points, point{hash: xxhash.Sum64String(s + "#" + strconv.Itoa(j)), server: i})
		}
	}
	// 哈希相同时按地址排序，与 servers 的顺序无关。
	slices.SortFunc(r.points, func(a, b point) int {
		return cmp.Or(cmp.Compare(a.hash, b.hash), cmp.Compare(servers[a.server], servers[b.server]))
	})
	return r, nil
}

// PickServer 返回 key 所在的节点：环上 key 的哈希之后的第一个虚拟节点。
func (r *Ring) PickServer(key string) (net.Addr, error) {
	h := xxhash.Sum64String(key)
	i, _ := slices.BinarySearchFunc(r.points, h, func(p point, h uint64) int { return cmp.Compare(p.hash, h) })
	if i == len(r.points) {
		i = 0
	}
	return r.servers[r.points[i].server], nil
}

// Each 按 NewRing 的参数顺序对每个节点调用 f，f 返回错误时停止并返回它。
func (r *Ring) Each(f func(net.Addr) error) error {
	for _, addr := range r.servers {
		if err := f(addr); err != nil {
			return err
		}
	}
	return nil
}

var _ memcache.ServerSelector = (*Ring)(nil)

// parseAddr 检查地址的格式，返回不解析的 net.Addr：连接时由 net.Dial 解析。
func parseAddr(s string) (net.Addr, error) {
	if strings.Contains(s, "/") {
		return addr{"unix", s}, nil
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return nil, errors.Wrapf(err, "cachex: server %q", s)
	}
	if host == "" || port == "" {
		return nil, errors.Newf("cachex: server %q needs host and port", s)
	}
	return addr{"tcp", s}, nil
}

// addr 是未解析的地址。
type addr struct{ network, address string }

func (a addr) Network() string { return a.network }
func (a addr) String() string  { return a.address }
