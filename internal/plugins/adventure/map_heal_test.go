package adventure

import (
	"context"
	"testing"
	"time"
)

// TestOnTickHealsMissingMap 验证当前图被删/损坏后，下一个 tick 自愈重建一张新图。
func TestOnTickHealsMissingMap(t *testing.T) {
	env := setup(t) // Init 已建第一张图
	env.adv.MapRefreshInterval = 0 // 关闭周期换图，只验自愈

	for _, q := range []string{
		`DELETE FROM adventure_edges`, `DELETE FROM adventure_nodes`, `DELETE FROM adventure_maps`,
	} {
		if _, err := env.st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := env.adv.currentMap(context.Background()); err == nil {
		t.Fatal("map should be gone before tick")
	}

	env.clock.Advance(time.Minute)
	env.engine.TickAll(context.Background())

	sm, err := env.adv.currentMap(context.Background())
	if err != nil {
		t.Fatalf("map should be healed: %v", err)
	}
	if len(sm.Graph.Nodes) != 6 || sm.Graph.IslandName == "" {
		t.Fatalf("healed map = %+v", sm.Graph)
	}
}
