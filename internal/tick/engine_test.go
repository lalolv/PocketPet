package tick

import (
	"context"
	"sync"
	"testing"
	"time"

	"pocketpet/internal/pet"
	"pocketpet/internal/store"
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
	if got.Stats.Hunger != 65 || got.Stats.Energy != 98 {
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
	clock.Advance(time.Hour) // 先流逝 1h：饱食度 70→65
	got, err := eng.Care(ctx, p.ID, pet.ActionFeed)
	if err != nil {
		t.Fatal(err)
	}
	// 补算后再 feed：65+20
	if got.Stats.Hunger != 85 || got.Stats.EXP != 2 {
		t.Fatalf("stats = %+v, want hunger 85 exp 2", got.Stats)
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
