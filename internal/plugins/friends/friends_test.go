package friends

import (
	"context"
	"encoding/json"
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
	"github.com/lalolv/PocketPet/internal/plugins/adventure"
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

func (s *fakeSink) last() pet.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events[len(s.events)-1]
}

type testEnv struct {
	st      *store.Store
	fs      *petfs.FS
	engine  *tick.Engine
	sink    *fakeSink
	clock   *pet.FakeClock
	fr      *Friends
	withAdv bool
}

// setup：withAdv 控制是否建 adventure 的背包表（验证无背包的优雅降级）。
func setup(t *testing.T, withAdv bool) *testEnv {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clock := pet.NewFakeClock(t0)
	sink := &fakeSink{}
	fs := petfs.New(t.TempDir())
	fr := New()
	engine := tick.NewEngine(st, sink, time.Minute, 24*time.Hour, clock)
	var plugins []plugin.Plugin
	if withAdv {
		adv := adventure.New()
		if err := st.RunPluginMigrations(adv.Name(), adv.Migrations()); err != nil {
			t.Fatal(err)
		}
		plugins = append(plugins, adv)
	}
	if err := st.RunPluginMigrations(fr.Name(), fr.Migrations()); err != nil {
		t.Fatal(err)
	}
	plugins = append(plugins, fr)
	reg := plugin.NewRegistry(plugins...)
	if err := fr.Init(plugin.NewPluginContext(engine, fs, st.DB(), slog.Default(), reg)); err != nil {
		t.Fatal(err)
	}
	return &testEnv{st: st, fs: fs, engine: engine, sink: sink, clock: clock, fr: fr, withAdv: withAdv}
}

// newPet 建两只互相认识的宠物（petfs 文件齐全，日记可写）。
func (env *testEnv) newPet(t *testing.T, name string) *pet.Pet {
	t.Helper()
	p, err := env.engine.CreatePet(context.Background(), name, "cat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.fs.CreatePet(p.ID, petfs.Identity{
		Name: p.Name, Species: p.Species, Stage: string(p.Stage), BornAt: p.BornAt,
	}); err != nil {
		t.Fatal(err)
	}
	return p
}

