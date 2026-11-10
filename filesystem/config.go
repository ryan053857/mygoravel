package filesystem

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
)

// Config 是 filesystem 配置段：上传限制和磁盘配置。
//
//	filesystem:
//	  default_disk: local
//	  max_upload_size: 1GiB
//	  temp_ttl: 24h
//	  disks:
//	    local: {root: storage/files, public_url: /storage}
//	    s3: {bucket: blog-files, public_url: https://cdn.example.com}
type Config struct {
	DefaultDisk   string        `yaml:"default_disk"`    // 默认磁盘：local 或 s3
	MaxUploadSize ByteSize      `yaml:"max_upload_size"` // 单个文件的最大声明大小
	TempTTL       time.Duration `yaml:"temp_ttl"`        // 未确认文件多久后被 Cleanup 回收
	Disks         DisksConfig   `yaml:"disks"`           // 只需填写用到的磁盘
}

// DisksConfig 是各磁盘的配置。没有填写的磁盘保持零值，表示不使用。
type DisksConfig struct {
	Local LocalConfig `yaml:"local"`
	S3    S3Config    `yaml:"s3"`
}

// LocalConfig 是本地磁盘（filesystem/local）的配置。
type LocalConfig struct {
	Root      string `yaml:"root"`       // 存储目录，由应用独占
	PublicURL string `yaml:"public_url"` // 公开访问基址，绝对 URL 或以 / 开头的路径
}

// S3Config 是 S3 磁盘（filesystem/s3）的配置。未提供静态密钥时使用 AWS 默认凭证链。
type S3Config struct {
	Bucket    string `yaml:"bucket"`
	Region    string `yaml:"region"`
	Endpoint  string `yaml:"endpoint"`   // S3 兼容服务的 API 地址，AWS 留空
	PathStyle bool   `yaml:"path_style"` // 使用 path-style 访问桶，MinIO 等通常需要
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
	PublicURL string `yaml:"public_url"` // 公开访问基址（CDN 或桶域名），必填
}

// SetDefaults 填充默认值：local 磁盘、1GiB、24h。
func (c *Config) SetDefaults() {
	*c = Config{DefaultDisk: "local", MaxUploadSize: GiB, TempTTL: 24 * time.Hour}
}

// Validate 检查上传限制，并要求默认磁盘已在 disks 下配置。各磁盘配置自己校验。
func (c Config) Validate() error {
	configured := map[string]bool{"local": !c.Disks.Local.IsZero(), "s3": !c.Disks.S3.IsZero()}
	if !configured[c.DefaultDisk] {
		return errors.Join(errors.Newf("default_disk %q must be local or s3 and configured under disks", c.DefaultDisk), c.validateLimits())
	}
	return c.validateLimits()
}

// validateLimits 检查与磁盘无关的上传限制，New 只依赖这部分。
func (c Config) validateLimits() error {
	var errs []error
	if strings.TrimSpace(c.DefaultDisk) == "" {
		errs = append(errs, errors.New("default_disk is required"))
	}
	if c.MaxUploadSize <= 0 || c.MaxUploadSize > maxObjectSize {
		errs = append(errs, errors.New("max_upload_size must be between 1B and 5TiB"))
	}
	if c.TempTTL < time.Minute {
		errs = append(errs, errors.New("temp_ttl must be at least 1m"))
	}
	return errors.Join(errs...)
}

// IsZero 报告本地磁盘是否未配置。
func (c LocalConfig) IsZero() bool { return c == LocalConfig{} }

// Validate 检查目录和公开基址；未配置时不检查。
func (c LocalConfig) Validate() error {
	if c.IsZero() {
		return nil
	}
	var errs []error
	if strings.TrimSpace(c.Root) == "" {
		errs = append(errs, errors.New("root is required"))
	}
	u, err := url.Parse(strings.TrimSpace(c.PublicURL))
	if err != nil || c.PublicURL == "" || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme == "" && (u.Host != "" || !strings.HasPrefix(u.Path, "/"))) ||
		(u.Scheme != "" && !httpURL(c.PublicURL)) {
		errs = append(errs, errors.New("public_url must be an http(s) URL or a path starting with /"))
	}
	return errors.Join(errs...)
}

// IsZero 报告 S3 磁盘是否未配置。
func (c S3Config) IsZero() bool { return c == S3Config{} }

// Validate 检查桶、公开基址、端点和静态凭证；未配置时不检查。
func (c S3Config) Validate() error {
	if c.IsZero() {
		return nil
	}
	var errs []error
	if strings.TrimSpace(c.Bucket) == "" {
		errs = append(errs, errors.New("bucket is required"))
	}
	if !httpURL(c.PublicURL) {
		errs = append(errs, errors.New("public_url must be an http(s) URL"))
	}
	if c.Endpoint != "" && !httpURL(c.Endpoint) {
		errs = append(errs, errors.New("endpoint must be an http(s) URL"))
	}
	if (c.AccessKey == "") != (c.SecretKey == "") {
		errs = append(errs, errors.New("access_key and secret_key must be set together"))
	}
	return errors.Join(errs...)
}

func httpURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.RawQuery == "" && u.Fragment == ""
}

// ByteSize 是字节数，配置中可写 1048576、512KiB、16MiB、1GiB 或 1TiB。
type ByteSize int64

const (
	KiB ByteSize = 1 << (10 * (iota + 1))
	MiB
	GiB
	TiB
)

var byteUnits = []struct {
	suffix string
	size   ByteSize
}{{"TiB", TiB}, {"GiB", GiB}, {"MiB", MiB}, {"KiB", KiB}, {"B", 1}}

// UnmarshalText 解析整数字节数或带二进制单位的写法。
func (b *ByteSize) UnmarshalText(text []byte) error {
	s := strings.TrimSpace(string(text))
	unit := ByteSize(1)
	for _, u := range byteUnits {
		if n, ok := strings.CutSuffix(s, u.suffix); ok {
			s, unit = strings.TrimSpace(n), u.size
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || ByteSize(n) > ByteSize(1<<62)/unit {
		return errors.Newf("invalid byte size %q, use e.g. 512MiB or 1GiB", text)
	}
	*b = ByteSize(n) * unit
	return nil
}

// String 返回能整除的最大单位写法，例如 1GiB。
func (b ByteSize) String() string {
	for _, u := range byteUnits {
		if b != 0 && b%u.size == 0 {
			return strconv.FormatInt(int64(b/u.size), 10) + u.suffix
		}
	}
	return "0B"
}

// MarshalText 与 UnmarshalText 对称，便于打印配置。
func (b ByteSize) MarshalText() ([]byte, error) { return []byte(b.String()), nil }
