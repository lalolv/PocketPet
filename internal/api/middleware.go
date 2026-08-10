package api

import (
	"log/slog"
	"net/http"
	"time"
)

// statusWriter 记录响应状态码，同时向下转发 http.Flusher（SSE 端点依赖它）。
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Flush 实现 http.Flusher：标记 200 已写出并向下转发。
func (w *statusWriter) Flush() {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// logRequests 记录每个 HTTP 请求的方法、路径、状态码与耗时。
// SSE 长连接（events / chat stream）在连接关闭时才记录，duration 即连接保持时长。
// 级别按价值分层：5xx 记 Warn；GET 成功（多为 TUI 每 4s 的状态轮询）记 Debug，避免刷屏；
// 其余（POST 变更、4xx）记 Info。
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		args := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"duration", time.Since(start).Round(time.Millisecond),
		}
		switch {
		case status >= 500:
			slog.Warn("http request", args...)
		case r.Method == http.MethodGet && status < 400:
			slog.Debug("http request", args...)
		default:
			slog.Info("http request", args...)
		}
	})
}
