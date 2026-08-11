package adventure

import (
	"context"
	"fmt"
	"time"

	"github.com/lalolv/PocketPet/internal/pet"
)

// OnTick 实现 plugin.TickHook：只负责地图刷新计数（跟养成 tick）。
// 行程步进由独立墙钟 StepInterval 驱动，避免默认 60s tick 导致一步一分钟。
func (a *Adventure) OnTick(ctx context.Context, now time.Time) {
	n, err := a.bumpRefreshCounter()
	if err != nil {
		a.ctx.Logger().Warn("adventure: refresh counter failed", "err", err)
	} else if a.MapRefreshTicks > 0 && n >= a.MapRefreshTicks {
		a.stepMu.Lock()
		defer a.stepMu.Unlock()
		if _, err := a.refreshMap(ctx, now); err != nil {
			a.ctx.Logger().Warn("adventure: map refresh failed", "err", err)
		}
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
		_ = a.deleteRun(ctx, r.PetID)
		return err
	}
	p, err := a.ctx.GetPet(ctx, r.PetID)
	if err != nil || !p.Alive {
		return a.deleteRun(ctx, r.PetID)
	}

	node, ok := nodeByID(sm.Graph, r.NodeID)
	if !ok {
		_ = a.deleteRun(ctx, r.PetID)
		return fmt.Errorf("adventure: node %d missing", r.NodeID)
	}

	// 先结算当前节点宝箱（含刚出发停在入口后的第一步之前；入口通常无箱）。
	if node.HasChest && !containsInt(r.ChestsFound, node.ID) {
		r.ChestsFound = append(r.ChestsFound, node.ID)
		a.ctx.Emit(ctx, pet.Event{
			PetID: r.PetID, Type: EventChest,
			Message: fmt.Sprintf("%s 在【%s】发现了一个宝箱！", p.Name, node.Name),
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
		return a.deleteRun(ctx, r.PetID)
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
