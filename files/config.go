package files

import (
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
)

// Config 是文件服务的上传限制，可以嵌入应用配置。磁盘配置由各磁盘包自己提供。
type Config struct {
	DefaultDisk   string        `yaml:"default_disk"`    // 默认磁盘名，对应 New 传入的 map 键
	MaxUploadSize ByteSize      `yaml:"max_upload_size"` // 单个文件的最大声明大小
	TempTTL       time.Duration `yaml:"temp_ttl"`        // 未确认文件多久后被 Cleanup 回收
}

// SetDefaults 填充默认值：local 磁盘、1GiB、24h。
func (c *Config) SetDefaults() {
	*c = Config{DefaultDisk: "local", MaxUploadSize: GiB, TempTTL: 24 * time.Hour}
}

// Validate 检查配置完整。
func (c Config) Validate() error {
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
