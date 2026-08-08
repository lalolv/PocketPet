package api

import (
	"net/http"

	"pocketpet/internal/plugin"
)

// Route 是插件路由类型（定义在契约层 internal/plugin，此处为别名，
// 保持设计文档 8.3 节 api.Route 的写法可用）。
type Route = plugin.Route

// RegisterPluginRoutes 把某插件的路由挂到 /v1/plugins/<plugin>/ 下。
// 应在服务启动前完成注册。
func (s *Server) RegisterPluginRoutes(pluginName string, routes []Route) {
	for _, rt := range routes {
		s.mux.HandleFunc(rt.Method+" /v1/plugins/"+pluginName+rt.Pattern, rt.Handler)
	}
}

// WriteJSON 以统一 JSON 形式写响应（导出给插件路由使用）。
func WriteJSON(w http.ResponseWriter, status int, v any) { writeJSON(w, status, v) }

// WriteError 以统一错误格式 {"error":{"code","message"}} 写响应（导出给插件路由使用）。
func WriteError(w http.ResponseWriter, status int, code, msg string) {
	writeError(w, status, code, msg)
}
