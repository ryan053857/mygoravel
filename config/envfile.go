package config

import (
	"github.com/cockroachdb/errors"
	"github.com/joho/godotenv"
)

// LoadEnv 读取 dotenv 文件，把其中的变量写入进程环境；进程中已经设置的变量
// （包括空字符串）不会被覆盖。文件不存在时返回的错误满足 errors.Is(err, fs.ErrNotExist)。
func LoadEnv(path string) error {
	return errors.Wrapf(godotenv.Load(path), "config: %s", path)
}
