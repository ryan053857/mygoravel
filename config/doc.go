// Package config 将一个 YAML 配置文件加载到应用的配置结构体。
//
//	cfg := Config{Database: database.DefaultConfig()}
//	if err := config.Load("config.yaml", &cfg); err != nil {
//		return err
//	}
//
// 值中的 ${VAR}、${VAR:-default} 从进程环境展开；顶层保留键 envfile 指向
// 一个 dotenv 文件，其中的变量只在进程环境未设置时使用。解码是严格的：
// 未知字段、类型错误和显式 null 都会报错，且诊断不回显配置值。
// 解码后对 dst 及其嵌套字段调用 Validate。Secret 在打印与序列化时脱敏。
package config
