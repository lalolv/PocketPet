package pet

import "time"

// 领域事件类型。事件由领域层产生，存储层落 pet_events 表，API 层经 SSE 推送。
const (
	EventBorn    = "pet.born"     // 宠物诞生（M1 额外补充，便于 SSE 回放与前端初始化）
	EventHungry  = "pet.hungry"   // 饱食度进入低位
	EventDirty   = "pet.dirty"    // 清洁度进入低位
	EventSleepy  = "pet.sleepy"   // 精力进入低位
	EventSick    = "pet.sick"     // 健康进入低位
	EventStageUp = "pet.stage_up" // 成长阶段晋升
	EventDead    = "pet.dead"     // 死亡

	EventFellAsleep = "pet.fell_asleep" // 入睡（M3：触发梦境整理）
	EventWokeUp     = "pet.woke_up"     // 醒来

	// 以下事件由梦境整理器（internal/dream）产生，经 Engine.Emit 走同一事件流水。
	EventDream        = "pet.dream"         // 梦境独白（message 为第一人称梦境文本）
	EventSkillLearned = "pet.skill_learned" // 经验沉淀为技能
	EventSoulChanged  = "pet.soul_changed"  // SOUL.md 被演化改写
)

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
