package pet

import (
	"strings"
	"time"
)

// 领域事件类型。事件由领域层产生，存储层落 pet_events 表，API 层经 SSE 推送。
const (
	EventBorn    = "pet.born"     // 宠物诞生（M1 额外补充，便于 SSE 回放与前端初始化）
	EventHungry  = "pet.hungry"   // 饱食度进入低位
	EventDirty   = "pet.dirty"    // 清洁度进入低位
	EventSleepy  = "pet.sleepy"   // 精力进入低位
	EventSick    = "pet.sick"     // 健康进入低位
	EventSad     = "pet.sad"      // 心情进入低位
	EventStageUp = "pet.stage_up" // 成长阶段晋升
	EventDead    = "pet.dead"     // 死亡

	EventFellAsleep = "pet.fell_asleep" // 入睡（M3：触发梦境整理）
	EventWokeUp     = "pet.woke_up"     // 醒来

	// EventProactive 由主动行为器（internal/proactive）产生，经 Engine.Emit 走同一事件流水：
	// 状态驱动的第一人称主动消息（message 为宠物对主人说的话）。
	EventProactive = "pet.proactive"

	// 以下事件由梦境整理器（internal/dream）产生，经 Engine.Emit 走同一事件流水。
	EventDream        = "pet.dream"         // 梦境独白（message 为第一人称梦境文本）
	EventDiaryWritten = "pet.diary_written" // 写了当日日记（message 含第一人称日记条目）
	EventSkillLearned = "pet.skill_learned" // 经验沉淀为技能
	EventSoulChanged  = "pet.soul_changed"  // SOUL.md 被演化改写

	// 以下事件由 MetaAgent 诞生流程（internal/metaagent）产生；
	// Message 为该阶段结构化 JSON 载荷（见 docs/04-MetaAgent宠物诞生设计.md）。
	EventGenesisStarted     = "genesis.started"
	EventGenesisNarration   = "genesis.narration"
	EventGenesisGenes       = "genesis.genes"
	EventGenesisTemperament = "genesis.temperament"
	EventGenesisAppearance  = "genesis.appearance"
	EventGenesisQuirks      = "genesis.quirks"
	EventGenesisSoul        = "genesis.soul"
	EventGenesisStats       = "genesis.stats"
	EventGenesisIdentity    = "genesis.identity"
	EventGenesisReady       = "genesis.ready"
	EventGenesisFailed      = "genesis.failed"
)

// MemorableEvent 报告事件是否值得进入"活动记录"（聊天侧的 recent_activities
// 工具与梦境整理的日记提炼共用同一份过滤）。过滤掉三类噪声：
//   - 身体预警（饿/脏/困/病/丧）：是状态而非经历，且 get_own_status 已覆盖；
//   - 宠物自己说过/写过的话（主动消息、梦境独白、日记条目）：已落日记，避免重复记；
//   - genesis.* 诞生流程的系统载荷（结构化 JSON，不是活动）。
//
// 未识别的类型（插件事件，如 pet.adventure_*）默认保留。
func MemorableEvent(eventType string) bool {
	switch eventType {
	case EventHungry, EventDirty, EventSleepy, EventSick, EventSad,
		EventProactive, EventDream, EventDiaryWritten:
		return false
	}
	return !strings.HasPrefix(eventType, "genesis.")
}

// Event 是一条领域事件。ID 由存储层写入 pet_events 表后回填。
type Event struct {
	ID        int64     `json:"id"`
	PetID     string    `json:"pet_id"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

func (p *Pet) newEvent(typ, msg string, now time.Time) Event {
	return Event{PetID: p.ID, Type: typ, Message: msg, CreatedAt: now}
}
