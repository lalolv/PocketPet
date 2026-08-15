package adventure

import (
	"context"
	"iter"
	"strings"
	"testing"

	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/lalolv/PocketPet/internal/llm"
)

// testGraph 构造一张固定的三层小图：0(入口) → 1,2 → 3。
func testGraph() MapGraph {
	return MapGraph{
		Nodes: []MapNode{{ID: 0}, {ID: 1}, {ID: 2}, {ID: 3, HasChest: true}},
		Edges: []MapEdge{{From: 0, To: 1}, {From: 0, To: 2}, {From: 1, To: 3}, {From: 2, To: 3}},
	}
}

func testRequest() ThemeRequest {
	g := testGraph()
	return ThemeRequest{
		Seed:       "火山孤岛",
		NodeCount:  len(g.Nodes),
		ChestNodes: []int{3},
		Edges:      g.Edges,
		Zones:      computeZones(g),
	}
}

func TestComputeZones(t *testing.T) {
	zones := computeZones(testGraph())
	want := []string{ZoneCoast, ZoneCoast, ZoneCoast, ZoneDeep} // maxDepth=2，depth 2 > 2/2
	if len(zones) != len(want) {
		t.Fatalf("zones = %v", zones)
	}
	for i := range want {
		if zones[i] != want[i] {
			t.Fatalf("zones[%d] = %s, want %s (%v)", i, zones[i], want[i], zones)
		}
	}
}

func TestFallbackThemer(t *testing.T) {
	req := testRequest()
	th, err := (FallbackThemer{IntN: func(n int) int { return 0 }}).ThemeIsland(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTheme(req, th); err != nil {
		t.Fatalf("fallback theme invalid: %v", err)
	}
	if th.Locations[0].Zone != ZoneCoast || th.Locations[3].Zone != ZoneDeep {
		t.Fatalf("zones = %+v", th.Locations)
	}
}

func TestFallbackThemerManyNodes(t *testing.T) {
	// 节点数超过单地带词库容量时回退"地点N"，仍需通过校验。
	g := GenerateMap(GenConfig{NodeCount: 12, MaxBranches: 2, IntN: func(n int) int { return 0 }, Float64: func() float64 { return 0.9 }})
	req := ThemeRequest{
		Seed: "迷雾海岛", NodeCount: len(g.Nodes), Edges: g.Edges, Zones: computeZones(g),
	}
	th, err := (FallbackThemer{IntN: func(n int) int { return n - 1 }}).ThemeIsland(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTheme(req, th); err != nil {
		t.Fatalf("fallback theme invalid: %v", err)
	}
}

func TestValidateTheme(t *testing.T) {
	valid := func() *IslandTheme {
		return &IslandTheme{
			IslandName: "雾鸣岛",
			Theme:      "被浓雾封锁的孤岛。",
			Locations: []LocationTheme{
				{ID: 0, Name: "碎浪滩", Description: "登陆点的黑色沙滩上铺满被潮水磨圆的火山石，风带咸味。", Zone: ZoneCoast, Elements: []string{"地貌", "环境"}},
				{ID: 1, Name: "潮声湾", Description: "礁石上布满贝壳与藤壶，潮池里映着破碎的天光。", Zone: ZoneCoast, Elements: []string{"地貌", "风景"}},
				{ID: 2, Name: "红树滩", Description: "红树林根系交错成片，据说是候鸟迁徙的中途驿站。", Zone: ZoneCoast, Elements: []string{"渊源", "生物"}},
				{ID: 3, Name: "沉鲸渊", Description: "高处风势凛冽，黑岩寸草不生，传说是古船队的埋骨之地。", Zone: ZoneDeep, Elements: []string{"地貌", "渊源", "危险"}},
			},
		}
	}
	req := testRequest()

	t.Run("合法", func(t *testing.T) {
		if err := validateTheme(req, valid()); err != nil {
			t.Fatal(err)
		}
	})

	cases := map[string]func(*IslandTheme){
		"岛名超长": func(th *IslandTheme) { th.IslandName = "这是一个名字实在太长的岛屿" },
		"地点缺失": func(th *IslandTheme) { th.Locations = th.Locations[:3] },
		"id重复":   func(th *IslandTheme) { th.Locations[3].ID = 2 },
		"地点名重复": func(th *IslandTheme) { th.Locations[1].Name = "碎浪滩" },
		"描述过短":   func(th *IslandTheme) { th.Locations[0].Description = "太短了。" },
		"描述换行": func(th *IslandTheme) {
			th.Locations[0].Description = "登陆点的黑色沙滩上铺满火山石，\n风里带着咸味。"
		},
		"地带不符":   func(th *IslandTheme) { th.Locations[3].Zone = ZoneCoast },
		"要素超量":   func(th *IslandTheme) { th.Locations[0].Elements = []string{"地貌", "风景", "渊源", "环境"} },
		"要素非法":   func(th *IslandTheme) { th.Locations[0].Elements = []string{"魔法"} },
		"缺常规要素": func(th *IslandTheme) { th.Locations[0].Elements = []string{"生物"} },
		"危险在海岸": func(th *IslandTheme) { th.Locations[0].Elements = []string{"地貌", "危险"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			th := valid()
			mutate(th)
			if err := validateTheme(req, th); err == nil {
				t.Fatal("want validation error")
			}
		})
	}
}

// fakeThemeModel 按脚本依次返回固定文本的 model.LLM。
type fakeThemeModel struct {
	replies []string
	calls   int
}

func (f *fakeThemeModel) Name() string { return "fake" }
func (f *fakeThemeModel) GenerateContent(_ context.Context, _ *adkmodel.LLMRequest, _ bool) iter.Seq2[*adkmodel.LLMResponse, error] {
	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		reply := f.replies[min(f.calls, len(f.replies)-1)]
		f.calls++
		yield(&adkmodel.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: reply}}},
		}, nil)
	}
}

