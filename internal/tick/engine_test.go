package tick

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petstate"
	"github.com/lalolv/PocketPet/internal/store"
)

var t0 = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

type fakeSink struct {
	mu   sync.Mutex
	evts []pet.Event
}

func (f *fakeSink) Publish(e pet.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evts = append(f.evts, e)
}

func (f *fakeSink) types() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.evts {
		out = append(out, e.Type)
	}
	return out
}

func setup(t *testing.T) (*Engine, *store.Store, *pet.FakeClock, *fakeSink) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clock := pet.NewFakeClock(t0)
	sink := &fakeSink{}
	return NewEngine(st, sink, time.Minute, 24*time.Hour, clock), st, clock, sink
}

func TestCreatePetEmitsBorn(t *testing.T) {
	eng, st, _, sink := setup(t)
	ctx := context.Background()

	p, err := eng.CreatePet(ctx, "团团", "cat")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == "" || p.Stage != pet.StageEgg || !p.Alive {
		t.Fatalf("bad new pet: %+v", p)
	}
	if got := sink.types(); len(got) != 1 || got[0] != pet.EventBorn {
		t.Fatalf("published = %v, want [pet.born]", got)
	}
	evs, err := st.RecentEvents(ctx, p.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Type != pet.EventBorn || evs[0].ID == 0 {
		t.Fatalf("stored events = %+v", evs)
	}
}

func TestTickAllAppliesDecay(t *testing.T) {
	eng, st, clock, _ := setup(t)
	ctx := context.Background()

	p, err := eng.CreatePet(ctx, "团团", "cat")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour)
	eng.TickAll(ctx)

	got, err := st.GetPet(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stats.Hunger != 67 || got.Stats.Energy != 98 {
		t.Fatalf("stats after 1h = %+v", got.Stats)
	}
	if !got.LastTickAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("LastTickAt = %v", got.LastTickAt)
	}
}

func TestCareThroughEngine(t *testing.T) {
	eng, _, clock, _ := setup(t)
	ctx := context.Background()

	p, err := eng.CreatePet(ctx, "团团", "cat")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour) // 先流逝 1h：饱食度 70→67
	got, err := eng.Care(ctx, p.ID, pet.ActionFeed)
	if err != nil {
		t.Fatal(err)
	}
	// 补算后再 feed：67+30
	if got.Stats.Hunger != 97 || got.Stats.EXP != 2 {
		t.Fatalf("stats = %+v, want hunger 97 exp 2", got.Stats)
	}
}

