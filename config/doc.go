// Package config 将 YAML 配置文件加载到应用的根配置结构体。
//
//	type Config struct {
//		HTTP     httpx.Config    `yaml:"http"`
//		Database database.Config `yaml:"database"`
//	}
//
//	cfg, err := config.Load[Config]("config/app.yaml", "config/prod.yaml")
//
// Load 先对各字段调用 SetDefaults 填充默认值，再按顺序处理每个文件：展开 ${VAR}、
// ${VAR:-default} 并解码到同一个结构体上，后面的文件覆盖前面的；未知字段会报错。
// 全部文件处理完后对各字段调用 Validate。LoadEnv 把 dotenv 文件中的变量写入进程环境。
package config
