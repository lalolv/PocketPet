package adventure

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	adkagent "google.golang.org/adk/v2/agent"
	adktool "google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petstate"
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
	if _, ok, err := a.getRun(petID); err != nil {
		return plugin.ToolResult{}, err
	} else if ok {
		return plugin.ToolResult{OK: false, Outcome: "我还在外面探险呢"}, nil
	}

	sm, err := a.currentMap(ctx)
	if err != nil {
		return plugin.ToolResult{OK: false, Outcome: "现在没有探险地图，等等再来吧"}, nil
	}

	cost := a.EnergyCost
	mapID := sm.ID
	now := time.Now().UTC()
	var petName string

	res, err := a.ctx.Apply(ctx, petID, petstate.Transition{
		To: pet.ActivityAdventuring, Owner: "adventure", Reason: "start",
		StatsDelta: pet.Stats{Energy: -cost},
		Guards: []petstate.Guard{
			func(before petstate.Snapshot) error {
				petName = before.Name
				if before.Stats.Energy < cost {
					return errLowEnergy
				}
				return nil
			},
		},
		OnCommit: func(ctx context.Context, _, _ petstate.Snapshot) error {
			a.stepMu.Lock()
			defer a.stepMu.Unlock()
			return a.insertRun(ctx, Run{
				PetID: petID, MapID: mapID, NodeID: 0, ChestsFound: []int{}, StartedAt: now,
			})
		},
	})
	if err != nil {
		return plugin.ToolResult{}, err
	}
	if !res.OK {
		switch {
		case errors.Is(res.Err, pet.ErrBusy), errors.Is(res.Err, pet.ErrAlready):
			if res.Snapshot.Activity.Kind == pet.ActivitySleeping {
				return plugin.ToolResult{OK: false, Outcome: "我正在睡觉，没法出门探险"}, nil
			}
			if res.Snapshot.Stats.Energy < pet.AlertWarn {
				return plugin.ToolResult{OK: false, Outcome: "我太困了，走不动，想先睡一觉"}, nil
			}
			return plugin.ToolResult{OK: false, Outcome: "我现在忙着，出不了门探险"}, nil
		case errors.Is(res.Err, errLowEnergy):
			return plugin.ToolResult{OK: false, Outcome: "我太累了，没力气去探险"}, nil
		default:
			if res.Err != nil {
				return plugin.ToolResult{OK: false, Outcome: res.Err.Error()}, nil
			}
			return plugin.ToolResult{OK: false, Outcome: "现在出不去"}, nil
		}
	}
	name := petName
	if name == "" {
		name = res.Snapshot.Name
	}
	a.ctx.Emit(ctx, pet.Event{
		PetID: petID, Type: EventStarted,
		Message: name + " 从【入口】出发去探险了", CreatedAt: now,
	})
	return plugin.ToolResult{OK: true, Outcome: "我从入口出发去探险了！会沿着道路一步步前进"}, nil
}

var errLowEnergy = errors.New("low energy")

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

func (a *Adventure) endRun(ctx context.Context, petID, reason string) error {
	_ = a.deleteRun(ctx, petID)
	_, err := a.ctx.Apply(ctx, petID, petstate.Transition{
		To: pet.ActivityIdle, Owner: "adventure", Reason: reason,
	})
	return err
}
