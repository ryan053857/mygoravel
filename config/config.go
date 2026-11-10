package config

import (
	"bytes"
	"io"
	"os"

	"github.com/cockroachdb/errors"
	"go.yaml.in/yaml/v4"
)

// Load 依次读取 files 中的 YAML 配置文件并返回 T，T 通常是应用的根配置结构体。
// 后面的文件覆盖前面的文件：没写的字段保留，写了的字段覆盖，map 按键合并，slice 整体替换。
//
// 流程：SetDefaults 填充默认值 → 逐个文件展开 ${VAR} 并解码（未知字段报错）→
// 全部文件合并后 Validate 校验一次。
func Load[T any](files ...string) (T, error) {
	var cfg T
	if len(files) == 0 {
		return cfg, errors.New("config: no configuration file")
	}
	setDefaults(&cfg)
	for _, path := range files {
		if err := decodeFile(path, &cfg); err != nil {
			var zero T
			return zero, errors.Wrapf(err, "config: %s", path)
		}
	}
	if err := validate(&cfg); err != nil {
		var zero T
		return zero, errors.Wrap(err, "config")
	}
	return cfg, nil
}

// decodeFile 展开 path 中的环境变量引用，并把内容解码到 dst 上。
func decodeFile(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	loader, err := yaml.NewLoader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	var doc yaml.Node // 空文件或只有注释时 Load 返回 io.EOF，doc 保持零值
	if err := loader.Load(&doc); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if doc.Kind == 0 {
		return nil
	}
	if err := expandNode(&doc, os.LookupEnv); err != nil {
		return err
	}
	return doc.Load(dst, yaml.WithKnownFields())
}
