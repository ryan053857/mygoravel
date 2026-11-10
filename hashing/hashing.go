package hashing

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/cockroachdb/errors"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// ErrPasswordTooLong 表示密码超过 bcrypt 能处理的 72 字节，只在 driver 为 bcrypt 时由 Make 返回。
var ErrPasswordTooLong = bcrypt.ErrPasswordTooLong

const (
	saltLen = 16
	keyLen  = 32
)

// Config 是哈希配置，对应配置文件的 hashing 段。
type Config struct {
	// Driver 是 Make 使用的算法：argon2id 或 bcrypt。
	Driver   string         `yaml:"driver"`
	Argon2id Argon2idConfig `yaml:"argon2id"`
	Bcrypt   BcryptConfig   `yaml:"bcrypt"`
}

// Argon2idConfig 是 argon2id 的参数。
type Argon2idConfig struct {
	// Memory 是每次哈希使用的内存，单位 KiB。
	Memory uint32 `yaml:"memory"`
	// Time 是迭代次数。
	Time uint32 `yaml:"time"`
	// Threads 是并行度。
	Threads uint8 `yaml:"threads"`
}

// BcryptConfig 是 bcrypt 的参数。
type BcryptConfig struct {
	// Cost 是计算成本，每加 1 耗时翻倍。
	Cost int `yaml:"cost"`
}

// SetDefaults 实现 config.Defaulter：argon2id，19 MiB 内存、2 次迭代、1 个线程（OWASP 推荐的最低配置）；
// bcrypt 成本为 12。
func (c *Config) SetDefaults() {
	*c = Config{
		Driver:   "argon2id",
		Argon2id: Argon2idConfig{Memory: 19 * 1024, Time: 2, Threads: 1},
		Bcrypt:   BcryptConfig{Cost: 12},
	}
}

// Validate 检查 driver 与两种算法的参数。
func (c Config) Validate() error {
	var errs []error
	if c.Driver != "argon2id" && c.Driver != "bcrypt" {
		errs = append(errs, errors.Newf("driver %q must be argon2id or bcrypt", c.Driver))
	}
	if c.Argon2id.Time < 1 {
		errs = append(errs, errors.New("argon2id.time must be at least 1"))
	}
	if c.Argon2id.Threads < 1 {
		errs = append(errs, errors.New("argon2id.threads must be at least 1"))
	}
	if c.Argon2id.Memory < 8*uint32(c.Argon2id.Threads) {
		errs = append(errs, errors.New("argon2id.memory must be at least 8 KiB per thread"))
	}
	if c.Bcrypt.Cost < bcrypt.MinCost || c.Bcrypt.Cost > bcrypt.MaxCost {
		errs = append(errs, errors.Newf("bcrypt.cost must be between %d and %d", bcrypt.MinCost, bcrypt.MaxCost))
	}
	return errors.Join(errs...)
}

// Hasher 按配置哈希和校验密码，可以并发使用。
type Hasher struct {
	cfg Config
}

// New 创建 Hasher。cfg 无效时 panic。
func New(cfg Config) *Hasher {
	if err := cfg.Validate(); err != nil {
		panic("hashing: " + err.Error())
	}
	return &Hasher{cfg: cfg}
}

// Make 用配置的算法和随机盐哈希密码。
func (h *Hasher) Make(password string) (string, error) {
	if h.cfg.Driver == "bcrypt" {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), h.cfg.Bcrypt.Cost)
		return string(hash), err
	}
	salt := make([]byte, saltLen)
	rand.Read(salt)
	p := argon2Params{Argon2idConfig: h.cfg.Argon2id, salt: salt}
	p.key = argon2.IDKey([]byte(password), p.salt, p.Time, p.Memory, p.Threads, keyLen)
	return p.String(), nil
}

// Check 报告 password 是否与 hash 匹配。hash 可以是 argon2id 或 bcrypt 格式，与当前 driver 无关；
// 格式无法识别时返回 false。
func (h *Hasher) Check(password, hash string) bool {
	if isBcrypt(hash) {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	}
	p, ok := parseArgon2id(hash)
	if !ok {
		return false
	}
	key := argon2.IDKey([]byte(password), p.salt, p.Time, p.Memory, p.Threads, uint32(len(p.key)))
	return subtle.ConstantTimeCompare(key, p.key) == 1
}

// NeedsRehash 报告 hash 是否不是用当前 driver 和参数生成的。登录校验通过后，
// 用它决定是否用 Make 重新生成并保存哈希。
func (h *Hasher) NeedsRehash(hash string) bool {
	if h.cfg.Driver == "bcrypt" {
		cost, err := bcrypt.Cost([]byte(hash))
		return !isBcrypt(hash) || err != nil || cost != h.cfg.Bcrypt.Cost
	}
	p, ok := parseArgon2id(hash)
	return !ok || p.Argon2idConfig != h.cfg.Argon2id || len(p.key) != keyLen
}

func isBcrypt(hash string) bool {
	return strings.HasPrefix(hash, "$2a$") || strings.HasPrefix(hash, "$2b$") || strings.HasPrefix(hash, "$2y$")
}

var b64 = base64.RawStdEncoding

// argon2Params 是一个 argon2id 哈希的各部分。
type argon2Params struct {
	Argon2idConfig
	salt []byte
	key  []byte
}

// String 按 PHC 格式编码：$argon2id$v=19$m=<memory>,t=<time>,p=<threads>$<salt>$<key>。
func (p argon2Params) String() string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads,
		b64.EncodeToString(p.salt), b64.EncodeToString(p.key))
}

func parseArgon2id(hash string) (argon2Params, bool) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return argon2Params{}, false
	}
	var p argon2Params
	_, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads)
	if err != nil || parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", p.Memory, p.Time, p.Threads) {
		return argon2Params{}, false
	}
	if p.salt, err = b64.DecodeString(parts[4]); err != nil {
		return argon2Params{}, false
	}
	if p.key, err = b64.DecodeString(parts[5]); err != nil {
		return argon2Params{}, false
	}
	return p, len(p.key) > 0 && p.Time >= 1 && p.Threads >= 1
}
