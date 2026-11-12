// Package aliyunapi 以 V3 签名（ACS3-HMAC-SHA256）调用阿里云 RPC 风格的 OpenAPI，供 push/aliyun 和 sms/aliyun 使用。
// 不依赖阿里云的 SDK：请求随 ctx 取消，http.Client 可以替换为 outboundtest 的客户端。
package aliyunapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
)

// maxResponse 是读取的响应体的最大字节数。
const maxResponse = 64 << 10

// Client 调用一个阿里云产品的接口，可以并发使用。
type Client struct {
	Host            string // 接口域名，例如 dysmsapi.aliyuncs.com
	Version         string // 接口版本，例如 2017-05-25
	AccessKeyID     string
	AccessKeySecret string
	HTTP            *http.Client
}

// Response 是一次调用的结果：HTTP 状态码、响应体，以及从响应体中解析出的通用字段。
type Response struct {
	Status    int
	Body      []byte
	RequestID string `json:"RequestId"`
	Code      string `json:"Code"`
	Message   string `json:"Message"`
}

// Error 返回描述这个响应的错误，用于调用失败时，例如 "HTTP 400 isv.MOBILE_NUMBER_ILLEGAL: ... (request ...)"。
func (r *Response) Error(action string) error {
	msg := r.Message
	if r.Code == "" && msg == "" {
		msg = strings.TrimSpace(string(r.Body))
	}
	return errors.Newf("%s: HTTP %d %s: %s (request %s)", action, r.Status, r.Code, msg, r.RequestID)
}

// Call 调用接口 action，参数放在表单请求体中。只有网络错误、读取响应失败时返回错误；
// HTTP 状态码和响应中的 Code 由调用方判断。
func (c *Client) Call(ctx context.Context, action string, params url.Values) (*Response, error) {
	body := []byte(params.Encode())
	sum := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(sum[:])
	headers := map[string]string{
		"host":                  c.Host,
		"content-type":          "application/x-www-form-urlencoded",
		"x-acs-action":          action,
		"x-acs-version":         c.Version,
		"x-acs-date":            time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"x-acs-signature-nonce": uuid.NewString(),
		"x-acs-content-sha256":  payloadHash,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+c.Host+"/", bytes.NewReader(body))
	if err != nil {
		return nil, errors.Wrap(err, "new request")
	}
	for name, value := range headers {
		if name != "host" {
			req.Header.Set(name, value)
		}
	}
	req.Header.Set("Authorization", authorization(http.MethodPost, headers, payloadHash, c.AccessKeyID, c.AccessKeySecret))

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, action)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return nil, errors.Wrapf(err, "%s: read response", action)
	}
	r := &Response{Status: resp.StatusCode, Body: data}
	_ = json.Unmarshal(data, r)
	return r, nil
}
