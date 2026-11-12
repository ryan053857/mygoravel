// Package article 与示例的 article 包同名，用于测试镜像包名冲突。
package article

// Note 是一条备注。
type Note struct {
	Text string `json:"text"`
}
