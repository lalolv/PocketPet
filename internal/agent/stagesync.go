package agent

import (
	"context"
	"log/slog"

	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
	"github.com/lalolv/PocketPet/internal/store"
)

// StageSync 订阅领域事件，在 pet.stage_up 时把宠物的最新阶段写回 PET.md。
// 实现 tick.EventSink 接口，经 tick.MultiSink 与 SSE hub 并联挂在 Engine 上，
// 对 Engine 零侵入。同步失败只记日志，不影响主流程。
type StageSync struct {
	fs *petfs.FS
	st *store.Store
}

// NewStageSync 创建阶段同步器。
func NewStageSync(fs *petfs.FS, st *store.Store) *StageSync {
	return &StageSync{fs: fs, st: st}
}

// Publish 实现 tick.EventSink。
func (s *StageSync) Publish(e pet.Event) {
	if e.Type != pet.EventStageUp {
		return
	}
	// 事件触发时新阶段已落库，直接读库取权威值。
	p, err := s.st.GetPet(context.Background(), e.PetID)
	if err != nil {
		slog.Warn("stage sync: load pet failed", "pet", e.PetID, "err", err)
		return
	}
	if err := s.fs.UpdateStage(e.PetID, string(p.Stage)); err != nil {
		slog.Warn("stage sync: update PET.md failed", "pet", e.PetID, "err", err)
	}
}
