package pet

import (
	"errors"
	"time"
)

// 数值规则（按小时计，设计文档 3.1/3.2 节的 M1 数值表）。
// 衰减与恢复均按实际流逝时间线性折算，tick 间隔变化不影响长期速率。
const (
	hungerDecayPerHour = 3.0 // 满饱食约 33 小时饿到 0
	happyDecayPerHour  = 2.0 // 满心情约 50 小时降到 0
	cleanDecayPerHour  = 1.5 // 满清洁约 67 小时降到 0

	energyAwakeDrainPerHour = 2.0  // 清醒时精力缓慢消耗，满精力约 50 小时耗尽
	energySleepGainPerHour  = 25.0 // 睡眠中恢复，约 4 小时从 0 睡满

	sleepDecayFactor = 0.5 // 睡眠中 Hunger/Happy/Clean 衰减减半

	healthDrainPerHour = 5.0 // 任一属性低于 AlertCritical 期间，健康每小时扣减量
	healthRegenPerHour = 2.0 // 四项属性均不低于 AlertWarn 时，健康每小时恢复量
)

// 告警/状态阈值。
const (
	// AlertWarn：Hunger/Happy/Clean/Energy 低于该值时进入"饥饿/脏/困/低落"状态，
	// 触发对应事件（边沿触发）。仅预警，不扣血，给主人留出反应窗口。
	AlertWarn = 30.0
	// AlertCritical：任一属性低于该值期间，Health 持续流失。
	AlertCritical = 10.0
	// SickBelow：Health 低于该值时触发 pet.sick。
	SickBelow = 50.0
)

// 照顾动作的数值效果（M1 数值表）。
const (
	feedHungerGain = 30.0
	feedCleanCost  = 5.0
	feedEXP        = 2

	playHappyGain  = 20.0
	playEnergyCost = 10.0 // 精力低于该值时拒绝 play
	playHungerCost = 5.0
	playEXP        = 3

	cleanCleanGain = 30.0
	cleanEXP       = 1
)

// 领域错误，由 API 层映射为业务错误码。
var (
	// ErrUnknownAction 表示请求了不存在的照顾动作。
	ErrUnknownAction = errors.New("unknown care action")
	// ErrDead 表示宠物已死亡，除复活（M1 未实现）外一切操作无效。
	ErrDead = errors.New("pet is dead")
	// ErrSleeping 表示宠物正在睡觉，不能喂食或玩耍。
	ErrSleeping = errors.New("pet is sleeping")
	// ErrAlreadySleeping 表示对睡眠中的宠物重复下达 sleep。
	ErrAlreadySleeping = errors.New("pet is already sleeping")
	// ErrNotSleeping 表示对清醒宠物下达 wake。
	ErrNotSleeping = errors.New("pet is not sleeping")
	// ErrLowEnergy 表示精力不足以玩耍。
	ErrLowEnergy = errors.New("not enough energy to play")
	// ErrBusy 表示宠物正被另一持续活动占用（如探险中不可入睡）。
	ErrBusy = errors.New("pet is busy with another activity")
	// ErrAlready 表示已处于目标活动态（Apply 非幂等）。
	ErrAlready = errors.New("pet is already in the target activity")
	// ErrIncubating 表示宠物仍在 MetaAgent 孵化中，尚不可照顾/对话。
	ErrIncubating = errors.New("pet is still incubating")
	// ErrNotIncubating 表示对非孵化状态宠物执行 finalize。
	ErrNotIncubating = errors.New("pet is not incubating")
)

// 活动态（Alive 下互斥）：由 petstate.Manager 维护并持久化到 Pet.Activity。
const (
	ActivityIdle        = "idle"
	ActivitySleeping    = "sleeping"
	ActivityAdventuring = "adventuring"
)

// IntentSleep 是排队入睡意图（忙时入队，回 idle 后消费）。
const IntentSleep = "sleep"

// Action 是一个照顾动作。
type Action string

// 支持的照顾动作。
const (
	ActionFeed  Action = "feed"
	ActionPlay  Action = "play"
	ActionClean Action = "clean"
	ActionSleep Action = "sleep"
	ActionWake  Action = "wake"
)

// Tick 按 now 与 LastTickAt 的时间差结算一次衰减（离线补算同样走这里），
// 补算时长被 maxElapsed 截断（<=0 表示不截断），防止久未开机直接饿死。
// 使用中性特质（倍率 1）；有 SOUL 特质时请用 TickTraits。
func (p *Pet) Tick(now time.Time, maxElapsed time.Duration) []Event {
	return p.TickTraits(now, maxElapsed, NeutralTraits())
}

