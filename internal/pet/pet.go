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
// 取值说明：feed +2 / play +3 / clean +1 EXP，正常互动约 3 天破壳（egg→baby），
// 之后逐步放慢，给长期养成留出空间。
var stageThresholds = []struct {
	Stage Stage
	EXP   int
}{
	{StageBaby, 30},   // egg → baby：累计 30 EXP
	{StageChild, 200}, // baby → child：累计 200 EXP
	{StageAdult, 500}, // child → adult：累计 500 EXP
}

// stageRank 返回成长阶段的序（egg=0，逐级 +1），用于"只晋升不回退"的比较。
func stageRank(s Stage) int {
	switch s {
	case StageBaby:
		return 1
	case StageChild:
		return 2
	case StageAdult:
		return 3
	}
	return 0
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
	Sad    bool `json:"sad"`
}

// 诞生（genesis）状态：由 MetaAgent 孵化流程写入。
const (
	GenesisIncubating = "incubating" // 蛋中，尚未 finalize
	GenesisReady      = "ready"      // 已诞生，可对话
)

// Pet 是一只宠物的完整数值状态（存储层以此为 JSON 快照持久化）。
type Pet struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Species string     `json:"species"`
	Stage   Stage      `json:"stage"`
	Stats   Stats      `json:"stats"`
	Alerts  AlertState `json:"alerts"`

	Sleeping bool `json:"sleeping"` // 派生于 Activity==sleeping；读兼容，写经 petstate.Manager
	Alive    bool `json:"alive"`    // Health 归零后死亡，此后 tick 与动作均不生效

	// GenesisStatus 是 MetaAgent 孵化状态。空字符串视为 ready（兼容旧宠物）。
	GenesisStatus string `json:"genesis_status,omitempty"`

	// Activity / ActivityOwner / StateSeq / Intents 由 petstate.Manager 维护（持久化占用）。
	Activity      string   `json:"activity,omitempty"`       // idle|sleeping|adventuring|…
	ActivityOwner string   `json:"activity_owner,omitempty"` // "" | plugin name
	StateSeq      uint64   `json:"state_seq,omitempty"`
	Intents       []string `json:"intents,omitempty"` // 排队意图，如 "sleep"

	BornAt     time.Time `json:"born_at"`
	LastTickAt time.Time `json:"last_tick_at"` // 上次衰减结算时刻，离线补算的基准
}

// Incubating 报告宠物是否仍在 MetaAgent 孵化中。
func (p *Pet) Incubating() bool {
	return p != nil && p.GenesisStatus == GenesisIncubating
}

// SyncSleepingFromActivity 让 Sleeping 与 Activity 一致（读兼容旧字段）。
func (p *Pet) SyncSleepingFromActivity() {
	if p == nil {
		return
	}
	if p.Activity == "" {
		if p.Sleeping {
			p.Activity = ActivitySleeping
		} else {
			p.Activity = ActivityIdle
		}
	}
	p.Sleeping = p.Activity == ActivitySleeping
}

// HasIntent 报告是否已排队某意图。
func (p *Pet) HasIntent(kind string) bool {
	for _, i := range p.Intents {
		if i == kind {
			return true
		}
	}
	return false
}

// AddIntent 排队意图（去重）。
func (p *Pet) AddIntent(kind string) {
	if p.HasIntent(kind) {
		return
	}
	p.Intents = append(p.Intents, kind)
}

// RemoveIntent 移除排队意图。
func (p *Pet) RemoveIntent(kind string) {
	out := p.Intents[:0]
	for _, i := range p.Intents {
		if i != kind {
			out = append(out, i)
		}
	}
	p.Intents = out
}

// DefaultStats 是即时创建（非 MetaAgent）时的默认初始属性。
func DefaultStats() Stats {
	return Stats{
		Hunger: 70,
		Happy:  80,
		Clean:  80,
		Energy: 100,
		Health: 100,
	}
}

// New 创建一只新生宠物（蛋阶段）。
// 初始属性取"略低于满值、需要主人照顾但不紧急"的合理水平。
func New(id, name, species string, now time.Time) *Pet {
	return NewWithStats(id, name, species, DefaultStats(), now)
}

// NewWithStats 用指定初始属性创建宠物（Health 若为 0 则补为 100）。
func NewWithStats(id, name, species string, stats Stats, now time.Time) *Pet {
	if stats.Health <= 0 {
		stats.Health = 100
	}
	p := &Pet{
		ID:         id,
		Name:       name,
		Species:    species,
		Stage:      StageEgg,
		Stats:      stats,
		Alive:      true,
		Activity:   ActivityIdle,
		BornAt:     now,
		LastTickAt: now,
	}
	p.clamp()
	return p
}

// ApplyBirthStats 在 finalize 时写入初始属性并钳制；Health 固定 100，EXP 归零。
func (p *Pet) ApplyBirthStats(stats Stats) {
	stats.Health = 100
	stats.EXP = 0
	p.Stats = stats
	p.clamp()
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
