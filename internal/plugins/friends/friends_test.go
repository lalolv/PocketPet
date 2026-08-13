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
	st     *store.Store
	fs     *petfs.FS
	engine *tick.Engine
	sink   *fakeSink
	clock  *pet.FakeClock
	fr     *Friends
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
	fr := New()
	engine := tick.NewEngine(st, sink, time.Minute, 24*time.Hour, clock)
	if err := st.RunPluginMigrations(fr.Name(), fr.Migrations()); err != nil {
		t.Fatal(err)
	}
	reg := plugin.NewRegistry(fr)
	if err := fr.Init(plugin.NewPluginContext(engine, fs, st.DB(), slog.Default(), reg)); err != nil {
		t.Fatal(err)
	}
	return &testEnv{st: st, fs: fs, engine: engine, sink: sink, clock: clock, fr: fr}
}

func (env *testEnv) newPet(t *testing.T, name string) *pet.Pet {
	t.Helper()
	p, err := env.engine.CreatePet(context.Background(), name, "cat")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (env *testEnv) affinity(t *testing.T, a, b string) float64 {
	t.Helper()
	var v float64
	err := env.st.DB().QueryRow(
		`SELECT affinity FROM friendships WHERE pet_id = ? AND friend_id = ?`, a, b).Scan(&v)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVisit(t *testing.T) {
	env := setup(t)
	a := env.newPet(t, "雪球")
	b := env.newPet(t, "团子")

	res, err := env.fr.visit(context.Background(), a.ID, "团子")
	if err != nil || !res.OK {
		t.Fatalf("visit = %+v, %v", res, err)
	}
	if v := env.affinity(t, a.ID, b.ID); v != 5 {
		t.Fatalf("affinity = %v, want 5", v)
	}
	pa, _ := env.st.GetPet(context.Background(), a.ID)
	pb, _ := env.st.GetPet(context.Background(), b.ID)
	if pa.Stats.Happy != 88 || pb.Stats.Happy != 88 {
		t.Fatalf("happy a=%v b=%v, want 88", pa.Stats.Happy, pb.Stats.Happy)
	}
	ev := env.sink.last()
	if ev.Type != EventFriendVisited || ev.PetID != b.ID {
		t.Fatalf("event = %+v", ev)
	}
}

func TestVisitSleeping(t *testing.T) {
	env := setup(t)
	a := env.newPet(t, "雪球")
	b := env.newPet(t, "团子")
	b.Activity = pet.ActivitySleeping
	b.SyncSleepingFromActivity()
	if err := env.st.SavePet(context.Background(), b); err != nil {
		t.Fatal(err)
	}

	res, err := env.fr.visit(context.Background(), a.ID, "团子")
	if err != nil || !res.OK || !strings.Contains(res.Outcome, "睡觉") {
		t.Fatalf("visit sleeping = %+v, %v", res, err)
	}
	if v := env.affinity(t, a.ID, b.ID); v != 2 {
		t.Fatalf("affinity = %v, want 2", v)
	}
}

func TestVisitSelfSleeping(t *testing.T) {
	env := setup(t)
	a := env.newPet(t, "雪球")
	_ = env.newPet(t, "团子")
	a.Activity = pet.ActivitySleeping
	a.SyncSleepingFromActivity()
	if err := env.st.SavePet(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	res, err := env.fr.visit(context.Background(), a.ID, "团子")
	if err != nil || res.OK || !strings.Contains(res.Outcome, "睡觉") {
		t.Fatalf("visit while self sleeping = %+v, %v", res, err)
	}
}

func TestVisitSelfAdventuring(t *testing.T) {
	env := setup(t)
	a := env.newPet(t, "雪球")
	_ = env.newPet(t, "团子")
	a.Activity = pet.ActivityAdventuring
	a.ActivityOwner = "adventure"
	a.SyncSleepingFromActivity()
	if err := env.st.SavePet(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	res, err := env.fr.visit(context.Background(), a.ID, "团子")
	if err != nil || res.OK || !strings.Contains(res.Outcome, "出不了门") {
		t.Fatalf("visit while self adventuring = %+v, %v", res, err)
	}
}

func TestVisitRejected(t *testing.T) {
	env := setup(t)
	a := env.newPet(t, "雪球")

	res, err := env.fr.visit(context.Background(), a.ID, "不存在")
	if err != nil || res.OK {
		t.Fatalf("visit unknown = %+v, %v", res, err)
	}
	res, err = env.fr.visit(context.Background(), a.ID, "雪球")
	if err != nil || res.OK || !strings.Contains(res.Outcome, "自己") {
		t.Fatalf("visit self = %+v, %v", res, err)
	}
}

func TestGift(t *testing.T) {
	env := setup(t)
	a := env.newPet(t, "雪球")
	b := env.newPet(t, "团子")

	res, err := env.fr.gift(context.Background(), a.ID, "团子", "小花")
	if err != nil || !res.OK {
		t.Fatalf("gift = %+v, %v", res, err)
	}
	if v := env.affinity(t, a.ID, b.ID); v != 3 {
		t.Fatalf("affinity = %v, want 3", v)
	}
	pb, _ := env.st.GetPet(context.Background(), b.ID)
	if pb.Stats.Happy != 85 {
		t.Fatalf("receiver happy = %v, want 85", pb.Stats.Happy)
	}
	ev := env.sink.last()
	if ev.Type != EventFriendGift || ev.PetID != b.ID || !strings.Contains(ev.Message, "小花") {
		t.Fatalf("event = %+v", ev)
	}

	res, err = env.fr.gift(context.Background(), a.ID, "团子", "  ")
	if err != nil || res.OK || !strings.Contains(res.Outcome, "想好") {
		t.Fatalf("gift empty = %+v, %v", res, err)
	}
}

func TestFriendsRoute(t *testing.T) {
	env := setup(t)
	a := env.newPet(t, "雪球")
	env.newPet(t, "团子")
	if _, err := env.fr.visit(context.Background(), a.ID, "团子"); err != nil {
		t.Fatal(err)
	}

	hub := api.NewHub()
	ag := agent.New(env.engine, env.fs, llm.Config{})
	srv := api.NewServer(env.st, env.engine, hub, env.fs, ag, nil)
	srv.RegisterPluginRoutes("friends", env.fr.Routes())
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
