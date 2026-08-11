package adventure

import (
	"fmt"
	"math"
	"math/rand/v2"
)

// GenConfig 控制最简图生成。
type GenConfig struct {
	NodeCount   int     // 节点数
	MaxBranches int     // 单节点出边上限
	ChestMinPct float64 // 宝箱数下限占比
	ChestMaxPct float64 // 宝箱数上限占比
	// IntN 返回 [0,n) 整数；nil 则用 rand.IntN。
	IntN func(n int) int
	// Float64 返回 [0,1)；nil 则用 rand.Float64。
	Float64 func() float64
}

// MapGraph 是一张探险地图（有向无环图：地点 + 道路）。
type MapGraph struct {
	Nodes []MapNode
	Edges []MapEdge
}

// MapNode 是地点节点。
type MapNode struct {
	ID       int
	Name     string
	HasChest bool
}

// MapEdge 是道路（有向边）。
type MapEdge struct {
	From int
	To   int
}

// GenerateMap 生成最简探险图：
// 1) 先建以 0 为入口的生成树，保证连通；
// 2) 再随机补边（只连向更大编号，保持 DAG），每点出边 ≤ MaxBranches；
// 3) 按节点数的 [ChestMinPct, ChestMaxPct] 取整后随机放置宝箱（避开入口）。
func GenerateMap(cfg GenConfig) MapGraph {
	n := cfg.NodeCount
	if n < 2 {
		n = 2
	}
	maxB := cfg.MaxBranches
	if maxB < 1 {
		maxB = 1
	}
	minPct, maxPct := cfg.ChestMinPct, cfg.ChestMaxPct
	if minPct <= 0 {
		minPct = defaultChestMinPct
	}
	if maxPct <= 0 {
		maxPct = defaultChestMaxPct
	}
	if maxPct < minPct {
		maxPct = minPct
	}
	intN := cfg.IntN
	if intN == nil {
		intN = rand.IntN
	}
	float64n := cfg.Float64
	if float64n == nil {
		float64n = rand.Float64
	}

	nodes := make([]MapNode, n)
	for i := 0; i < n; i++ {
		nodes[i] = MapNode{ID: i, Name: fmt.Sprintf("地点%d", i+1)}
	}
	nodes[0].Name = "入口"

	outdeg := make([]int, n)
	var edges []MapEdge
	hasEdge := make(map[[2]int]bool)

	addEdge := func(from, to int) bool {
		if from == to || outdeg[from] >= maxB {
			return false
		}
		key := [2]int{from, to}
		if hasEdge[key] {
			return false
		}
		hasEdge[key] = true
		edges = append(edges, MapEdge{From: from, To: to})
		outdeg[from]++
		return true
	}

	// 生成树：节点 i 挂到 [0,i) 中仍有出边配额的父节点。
	for i := 1; i < n; i++ {
		cands := make([]int, 0, i)
		for p := 0; p < i; p++ {
			if outdeg[p] < maxB {
				cands = append(cands, p)
			}
		}
		parent := 0
		if len(cands) > 0 {
			parent = cands[intN(len(cands))]
		} else {
			// 极端情况：选当前出边最少的祖先，允许临时超过上限以保连通。
			parent = 0
			for p := 1; p < i; p++ {
				if outdeg[p] < outdeg[parent] {
					parent = p
				}
			}
			key := [2]int{parent, i}
			hasEdge[key] = true
			edges = append(edges, MapEdge{From: parent, To: i})
			outdeg[parent]++
			continue
		}
		addEdge(parent, i)
	}

	// 额外分支：只连向更大编号，保持 DAG，避免环路走不完。
	for from := 0; from < n-1; from++ {
		for outdeg[from] < maxB {
			if float64n() > 0.45 {
				break
			}
			span := n - from - 1
			if span <= 0 {
				break
			}
			to := from + 1 + intN(span)
			if !addEdge(from, to) {
				// 可能已连过；再试一次后放弃本轮。
				to2 := from + 1 + intN(span)
				if !addEdge(from, to2) {
					break
				}
			}
		}
	}

	// 宝箱数量：round(N*pct)，夹在 [1, N-1]。
	minC := int(math.Round(float64(n) * minPct))
	maxC := int(math.Round(float64(n) * maxPct))
	if minC < 1 {
		minC = 1
	}
	if maxC < minC {
		maxC = minC
	}
	if maxC > n-1 {
		maxC = n - 1
	}
	if minC > maxC {
		minC = maxC
	}
	chestCount := minC
	if maxC > minC {
		chestCount = minC + intN(maxC-minC+1)
	}

	// 在非入口节点上均匀抽样。
	pool := make([]int, 0, n-1)
	for i := 1; i < n; i++ {
		pool = append(pool, i)
	}
	for i := len(pool) - 1; i > 0; i-- {
		j := intN(i + 1)
		pool[i], pool[j] = pool[j], pool[i]
	}
	for i := 0; i < chestCount && i < len(pool); i++ {
		nodes[pool[i]].HasChest = true
	}

	return MapGraph{Nodes: nodes, Edges: edges}
}

// OutNeighbors 返回 from 的出边目标列表。
func (g MapGraph) OutNeighbors(from int) []int {
	var out []int
	for _, e := range g.Edges {
		if e.From == from {
			out = append(out, e.To)
		}
	}
	return out
}

// ChestCount 返回带宝箱的节点数。
func (g MapGraph) ChestCount() int {
	n := 0
	for _, node := range g.Nodes {
		if node.HasChest {
			n++
		}
	}
	return n
}
