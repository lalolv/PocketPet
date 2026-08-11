package adventure

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lalolv/PocketPet/internal/agent"
	"github.com/lalolv/PocketPet/internal/api"
	"github.com/lalolv/PocketPet/internal/llm"
	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
	"github.com/lalolv/PocketPet/internal/plugin"
	"github.com/lalolv/PocketPet/internal/store"
	"github.com/lalolv/PocketPet/internal/tick"
)

var t0 = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

type fakeSink struct {
	mu     sync.Mutex
	events []pet.Event
}

func (s *fakeSink) Publish(e pet.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *fakeSink) types() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.events {
		out = append(out, e.Type)
	}
	return out
}

type testEnv struct {
	st     *store.Store
	fs     *petfs.FS
	engine *tick.Engine
	sink   *fakeSink
	clock  *pet.FakeClock
	adv    *Adventure
}

func setup(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clock := pet.NewFakeClock(t0)
	sink := &fakeSink{}
	fs := petfs.New(t.TempDir())
	adv := New()
	engine := tick.NewEngine(st, sink, time.Minute, 24*time.Hour, clock)
	if err := st.RunPluginMigrations(adv.Name(), adv.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := adv.Init(plugin.NewPluginContext(engine, fs, st.DB(), slog.Default())); err != nil {
		t.Fatal(err)
	}
	engine.AddTickHook(adv)
	return &testEnv{st: st, fs: fs, engine: engine, sink: sink, clock: clock, adv: adv}
}

func (env *testEnv) newPet(t *testing.T) *pet.Pet {
	t.Helper()
	p, err := env.engine.CreatePet(context.Background(), "团团", "cat")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestStartAndSettle 验证完整链路：开始 → 3 个 tick 结算 → 掉落入背包 + EXP + 事件。
func TestStartAndSettle(t *testing.T) {
	env := setup(t)
	p := env.newPet(t)
	// 确定性随机：掉落 roll=0.1（松果），受伤 roll=0.9（不受伤）
	rolls := []float64{0.1, 0.9}
	env.adv.Roll = func() float64 { r := rolls[0]; rolls = rolls[1:]; return r }

	res, err := env.adv.start(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("start = %+v", res)
	}
	// 精力扣 15
	got, _ := env.st.GetPet(context.Background(), p.ID)
	if got.Stats.Energy != 85 {
		t.Fatalf("energy = %v, want 85", got.Stats.Energy)
	}
	// 已在探险中：重复开始被拒
	res, err = env.adv.start(context.Background(), p.ID)
	if err != nil || res.OK {
		t.Fatalf("re-start = %+v, %v", res, err)
	}

	// 推进 3 个 tick：前两次不结算，第三次结算
	for i := 0; i < 2; i++ {
		env.clock.Advance(time.Minute)
		env.engine.TickAll(context.Background())
	}
	for _, typ := range env.sink.types() {
		if typ == EventFinished {
			t.Fatal("finished too early")
		}
	}
	env.clock.Advance(time.Minute)
	env.engine.TickAll(context.Background())

	// 结算：EXP +10、Happy +5（80+5=85，注意 tick 衰减 2 分钟 ≈ 0.1）
	got, _ = env.st.GetPet(context.Background(), p.ID)
	if got.Stats.EXP != 10 {
		t.Fatalf("exp = %v, want 10", got.Stats.EXP)
	}
	if got.Stats.Happy < 84 || got.Stats.Happy > 85 {
		t.Fatalf("happy = %v, want ~85", got.Stats.Happy)
	}
	if got.Stats.Health != 100 {
		t.Fatalf("health = %v, want 100 (no injury)", got.Stats.Health)
	}
	items, err := Inventory(env.st.DB(), p.ID)
	if err != nil || items["pinecone"] != 1 {
		t.Fatalf("inventory = %v, %v", items, err)
	}
	// 事件序列：born → started → finished（含掉落名）
	types := env.sink.types()
	if types[len(types)-1] != EventFinished {
		t.Fatalf("events = %v", types)
	}
	var last pet.Event
	for _, e := range env.sink.events {
		last = e
	}
	if !strings.Contains(last.Message, "松果") {
		t.Fatalf("finish message = %q", last.Message)
	}
	// 状态已清理
	if _, ok, _ := env.adv.activeTicks(p.ID); ok {
		t.Fatal("active row not cleared")
	}
}

// TestStartRejected 验证开始探险的前置拒绝：睡觉中 / 精力不足。
func TestStartRejected(t *testing.T) {
	env := setup(t)
	p := env.newPet(t)

	// 精力不足（直接改库存档）
	p.Stats.Energy = 10
	if err := env.st.SavePet(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	res, err := env.adv.start(context.Background(), p.ID)
	if err != nil || res.OK || !strings.Contains(res.Outcome, "太累") {
		t.Fatalf("low energy start = %+v, %v", res, err)
	}

	// 睡觉中
	p.Stats.Energy = 100
	p.Sleeping = true
	if err := env.st.SavePet(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	res, err = env.adv.start(context.Background(), p.ID)
	if err != nil || res.OK || !strings.Contains(res.Outcome, "睡觉") {
		t.Fatalf("sleeping start = %+v, %v", res, err)
	}
	// 无 started 事件
	for _, typ := range env.sink.types() {
		if typ == EventStarted {
			t.Fatal("no start event expected")
		}
	}
}

// TestInventoryRoute 验证背包路由（含 /v1/plugins/adventure 前缀挂载）。
func TestInventoryRoute(t *testing.T) {
	env := setup(t)
	p := env.newPet(t)
	if err := AddItem(env.st.DB(), p.ID, "feather", 2); err != nil {
		t.Fatal(err)
	}

	hub := api.NewHub()
	ag := agent.New(env.engine, env.fs, llm.Config{})
	srv := api.NewServer(env.st, env.engine, hub, env.fs, ag)
	srv.RegisterPluginRoutes(env.adv.Name(), env.adv.Routes())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/v1/plugins/adventure/pets/" + p.ID + "/inventory")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", body)
	}
	first, _ := items[0].(map[string]any)
	if first["item"] != "feather" || first["count"] != 2.0 || first["label"] != "羽毛" {
		t.Fatalf("item = %v", first)
	}
	if body["adventuring"] != false {
		t.Fatalf("adventuring = %v", body["adventuring"])
	}

	// 未知宠物 404
	resp2, err := http.Get(ts.URL + "/v1/plugins/adventure/pets/nope/inventory")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 404 {
		t.Fatalf("unknown pet status = %d", resp2.StatusCode)
	}
}

// TestInventoryNoTable 验证背包表不存在时的优雅错误（adventure 未注册的场景）。
func TestInventoryNoTable(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := Inventory(st.DB(), "p1"); !errors.Is(err, ErrNoInventory) {
		t.Fatalf("Inventory = %v, want ErrNoInventory", err)
	}
	if err := TakeItem(st.DB(), "p1", "pinecone"); !errors.Is(err, ErrNoInventory) {
		t.Fatalf("TakeItem = %v, want ErrNoInventory", err)
	}
}
