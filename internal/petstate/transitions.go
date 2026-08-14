package petstate

import (
	"time"

	"github.com/lalolv/PocketPet/internal/pet"
)

// activityPolicy 是一类活动的进入策略，即转移表的一行。
type activityPolicy struct {
	needIdle  bool    // 仅能从 idle 进入（不抢占进行中的活动）
	minEnergy float64 // 进入所需最低精力；0 表示不限（睡觉不设限——困到极致随时能睡）
}

// blocks 报告 cur → 目标活动的转移是否被互斥规则阻止。
func (pol activityPolicy) blocks(cur string, energy float64) bool {
	if pol.needIdle && cur != pet.ActivityIdle {
		return true
	}
	return pol.minEnergy > 0 && energy < pol.minEnergy
}

// transitionTable 是活动态转移策略表（docs/07 §2 的代码落地），按目标活动索引：
//
//	idle      → 不在表内：回 idle 是"释放"，任何活动都允许回来
//	sleeping  → 仅 idle 可入睡，不抢占任何活动；忙时由 QueueIfBusy 排队 IntentSleep
//	插件 kind → 未单独配置时适用 defaultPluginPolicy
//
// 新增核心活动态 = 在表内加一行，无需改动 applyLocked 的流程代码。
var transitionTable = map[string]activityPolicy{
	pet.ActivitySleeping: {needIdle: true},
}

// defaultPluginPolicy 是插件活动的默认进入策略：仅 idle 可进入，且精力低于预警线时拒入
// （太困的宠物不出门，先睡）。
var defaultPluginPolicy = activityPolicy{needIdle: true, minEnergy: pet.AlertWarn}

// policyFor 返回目标活动的进入策略。
func policyFor(to string) activityPolicy {
	if to == pet.ActivityIdle {
		return activityPolicy{}
	}
	if pol, ok := transitionTable[to]; ok {
		return pol
	}
	return defaultPluginPolicy
}

// consumeIntents 在回到 idle 后消费排队意图：有 IntentSleep 或精力仍低于预警线时
// 自动入睡（如困着走完行程，回来倒头就睡）。死宠不消费——死亡已回收一切。
func consumeIntents(p *pet.Pet, now time.Time) []pet.Event {
	if !p.Alive || p.Activity != pet.ActivityIdle {
		return nil
	}
	if !p.HasIntent(pet.IntentSleep) && p.Stats.Energy >= pet.AlertWarn {
		return nil
	}
	p.RemoveIntent(pet.IntentSleep)
	p.Activity = pet.ActivitySleeping
	p.ActivityOwner = "core"
	p.Sleeping = true
	p.StateSeq++
	p.SyncSleepingFromActivity()
	return []pet.Event{{PetID: p.ID, Type: pet.EventFellAsleep, Message: p.Name + " 睡着了", CreatedAt: now}}
}
