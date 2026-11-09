// Package config 将一个 YAML 配置文件加载到应用的根配置结构体。
//
//	type Config struct {
//		HTTP     httpx.Config    `yaml:"http"`
//		Database database.Config `yaml:"database"`
//	}
//
//	cfg, err := config.Load[Config]("config.yaml")
//
// Load 先对各字段调用 SetDefaults 填充默认值，再展开 ${VAR}、${VAR:-default}
// 并解码；顶层保留键 envfile 指向一个 dotenv 文件，其中的变量只在进程环境
// 未设置时使用。未知字段会报错。解码后对各字段调用 Validate。
package config