func (env *testEnv) affinity(t *testing.T, a, b string) float64 {
	t.Helper()
	var v float64
	if err := env.st.DB().QueryRow(
		`SELECT affinity FROM friendships WHERE pet_id = ? AND friend_id = ?`, a, b).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestVisit 验证正常访问：双方好感+5、心情+8、事件送达、对方日记可感知。
func TestVisit(t *testing.T) {
	env := setup(t, false)
	a := env.newPet(t, "雪球")
	b := env.newPet(t, "团子")

	res, err := env.fr.visit(context.Background(), a.ID, "团子")
	if err != nil || !res.OK {
		t.Fatalf("visit = %+v, %v", res, err)
	}
	if v := env.affinity(t, a.ID, b.ID); v != 5 {
		t.Fatalf("affinity a→b = %v, want 5", v)
	}
	if v := env.affinity(t, b.ID, a.ID); v != 5 {
		t.Fatalf("affinity b→a = %v, want 5", v)
	}
	pa, _ := env.st.GetPet(context.Background(), a.ID)
	pb, _ := env.st.GetPet(context.Background(), b.ID)
	if pa.Stats.Happy != 88 || pb.Stats.Happy != 88 {
		t.Fatalf("happy = %v/%v, want 88/88", pa.Stats.Happy, pb.Stats.Happy)
	}
	// 事件送达被访方
	ev := env.sink.last()
	if ev.Type != EventFriendVisited || ev.PetID != b.ID || !strings.Contains(ev.Message, "雪球") {
		t.Fatalf("event = %+v", ev)
	}
	// 对方日记有记录（聊天 recall 可达）
	journals, _ := env.fs.ListJournals(b.ID)
	if len(journals) != 1 {
		t.Fatalf("friend journals = %v", journals)
	}
	content, _ := env.fs.ReadJournal(b.ID, journals[0])
	if !strings.Contains(content, "雪球 来看望你了") {
		t.Fatalf("friend journal:\n%s", content)
	}
}

// TestVisitSleeping 验证对方睡觉时的降级：好感只 +2，不加心情。
func TestVisitSleeping(t *testing.T) {
	env := setup(t, false)
	a := env.newPet(t, "雪球")
	b := env.newPet(t, "团子")
	if _, err := env.engine.Care(context.Background(), b.ID, pet.ActionSleep); err != nil {
		t.Fatal(err)
	}

	res, err := env.fr.visit(context.Background(), a.ID, "团子")
	if err != nil || !res.OK {
		t.Fatalf("visit = %+v, %v", res, err)
	}
	if !strings.Contains(res.Outcome, "隔着门") {
		t.Fatalf("outcome = %q", res.Outcome)
	}
	if v := env.affinity(t, a.ID, b.ID); v != 2 {
		t.Fatalf("affinity = %v, want 2", v)
	}
	pb, _ := env.st.GetPet(context.Background(), b.ID)
	if pb.Stats.Happy != 80 {
		t.Fatalf("sleeper happy = %v, want unchanged 80", pb.Stats.Happy)
	}
}

// TestVisitNotFound 验证找不到对方与拜访自己。
func TestVisitNotFound(t *testing.T) {
	env := setup(t, false)
	a := env.newPet(t, "雪球")

	res, err := env.fr.visit(context.Background(), a.ID, "不存在")
	if err != nil || res.OK || !strings.Contains(res.Outcome, "没有叫") {
		t.Fatalf("visit unknown = %+v, %v", res, err)
	}
	res, err = env.fr.visit(context.Background(), a.ID, "雪球")
	if err != nil || res.OK || !strings.Contains(res.Outcome, "自己") {
		t.Fatalf("visit self = %+v, %v", res, err)
	}
}

// TestGiftWithoutAdventure 验证背包不存在（adventure 未注册）时的优雅业务错误。
func TestGiftWithoutAdventure(t *testing.T) {
	env := setup(t, false)
	a := env.newPet(t, "雪球")
	env.newPet(t, "团子")

	res, err := env.fr.gift(context.Background(), a.ID, "团子", "pinecone")
	if err != nil || res.OK {
		t.Fatalf("gift = %+v, %v", res, err)
	}
	if !strings.Contains(res.Outcome, "探险") {
		t.Fatalf("outcome = %q, want 提示先去探险", res.Outcome)
	}
}

// TestGift 验证送礼：背包扣减、对方入包、好感/心情、事件与日记。
func TestGift(t *testing.T) {
	env := setup(t, true)
	a := env.newPet(t, "雪球")
	b := env.newPet(t, "团子")
	if err := adventure.AddItem(env.st.DB(), a.ID, "pinecone", 1); err != nil {
		t.Fatal(err)
	}

	res, err := env.fr.gift(context.Background(), a.ID, "团子", "pinecone")
	if err != nil || !res.OK {
		t.Fatalf("gift = %+v, %v", res, err)
	}
	invA, _ := adventure.Inventory(env.st.DB(), a.ID)
	if len(invA) != 0 {
		t.Fatalf("sender inventory = %v, want empty", invA)
	}
	invB, _ := adventure.Inventory(env.st.DB(), b.ID)
	if invB["pinecone"] != 1 {
		t.Fatalf("receiver inventory = %v", invB)
	}
	if v := env.affinity(t, a.ID, b.ID); v != 3 {
		t.Fatalf("affinity = %v, want 3", v)
	}
	pb, _ := env.st.GetPet(context.Background(), b.ID)
	if pb.Stats.Happy != 85 {
		t.Fatalf("receiver happy = %v, want 85", pb.Stats.Happy)
	}
	ev := env.sink.last()
	if ev.Type != EventFriendGift || ev.PetID != b.ID {
		t.Fatalf("event = %+v", ev)
	}

	// 背包里没有该物品
	res, err = env.fr.gift(context.Background(), a.ID, "团子", "feather")
	if err != nil || res.OK || !strings.Contains(res.Outcome, "没有") {
		t.Fatalf("gift missing item = %+v, %v", res, err)
	}
}

// TestFriendsRoute 验证好感度列表路由。
func TestFriendsRoute(t *testing.T) {
	env := setup(t, false)
	a := env.newPet(t, "雪球")
	env.newPet(t, "团子")
	if _, err := env.fr.visit(context.Background(), a.ID, "团子"); err != nil {
		t.Fatal(err)
	}

	hub := api.NewHub()
	ag := agent.New(env.engine, env.fs, llm.Config{})
	srv := api.NewServer(env.st, env.engine, hub, env.fs, ag, nil)
	for _, pr := range []struct {
		name   string
		routes []api.Route
	}{{"friends", env.fr.Routes()}} {
		srv.RegisterPluginRoutes(pr.name, pr.routes)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/v1/plugins/friends/pets/" + a.ID + "/friends")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	list, _ := body["friends"].([]any)
	if len(list) != 1 {
		t.Fatalf("friends = %v", body)
	}
	first, _ := list[0].(map[string]any)
	if first["name"] != "团子" || first["affinity"] != 5.0 || first["interactions"] != 1.0 {
		t.Fatalf("friend entry = %v", first)
	}
}

// TestAdventureFinishedSharesWithFriends 验证 EventSubscriber：探险归来加深好友感情并写日记。
func TestAdventureFinishedSharesWithFriends(t *testing.T) {
	env := setup(t, true)
	a := env.newPet(t, "雪球")
	b := env.newPet(t, "团子")
	if _, err := env.fr.visit(context.Background(), a.ID, "团子"); err != nil {
		t.Fatal(err)
	}
	before := env.affinity(t, a.ID, b.ID)

	env.fr.OnEvent(context.Background(), pet.Event{
		PetID: a.ID, Type: adventure.EventFinished, Message: "探险归来", CreatedAt: t0,
	})

	if v := env.affinity(t, a.ID, b.ID); v != before+1 {
		t.Fatalf("affinity after adventure = %v, want %v", v, before+1)
	}
	journals, _ := env.fs.ListJournals(b.ID)
	found := false
	for _, j := range journals {
		content, _ := env.fs.ReadJournal(b.ID, j)
		if strings.Contains(content, "雪球 探险回来了") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("friend journal missing adventure share note")
	}
}
