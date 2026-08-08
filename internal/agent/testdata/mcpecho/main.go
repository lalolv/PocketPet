// mcpecho 是测试/冒烟用的迷你 MCP stdio server，提供一个 get_weather 工具。
// 位于 testdata 下：不参与 go build ./...，需要时显式构建：
//
//	go build -o /tmp/mcpecho ./internal/agent/testdata/mcpecho
package main

import (
	"context"
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type weatherIn struct {
	City string `json:"city" jsonschema:"城市名"`
}

func main() {
	srv := mcp.NewServer(&mcp.Implementation{Name: "mcpecho", Version: "0.1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_weather", Description: "查询指定城市的天气"},
		func(_ context.Context, _ *mcp.CallToolRequest, in weatherIn) (*mcp.CallToolResult, any, error) {
			city := in.City
			if city == "" {
				city = "本地"
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: city + "：晴天，25℃，微风"}},
			}, nil, nil
		})
	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
