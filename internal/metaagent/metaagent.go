// Package metaagent 是 MetaAgent 宠物诞生流程。
// G1 工具/脚本 → G2 LLM → G3 超时/降级/兼容 → G4 特质修饰与剧场。
// 设计见 docs/04-MetaAgent宠物诞生设计.md。
package metaagent

import (
	"context"
	"time"

	"github.com/lalolv/PocketPet/internal/llm"
	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
	"github.com/lalolv/PocketPet/internal/tick"
)

// 诞生模式。
const (
	ModeRandom   = "random"
	ModeDescribe = "describe"
	ModeTemplate = "template"
)

// 诞生路径标记（写入 genesis.ready / BirthResult.Via）。
const (
	ViaLLM      = "llm"
	ViaScript   = "script"
	ViaFallback = "fallback"
)

// TraitKeys 是数值修饰器用的固定特质词表（顺序稳定，便于测试）。
var TraitKeys = []string{
	"playfulness",
	"timidity",
	"appetite",
	"sociability",
}

// Stage 是严格串行的诞生阶段位。
type Stage uint

const (
	StageGenes Stage = 1 << iota
	StageTemperament
	StageAppearance
	StageQuirks
	StageSoul
	StageStats
	StageIdentity
	StageFinalized
)

// RequiredStages 是 finalize 前必须完成的阶段。
const RequiredStages = StageGenes | StageTemperament | StageAppearance |
	StageQuirks | StageSoul | StageStats | StageIdentity

// Request 是一次诞生请求。
type Request struct {
	Name        string // 可选；空则由 identity / fallback 命名
	Species     string // 必填
	Mode        string // random|describe|template；空 = random
	Personality string // template 模式
	Prompt      string // describe / random 软愿望
	Seed        string // 空则服务端生成
	Master      string // 空 = 主人
	// AwaitSoul 为 true 时 Start 同步跑完诞生再返回（genesis_status=ready）；
	// 适合旧 API 兼容与集成测试。默认 false（201 后异步孵化）。
	AwaitSoul bool
}

// BirthResult 是 Start 的返回值。
type BirthResult struct {
	PetID         string `json:"id"`
	Seed          string `json:"seed"`
	Species       string `json:"species"`
	Mode          string `json:"mode"`
	GenesisStatus string `json:"genesis_status"`
	EventsURL     string `json:"events_url"`
	// Via 在 AwaitSoul 或已完成时标明路径：llm / script / fallback。
	Via string `json:"via,omitempty"`
}

// Emitter 把诞生事件落库并推送（通常接 tick.Engine.Emit）。
type Emitter func(ctx context.Context, evs ...pet.Event)

// Midwife 协调孵化：BeginBirth → LLM/脚本工具链 → FinalizeBirth。
type Midwife struct {
	Engine *tick.Engine
	FS     *petfs.FS
	Emit   Emitter
	Now    func() time.Time

	// LLM 为全局模型配置；Configured 时走 MetaAgent，否则走脚本诞生。
	LLM llm.Config
	// ModelFactory 可选；nil 时用 llm.NewModel。
	ModelFactory ModelFactory
	// BirthTimeout 单次诞生上限；<=0 则用默认 90s。
	BirthTimeout time.Duration
	// ScriptPace 无 LLM 脚本阶段间隔；0=尽快（测试）；生产默认约 80ms。
	ScriptPace time.Duration
	// ForceScript 强制走脚本（测试或显式降级）。
	ForceScript bool
	// Sync 为 true 时 Start 内同步跑完（测试用）；默认异步。
	// 请求级 AwaitSoul 也会强制同步。
	Sync bool
}

func (m *Midwife) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Midwife) emit(ctx context.Context, evs ...pet.Event) {
	if m.Emit != nil {
		m.Emit(ctx, evs...)
	}
}

func (m *Midwife) birthTimeout() time.Duration {
	if m.BirthTimeout > 0 {
		return m.BirthTimeout
	}
	return defaultBirthTimeout
}