const validThemeJSON = `{"island_name":"雾鸣岛","theme":"被浓雾封锁的孤岛。","locations":[
{"id":0,"name":"碎浪滩","description":"登陆点的黑色沙滩上铺满被潮水磨圆的火山石，风带咸味。","zone":"海岸带","elements":["地貌","环境"]},
{"id":1,"name":"潮声湾","description":"礁石上布满贝壳与藤壶，潮池里映着破碎的天光。","zone":"海岸带","elements":["地貌","风景"]},
{"id":2,"name":"红树滩","description":"红树林根系交错成片，据说是候鸟迁徙的中途驿站。","zone":"海岸带","elements":["渊源","生物"]},
{"id":3,"name":"沉箱窟","description":"洞穴深处阴冷潮湿，石壁上留着前人藏宝的凿刻记号。","zone":"深处带","elements":["环境","渊源"]}
]}`

func newLLMThemer(m *fakeThemeModel) LLMThemer {
	return LLMThemer{
		Cfg: llm.Config{Model: "fake", APIKey: "k"},
		ModelFactory: func(context.Context, llm.Config) (adkmodel.LLM, error) {
			return m, nil
		},
	}
}

func TestLLMThemer(t *testing.T) {
	req := testRequest()

	t.Run("一次成功", func(t *testing.T) {
		m := &fakeThemeModel{replies: []string{"前言\n```json\n" + validThemeJSON + "\n```\n后记"}}
		th, err := newLLMThemer(m).ThemeIsland(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if th.IslandName != "雾鸣岛" || th.Locations[3].Name != "沉箱窟" {
			t.Fatalf("theme = %+v", th)
		}
		if m.calls != 1 {
			t.Fatalf("calls = %d, want 1", m.calls)
		}
	})

	t.Run("校验失败重试一次", func(t *testing.T) {
		// 把深处带节点的 zone 改成海岸带，触发地带校验失败 → 应重试一次后成功。
		bad := strings.Replace(validThemeJSON, `"zone":"深处带"`, `"zone":"海岸带"`, 1)
		m := &fakeThemeModel{replies: []string{bad, validThemeJSON}}
		if _, err := newLLMThemer(m).ThemeIsland(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		if m.calls != 2 {
			t.Fatalf("calls = %d, want 2", m.calls)
		}
	})

	t.Run("两次都失败则报错", func(t *testing.T) {
		m := &fakeThemeModel{replies: []string{"not json at all", "{}"}}
		if _, err := newLLMThemer(m).ThemeIsland(context.Background(), req); err == nil {
			t.Fatal("want error")
		}
		if m.calls != 2 {
			t.Fatalf("calls = %d, want 2", m.calls)
		}
	})
}
