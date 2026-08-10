package proactive

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lalolv/PocketPet/internal/llm"
	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
	"github.com/lalolv/PocketPet/internal/store"
)

// fakeMessager 记录 Compose 请求并返回固定文案。
type fakeMessager struct {
	mu   sync.Mutex
	reqs []ComposeRequest
	text string
	err  error
}

func (f *fakeMessager) Compose(_ context.Context, req ComposeRequest) (string, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	return f.text, f.err
}

func (f *fakeMessager) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

// careCall 记录一次自动照顾动作。
type careCall struct {
	id     string
	action pet.Action
}

// fakeEngine 实现 CareEngine：记录 Care 调用，ListPets 返回固定列表。
type fakeEngine struct {
	mu        sync.Mutex
	pets      []*pet.Pet
	cares     []careCall
	listCalls int
}

func (f *fakeEngine) Care(_ context.Context, id string, action pet.Action) (*pet.Pet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cares = append(f.cares, careCall{id, action})
	return nil, nil
}

func (f *fakeEngine) ListPets(context.Context) ([]*pet.Pet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	return f.pets, nil
}

func (f *fakeEngine) careList() []careCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]careCall(nil), f.cares...)
}

// testEnv 是一次测试的装配：内存 store + 临时 petfs + 一只宠物 + 事件收集器。
type testEnv struct {
	st      *store.Store
	fs      *petfs.FS
	pet     *pet.Pet
	monitor *Monitor
	msgr    *fakeMessager
	engine  *fakeEngine

	mu     sync.Mutex
	events []pet.Event
}

var allOn = Options{Enabled: true, AutoSleep: true, AutoWake: true, Messages: true}

func setup(t *testing.T, opts Options) *testEnv {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fs := petfs.New(t.TempDir())

	p := pet.New("pet1", "团子", "猫", time.Now())
	if err := st.SavePet(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	env := &testEnv{st: st, fs: fs, pet: p, msgr: &fakeMessager{text: "主人，我饿啦！"}, engine: &fakeEngine{}}
	m := NewMonitor(st, fs, llm.Config{}, opts)
	m.Messager = env.msgr
	m.Engine = env.engine
	m.Emitter = func(_ context.Context, evs ...pet.Event) {
		env.mu.Lock()
		env.events = append(env.events, evs...)
		env.mu.Unlock()
	}
	env.monitor = m
	return env
}

func (e *testEnv) emitted() []pet.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]pet.Event(nil), e.events...)
}

// TestHungryEmitsProactiveMessage 验证饥饿事件触发 LLM 主动消息（pet.proactive）。
func TestHungryEmitsProactiveMessage(t *testing.T) {
	env := setup(t, allOn)
	env.monitor.handle(context.Background(), pet.Event{PetID: env.pet.ID, Type: pet.EventHungry})

	evs := env.emitted()
	if len(evs) != 1 || evs[0].Type != pet.EventProactive || evs[0].Message != "主人，我饿啦！" {
		t.Fatalf("events = %+v", evs)
	}
	if evs[0].PetID != env.pet.ID {
		t.Fatalf("pet id = %q", evs[0].PetID)
	}
	req := env.msgr.reqs[0]
	if req.Trigger != pet.EventHungry || req.Name != "团子" || req.Species != "猫" {
		t.Fatalf("compose req = %+v", req)
	}
	// 饥饿不关联自动动作。
	if len(env.engine.careList()) != 0 {
		t.Fatalf("cares = %+v", env.engine.careList())
	}
}

// TestSleepyEmitsMessageAndAutoSleeps 验证困顿时既发主动消息又自动入睡。
func TestSleepyEmitsMessageAndAutoSleeps(t *testing.T) {
	env := setup(t, allOn)
	env.monitor.handle(context.Background(), pet.Event{PetID: env.pet.ID, Type: pet.EventSleepy})

	if len(env.emitted()) != 1 {
		t.Fatalf("events = %+v", env.emitted())
	}
	cares := env.engine.careList()
	if len(cares) != 1 || cares[0].action != pet.ActionSleep || cares[0].id != env.pet.ID {
		t.Fatalf("cares = %+v", cares)
	}
}

// TestMessagesDisabledStillAutoSleeps 验证关闭主动消息不影响自动动作。
func TestMessagesDisabledStillAutoSleeps(t *testing.T) {
	env := setup(t, Options{Enabled: true, AutoSleep: true, AutoWake: true, Messages: false})
	env.monitor.handle(context.Background(), pet.Event{PetID: env.pet.ID, Type: pet.EventSleepy})

	if env.msgr.calls() != 0 || len(env.emitted()) != 0 {
		t.Fatalf("messages should be off: calls=%d events=%+v", env.msgr.calls(), env.emitted())
	}
	if len(env.engine.careList()) != 1 {
		t.Fatalf("cares = %+v", env.engine.careList())
	}
}

// TestNoLLMSkipsMessageSilently 验证未配置 LLM（且未注入 Messager）时静默跳过消息。
func TestNoLLMSkipsMessageSilently(t *testing.T) {
	env := setup(t, allOn)
	env.monitor.Messager = nil // 走 LLM 配置路径：全局零值 → 未配置
	env.monitor.handle(context.Background(), pet.Event{PetID: env.pet.ID, Type: pet.EventHungry})

	if len(env.emitted()) != 0 {
		t.Fatalf("events = %+v", env.emitted())
	}
}

