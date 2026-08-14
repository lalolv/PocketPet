package pet

import (
	"errors"
	"testing"
)

// TestCareActions 表驱动验证每个照顾动作的数值效果。
func TestCareActions(t *testing.T) {
	cases := []struct {
		name   string
		action Action
		mutate func(*Pet) // 动作前调整初始状态
		want   Stats
		check  func(*testing.T, *Pet)
	}{
		{
			name:   "feed",
			action: ActionFeed,
			want:   Stats{Hunger: 100, Happy: 80, Clean: 75, Energy: 100, Health: 100, EXP: 2},
		},
		{
			name:   "play",
			action: ActionPlay,
			want:   Stats{Hunger: 65, Happy: 100, Clean: 80, Energy: 90, Health: 100, EXP: 3},
		},
		{
			name:   "clean",
			action: ActionClean,
			want:   Stats{Hunger: 70, Happy: 80, Clean: 100, Energy: 100, Health: 100, EXP: 1},
		},
		{
			name:   "play drops hunger below threshold triggers event",
			action: ActionPlay,
			mutate: func(p *Pet) { p.Stats.Hunger = 22 },
			want:   Stats{Hunger: 17, Happy: 100, Clean: 80, Energy: 90, Health: 100, EXP: 3},
			check: func(t *testing.T, p *Pet) {
				if !p.Alerts.Hungry {
					t.Fatal("hungry alert should be set")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPet()
			if tc.mutate != nil {
				tc.mutate(p)
			}
			if _, err := p.Care(tc.action, t0); err != nil {
				t.Fatalf("Care(%s) error: %v", tc.action, err)
			}
			if p.Stats != tc.want {
				t.Fatalf("stats = %+v, want %+v", p.Stats, tc.want)
			}
			if tc.check != nil {
				tc.check(t, p)
			}
		})
	}
}

// TestCareErrors 表驱动验证状态不允许时的领域错误。
func TestCareErrors(t *testing.T) {
	cases := []struct {
		name   string
		action Action
		mutate func(*Pet)
		want   error
	}{
		{"unknown action", Action("dance"), nil, ErrUnknownAction},
		// sleep/wake 是活动态切换，领域 Care 不收理（由 petstate.Manager 经 Engine.Care 处理）。
		{"sleep not a domain care action", ActionSleep, nil, ErrUnknownAction},
		{"wake not a domain care action", ActionWake, func(p *Pet) { p.Sleeping = true }, ErrUnknownAction},
		{"feed while sleeping", ActionFeed, func(p *Pet) { p.Sleeping = true }, ErrSleeping},
		{"play while sleeping", ActionPlay, func(p *Pet) { p.Sleeping = true }, ErrSleeping},
		{"play with low energy", ActionPlay, func(p *Pet) { p.Stats.Energy = 9 }, ErrLowEnergy},
		{"dead pet rejects feed", ActionFeed, func(p *Pet) { p.Alive = false }, ErrDead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPet()
			if tc.mutate != nil {
				tc.mutate(p)
			}
			statsBefore := p.Stats
			if _, err := p.Care(tc.action, t0); !errors.Is(err, tc.want) {
				t.Fatalf("Care(%s) error = %v, want %v", tc.action, err, tc.want)
			}
			if p.Stats != statsBefore {
				t.Fatal("rejected care should not change stats")
			}
		})
	}
}

// TestDeathReclaimsActivity 死亡是最高优先级中断：回收活动态、清空排队意图。
func TestDeathReclaimsActivity(t *testing.T) {
	p := newTestPet()
	p.Activity = ActivityAdventuring
	p.ActivityOwner = "adventure"
	p.AddIntent(IntentSleep)

	evs := p.Adjust(Stats{Health: -200}, t0)
	if p.Alive {
		t.Fatal("pet should be dead")
	}
	assertEventTypes(t, evs, EventSick, EventDead)
	if p.Activity != ActivityIdle || p.ActivityOwner != "" || p.Sleeping {
		t.Fatalf("activity not reclaimed: act=%q owner=%q sleeping=%v", p.Activity, p.ActivityOwner, p.Sleeping)
	}
	if len(p.Intents) != 0 {
		t.Fatalf("intents not cleared: %v", p.Intents)
	}
}

// TestAdjust 验证插件用数值调整（M5）：钳制、晋升事件、死亡不生效。
func TestAdjust(t *testing.T) {
	p := newTestPet()
	// 加 EXP 跨过 baby 阈值（30）：晋升事件
	evs := p.Adjust(Stats{EXP: 50, Happy: 30}, t0)
	assertEventTypes(t, evs, EventStageUp)
	if p.Stage != StageBaby || p.Stats.EXP != 50 || p.Stats.Happy != 100 { // Happy 80+30 钳到 100
		t.Fatalf("after adjust: %+v stage=%v", p.Stats, p.Stage)
	}
	// 负值扣减 + 下钳制
	evs = p.Adjust(Stats{Health: -200}, t0)
	if p.Stats.Health != 0 {
		t.Fatalf("health = %v, want 0", p.Stats.Health)
	}
	assertEventTypes(t, evs, EventSick, EventDead) // 健康归零：先 sick 边沿再 dead
	// 死亡后不生效
	if evs := p.Adjust(Stats{Happy: 10}, t0); evs != nil {
		t.Fatalf("dead pet adjust = %v", evs)
	}
}


// TestSadAlert 验证心情进入低位触发 pet.sad 边沿告警，恢复后清除标志。
func TestSadAlert(t *testing.T) {
	p := newTestPet()
	evs := p.Adjust(Stats{Happy: -61}, t0) // 80-61=19 < AlertWarn
	assertEventTypes(t, evs, EventSad)
	if !p.Alerts.Sad {
		t.Fatal("sad alert should be set")
	}
	// 持续低落不重复触发；恢复后清除标志。
	if evs := p.Adjust(Stats{}, t0); evs != nil {
		t.Fatalf("re-alert = %v", evs)
	}
	p.Adjust(Stats{Happy: 50}, t0)
	if p.Alerts.Sad {
		t.Fatal("sad alert should be cleared")
	}
}
