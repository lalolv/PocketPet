package adventure

// 地图主题层：岛名、地点名、地点描述等叙事皮肤（规范见 docs/08）。
// 拓扑仍由 mapgen.go 确定性生成；本文件只负责"皮肤"的类型、地带划分、
// 输出校验与内置降级词库。LLM 实现在 themer_llm.go。

import (
	"context"
	"fmt"
	"slices"
	"unicode/utf8"
)

// 地带（zone）：按入口 BFS 深度划分，约束各地点允许的地形。
const (
	ZoneCoast  = "海岸带" // depth ≤ 1
	ZoneInland = "内陆带" // 1 < depth ≤ maxDepth/2
	ZoneDeep   = "深处带" // depth > maxDepth/2
)

// 要素词表：elements 的受控枚举（LLM 输出与校验共用）。
var (
	regularElements = []string{"地貌", "风景", "渊源", "环境"}
	optionalElements = []string{"气候", "生物", "危险"}
)

// themeSeeds 是主题种子词库，换图时随机抽一个保证多样性。
var themeSeeds = []string{"迷雾海岛", "火山孤岛", "珊瑚环礁", "沉船海湾", "翡翠群岛", "风暴海角"}

// ThemeRequest 是一次主题生成的输入（Go 侧从拓扑计算）。
type ThemeRequest struct {
	Seed       string    // 主题种子
	NodeCount  int       // 节点数
	ChestNodes []int     // 宝箱节点编号，供描述暗示藏宝
	Edges      []MapEdge // 完整边列表，供安排地形过渡
	Zones      []string  // 每个节点所属地带（按下标对应，硬约束）
}

// LocationTheme 是一个地点的叙事皮肤。
type LocationTheme struct {
	ID          int      `json:"id"`          // 对应节点编号
	Name        string   `json:"name"`        // 地点名（2–6 字，全图唯一）
	Description string   `json:"description"` // 一句话描述（15–40 字）
	Zone        string   `json:"zone"`        // 必须等于请求下发的值
	Elements    []string `json:"elements"`    // 描述覆盖的要素（1–3 项，至少一个常规要素）
}

// IslandTheme 是一张地图的叙事皮肤。
type IslandTheme struct {
	IslandName string          `json:"island_name"` // 岛名（2–8 字）
	Theme      string          `json:"theme"`       // 一句话岛屿主题（≤30 字）
	Locations  []LocationTheme `json:"locations"`   // 长度必须等于 NodeCount，ID 为 0..N-1 的排列
}

// Themer 为地图生成主题文案；nil 或返回错误时使用 FallbackThemer。
type Themer interface {
	ThemeIsland(ctx context.Context, req ThemeRequest) (*IslandTheme, error)
}

// themeRequest 从拓扑构造主题请求：计算地带并抽取主题种子。
func (a *Adventure) themeRequest(g MapGraph) ThemeRequest {
	var chests []int
	for _, n := range g.Nodes {
		if n.HasChest {
			chests = append(chests, n.ID)
		}
	}
	return ThemeRequest{
		Seed:       themeSeeds[a.IntN(len(themeSeeds))],
		NodeCount:  len(g.Nodes),
		ChestNodes: chests,
		Edges:      g.Edges,
		Zones:      computeZones(g),
	}
}

// computeZones 从入口 BFS 求深度，按深度划带（按下标对应节点 ID）。
func computeZones(g MapGraph) []string {
	n := len(g.Nodes)
	depth := make([]int, n)
	for i := range depth {
		depth[i] = -1
	}
	depth[0] = 0
	queue := []int{0}
	maxDepth := 0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range g.OutNeighbors(cur) {
			if depth[next] < 0 {
				depth[next] = depth[cur] + 1
				if depth[next] > maxDepth {
					maxDepth = depth[next]
				}
				queue = append(queue, next)
			}
		}
	}
	zones := make([]string, n)
	for i := 0; i < n; i++ {
		d := depth[i]
		if d < 0 {
			d = 0 // 不可达节点（不应出现）按入口处理
		}
		switch {
		case d <= 1:
			zones[i] = ZoneCoast
		case d <= maxDepth/2:
			zones[i] = ZoneInland
		default:
			zones[i] = ZoneDeep
		}
	}
	return zones
}

