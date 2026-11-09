package config

import (
	"bytes"
	"io"
	"os"
	"path/filepath"

	"github.com/cockroachdb/errors"
	"go.yaml.in/yaml/v4"
)

// Load 读取 YAML 配置文件到 dst。dst 必须是非 nil 指针，调用前填好默认值。
//
// 流程：加载 envfile → 展开 ${VAR} → 解码（未知字段报错）→
// 对 dst 及其嵌套字段调用 Validate。文件中未写的字段保留默认值。
func Load(path string, dst any) error {
	if err := load(path, dst); err != nil {
		return errors.Wrapf(err, "config: %s", path)
	}
	return nil
}

func load(path string, dst any) error {
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
	if root := mapping(&doc); root != nil {
		lookup := lookupFunc(os.LookupEnv)
		if node := removeKey(root, envFileKey); node != nil {
			env, err := loadEnvFile(node.Value, filepath.Dir(path))
			if err != nil {
				return err
			}
			lookup = withFallback(lookup, env)
		}
		if err := expandNode(root, lookup); err != nil {
			return err
		}
	}
	if doc.Kind != 0 {
		if err := doc.Load(dst, yaml.WithKnownFields()); err != nil {
			return err
		}
	}
	return validate(dst)
}

// mapping 返回文档的根映射；根不是映射时返回 nil，交给解码报错。
func mapping(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
		return doc.Content[0]
	}
	return nil
}

// removeKey 从映射中移除 key 并返回其值节点；不存在时返回 nil。
func removeKey(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v := m.Content[i+1]
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return v
		}
	}
	return nil
}
