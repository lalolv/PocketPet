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
	adv.MapRefreshInterval = 24 * time.Hour // 测试默认不自动换图，需要时用短周期覆盖
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

	// 精力低于「困了」阈值：禁止出门（优先于单纯 cost 检查）。
	p.Stats.Energy = 25
	if err := env.st.SavePet(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	res, err := env.adv.start(context.Background(), p.ID)
	if err != nil || res.OK || !strings.Contains(res.Outcome, "太困") {
		t.Fatalf("sleepy = %+v, %v", res, err)
	}

	p.Stats.Energy = 100
	p.Activity = pet.ActivitySleeping
	p.SyncSleepingFromActivity()
	if err := env.st.SavePet(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	res, err = env.adv.start(context.Background(), p.ID)
	if err != nil || res.OK || !strings.Contains(res.Outcome, "睡觉") {
		t.Fatalf("sleeping = %+v, %v", res, err)
	}
}

func TestSleepWhileAdventuringQueuesThenSleepsOnFinish(t *testing.T) {
	env := setup(t)
	p := env.newPet(t)
	if _, err := env.adv.start(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if act, err := env.engine.Activity(context.Background(), p.ID); err != nil || act != pet.ActivityAdventuring {
		t.Fatalf("activity = %q, %v", act, err)
	}
	if _, err := env.engine.Care(context.Background(), p.ID, pet.ActionSleep); !errors.Is(err, pet.ErrBusy) {
		t.Fatalf("care sleep = %v, want ErrBusy", err)
	}
	got, _ := env.st.GetPet(context.Background(), p.ID)
	if !got.HasIntent(pet.IntentSleep) {
		t.Fatal("want IntentSleep queued")
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
	if act, err := env.engine.Activity(context.Background(), p.ID); err != nil || act != pet.ActivitySleeping {
		t.Fatalf("activity after finish = %q, %v want sleeping", act, err)
	}
}

// TestConcurrentStartDeductsEnergyOnce 回归：并发 adventure_start 只成功一次、精力只扣一档。
func TestConcurrentStartDeductsEnergyOnce(t *testing.T) {
	env := setup(t)
	p := env.newPet(t)
	before, err := env.st.GetPet(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantEnergy := before.Stats.Energy - env.adv.EnergyCost

	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	okN := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := env.adv.start(context.Background(), p.ID)
			if err != nil {
				t.Errorf("start err: %v", err)
				return
			}
			if res.OK {
				mu.Lock()
				okN++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if okN != 1 {
		t.Fatalf("ok starts = %d, want 1", okN)
	}
	got, err := env.st.GetPet(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stats.Energy != wantEnergy {
		t.Fatalf("energy = %v, want %v (single deduct)", got.Stats.Energy, wantEnergy)
	}
	if act := got.Activity; act != pet.ActivityAdventuring {
		t.Fatalf("activity = %q, want adventuring", act)
	}
	runs, err := env.adv.listRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
}

// TestSleepyStartInterleavingNoTornActivity 回归：sleepy 与 start 交错时不会 Sleeping∧Adventuring。
func TestSleepyStartInterleavingNoTornActivity(t *testing.T) {
	env := setup(t)
	p := env.newPet(t)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = env.engine.Care(context.Background(), p.ID, pet.ActionSleep)
	}()
	go func() {
		defer wg.Done()
		_, _ = env.adv.start(context.Background(), p.ID)
	}()
	wg.Wait()

	got, err := env.st.GetPet(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	got.SyncSleepingFromActivity()
	if got.Sleeping && got.Activity == pet.ActivityAdventuring {
		t.Fatalf("torn state: Sleeping && Adventuring: %+v", got)
	}
	switch got.Activity {
	case pet.ActivitySleeping:
		if !got.Sleeping {
			t.Fatal("activity sleeping but flag false")
		}
	case pet.ActivityAdventuring:
		if got.Sleeping {
			t.Fatal("adventuring with Sleeping true")
		}
	case pet.ActivityIdle, "":
		// 两者都失败极罕见；允许但不应撕裂
	default:
		t.Fatalf("unexpected activity %q", got.Activity)
	}
}

// TestStartWaitsStepMuWithoutHoldingPetLock 回归：start 不得在持 petID 锁时抢 stepMu，
// 否则与步进结束/换图路径（持 stepMu 抢 petID 锁）形成 AB-BA 死锁。
func TestStartWaitsStepMuWithoutHoldingPetLock(t *testing.T) {
	env := setup(t)
	p := env.newPet(t)

	// 模拟步进/换图持有 stepMu 期间，同宠 start 交错进来。
	env.adv.stepMu.Lock()
	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		if _, err := env.adv.start(context.Background(), p.ID); err != nil {
			t.Errorf("start err: %v", err)
		}
	}()

	// start 应先完成活动态提交（需要 petID 锁），再阻塞在 stepMu 上登记行程；
	// 此时其它需要 petID 锁的操作必须仍能进行（旧实现会在这里卡死）。
	applied := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !applied {
		actCh := make(chan string, 1)
		go func() {
			act, err := env.engine.Activity(context.Background(), p.ID)
			if err != nil {
				actCh <- "err"
				return
			}
			actCh <- act
		}()
		select {
		case act := <-actCh:
			if act == pet.ActivityAdventuring {
				applied = true
			} else {
				time.Sleep(20 * time.Millisecond)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("pet lock held while start waits on stepMu: AB-BA deadlock")
		}
	}
	if !applied {
		t.Fatal("start did not commit activity within deadline")
	}

	env.adv.stepMu.Unlock()
	select {
	case <-startDone:
	case <-time.After(3 * time.Second):
		t.Fatal("start did not finish after stepMu released")
	}
	if _, ok, _ := env.adv.getRun(p.ID); !ok {
		t.Fatal("run should exist after start")
	}
}

// TestStartInsertRunFailureRollsBack 回归：登记行程失败时补偿回 idle，不留卡死态。
func TestStartInsertRunFailureRollsBack(t *testing.T) {
	env := setup(t)
	p := env.newPet(t)
	// 只让 INSERT 失败（SELECT 不受影响），模拟登记行程出错。
	if _, err := env.st.DB().Exec(`CREATE TRIGGER block_run_insert BEFORE INSERT ON adventure_runs
		BEGIN SELECT RAISE(ABORT, 'blocked'); END;`); err != nil {
		t.Fatal(err)
	}
	res, err := env.adv.start(context.Background(), p.ID)
	if err == nil || res.OK {
		t.Fatalf("start = %+v, %v, want error", res, err)
	}
	got, err := env.st.GetPet(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Activity == pet.ActivityAdventuring {
		t.Fatal("activity should roll back to idle when run registration fails")
	}
}

func TestMapRefreshAbortsRun(t *testing.T) {
	env := setup(t)
	env.adv.MapRefreshInterval = time.Minute
	p := env.newPet(t)
	if _, err := env.adv.start(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	// Init 建图用的是真实墙钟；把建图时间回拨到 fake clock 之前，模拟地图已到期。
	if _, err := env.st.DB().Exec(`UPDATE adventure_maps SET created_at = ?`,
		t0.Add(-2*time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
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
	if mapBody["island_name"] == "" || mapBody["theme"] == "" {
		t.Fatalf("map missing island theme: %v", mapBody)
	}
	node0 := mapBody["nodes"].([]any)[0].(map[string]any)
	if node0["description"] == "" || node0["zone"] == "" {
		t.Fatalf("node missing theme fields: %v", node0)
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
	if runBody["island_name"] == "" || runBody["node_desc"] == "" || runBody["node_zone"] == "" {
		t.Fatalf("run missing theme fields: %v", runBody)
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
