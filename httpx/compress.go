package httpx

import (
	"mime"
	"net/http"
	"strings"

	"github.com/klauspost/compress/gzhttp"
)

// Compress 按请求的 Accept-Encoding 用 zstd 或 gzip 压缩响应，两者都接受时用 zstd。
//
// 只压缩文本类内容：text/*、JSON、JavaScript、XML、SVG 和 WebAssembly；图片、字体、压缩包等原样发送。
// 小于 1 KiB 的响应、已经带 Content-Encoding 的响应（例如 web/ui 预先压缩的资源）
// 和 Range 请求的响应也不压缩。
//
// 流式响应不受影响：text/event-stream（SSE）直接写出，不经过压缩缓冲；
// 带 Upgrade 请求头的请求（WebSocket）原样交给下一个处理器。
//
// 压缩的响应保留原来的 ETag，条件请求仍然返回 304；同时去掉 Accept-Ranges。
// 应用前面的反向代理或 CDN 已经负责压缩时，不需要安装它。
func Compress() func(http.Handler) http.Handler {
	wrap, err := gzhttp.NewWrapper(gzhttp.ContentTypeFilter(compressible))
	if err != nil {
		panic(err)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Upgrade") != "" {
				next.ServeHTTP(w, r)
				return
			}
			wrap(http.HandlerFunc(func(gz http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(&compressWriter{gz: gz, raw: w}, r)
			})).ServeHTTP(w, r)
		})
	}
}

// compressible 判断某种内容类型是否值得压缩。
func compressible(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	switch {
	case streaming(mt):
		return false
	case strings.HasPrefix(mt, "text/"),
		strings.HasSuffix(mt, "+json"), strings.HasSuffix(mt, "+xml"): // 包括 problem+json、svg+xml
		return true
	}
	switch mt {
	case "application/json", "application/javascript", "application/xml", "application/wasm":
		return true
	}
	return false
}

func streaming(mediaType string) bool { return mediaType == "text/event-stream" }

// compressWriter 在响应开始时按 Content-Type 选择出口：流式响应直接写到原始 ResponseWriter，
// 其余经过 gzhttp。gzhttp 会推迟 WriteHeader，而且在没有内容时 Flush 什么也不做，
// SSE 先发出响应头再等事件的做法经过它会让客户端迟迟收不到响应头。
type compressWriter struct {
	gz, raw http.ResponseWriter
	out     http.ResponseWriter // 响应开始后确定
}

func (w *compressWriter) target() http.ResponseWriter {
	if w.out == nil {
		w.out = w.gz
		if mt, _, err := mime.ParseMediaType(w.raw.Header().Get("Content-Type")); err == nil && streaming(mt) {
			w.out = w.raw
		}
	}
	return w.out
}

// Header 返回响应头。gzhttp 的 ResponseWriter 和原始的共用同一个响应头。
func (w *compressWriter) Header() http.Header { return w.raw.Header() }

func (w *compressWriter) WriteHeader(code int) {
	if code >= 100 && code < 200 {
		w.raw.WriteHeader(code)
		return
	}
	w.target().WriteHeader(code)
}

func (w *compressWriter) Write(b []byte) (int, error) { return w.target().Write(b) }

// FlushError 供 http.ResponseController.Flush 使用。
func (w *compressWriter) FlushError() error { return http.NewResponseController(w.target()).Flush() }

// Flush 实现 http.Flusher。
func (w *compressWriter) Flush() { _ = w.FlushError() }

// Unwrap 供 http.ResponseController 和 realtime 查找底层能力。响应开始之前返回原始 ResponseWriter，
// 例如 SSE 在设置响应头之前取消写超时，这时还不能确定出口。
func (w *compressWriter) Unwrap() http.ResponseWriter {
	if w.out != nil {
		return w.out
	}
	return w.raw
}
