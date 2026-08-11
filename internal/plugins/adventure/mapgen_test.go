package adventure

import (
	"testing"
)

func TestGenerateMapBasics(t *testing.T) {
	seq := 0
	ints := []int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	g := GenerateMap(GenConfig{
		NodeCount:   12,
		MaxBranches: 5,
		ChestMinPct: 0.15,
		ChestMaxPct: 0.25,
		IntN: func(n int) int {
			if n <= 0 {
				return 0
			}
			v := ints[seq%len(ints)] % n
			seq++
			return v
		},
		Float64: func() float64 { return 0.9 }, // 少补边
	})
	if len(g.Nodes) != 12 {
		t.Fatalf("nodes = %d", len(g.Nodes))
	}
	if g.Nodes[0].Name != "入口" || g.Nodes[0].HasChest {
		t.Fatalf("start = %+v", g.Nodes[0])
	}
	outdeg := map[int]int{}
	reachable := map[int]bool{0: true}
	for _, e := range g.Edges {
		if e.To <= e.From {
			t.Fatalf("non-forward edge %d→%d (expect DAG by id)", e.From, e.To)
		}
		outdeg[e.From]++
		if outdeg[e.From] > 5 {
			t.Fatalf("outdeg[%d]=%d > 5", e.From, outdeg[e.From])
		}
	}
	// BFS 可达性（生成树保证）
	changed := true
	for changed {
		changed = false
		for _, e := range g.Edges {
			if reachable[e.From] && !reachable[e.To] {
				reachable[e.To] = true
				changed = true
			}
		}
	}
	for i := 0; i < 12; i++ {
		if !reachable[i] {
			t.Fatalf("node %d not reachable", i)
		}
	}
	chests := g.ChestCount()
	if chests < 2 || chests > 3 {
		t.Fatalf("chests = %d, want 2..3 for N=12", chests)
	}
}

func TestGenerateMapChestRangeN20(t *testing.T) {
	g := GenerateMap(GenConfig{
		NodeCount: 20, MaxBranches: 5,
		ChestMinPct: 0.15, ChestMaxPct: 0.25,
		IntN:    func(n int) int { return 0 },
		Float64: func() float64 { return 1 },
	})
	c := g.ChestCount()
	if c < 3 || c > 5 {
		t.Fatalf("chests = %d, want 3..5 for N=20", c)
	}
}
