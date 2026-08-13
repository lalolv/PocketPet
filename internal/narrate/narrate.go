// Package narrate 是叙事层：主人可见文案只能复述 pet 快照 / Apply 结果，
// 禁止凭事件类型臆造活动态。与 petstate.Manager（写状态）成对使用。
package narrate

import (
	"fmt"
	"strings"

	"github.com/lalolv/PocketPet/internal/pet"
)

// Effect 是与本次开口相关的最近一次活动转移结果。
type Effect string

const (
	EffectNone        Effect = ""
	EffectSlept       Effect = "slept"        // 已进入 sleeping
	EffectQueuedSleep Effect = "queued_sleep" // 忙，已登记 IntentSleep
	EffectWoke        Effect = "woke"
)

// Context 是喂给 LLM / Outcome 的唯一叙事输入。
type Context struct {
	Activity string
	Intents  []string
	Alive    bool
	Sleeping bool
	Effect   Effect
	WakeNote string
}

// FromPet 从领域宠物构造叙事上下文。
func FromPet(p *pet.Pet) Context {
	if p == nil {
		return Context{}
	}
	p.SyncSleepingFromActivity()
	act := p.Activity
	if act == "" {
		if p.Sleeping {
			act = pet.ActivitySleeping
		} else {
			act = pet.ActivityIdle
		}
	}
	return Context{
		Activity: act,
		Intents:  append([]string(nil), p.Intents...),
		Alive:    p.Alive,
		Sleeping: p.Sleeping,
	}
}

// HasIntent 报告是否排队某意图。
func (c Context) HasIntent(kind string) bool {
	for _, i := range c.Intents {
		if i == kind {
			return true
		}
	}
	return false
}

// BusyAway 报告是否在玩法占用中（非 idle/sleeping）。
func (c Context) BusyAway() bool {
	return c.Activity != "" && c.Activity != pet.ActivityIdle && c.Activity != pet.ActivitySleeping
}

// Frame 是一次开口的策略结果。
type Frame struct {
	MaySpeak    bool
	Fact        string   // 必须遵守的事实（给 prompt）
	Instruction string   // 允许的口吻 / 该说什么
	Forbid      []string // 禁止声称的内容
	Outcome     string   // 工具等确定性短句（可空）
}

// PromptBlock 拼进 LLM user/system 的约束段落。
func (f Frame) PromptBlock() string {
	if !f.MaySpeak {
		return ""
	}
	var b strings.Builder
	b.WriteString("# 必须遵守的事实（不得与此矛盾）\n")
	b.WriteString(f.Fact)
	b.WriteString("\n")
	if len(f.Forbid) > 0 {
		b.WriteString("\n# 禁止声称\n")
		for _, x := range f.Forbid {
			b.WriteString("- ")
			b.WriteString(x)
			b.WriteString("\n")
		}
	}
	b.WriteString("\n# 这一次该说什么\n")
	b.WriteString(f.Instruction)
	b.WriteString("\n")
	return b.String()
}

// Policy 按触发事件 + 叙事上下文决定能否开口及如何说。
// trigger 为 pet.Event* 或 "care.sleep" / "care.wake"。
func Policy(trigger string, ctx Context) Frame {
	if !ctx.Alive && trigger != "" {
		return Frame{MaySpeak: false}
	}

	switch trigger {
	case pet.EventHungry:
		return conditionFrame(ctx, "你饿了，肚子不舒服。", "跟主人说你想吃东西了。")
	case pet.EventDirty:
		return conditionFrame(ctx, "你觉得自己脏兮兮的，不舒服。", "提醒主人该给你清洁一下了。")
	case pet.EventSick:
		return conditionFrame(ctx, "你生病了，浑身难受。", "告诉主人你需要照顾。")
	case pet.EventSad:
		return conditionFrame(ctx, "你心情低落，有点孤单。", "撒撒娇，让主人陪陪你。")
	case pet.EventSleepy:
		return sleepyFrame(ctx)
	case pet.EventWokeUp:
		return wokeFrame(ctx)
	case pet.EventFellAsleep:
		return Frame{
			MaySpeak:    true,
			Fact:        activityFact(ctx) + "你刚刚睡着了。",
			Instruction: "用一两句话跟主人说晚安或你去睡了。",
			Forbid:      []string{"还在外面探险", "正在走路", "马上出发"},
			Outcome:     "我睡着了",
		}
	case "care.sleep":
		return careSleepFrame(ctx)
	case "care.wake":
		return Frame{
			MaySpeak:    true,
			Fact:        activityFact(ctx),
			Instruction: "你醒了。",
			Outcome:     "我醒了",
		}
	default:
		return Frame{MaySpeak: false}
	}
}