// applyTheme 把主题回填到图（岛名、主题、各地点名称/描述/地带/要素）。
func applyTheme(g *MapGraph, t *IslandTheme) {
	g.IslandName = t.IslandName
	g.Theme = t.Theme
	for _, loc := range t.Locations {
		if loc.ID < 0 || loc.ID >= len(g.Nodes) {
			continue
		}
		n := &g.Nodes[loc.ID]
		n.Name = loc.Name
		n.Description = loc.Description
		n.Zone = loc.Zone
		n.Elements = loc.Elements
	}
}

// validateTheme 按 docs/08 §8 校验主题输出（不信任 LLM，降级词库同样过一遍）。
func validateTheme(req ThemeRequest, t *IslandTheme) error {
	if t == nil {
		return fmt.Errorf("theme is nil")
	}
	if l := utf8.RuneCountInString(t.IslandName); l < 2 || l > 8 {
		return fmt.Errorf("island_name 长度 %d 不在 2-8", l)
	}
	if l := utf8.RuneCountInString(t.Theme); l < 1 || l > 30 {
		return fmt.Errorf("theme 长度 %d 不在 1-30", l)
	}
	if len(t.Locations) != req.NodeCount {
		return fmt.Errorf("locations 数量 %d != %d", len(t.Locations), req.NodeCount)
	}
	seenID := make(map[int]bool, req.NodeCount)
	seenName := make(map[string]bool, req.NodeCount)
	for _, loc := range t.Locations {
		if loc.ID < 0 || loc.ID >= req.NodeCount || seenID[loc.ID] {
			return fmt.Errorf("location id %d 缺失或重复", loc.ID)
		}
		seenID[loc.ID] = true
		if l := utf8.RuneCountInString(loc.Name); l < 2 || l > 6 {
			return fmt.Errorf("地点名 %q 长度 %d 不在 2-6", loc.Name, l)
		}
		if seenName[loc.Name] {
			return fmt.Errorf("地点名 %q 重复", loc.Name)
		}
		seenName[loc.Name] = true
		if l := utf8.RuneCountInString(loc.Description); l < 15 || l > 40 {
			return fmt.Errorf("地点 %q 描述长度 %d 不在 15-40", loc.Name, l)
		}
		for _, r := range loc.Description {
			if r == '\n' || r == '\r' {
				return fmt.Errorf("地点 %q 描述含换行", loc.Name)
			}
		}
		if loc.Zone != req.Zones[loc.ID] {
			return fmt.Errorf("地点 %q 地带 %q != 下发值 %q", loc.Name, loc.Zone, req.Zones[loc.ID])
		}
		if err := validateElements(loc); err != nil {
			return err
		}
	}
	return nil
}

// validateElements 校验要素：1–3 项、∈ 词表、至少一个常规要素、危险仅深处带。
func validateElements(loc LocationTheme) error {
	if len(loc.Elements) < 1 || len(loc.Elements) > 3 {
		return fmt.Errorf("地点 %q 要素数 %d 不在 1-3", loc.Name, len(loc.Elements))
	}
	regular := false
	for _, e := range loc.Elements {
		switch {
		case slices.Contains(regularElements, e):
			regular = true
		case slices.Contains(optionalElements, e):
			if e == "危险" && loc.Zone != ZoneDeep {
				return fmt.Errorf("地点 %q 非深处带却含危险要素", loc.Name)
			}
		default:
			return fmt.Errorf("地点 %q 要素 %q 不在词表", loc.Name, e)
		}
	}
	if !regular {
		return fmt.Errorf("地点 %q 缺少常规要素", loc.Name)
	}
	return nil
}

