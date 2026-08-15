package adventure

import (
	"context"
	"testing"
	"time"
)

// TestRefreshMapDropsOldMaps 验证换图后旧图被清理，不会在库里累积。
func TestRefreshMapDropsOldMaps(t *testing.T) {
	env := setup(t) // Init 已建第一张图
	if _, err := env.adv.refreshMap(context.Background(), t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := env.adv.refreshMap(context.Background(), t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var maps, nodes, edges int
	row := env.st.DB().QueryRow(`SELECT
		(SELECT COUNT(*) FROM adventure_maps),
		(SELECT COUNT(*) FROM adventure_nodes),
		(SELECT COUNT(*) FROM adventure_edges)`)
	if err := row.Scan(&maps, &nodes, &edges); err != nil {
		t.Fatal(err)
	}
	if maps != 1 || nodes != 6 || edges == 0 {
		t.Fatalf("maps=%d nodes=%d edges=%d, want 1/6/>0", maps, nodes, edges)
	}
}