// TickTraits 与 Tick 相同，但按 traits 修饰衰减速率。
func (p *Pet) TickTraits(now time.Time, maxElapsed time.Duration, traits Traits) []Event {
	if !p.Alive || !now.After(p.LastTickAt) {
		return nil
	}
	elapsed := now.Sub(p.LastTickAt)
	if maxElapsed > 0 && elapsed > maxElapsed {
		elapsed = maxElapsed
	}
	p.LastTickAt = now

	h := elapsed.Hours()
	rates := ratesFor(traits, p.Sleeping)
	p.Stats.Hunger -= rates.hunger * h
	p.Stats.Happy -= rates.happy * h
	p.Stats.Clean -= rates.clean * h
	if p.Sleeping {
		p.Stats.Energy += rates.energySleep * h
	} else {
		p.Stats.Energy -= rates.energyAwake * h
	}

	// 健康结算分两段估算（按线性速率回推各属性处于阈值以下的时长）：
	// 1) 流失：只在"任一属性低于 AlertCritical"的时间段内扣减——这样 24h
	//    离线补算时，一只原本健康的宠物只会从跌破临界值后开始扣血，
	//    不会直接被扣死；
	// 2) 恢复："四项属性均不低于 AlertWarn"的时间段内缓慢回血，
	//    让生病在恢复正常照顾后可逆。
	if d := p.lowDuration(h, AlertCritical, rates); d > 0 {
		p.Stats.Health -= healthDrainPerHour * d
	}
	if d := p.lowDuration(h, AlertWarn, rates); d < h {
		p.Stats.Health += healthRegenPerHour * (h - d)
	}

	p.clamp()
	return p.refresh(now)
}

// lowDuration 估算在本次结算的 h 小时内，任一属性处于 threshold 以下的
// 最长时长（小时）。调用时各属性已应用本段衰减但尚未钳制，
// 函数按速率回推结算前的值进行估算。
func (p *Pet) lowDuration(h, threshold float64, rates decayRates) float64 {
	longest := 0.0
	// 持续下降的属性：after 为结算后值，rate 为每小时下降量。
	down := []struct{ after, rate float64 }{
		{p.Stats.Hunger, rates.hunger},
		{p.Stats.Happy, rates.happy},
		{p.Stats.Clean, rates.clean},
	}
	if !p.Sleeping {
		down = append(down, struct{ after, rate float64 }{p.Stats.Energy, rates.energyAwake})
	}
	for _, r := range down {
		before := r.after + r.rate*h
		switch {
		case before < threshold:
			// 结算前已低于阈值：整段都在阈值以下，不可能更长，直接返回。
			return h
		case r.after < threshold:
			// 本段内跌破阈值：低于阈值的时长 = h - 跌破所需时间。
			if d := h - (before-threshold)/r.rate; d > longest {
				longest = d
			}
		}
	}
	// 睡眠中精力在恢复：若结算前精力低于阈值，恢复到阈值之前仍在阈值以下。
	if p.Sleeping {
		if beforeE := p.Stats.Energy - rates.energySleep*h; beforeE < threshold {
			t := (threshold - beforeE) / rates.energySleep
			if t > h {
				t = h
			}
			if t > longest {
				longest = t
			}
		}
	}
	return longest
}

// Care 执行一个照顾动作，返回产生的事件（含动作事件、边沿告警与晋升）。
// 非法动作或状态不允许时返回领域错误，状态不变。使用中性特质。
func (p *Pet) Care(action Action, now time.Time) ([]Event, error) {
	return p.CareTraits(action, now, NeutralTraits())
}

