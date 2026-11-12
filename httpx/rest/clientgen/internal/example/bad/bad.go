// Package bad 是 clientgen 测试用的、不能生成客户端的类型。
package bad

import (
	"strconv"

	"github.com/uptrace/bun"

	other "github.com/ryan053857/mygoravel/httpx/rest/clientgen/internal/example/bad/article"
)

// Money 自定义了 JSON 编码，复制字段得不到相同的 JSON。
type Money int64

func (m Money) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(strconv.FormatInt(int64(m), 10))), nil
}

type MoneyOutput struct {
	Body struct {
		Price Money `json:"price"`
	}
}

// RowOutput 用到了不能复制、也没有允许导入的第三方类型。
type RowOutput struct {
	Body struct {
		Deleted bun.NullTime `json:"deleted"`
	}
}

// ConflictOutput 用到了另一个名为 article 的包。
type ConflictOutput struct {
	Body other.Note
}
