// Package httpx 提供统一的 JSON HTTP 响应助手。
// 放在契约层之下，供 api 与插件路由共用，避免 plugins → api 的反向依赖。
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// errorBody 是统一错误响应格式 {"error":{"code","message"}}。
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// WriteJSON 以 application/json 写响应。
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json failed", "err", err)
	}
}

// WriteError 以统一错误格式写响应。
func WriteError(w http.ResponseWriter, status int, code, msg string) {
	var b errorBody
	b.Error.Code = code
	b.Error.Message = msg
	WriteJSON(w, status, b)
}
