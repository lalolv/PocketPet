package pet

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

func newTestPet() *Pet {
	return New("p1", "团团", "cat", t0)
}

func TestNewDefaults(t *testing.T) {
	p := newTestPet()
	if p.Stage != StageEgg || !p.Alive || p.Sleeping {
		t.Fatalf("unexpected initial state: %+v", p)
	}
	want := Stats{Hunger: 70, Happy: 80, Clean: 80, Energy: 100, Health: 100}
	if p.Stats != want {
		t.Fatalf("initial stats = %+v, want %+v", p.Stats, want)
	}
	if !p.BornAt.Equal(t0) || !p.LastTickAt.Equal(t0) {
		t.Fatalf("timestamps not initialized: %+v", p)
	}
}

func TestTickDecay(t *testing.T) {
	cases := []struct {
		name    string
		elapsed time.Duration
		want    Stats
	}{
		{"one hour", time.Hour, Stats{Hunger: 65, Happy: 77, Clean: 78, Energy: 98, Health: 100}},
		{"two hours", 2 * time.Hour, Stats{Hunger: 60, Happy: 74, Clean: 76, Energy: 96, Health: 100}},
		{"one minute", time.Minute, Stats{Hunger: 70 - 5.0/60, Happy: 80 - 3.0/60, Clean: 80 - 2.0/60, Energy: 100 - 2.0/60, Health: 100}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPet()
			evs := p.Tick(t0.Add(tc.elapsed), 0)
			if p.Stats != tc.want {
				t.Fatalf("stats = %+v, want %+v", p.Stats, tc.want)
			}
			if len(evs) != 0 {
				t.Fatalf("unexpected events: %+v", evs)
			}
			if !p.LastTickAt.Equal(t0.Add(tc.elapsed)) {
				t.Fatalf("LastTickAt not advanced: %v", p.LastTickAt)
			}
		})
	}
}

func TestTickSleepingRecoversEnergy(t *testing.T) {
	p := newTestPet()
	p.Stats.Energy = 50
	p.Sleeping = true
	p.Tick(t0.Add(time.Hour), 0)
	if p.Stats.Energy != 75 {
		t.Fatalf("energy = %v, want 75", p.Stats.Energy)
	}
	// 睡眠中饱食度照常衰减
	if p.Stats.Hunger != 65 {
		t.Fatalf("hunger = %v, want 65", p.Stats.Hunger)
	}
}

func TestTickHealthDrainOnlyAfterLow(t *testing.T) {
	// 满属性宠物离线 24h（上限内）：Hunger 100 在第 16h 跌破 20，
	// 只有后 8h 扣血 5/h → Health 60，不应直接死亡。
	p := New("p2", "满满", "dog", t0)
	p.Stats = Stats{Hunger: 100, Happy: 100, Clean: 100, Energy: 100, Health: 100}
	p.Tick(t0.Add(24*time.Hour), 24*time.Hour)
	if !p.Alive {
		t.Fatal("healthy pet should survive 24h offline")
	}
	if p.Stats.Health != 60 {
		t.Fatalf("health = %v, want 60", p.Stats.Health)
	}
}

func TestTickOfflineCap(t *testing.T) {
	// 100h 与 24h 上限：补算结果应与只结算 24h 完全一致。
	a := newTestPet()
	a.Tick(t0.Add(100*time.Hour), 24*time.Hour)
	b := newTestPet()
	b.Tick(t0.Add(24*time.Hour), 24*time.Hour)
	if a.Stats != b.Stats {
		t.Fatalf("capped stats = %+v, want %+v", a.Stats, b.Stats)
	}
	if !a.Alive {
		t.Fatal("pet should survive capped offline settlement")
	}
	// 无上限时 100h 直接饿死。
	c := newTestPet()
	c.Tick(t0.Add(100*time.Hour), 0)
	if c.Alive {
		t.Fatal("pet should be dead after 100h uncapped neglect")
	}
}

func TestTickDeathAndEvents(t *testing.T) {
	p := newTestPet()
	p.Stats.Hunger = 10 // 已低于阈值，立即开始扣血

	evs := p.Tick(t0.Add(time.Hour), 0)
	if p.Stats.Health != 95 {
		t.Fatalf("health = %v, want 95", p.Stats.Health)
	}
	assertEventTypes(t, evs, EventHungry)

	// 再 19h 扣完 95 点血 → 死亡
	evs = p.Tick(t0.Add(20*time.Hour), 0)
	if p.Alive {
		t.Fatal("pet should be dead")
	}
	assertEventTypes(t, evs, EventSick, EventDead)

	// 死亡后 tick 不再生效
	statsBefore := p.Stats
	if evs := p.Tick(t0.Add(21*time.Hour), 0); evs != nil || p.Stats != statsBefore {
		t.Fatal("dead pet should not tick")
	}
}

func TestAlertEdgeTrigger(t *testing.T) {
	p := newTestPet()
	p.Stats.Hunger = 21

	// 跌破阈值：触发一次
	evs := p.Tick(t0.Add(15*time.Minute), 0)
	assertEventTypes(t, evs, EventHungry)
	// 持续低于阈值：不重复触发
	evs = p.Tick(t0.Add(30*time.Minute), 0)
	assertEventTypes(t, evs)
	// 喂食恢复后标志清除
	if _, err := p.Care(ActionFeed, t0.Add(31*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// 再次跌破：重新触发
	evs = p.Tick(t0.Add(5*time.Hour), 0)
	assertEventTypes(t, evs, EventHungry)
}

func TestStageUp(t *testing.T) {
	p := newTestPet()
	p.Stats.EXP = 49
	evs, err := p.Care(ActionFeed, t0) // +2 → 51，破壳
	if err != nil {
		t.Fatal(err)
	}
	if p.Stage != StageBaby {
		t.Fatalf("stage = %v, want baby", p.Stage)
	}
	assertEventTypes(t, evs, EventStageUp)

	p.Stats.EXP = 199
	if _, err := p.Care(ActionClean, t0); err != nil { // +1 → 200
		t.Fatal(err)
	}
	if p.Stage != StageChild {
		t.Fatalf("stage = %v, want child", p.Stage)
	}

	p.Stats.EXP = 499
	evs, err = p.Care(ActionPlay, t0) // +3 → 502
	if err != nil {
		t.Fatal(err)
	}
	if p.Stage != StageAdult {
		t.Fatalf("stage = %v, want adult", p.Stage)
	}
	assertEventTypes(t, evs, EventStageUp)
}

func TestCareClamp(t *testing.T) {
	p := newTestPet()
	p.Stats.Hunger = 95
	if _, err := p.Care(ActionFeed, t0); err != nil {
		t.Fatal(err)
	}
	if p.Stats.Hunger != 100 {
		t.Fatalf("hunger = %v, want clamped 100", p.Stats.Hunger)
	}
}

func assertEventTypes(t *testing.T, evs []Event, want ...string) {
	t.Helper()
	if len(evs) != len(want) {
		t.Fatalf("events = %+v, want types %v", evs, want)
	}
	for i, typ := range want {
		if evs[i].Type != typ {
			t.Fatalf("event[%d].Type = %v, want %v", i, evs[i].Type, typ)
		}
	}
}