func sleepyFrame(ctx Context) Frame {
	queued := ctx.Effect == EffectQueuedSleep || (ctx.HasIntent(pet.IntentSleep) && !ctx.Sleeping && ctx.Activity != pet.ActivitySleeping)
	slept := ctx.Effect == EffectSlept || ctx.Sleeping || ctx.Activity == pet.ActivitySleeping

	switch {
	case slept:
		return Frame{
			MaySpeak:    true,
			Fact:        activityFact(ctx) + "你已经睡着了（或正在入睡）。",
			Instruction: "睡前跟主人说一声晚安。",
			Forbid:      []string{"还在探险", "正在走路逛地图"},
			Outcome:     "我睡着了",
		}
	case queued:
		place := "还在忙别的事"
		if ctx.Activity == pet.ActivityAdventuring {
			place = "还在外面探险"
		}
		return Frame{
			MaySpeak:    true,
			Fact:        activityFact(ctx) + place + "；你已经打算忙完再睡，但现在还没睡着。",
			Instruction: "说你好困、想睡，但还得先把眼前的事忙完；不要说已经去睡了或晚安结束对话。",
			Forbid: []string{
				"已经睡着了", "我去睡了", "去窝里睡了", "主人晚安",
				"抱着袜子睡了", "我先去睡觉了",
			},
			Outcome: "我好困，忙完再睡",
		}
	default:
		return Frame{
			MaySpeak:    true,
			Fact:        activityFact(ctx) + "你很困，但还醒着。",
			Instruction: "跟主人说你困了、想去睡觉。",
			Forbid:      []string{"已经睡着了"},
			Outcome:     "我好困，想睡觉",
		}
	}
}

func wokeFrame(ctx Context) Frame {
	s := "你刚睡醒，精神好些了。"
	if note := strings.TrimSpace(ctx.WakeNote); note != "" {
		s += "这一觉的情况：" + note
	}
	return Frame{
		MaySpeak:    true,
		Fact:        activityFact(ctx) + s,
		Instruction: "跟主人打个招呼；若有梦/这一觉的情况，可以自然提起。",
		Forbid:      []string{"还在睡觉", "正在探险路上"},
	}
}

func careSleepFrame(ctx Context) Frame {
	switch {
	case ctx.Effect == EffectQueuedSleep || (ctx.HasIntent(pet.IntentSleep) && !ctx.Sleeping):
		return Frame{
			MaySpeak: true,
			Fact:     activityFact(ctx) + "睡意已记下，忙完才会睡。",
			Forbid:   []string{"已经睡着了", "我睡着了"},
			Outcome:  "我还在忙，忙完再睡",
		}
	case ctx.Sleeping || ctx.Activity == pet.ActivitySleeping || ctx.Effect == EffectSlept:
		return Frame{
			MaySpeak: true,
			Fact:     activityFact(ctx),
			Outcome:  "我睡着了",
		}
	default:
		return Frame{
			MaySpeak: true,
			Fact:     activityFact(ctx),
			Outcome:  "我想睡觉",
		}
	}
}

func conditionFrame(ctx Context, feeling, instruction string) Frame {
	fact := activityFact(ctx) + feeling
	forbid := []string{"已经死了"}
	if ctx.BusyAway() {
		instruction += "可以提你人还在外面/正忙着，但不要假装在家安稳睡觉。"
		forbid = append(forbid, "正在家里睡觉", "已经睡着了")
	}
	if ctx.Sleeping {
		instruction += "你在睡觉中感到不适，可以说梦里或迷迷糊糊地提一句。"
	}
	return Frame{
		MaySpeak:    true,
		Fact:        fact,
		Instruction: instruction,
		Forbid:      forbid,
	}
}

func activityFact(ctx Context) string {
	switch {
	case !ctx.Alive:
		return "你已经离开了。"
	case ctx.Activity == pet.ActivitySleeping || ctx.Sleeping:
		return "当前活动：正在睡觉。"
	case ctx.Activity == pet.ActivityAdventuring:
		return "当前活动：正在外面探险。"
	case ctx.Activity != "" && ctx.Activity != pet.ActivityIdle:
		return fmt.Sprintf("当前活动：正忙着（%s）。", ctx.Activity)
	default:
		return "当前活动：在家醒着。"
	}
}

// StatusBlock 生成主对话 / get_own_status 用的定性状态段（不含裸数值）。
func StatusBlock(p *pet.Pet, stageLabel, hunger, happy, clean, energy, health, mood string) string {
	ctx := FromPet(p)
	activity := "在家醒着"
	switch {
	case !ctx.Alive:
		activity = "已经离开了"
	case ctx.Activity == pet.ActivitySleeping || ctx.Sleeping:
		activity = "正在睡觉"
	case ctx.Activity == pet.ActivityAdventuring:
		activity = "正在外面探险"
	case ctx.BusyAway():
		activity = "正忙着（" + ctx.Activity + "）"
	}
	intent := ""
	if ctx.HasIntent(pet.IntentSleep) {
		intent = "\n- 心里想：忙完想去睡觉（还没睡着）"
	}
	return fmt.Sprintf(`- 成长阶段：%s
- 当前活动：%s%s
- 饱食感：%s；心情：%s；清洁度：%s；精力：%s；健康：%s
- 总体感受：%s`,
		stageLabel, activity, intent, hunger, happy, clean, energy, health, mood)
}

// ChatConstraint 主对话 system 约束：不得与 Snapshot 矛盾。
func ChatConstraint(p *pet.Pet) string {
	ctx := FromPet(p)
	var b strings.Builder
	b.WriteString("说话时必须与「当前活动」一致，不要编造矛盾的自称。\n")
	b.WriteString(activityFact(ctx))
	if ctx.HasIntent(pet.IntentSleep) && !ctx.Sleeping {
		b.WriteString("你想睡但还没睡着；不要说已经去睡了或晚安结束。")
	}
	return b.String()
}

// SleepBusyOutcome 在 Care(sleep) 因忙碌失败时，按是否已排队意图选文案。
func SleepBusyOutcome(p *pet.Pet) string {
	ctx := FromPet(p)
	ctx.Effect = EffectQueuedSleep
	f := Policy("care.sleep", ctx)
	if f.Outcome != "" {
		return f.Outcome
	}
	return "我正在忙别的事，现在睡不了"
}
