package config

import (
	"os"
	"path/filepath"

	"github.com/cockroachdb/errors"
	"github.com/joho/godotenv"
)

// envFileKey 是保留的顶层键，指向一个 dotenv 文件。
const envFileKey = "envfile"

// loadEnvFile 读取 dotenv 文件。路径可引用进程环境变量，相对路径基于 dir。
func loadEnvFile(path, dir string) (map[string]string, error) {
	path, err := expand(path, os.LookupEnv)
	if err != nil {
		return nil, errors.Wrap(err, envFileKey)
	}
	if path == "" {
		return nil, errors.Newf("%s must not be empty", envFileKey)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	env, err := godotenv.Read(path)
	return env, errors.Wrap(err, envFileKey)
}

// withFallback 先查 primary，未设置时再查 fallback；primary 中的空值同样优先。
func withFallback(primary lookupFunc, fallback map[string]string) lookupFunc {
	return func(key string) (string, bool) {
		if v, ok := primary(key); ok {
			return v, true
		}
		v, ok := fallback[key]
		return v, ok
	}
}