func TestCareRejectedStillSettles(t *testing.T) {
	eng, st, clock, _ := setup(t)
	ctx := context.Background()

	p, err := eng.CreatePet(ctx, "团团", "cat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Care(ctx, p.ID, pet.ActionSleep); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour)
	if _, err := eng.Care(ctx, p.ID, pet.ActionFeed); err == nil {
		t.Fatal("feed while sleeping should fail")
	}
	// 尽管动作被拒，1h 的睡眠恢复应已保存
	got, err := st.GetPet(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastTickAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("settlement not persisted on rejected care: %v", got.LastTickAt)
	}
}

func TestEventsPublishedOnTick(t *testing.T) {
	eng, st, clock, sink := setup(t)
	ctx := context.Background()

	p, err := eng.CreatePet(ctx, "团团", "cat")
	if err != nil {
		t.Fatal(err)
	}
	// 直接把饱食度调到濒临饥饿，再 tick 一次触发事件
	p.Stats.Hunger = 21
	if err := st.SavePet(ctx, p); err != nil {
		t.Fatal(err)
	}
	clock.Advance(15 * time.Minute)
	eng.TickAll(ctx)

	types := sink.types()
	if len(types) != 2 || types[0] != pet.EventBorn || types[1] != pet.EventHungry {
		t.Fatalf("published = %v", types)
	}
	// 再 tick 不应重复推送
	clock.Advance(15 * time.Minute)
	eng.TickAll(ctx)
	if got := sink.types(); len(got) != 2 {
		t.Fatalf("event re-published: %v", got)
	}
}

func TestDeadPetSkipped(t *testing.T) {
	eng, st, clock, sink := setup(t)
	ctx := context.Background()

	p, err := eng.CreatePet(ctx, "团团", "cat")
	if err != nil {
		t.Fatal(err)
	}
	p.Stats.Hunger = 10
	p.Stats.Health = 1
	if err := st.SavePet(ctx, p); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour)
	eng.TickAll(ctx) // 扣血归零 → 死亡

	got, err := st.GetPet(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Alive {
		t.Fatal("pet should be dead")
	}
	statsBefore := got.Stats
	// 死亡后 TickAll 跳过，状态不再变化
	clock.Advance(time.Hour)
	eng.TickAll(ctx)
	got, _ = st.GetPet(ctx, p.ID)
	if got.Stats != statsBefore {
		t.Fatal("dead pet should not be ticked")
	}
	// 全程应有 born + dead（可能还有 sick，但 dead 必须出现且只出现一次）
	dead := 0
	for _, typ := range sink.types() {
		if typ == pet.EventDead {
			dead++
		}
	}
	if dead != 1 {
		t.Fatalf("pet.dead published %d times, want 1", dead)
	}
}

// TestAdjustAndTickHook 验证 M5 接缝：Engine.Adjust 持久化数值调整，
// AddTickHook 注册的钩子每周期被调用。
func TestAdjustAndTickHook(t *testing.T) {
	eng, st, clock, _ := setup(t)
	ctx := context.Background()

	p, err := eng.CreatePet(ctx, "团团", "cat")
	if err != nil {
		t.Fatal(err)
	}
	got, err := eng.Adjust(ctx, p.ID, pet.Stats{Happy: -10.5, EXP: 7})
	if err != nil {
		t.Fatal(err)
	}
	if got.Stats.Happy != 69.5 || got.Stats.EXP != 7 {
		t.Fatalf("stats = %+v", got.Stats)
	}
	// 已落库
	stored, err := st.GetPet(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Stats.EXP != 7 {
		t.Fatalf("persisted exp = %v", stored.Stats.EXP)
	}

	// tick 钩子
	h := &recordingHook{}
	eng.AddTickHook(h)
	clock.Advance(time.Minute)
	eng.TickAll(ctx)
	if h.calls != 1 || h.lastNow.IsZero() {
		t.Fatalf("hook calls = %d", h.calls)
	}
}

type recordingHook struct {
	calls   int
	lastNow time.Time
}

func (h *recordingHook) OnTick(_ context.Context, now time.Time) {
	h.calls++
	h.lastNow = now
}

func TestActivityOccupyBlocksSleep(t *testing.T) {
	eng, _, _, _ := setup(t)
	ctx := context.Background()

	p, err := eng.CreatePet(ctx, "团团", "cat")
	if err != nil {
		t.Fatal(err)
	}
	if act, err := eng.Activity(ctx, p.ID); err != nil || act != pet.ActivityIdle {
		t.Fatalf("activity = %q, %v", act, err)
	}
	res, err := eng.State().Apply(ctx, p.ID, petstate.Transition{
		To: pet.ActivityAdventuring, Owner: "adventure", Reason: "test",
	})
	if err != nil || res.Err != nil {
		t.Fatalf("apply adventuring: %v %v", err, res.Err)
	}
	if act, err := eng.Activity(ctx, p.ID); err != nil || act != pet.ActivityAdventuring {
		t.Fatalf("activity = %q, %v want adventuring", act, err)
	}
	if _, err := eng.Care(ctx, p.ID, pet.ActionSleep); !errors.Is(err, pet.ErrBusy) {
		t.Fatalf("sleep while adventuring = %v, want ErrBusy", err)
	}
	// 回 idle 时消费 IntentSleep → 自动入睡
	res, err = eng.State().GoIdle(ctx, p.ID, "end:test")
	if err != nil || (res.Err != nil && !errors.Is(res.Err, pet.ErrAlready)) {
		t.Fatalf("go idle: %v %v", err, res.Err)
	}
	if act, err := eng.Activity(ctx, p.ID); err != nil || act != pet.ActivitySleeping {
		t.Fatalf("activity after end = %q, %v want sleeping", act, err)
	}
	res, err = eng.State().Apply(ctx, p.ID, petstate.Transition{
		To: pet.ActivityAdventuring, Owner: "adventure", Reason: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(res.Err, pet.ErrBusy) {
		t.Fatalf("adventure while sleeping = %v, want ErrBusy", res.Err)
	}
}

// TestRunTicksImmediately 回归：Run 不得空等一个完整 interval 才首次结算。
func TestRunTicksImmediately(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clock := pet.NewFakeClock(t0)
	eng := NewEngine(st, nil, time.Hour, 24*time.Hour, clock) // interval 很大，若空等则本测会超时失败语义上不触发
	ctx := context.Background()
	p, err := eng.CreatePet(ctx, "团团", "cat")
	if err != nil {
		t.Fatal(err)
	}
	before := p.Stats.Hunger
	clock.Advance(time.Hour) // 待结算的衰减已积压；Run 首 Tick 应立刻吃掉

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go eng.Run(runCtx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := st.GetPet(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Stats.Hunger < before && got.LastTickAt.After(t0) {
			cancel()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Run should TickAll immediately on start")
}

// TestAfterMutateCareFeedTriggersAutoSleepWhenAlreadySleepy 回归：边沿已过仍困着时，喂食后经 AfterMutate 补睡。
func TestAfterMutateCareFeedTriggersAutoSleepWhenAlreadySleepy(t *testing.T) {
	eng, st, _, _ := setup(t)
	ctx := context.Background()
	p, err := eng.CreatePet(ctx, "团团", "cat")
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetPet(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	got.Stats.Energy = 20
	got.Alerts.Sleepy = true
	if err := st.SavePet(ctx, got); err != nil {
		t.Fatal(err)
	}

	eng.AddAfterMutate(func(ctx context.Context, p *pet.Pet) {
		if p.Stats.Energy < pet.AlertWarn && !p.Sleeping {
			_, _ = eng.RequestSleep(ctx, p.ID, "after-mutate")
		}
	})

	if _, err := eng.Care(ctx, p.ID, pet.ActionFeed); err != nil {
		t.Fatal(err)
	}
	act, err := eng.Activity(ctx, p.ID)
	if err != nil || act != pet.ActivitySleeping {
		t.Fatalf("activity = %q, %v want sleeping after feed+AfterMutate", act, err)
	}
}

// TestAfterMutateAdjustTriggersAutoSleep 回归：Adjust 把精力打到预警下后也应补睡。
func TestAfterMutateAdjustTriggersAutoSleep(t *testing.T) {
	eng, _, _, _ := setup(t)
	ctx := context.Background()
	p, err := eng.CreatePet(ctx, "团团", "cat")
	if err != nil {
		t.Fatal(err)
	}
	eng.AddAfterMutate(func(ctx context.Context, p *pet.Pet) {
		if p.Stats.Energy < pet.AlertWarn && !p.Sleeping {
			_, _ = eng.RequestSleep(ctx, p.ID, "after-adjust")
		}
	})
	if _, err := eng.Adjust(ctx, p.ID, pet.Stats{Energy: -80}); err != nil { // 100→20
		t.Fatal(err)
	}
	act, err := eng.Activity(ctx, p.ID)
	if err != nil || act != pet.ActivitySleeping {
		t.Fatalf("activity = %q, %v want sleeping", act, err)
	}
}

// TestAfterMutateCanReenterWithoutDeadlock 回归：AfterMutate 在 unlock 后调用，可重入 RequestSleep。
func TestAfterMutateCanReenterWithoutDeadlock(t *testing.T) {
	eng, st, _, _ := setup(t)
	ctx := context.Background()
	p, err := eng.CreatePet(ctx, "团团", "cat")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetPet(ctx, p.ID)
	got.Stats.Energy = 15
	_ = st.SavePet(ctx, got)

	done := make(chan struct{})
	eng.AddAfterMutate(func(ctx context.Context, p *pet.Pet) {
		_, _ = eng.RequestSleep(ctx, p.ID, "reenter")
		close(done)
	})

	errCh := make(chan error, 1)
	go func() {
		_, err := eng.Care(ctx, p.ID, pet.ActionClean)
		errCh <- err
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("AfterMutate reentrant RequestSleep deadlocked")
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Care did not return")
	}
}
