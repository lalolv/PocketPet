// Package pet 是 PocketPet 的领域层：宠物数值状态、成长阶段、
// tick 衰减/离线结算规则、照顾动作与领域事件。
// 本包为纯 Go 实现：不 import net/http，不 import 任何数据库驱动。
package pet

import "time"

// Stage 是宠物的成长阶段。
type Stage string

// 成长阶段，按 EXP 累计值依次晋升。
const (
	StageEgg   Stage = "egg"
	StageBaby  Stage = "baby"
	StageChild Stage = "child"
	StageAdult Stage = "adult"
)

// stageThresholds 定义晋升到某阶段所需的累计 EXP 阈值（须按从小到大排列）。
// 取值说明：feed +2 / play +3 / clean +1 EXP，正常互动约 20 次破壳（egg→baby），
// 之后逐步放慢，给长期养成留出空间。
var stageThresholds = []struct {
	Stage Stage
	EXP   int
}{
	{StageBaby, 50},   // egg → baby：累计 50 EXP
	{StageChild, 200}, // baby → child：累计 200 EXP
	{StageAdult, 500}, // child → adult：累计 500 EXP
}

// Stats 是宠物的数值属性。Hunger/Happy/Clean/Energy/Health 取值 0-100（钳制），
// EXP 为累计经验值，只增不减。
// 内部用 float64 保存以支持按时间比例衰减的小数累积；对外展示时取整。
type Stats struct {
	Hunger float64 `json:"hunger"` // 饱食度，越高越饱
	Happy  float64 `json:"happy"`  // 心情
	Clean  float64 `json:"clean"`  // 清洁度
	Energy float64 `json:"energy"` // 精力
	Health float64 `json:"health"` // 健康，归零即死亡
	EXP    int     `json:"exp"`    // 累计经验
}

// AlertState 记录各告警事件的边沿触发标志，
// 用于保证 pet.hungry 等事件只在"进入该状态"时触发一次，而非每 tick 重复。
type AlertState struct {
	Hungry bool `json:"hungry"`
	Dirty  bool `json:"dirty"`
	Sleepy bool `json:"sleepy"`
	Sick   bool `json:"sick"`
}

// Pet 是一只宠物的完整数值状态（存储层以此为 JSON 快照持久化）。
type Pet struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Species string     `json:"species"`
	Stage   Stage      `json:"stage"`
	Stats   Stats      `json:"stats"`
	Alerts  AlertState `json:"alerts"`

	Sleeping bool `json:"sleeping"` // 睡眠中精力恢复，且不可 feed/play
	Alive    bool `json:"alive"`    // Health 归零后死亡，此后 tick 与动作均不生效

	BornAt     time.Time `json:"born_at"`
	LastTickAt time.Time `json:"last_tick_at"` // 上次衰减结算时刻，离线补算的基准
}

// New 创建一只新生宠物（蛋阶段）。
// 初始属性取"略低于满值、需要主人照顾但不紧急"的合理水平。
func New(id, name, species string, now time.Time) *Pet {
	return &Pet{
		ID:      id,
		Name:    name,
		Species: species,
		Stage:   StageEgg,
		Stats: Stats{
			Hunger: 70,
			Happy:  80,
			Clean:  80,
			Energy: 100,
			Health: 100,
		},
		Alive:      true,
		BornAt:     now,
		LastTickAt: now,
	}
}

// clamp 把各属性钳制到合法区间。
func (p *Pet) clamp() {
	p.Stats.Hunger = clamp100(p.Stats.Hunger)
	p.Stats.Happy = clamp100(p.Stats.Happy)
	p.Stats.Clean = clamp100(p.Stats.Clean)
	p.Stats.Energy = clamp100(p.Stats.Energy)
	p.Stats.Health = clamp100(p.Stats.Health)
	if p.Stats.EXP < 0 {
		p.Stats.EXP = 0
	}
}

func clamp100(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
