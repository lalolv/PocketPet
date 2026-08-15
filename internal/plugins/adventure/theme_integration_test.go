package adventure

import (
	"context"
	"testing"
)

// TestRefreshMapAsyncTheme 验证两阶段换图：refreshMap 同步段落降级主题，
// 异步 goroutine 用 LLM 主题就地更新岛名与地点名（setup 的图是确定性的：
// NodeCount=6/MaxBranches=3/IntN=0 → 地带为 [海岸×4, 深处×2]）。
func TestRefreshMapAsyncTheme(t *testing.T) {
	env := setup(t)
	m := &fakeThemeModel{replies: []string{`{"island_name":"雾鸣岛","theme":"被浓雾封锁的孤岛。","locations":[
{"id":0,"name":"碎浪滩","description":"登陆点的黑色沙滩上铺满被潮水磨圆的火山石，风带咸味。","zone":"海岸带","elements":["地貌","环境"]},
{"id":1,"name":"潮声湾","description":"礁石上布满贝壳与藤壶，潮池里映着破碎的天光。","zone":"海岸带","elements":["地貌","风景"]},
{"id":2,"name":"红树滩","description":"红树林根系交错成片，据说是候鸟迁徙的中途驿站。","zone":"海岸带","elements":["渊源","生物"]},
{"id":3,"name":"白沙咀","description":"白色沙咀伸入海中，浪花在两侧碎成细小的水珠。","zone":"海岸带","elements":["地貌","风景"]},
{"id":4,"name":"星陨窟","description":"洞穴深处阴冷潮湿，石壁上留着前人凿刻的记号。","zone":"深处带","elements":["环境","渊源"]},
{"id":5,"name":"沉鲸渊","description":"高处风势凛冽，黑岩寸草不生，传说是古船队的埋骨之地。","zone":"深处带","elements":["地貌","渊源","危险"]}
]}`}}
	env.adv.Themer = newLLMThemer(m)

	sm, err := env.adv.refreshMap(context.Background(), t0)
	if err != nil {
		t.Fatal(err)
	}
	env.adv.themeWg.Wait()

	loaded, err := env.adv.loadMap(context.Background(), sm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Graph.IslandName != "雾鸣岛" {
		t.Fatalf("island = %q, want 雾鸣岛", loaded.Graph.IslandName)
	}
	if loaded.Graph.Nodes[5].Name != "沉鲸渊" || loaded.Graph.Nodes[5].Description == "" {
		t.Fatalf("node5 = %+v", loaded.Graph.Nodes[5])
	}
	if len(loaded.Graph.Nodes[5].Elements) != 3 {
		t.Fatalf("node5 elements = %v", loaded.Graph.Nodes[5].Elements)
	}
}

// TestRefreshMapThemerFailureKeepsFallback 验证 LLM 主题失败时地图保持降级主题可用。
func TestRefreshMapThemerFailureKeepsFallback(t *testing.T) {
	env := setup(t)
	env.adv.Themer = newLLMThemer(&fakeThemeModel{replies: []string{"garbage"}})

	sm, err := env.adv.refreshMap(context.Background(), t0)
	if err != nil {
		t.Fatal(err)
	}
	env.adv.themeWg.Wait()

	loaded, err := env.adv.loadMap(context.Background(), sm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Graph.IslandName == "" || loaded.Graph.Nodes[0].Name == "" {
		t.Fatal("fallback theme should still be applied")
	}
}
