package adventure

import (
	"context"
	"fmt"
	"strings"
	"time"

	adkagent "google.golang.org/adk/v2/agent"
	adktool "google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/plugin"
)

func (a *Adventure) initTools() error {
	start, err := functiontool.New(functiontool.Config{
		Name:        "adventure_start",
		Description: fmt.Sprintf("出发去探险：消耗精力（-%d），在当前地图上从入口出发，之后会一步步沿道路前进。", int(a.EnergyCost)),
	}, func(actx adkagent.Context, _ struct{}) (plugin.ToolResult, error) {
		return a.start(actx, plugin.PetIDOf(actx))
	})
	if err != nil {
		return err
	}
	status, err := functiontool.New(functiontool.Config{
		Name:        "adventure_status",
		Description: "查看自己是否在探险、当前所在地点，以及地图概况。",
	}, func(actx adkagent.Context, _ struct{}) (plugin.ToolResult, error) {
		return a.status(actx, plugin.PetIDOf(actx))
	})
	if err != nil {
		return err
	}
	a.tools = []adktool.Tool{start, status}
	return nil
}

func (a *Adventure) start(ctx context.Context, petID string) (plugin.ToolResult, error) {
	p, err := a.ctx.GetPet(ctx, petID)
	if err != nil {
		return plugin.ToolResult{}, err
	}
	if p.Sleeping {
		return plugin.ToolResult{OK: false, Outcome: "我正在睡觉，没法出门探险"}, nil
	}
	if p.Stats.Energy < a.EnergyCost {
		return plugin.ToolResult{OK: false, Outcome: "我太累了，没力气去探险"}, nil
	}
	if _, ok, err := a.getRun(petID); err != nil {
		return plugin.ToolResult{}, err
	} else if ok {
		return plugin.ToolResult{OK: false, Outcome: "我还在外面探险呢"}, nil
	}

	sm, err := a.currentMap(ctx)
	if err != nil {
		return plugin.ToolResult{OK: false, Outcome: "现在没有探险地图，等等再来吧"}, nil
	}

	if _, err := a.ctx.AdjustStats(ctx, petID, pet.Stats{Energy: -a.EnergyCost}); err != nil {
		return plugin.ToolResult{}, err
	}
	now := time.Now().UTC()
	a.stepMu.Lock()
	err = a.insertRun(ctx, Run{
		PetID: petID, MapID: sm.ID, NodeID: 0, ChestsFound: []int{}, StartedAt: now,
	})
	a.stepMu.Unlock()
	if err != nil {
		return plugin.ToolResult{}, err
	}
	a.ctx.Emit(ctx, pet.Event{
		PetID: petID, Type: EventStarted,
		Message: p.Name + " 从【入口】出发去探险了", CreatedAt: now,
	})
	return plugin.ToolResult{OK: true, Outcome: "我从入口出发去探险了！会沿着道路一步步前进"}, nil
}

func (a *Adventure) status(ctx context.Context, petID string) (plugin.ToolResult, error) {
	var sb strings.Builder
	sm, err := a.currentMap(ctx)
	if err != nil {
		sb.WriteString("现在没有探险地图。")
		return plugin.ToolResult{OK: true, Outcome: sb.String()}, nil
	}
	fmt.Fprintf(&sb, "当前地图有 %d 个地点、%d 个宝箱。", len(sm.Graph.Nodes), sm.Graph.ChestCount())

	r, ok, err := a.getRun(petID)
	if err != nil {
		return plugin.ToolResult{}, err
	}
	if !ok {
		sb.WriteString("我现在没有出门。")
		return plugin.ToolResult{OK: true, Outcome: strings.TrimSpace(sb.String())}, nil
	}
	node, _ := nodeByID(sm.Graph, r.NodeID)
	branches := len(sm.Graph.OutNeighbors(r.NodeID))
	fmt.Fprintf(&sb, "我正在【%s】，前方有 %d 条路可走，已经发现 %d 个宝箱。",
		node.Name, branches, len(r.ChestsFound))
	return plugin.ToolResult{OK: true, Outcome: strings.TrimSpace(sb.String())}, nil
}
