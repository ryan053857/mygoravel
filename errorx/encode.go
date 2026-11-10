package errorx

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/cockroachdb/errors/errorspb"
	"github.com/gogo/protobuf/proto"
)

// 注册 cockroachdb/errors 编解码器，使 Error 经 errors.EncodeError/DecodeError
// 跨进程传输后保留错误码、分类、公开参数和重试等待时间。错误码和分类作为可报告的安全详情，
// 公开参数以 JSON 放入载荷，解码后数值类型变为 float64。
func init() {
	key := errors.GetTypeKey((*Error)(nil))
	errors.RegisterLeafEncoder(key, func(_ context.Context, err error) (string, []string, proto.Message) {
		e := err.(*Error)
		return e.code, encodeDetails(e), encodePublic(e)
	})
	errors.RegisterWrapperEncoder(key, func(_ context.Context, err error) (string, []string, proto.Message) {
		e := err.(*Error)
		return e.code, encodeDetails(e), encodePublic(e)
	})
	errors.RegisterLeafDecoder(key, func(_ context.Context, _ string, details []string, payload proto.Message) error {
		return decode(nil, details, payload)
	})
	errors.RegisterWrapperDecoder(key, func(_ context.Context, cause error, _ string, details []string, payload proto.Message) error {
		return decode(cause, details, payload)
	})
}

func encodeDetails(e *Error) []string {
	details := []string{e.code, e.kind.String()}
	if e.retryAfter > 0 {
		details = append(details, e.retryAfter.String())
	}
	return details
}

func encodePublic(e *Error) proto.Message {
	if len(e.public) == 0 {
		return nil
	}
	data, err := json.Marshal(e.public)
	if err != nil {
		return nil
	}
	return &errorspb.StringPayload{Msg: string(data)}
}

// decode 还原 Error；格式不符时返回 nil，由 cockroachdb/errors 回退为不透明错误。
func decode(cause error, details []string, payload proto.Message) error {
	if len(details) < 2 || details[0] == "" {
		return nil
	}
	e := &Error{code: details[0], kind: parseKind(details[1]), cause: cause}
	if len(details) > 2 {
		e.retryAfter, _ = time.ParseDuration(details[2])
	}
	if p, ok := payload.(*errorspb.StringPayload); ok {
		_ = json.Unmarshal([]byte(p.Msg), &e.public)
	}
	return e
}