// CareTraits 与 Care 相同，但按 traits 修饰喂食/玩耍等数值效果。
func (p *Pet) CareTraits(action Action, now time.Time, traits Traits) ([]Event, error) {
	if !p.Alive {
		return nil, ErrDead
	}
	traits = traits.Clamped()
	var actionEvs []Event
	switch action {
	case ActionFeed:
		if p.Sleeping {
			return nil, ErrSleeping
		}
		p.Stats.Hunger += feedHungerGain * mult(traits.Appetite, ampAppetiteFeed)
		p.Stats.Clean -= feedCleanCost
		p.Stats.EXP += feedEXP
	case ActionPlay:
		if p.Sleeping {
			return nil, ErrSleeping
		}
		energyCost := playEnergyCost * mult(traits.Playfulness, ampPlayEnergy)
		if p.Stats.Energy < energyCost {
			return nil, ErrLowEnergy
		}
		p.Stats.Happy += playHappyGain * mult(traits.Playfulness, ampPlayHappy)
		p.Stats.Energy -= energyCost
		p.Stats.Hunger -= playHungerCost
		p.Stats.EXP += playEXP
	case ActionClean:
		p.Stats.Clean += cleanCleanGain
		p.Stats.EXP += cleanEXP
	case ActionSleep:
		if p.Sleeping || p.Activity == ActivitySleeping {
			return nil, ErrAlreadySleeping
		}
		p.Activity = ActivitySleeping
		p.ActivityOwner = "core"
		p.SyncSleepingFromActivity()
		actionEvs = append(actionEvs, p.newEvent(EventFellAsleep, p.Name+" 睡着了", now))
	case ActionWake:
		if !p.Sleeping && p.Activity != ActivitySleeping {
			return nil, ErrNotSleeping
		}
		p.Activity = ActivityIdle
		p.ActivityOwner = ""
		p.SyncSleepingFromActivity()
		actionEvs = append(actionEvs, p.newEvent(EventWokeUp, p.Name+" 醒来了", now))
	default:
		return nil, ErrUnknownAction
	}
	p.clamp()
	return append(actionEvs, p.refresh(now)...), nil
}

// Adjust 应用一次确定性的数值增减（供插件等受控扩展使用，M5）：各属性加上
// delta（负值即扣减），随后走与照顾动作相同的钳制、晋升与告警检查。
// 死亡宠物不生效。
func (p *Pet) Adjust(delta Stats, now time.Time) []Event {
	if !p.Alive {
		return nil
	}
	p.Stats.Hunger += delta.Hunger
	p.Stats.Happy += delta.Happy
	p.Stats.Clean += delta.Clean
	p.Stats.Energy += delta.Energy
	p.Stats.Health += delta.Health
	p.Stats.EXP += delta.EXP
	p.clamp()
	return p.refresh(now)
}

// refresh 在数值变动后统一检查：成长晋升、边沿告警、死亡。
func (p *Pet) refresh(now time.Time) []Event {
	var evs []Event

	// 成长阶段：按累计 EXP 计算目标阶段，跨越即晋升（可多级连跳，只报最终阶段）。
	// 只晋升不回退：EXP 只增不减，阶段不应倒退——否则阈值表版本不一致
	// （如多实例并存跑新旧二进制）时会反复降级/晋升刷事件。
	target := StageEgg
	for _, th := range stageThresholds {
		if p.Stats.EXP >= th.EXP {
			target = th.Stage
		}
	}
	if stageRank(target) > stageRank(p.Stage) {
		p.Stage = target
		evs = append(evs, p.newEvent(EventStageUp, p.Name+" 成长到了 "+string(target)+" 阶段", now))
	}

	// 边沿告警：状态从"正常"变为"异常"时才触发，恢复正常时清除标志。
	// 预警线为 AlertWarn；健康扣减在 Tick 中按 AlertCritical 结算。
	evs = append(evs, p.checkAlert(&p.Alerts.Hungry, p.Stats.Hunger < AlertWarn, EventHungry, p.Name+" 饿了，肚子咕咕叫", now)...)
	evs = append(evs, p.checkAlert(&p.Alerts.Dirty, p.Stats.Clean < AlertWarn, EventDirty, p.Name+" 脏兮兮的，该洗澡了", now)...)
	evs = append(evs, p.checkAlert(&p.Alerts.Sleepy, p.Stats.Energy < AlertWarn, EventSleepy, p.Name+" 困了，睁不开眼", now)...)
	evs = append(evs, p.checkAlert(&p.Alerts.Sick, p.Stats.Health < SickBelow, EventSick, p.Name+" 看起来生病了", now)...)
	evs = append(evs, p.checkAlert(&p.Alerts.Sad, p.Stats.Happy < AlertWarn, EventSad, p.Name+" 心情低落，想找人陪陪", now)...)

	// 死亡：健康归零。
	if p.Alive && p.Stats.Health <= 0 {
		p.Alive = false
		evs = append(evs, p.newEvent(EventDead, p.Name+" 死了……", now))
	}
	return evs
}

// checkAlert 实现单条告警的边沿触发：cond 由 false 变 true 时产生事件并置标志。
func (p *Pet) checkAlert(flag *bool, cond bool, typ, msg string, now time.Time) []Event {
	if cond && !*flag {
		*flag = true
		return []Event{p.newEvent(typ, msg, now)}
	}
	if !cond {
		*flag = false
	}
	return nil
}
