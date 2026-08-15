package adventure

import (
	"context"
	"fmt"
	"time"

	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petstate"
)

// OnTick 实现 plugin.TickHook：保证"有且只有一张活跃地图"（跟养成 tick 检查一次）。
// 当前图缺失/损坏时立即自愈重建；否则按墙钟周期到期换图。
// 行程步进由独立墙钟 StepInterval 驱动，避免默认 60s tick 导致一步一分钟。
// 换图同步段只做拓扑生成 + 降级主题落库（毫秒级）；LLM 主题由 goroutine 异步升级。
func (a *Adventure) OnTick(ctx context.Context, now time.Time) {
	a.stepMu.Lock()
	defer a.stepMu.Unlock()
	sm, err := a.currentMap(ctx)
	if err != nil {
		// 自愈：当前图不可用（被删/损坏），立即重建，不等重启。
		if _, err := a.refreshMap(ctx, now); err != nil {
			a.ctx.Logger().Warn("adventure: map heal failed", "err", err)
		}
		return
	}
	if a.MapRefreshInterval <= 0 || now.Sub(sm.CreatedAt) < a.MapRefreshInterval {
		return
	}
	if _, err := a.refreshMap(ctx, now); err != nil {
		a.ctx.Logger().Warn("adventure: map refresh failed", "err", err)
	}
}

// advanceAllRuns 推进所有进行中行程各一步（步进循环与单测调用）。
func (a *Adventure) advanceAllRuns(ctx context.Context, now time.Time) {
	a.stepMu.Lock()
	defer a.stepMu.Unlock()

	runs, err := a.listRuns()
	if err != nil {
		a.ctx.Logger().Warn("adventure: list runs failed", "err", err)
		return
	}
	for _, r := range runs {
		if err := a.advanceRun(ctx, r, now); err != nil {
			a.ctx.Logger().Warn("adventure: advance failed", "pet", r.PetID, "err", err)
		}
	}
}

// advanceRun 在当前节点处理宝箱，再随机选分支前进；无出路则结束。
func (a *Adventure) advanceRun(ctx context.Context, r Run, now time.Time) error {
	sm, err := a.loadMap(ctx, r.MapID)
	if err != nil {
		_ = a.endRun(ctx, r.PetID, "bad-map")
		return err
	}
	p, err := a.ctx.GetPet(ctx, r.PetID)
	if err != nil || !p.Alive {
		_ = a.endRun(ctx, r.PetID, "dead")
		return a.deleteRun(ctx, r.PetID)
	}
	// 自愈：Activity 已非 adventuring 但仍有 run
	if p.Activity != pet.ActivityAdventuring {
		a.ctx.Emit(ctx, pet.Event{
			PetID: r.PetID, Type: EventAborted,
			Message:   p.Name + " 的探险中断了",
			CreatedAt: now,
		})
		return a.deleteRun(ctx, r.PetID)
	}

	node, ok := nodeByID(sm.Graph, r.NodeID)
	if !ok {
		_ = a.endRun(ctx, r.PetID, "bad-node")
		return fmt.Errorf("adventure: node %d missing", r.NodeID)
	}

	if node.HasChest && !containsInt(r.ChestsFound, node.ID) {
		r.ChestsFound = append(r.ChestsFound, node.ID)
		a.ctx.Emit(ctx, pet.Event{
			PetID: r.PetID, Type: EventChest,
			Message:   fmt.Sprintf("%s 在【%s】发现了一个宝箱！", p.Name, node.Name),
			CreatedAt: now,
		})
		if err := a.updateRun(ctx, r); err != nil {
			return err
		}
	}

	neighbors := sm.Graph.OutNeighbors(r.NodeID)
	if len(neighbors) == 0 {
		msg := fmt.Sprintf("%s 探险回来了", p.Name)
		if len(r.ChestsFound) > 0 {
			msg += fmt.Sprintf("，沿途发现了 %d 个宝箱", len(r.ChestsFound))
		}
		a.ctx.Emit(ctx, pet.Event{PetID: r.PetID, Type: EventFinished, Message: msg, CreatedAt: now})
		_ = a.deleteRun(ctx, r.PetID)
		_, _ = a.ctx.Apply(ctx, r.PetID, petstate.Transition{
			To: pet.ActivityIdle, Owner: "adventure", Reason: "finished",
		})
		return nil
	}

	next := neighbors[a.IntN(len(neighbors))]
	r.NodeID = next
	if err := a.updateRun(ctx, r); err != nil {
		return err
	}
	nextName := fmt.Sprintf("地点%d", next+1)
	if n, ok := nodeByID(sm.Graph, next); ok {
		nextName = n.Name
	}
	a.ctx.Emit(ctx, pet.Event{
		PetID: r.PetID, Type: EventMoved,
		Message:   fmt.Sprintf("%s 走到了【%s】", p.Name, nextName),
		CreatedAt: now,
	})
	return nil
}
