package adventure

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
	adv.MapRefreshTicks = 100
	adv.NodeCount = 6
	adv.MaxBranches = 3
	adv.StepInterval = 0 // 单测手动 advanceAllRuns，不启墙钟步进
	adv.IntN = func(n int) int {
		if n <= 0 {
			return 0
		}
		return 0
	}
	adv.Float64 = func() float64 { return 0.9 }
	engine := tick.NewEngine(st, sink, time.Minute, 24*time.Hour, clock)
	if err := st.RunPluginMigrations(adv.Name(), adv.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := adv.Init(plugin.NewPluginContext(engine, fs, st.DB(), slog.Default(), nil)); err != nil {
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

func TestInitCreatesMap(t *testing.T) {
	env := setup(t)
	sm, err := env.adv.currentMap(context.Background())
	if err != nil || len(sm.Graph.Nodes) != 6 {
		t.Fatalf("map = %+v, %v", sm, err)
	}
}

func TestStartAndWalkToFinish(t *testing.T) {
	env := setup(t)
	p := env.newPet(t)

	res, err := env.adv.start(context.Background(), p.ID)
	if err != nil || !res.OK {
		t.Fatalf("start = %+v, %v", res, err)
	}
	got, _ := env.st.GetPet(context.Background(), p.ID)
	if got.Stats.Energy != 85 {
		t.Fatalf("energy = %v, want 85", got.Stats.Energy)
	}
	res, err = env.adv.start(context.Background(), p.ID)
	if err != nil || res.OK {
		t.Fatalf("re-start = %+v, %v", res, err)
	}

	finished := false
	for i := 0; i < 20; i++ {
		env.adv.advanceAllRuns(context.Background(), env.clock.Now())
		if _, ok, _ := env.adv.getRun(p.ID); !ok {
			finished = true
			break
		}
	}
	if !finished {
		t.Fatal("run did not finish")
	}
	types := env.sink.types()
	foundFinish := false
	for _, typ := range types {
		if typ == EventFinished {
			foundFinish = true
		}
	}
	if !foundFinish {
		t.Fatalf("events = %v, want finished", types)
	}
}

func TestStartRejected(t *testing.T) {
	env := setup(t)
	p := env.newPet(t)

	p.Stats.Energy = 10
	if err := env.st.SavePet(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	res, err := env.adv.start(context.Background(), p.ID)
	if err != nil || res.OK || !strings.Contains(res.Outcome, "太累") {
		t.Fatalf("low energy = %+v, %v", res, err)
	}

	p.Stats.Energy = 100
	p.Sleeping = true
	if err := env.st.SavePet(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	res, err = env.adv.start(context.Background(), p.ID)
	if err != nil || res.OK || !strings.Contains(res.Outcome, "睡觉") {
		t.Fatalf("sleeping = %+v, %v", res, err)
	}
}

func TestMapRefreshAbortsRun(t *testing.T) {
	env := setup(t)
	env.adv.MapRefreshTicks = 2
	p := env.newPet(t)
	if _, err := env.adv.start(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	env.clock.Advance(time.Minute)
	env.engine.TickAll(context.Background())
	env.clock.Advance(time.Minute)
	env.engine.TickAll(context.Background())

	if _, ok, _ := env.adv.getRun(p.ID); ok {
		t.Fatal("run should be aborted on map refresh")
	}
	found := false
	for _, typ := range env.sink.types() {
		if typ == EventAborted {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %v, want aborted", env.sink.types())
	}
}

func TestRoutes(t *testing.T) {
	env := setup(t)
	p := env.newPet(t)
	if _, err := env.adv.start(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}

	hub := api.NewHub()
	ag := agent.New(env.engine, env.fs, llm.Config{})
	srv := api.NewServer(env.st, env.engine, hub, env.fs, ag, nil)
	srv.RegisterPluginRoutes(env.adv.Name(), env.adv.Routes())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/v1/plugins/adventure/maps/current")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var mapBody map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&mapBody); err != nil {
		t.Fatal(err)
	}
	if mapBody["node_count"].(float64) != 6 {
		t.Fatalf("map = %v", mapBody)
	}

	resp2, err := http.Get(ts.URL + "/v1/plugins/adventure/pets/" + p.ID + "/run")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var runBody map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&runBody); err != nil {
		t.Fatal(err)
	}
	if runBody["adventuring"] != true {
		t.Fatalf("run = %v", runBody)
	}

	// POST start 在已探险时拒绝
	resp3, err := http.Post(ts.URL+"/v1/plugins/adventure/pets/"+p.ID+"/start", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusConflict {
		t.Fatalf("re-start status = %d", resp3.StatusCode)
	}
}

func TestStartHTTP(t *testing.T) {
	env := setup(t)
	p := env.newPet(t)
	hub := api.NewHub()
	ag := agent.New(env.engine, env.fs, llm.Config{})
	srv := api.NewServer(env.st, env.engine, hub, env.fs, ag, nil)
	srv.RegisterPluginRoutes(env.adv.Name(), env.adv.Routes())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/v1/plugins/adventure/pets/"+p.ID+"/start", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["adventuring"] != true {
		t.Fatalf("body = %v", body)
	}
}
