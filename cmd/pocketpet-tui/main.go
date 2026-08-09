// pocketpet-tui 是 PocketPet 的终端互动客户端：
// 连接 pocketpetd（REST + SSE），提供选宠/创建、ASCII 精灵动画、
// 照顾动作、聊天与事件日志。
package main

import (
	"flag"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/lalolv/PocketPet/internal/tui"
)

func main() {
	addr := flag.String("server", "", "pocketpetd 地址（默认环境变量 POCKETPET_SERVER，再默认 http://localhost:8080）")
	flag.Parse()

	base := *addr
	if base == "" {
		base = os.Getenv("POCKETPET_SERVER")
	}
	if base == "" {
		base = "http://localhost:8080"
	}

	m := tui.NewModel(tui.NewClient(base))
	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tui:", err)
		os.Exit(1)
	}
}