// ---- 降级词库（FallbackThemer）----

// FallbackThemer 是内置词库主题生成器：LLM 未配置/失败/超时时的降级路径。
type FallbackThemer struct {
	// IntN 返回 [0,n) 整数；nil 用全局 rand（经 Adventure.IntN 注入保证单测确定性）。
	IntN func(n int) int
}

var fallbackIslandPrefixes = []string{"雾鸣", "碎星", "鲸落", "赤羽", "潮声", "月礁", "青鳞", "风眠"}
var fallbackIslandSuffixes = []string{"岛", "屿", "礁", "群岛", "沙洲", "渚"}

var fallbackNames = map[string][]string{
	ZoneCoast:  {"碎浪滩", "旧栈桥", "潮声湾", "白沙咀", "贝壳滩", "风帆礁", "红树滩", "浪花岬"},
	ZoneInland: {"藤语林", "迷雾泽", "溪流谷", "苔原径", "翠叶原", "乱石丘", "萤火林", "回声谷"},
	ZoneDeep:   {"星陨窟", "云顶巅", "古祭坛", "沉鲸渊", "黑岩窟", "风蚀台", "龙骨场", "禁地坛"},
}

type fallbackDesc struct {
	text     string
	elements []string
}

var fallbackDescs = map[string][]fallbackDesc{
	ZoneCoast: {
		{"海浪一遍遍刷过岸边细沙，空气里带着淡淡的咸味。", []string{"地貌", "环境"}},
		{"礁石上布满贝壳与藤壶，潮池里映着破碎的天光。", []string{"地貌", "风景"}},
	},
	ZoneInland: {
		{"林间雾气弥漫，脚下苔藓湿滑，虫鸣此起彼伏。", []string{"环境", "生物"}},
		{"溪谷水声清冽，两岸草木繁茂，据说曾是旅人歇脚的地方。", []string{"地貌", "渊源"}},
	},
	ZoneDeep: {
		{"高处风势凛冽，黑岩上寸草不生，传说是古船队的埋骨之地。", []string{"地貌", "渊源", "危险"}},
		{"洞穴深处阴冷潮湿，石壁上留着前人凿刻的记号。", []string{"环境", "渊源"}},
	},
}

// ThemeIsland 实现 Themer：用词库拼装主题，产出同样过 validateTheme。
func (f FallbackThemer) ThemeIsland(_ context.Context, req ThemeRequest) (*IslandTheme, error) {
	intN := f.IntN
	if intN == nil {
		intN = func(n int) int { return 0 }
	}
	t := &IslandTheme{
		IslandName: fallbackIslandPrefixes[intN(len(fallbackIslandPrefixes))] +
			fallbackIslandSuffixes[intN(len(fallbackIslandSuffixes))],
		Theme: fmt.Sprintf("一座%s，藏着不为人知的秘密。", req.Seed),
	}
	used := make(map[string]bool, req.NodeCount)
	for i := 0; i < req.NodeCount; i++ {
		zone := req.Zones[i]
		name := f.pickName(intN, zone, used)
		descs := fallbackDescs[zone]
		d := descs[intN(len(descs))]
		t.Locations = append(t.Locations, LocationTheme{
			ID: i, Name: name, Description: d.text, Zone: zone, Elements: d.elements,
		})
	}
	if err := validateTheme(req, t); err != nil {
		return nil, err
	}
	return t, nil
}

// pickName 从地带词库取不重名地点名；词库耗尽时回退为"地点N"。
func (f FallbackThemer) pickName(intN func(int) int, zone string, used map[string]bool) string {
	names := fallbackNames[zone]
	for range len(names) {
		name := names[intN(len(names))]
		if !used[name] {
			used[name] = true
			return name
		}
	}
	name := fmt.Sprintf("地点%d", len(used)+1)
	used[name] = true
	return name
}
