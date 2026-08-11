package api

import (
	"github.com/lalolv/PocketPet/internal/plugin"
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
