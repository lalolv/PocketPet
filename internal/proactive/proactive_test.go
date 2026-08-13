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
	"github.com/lalolv/PocketPet/internal/petstate"
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

func (f *fakeEngine) RequestSleep(_ context.Context, id, _ string) (petstate.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cares = append(f.cares, careCall{id, pet.ActionSleep})
	return petstate.Result{OK: true}, nil
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

// TestAutoSleepBeforeMessage 验证自动入睡在 LLM 文案之前，缩小与探险等入口的竞态窗。
func TestAutoSleepBeforeMessage(t *testing.T) {
	env := setup(t, allOn)
	var sawCareBeforeCompose bool
	env.msgr = &fakeMessager{text: "好困…"}
	env.monitor.Messager = sleepOrderMessager{
		inner: env.msgr,
		onCompose: func() {
			sawCareBeforeCompose = len(env.engine.careList()) > 0
		},
	}
	env.monitor.handle(context.Background(), pet.Event{PetID: env.pet.ID, Type: pet.EventSleepy})
	if !sawCareBeforeCompose {
		t.Fatal("AutoSleep should run before compose")
	}
}

type sleepOrderMessager struct {
	inner     Messager
	onCompose func()
}

func (m sleepOrderMessager) Compose(ctx context.Context, req ComposeRequest) (string, error) {
	if m.onCompose != nil {
		m.onCompose()
	}
	return m.inner.Compose(ctx, req)
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
	if req.Trigger != pet.EventWokeUp || !strings.Contains(req.Frame.Fact, "鱼干海") {
		t.Fatalf("compose req = %+v", req)
	}
	// 便签未被消费：仍可读。
	if note, _ := env.fs.ReadWakeNote(env.pet.ID); !strings.Contains(note, "鱼干海") {
		t.Fatalf("wake note should not be consumed, got %q", note)
	}
}

// TestSleepyWhileBusyQueuesSpeechFrame 验证探险中困了：文案 Frame 禁止「已入睡」口吻。
func TestSleepyWhileBusyQueuesSpeechFrame(t *testing.T) {
	env := setup(t, allOn)
	env.pet.Activity = pet.ActivityAdventuring
	env.pet.SyncSleepingFromActivity()
	if err := env.st.SavePet(context.Background(), env.pet); err != nil {
		t.Fatal(err)
	}
	env.monitor.Engine = &busySleepEngine{fakeEngine: env.engine}
	env.monitor.handle(context.Background(), pet.Event{PetID: env.pet.ID, Type: pet.EventSleepy})
	if len(env.msgr.reqs) != 1 {
		t.Fatalf("reqs = %d", len(env.msgr.reqs))
	}
	f := env.msgr.reqs[0].Frame
	joined := strings.Join(f.Forbid, " ")
	if !strings.Contains(joined, "主人晚安") && !strings.Contains(joined, "已经睡着了") {
		t.Fatalf("forbid = %v fact=%q", f.Forbid, f.Fact)
	}
	if !strings.Contains(f.Fact, "还没睡着") && !strings.Contains(f.Instruction, "忙完") {
		t.Fatalf("want queued-sleep guidance: fact=%q instr=%q", f.Fact, f.Instruction)
	}
}

type busySleepEngine struct {
	*fakeEngine
}

func (b *busySleepEngine) RequestSleep(_ context.Context, id, _ string) (petstate.Result, error) {
	b.mu.Lock()
	b.cares = append(b.cares, careCall{id, pet.ActionSleep})
	b.mu.Unlock()
	return petstate.Result{OK: true, Queued: true}, nil
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

// TestOnTickAutoSleepWhenStuckSleepy 醒着且精力持续低于预警时，tick 会补 AutoSleep。
func TestOnTickAutoSleepWhenStuckSleepy(t *testing.T) {
	env := setup(t, allOn)
	p := pet.New("sleepy", "sleepy", "猫", time.Now())
	p.Stats.Energy = 20
	p.Alerts.Sleepy = true // 边沿已过，不会再发 pet.sleepy
	env.engine.pets = []*pet.Pet{p}
	env.monitor.OnTick(context.Background(), time.Now())
	cares := env.engine.careList()
	if len(cares) != 1 || cares[0].action != pet.ActionSleep {
		t.Fatalf("cares = %+v, want AutoSleep", cares)
	}
}

// TestEnsureAutoSleepSkippedWhenRested 精力充足时不入睡。
func TestEnsureAutoSleepSkippedWhenRested(t *testing.T) {
	env := setup(t, allOn)
	p := pet.New("ok", "ok", "猫", time.Now())
	p.Stats.Energy = 80
	env.monitor.EnsureAutoSleep(context.Background(), p)
	if len(env.engine.careList()) != 0 {
		t.Fatalf("cares = %+v", env.engine.careList())
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

// TestPublishAsyncAndDedup 验证同宠消息处理中重复触发会去重，不会并发双开 compose。
func TestPublishAsyncAndDedup(t *testing.T) {
	env := setup(t, allOn)
	release := make(chan struct{})
	env.monitor.Messager = blockingMessager{release: release, text: "主人，我饿啦！"}

	ev := pet.Event{PetID: env.pet.ID, Type: pet.EventHungry}
	env.monitor.Publish(ev)
	time.Sleep(20 * time.Millisecond)
	env.monitor.Publish(ev) // pending 中：同类型入队去重
	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		n := len(env.emitted())
		if n == 0 {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if n != 1 {
			t.Fatalf("dedup failed: events = %+v", env.emitted())
		}
		if env.emitted()[0].Type != pet.EventProactive {
			t.Fatalf("event = %+v", env.emitted()[0])
		}
		return
	}
	t.Fatal("no proactive event emitted within 2s")
}

// TestMsgQueueDrainsSleepyAfterHungry 饥饿文案处理中到达的 sleepy 会排队补发文案。
func TestMsgQueueDrainsSleepyAfterHungry(t *testing.T) {
	env := setup(t, allOn)
	release := make(chan struct{})
	var mu sync.Mutex
	var triggers []string
	env.monitor.Messager = &recordingBlockingMessager{release: release, onCompose: func(req ComposeRequest) {
		mu.Lock()
		triggers = append(triggers, req.Trigger)
		mu.Unlock()
	}}

	env.monitor.Publish(pet.Event{PetID: env.pet.ID, Type: pet.EventHungry})
	time.Sleep(20 * time.Millisecond)
	env.monitor.Publish(pet.Event{PetID: env.pet.ID, Type: pet.EventSleepy})
	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(triggers)
		mu.Unlock()
		if n >= 2 {
			mu.Lock()
			got := append([]string(nil), triggers...)
			mu.Unlock()
			if got[0] != pet.EventHungry || got[1] != pet.EventSleepy {
				t.Fatalf("triggers = %v", got)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("want hungry then sleepy compose, got %v", triggers)
}

type recordingBlockingMessager struct {
	release   <-chan struct{}
	onCompose func(ComposeRequest)
}

func (m *recordingBlockingMessager) Compose(ctx context.Context, req ComposeRequest) (string, error) {
	if m.onCompose != nil {
		m.onCompose(req)
	}
	select {
	case <-m.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return "msg", nil
}

// TestSleepyAutoSleepNotBlockedByHungryMessage 同 tick 先饿后困：AutoSleep 不被消息 pending 丢掉。
func TestSleepyAutoSleepNotBlockedByHungryMessage(t *testing.T) {
	env := setup(t, allOn)
	release := make(chan struct{})
	env.monitor.Messager = blockingMessager{release: release, text: "饿…"}

	env.monitor.Publish(pet.Event{PetID: env.pet.ID, Type: pet.EventHungry})
	time.Sleep(20 * time.Millisecond) // 让 hungry goroutine 跑起来并占用 pending
	env.monitor.Publish(pet.Event{PetID: env.pet.ID, Type: pet.EventSleepy})

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, c := range env.engine.careList() {
			if c.action == pet.ActionSleep {
				close(release)
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	t.Fatalf("AutoSleep should run while hungry message pending; cares=%+v", env.engine.careList())
}

type blockingMessager struct {
	release <-chan struct{}
	text    string
}

func (b blockingMessager) Compose(ctx context.Context, req ComposeRequest) (string, error) {
	select {
	case <-b.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return b.text, nil
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