// TestWokeUpMessageCarriesWakeNote 验证睡醒消息带上睡醒便签（非消费式读取）。
func TestWokeUpMessageCarriesWakeNote(t *testing.T) {
	env := setup(t, allOn)
	if _, err := env.fs.CreatePet(env.pet.ID, petfs.Identity{
		Name: env.pet.Name, Species: env.pet.Species, Stage: string(env.pet.Stage), BornAt: env.pet.BornAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.fs.WriteWakeNote(env.pet.ID, "你做了一个梦：梦见了一片鱼干海。\n"); err != nil {
		t.Fatal(err)
	}

	env.monitor.handle(context.Background(), pet.Event{PetID: env.pet.ID, Type: pet.EventWokeUp})

	req := env.msgr.reqs[0]
	if req.Trigger != pet.EventWokeUp || !strings.Contains(req.WakeNote, "鱼干海") {
		t.Fatalf("compose req = %+v", req)
	}
	// 便签未被消费：仍可读。
	if note, _ := env.fs.ReadWakeNote(env.pet.ID); !strings.Contains(note, "鱼干海") {
		t.Fatalf("wake note should not be consumed, got %q", note)
	}
}

// TestComposeErrorSkipsMessage 验证 LLM 失败只跳过消息，不产生事件也不出错。
func TestComposeErrorSkipsMessage(t *testing.T) {
	env := setup(t, allOn)
	env.msgr.err = errors.New("llm down")
	env.monitor.handle(context.Background(), pet.Event{PetID: env.pet.ID, Type: pet.EventDirty})

	if len(env.emitted()) != 0 {
		t.Fatalf("events = %+v", env.emitted())
	}
}

// TestOnTickAutoWake 验证睡饱自动醒来的各分支。
func TestOnTickAutoWake(t *testing.T) {
	env := setup(t, allOn)
	mk := func(id string, sleeping bool, energy float64, alive bool) *pet.Pet {
		p := pet.New(id, id, "猫", time.Now())
		p.Sleeping, p.Stats.Energy, p.Alive = sleeping, energy, alive
		return p
	}
	env.engine.pets = []*pet.Pet{
		mk("rested", true, 100, true),  // 睡饱 → 自动醒来
		mk("tired", true, 50, true),    // 没睡够 → 不动
		mk("awake", false, 100, true),  // 醒着 → 不动
		mk("dead", true, 100, false),   // 死亡 → 不动
	}
	env.monitor.OnTick(context.Background(), time.Now())

	cares := env.engine.careList()
	if len(cares) != 1 || cares[0].id != "rested" || cares[0].action != pet.ActionWake {
		t.Fatalf("cares = %+v", cares)
	}
}

// TestOnTickRespectsOptions 验证总开关/自动醒来开关关闭时不动作。
func TestOnTickRespectsOptions(t *testing.T) {
	for _, opts := range []Options{
		{Enabled: false, AutoSleep: true, AutoWake: true, Messages: true},
		{Enabled: true, AutoSleep: true, AutoWake: false, Messages: true},
	} {
		env := setup(t, opts)
		p := pet.New("rested", "rested", "猫", time.Now())
		p.Sleeping, p.Stats.Energy = true, 100
		env.engine.pets = []*pet.Pet{p}
		env.monitor.OnTick(context.Background(), time.Now())
		if len(env.engine.careList()) != 0 {
			t.Fatalf("opts=%+v cares=%+v", opts, env.engine.careList())
		}
	}
}

// TestPublishAsyncAndDedup 验证 Publish 立即返回、异步处理且同宠物并发去重。
func TestPublishAsyncAndDedup(t *testing.T) {
	env := setup(t, allOn)
	ev := pet.Event{PetID: env.pet.ID, Type: pet.EventHungry}

	env.monitor.Publish(ev)
	env.monitor.Publish(ev) // 可能与前一次并发：去重后至多一条消息

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n := len(env.emitted()); n > 0 {
			if n > 1 {
				t.Fatalf("dedup failed: events = %+v", env.emitted())
			}
			if env.emitted()[0].Type != pet.EventProactive {
				t.Fatalf("event = %+v", env.emitted()[0])
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no proactive event emitted within 2s")
}

// TestPublishIgnoresUnrelated 验证无关事件与总开关关闭时不触发。
func TestPublishIgnoresUnrelated(t *testing.T) {
	env := setup(t, allOn)
	env.monitor.Publish(pet.Event{PetID: env.pet.ID, Type: pet.EventStageUp})
	env.monitor.Publish(pet.Event{PetID: env.pet.ID, Type: pet.EventFellAsleep})

	off := setup(t, Options{Enabled: false})
	off.monitor.Publish(pet.Event{PetID: off.pet.ID, Type: pet.EventHungry})

	// 给异步处理留出时间：不应有任何输出。
	time.Sleep(50 * time.Millisecond)
	if len(env.emitted()) != 0 || len(off.emitted()) != 0 {
		t.Fatalf("events = %+v / %+v", env.emitted(), off.emitted())
	}
}
