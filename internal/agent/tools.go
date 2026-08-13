package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	adkagent "google.golang.org/adk/v2/agent"
	adktool "google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/lalolv/PocketPet/internal/narrate"
	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
)

// recallDays 是 recall 工具回看的日记天数。
const recallDays = 7

// recallMaxRunes 是 recall 返回内容的字符数上限。
const recallMaxRunes = 3000

// noArgs 是无参工具的输入类型。
type noArgs struct{}

// careResult 是自我行为工具的统一返回：领域拒绝（如正在睡觉）也走 OK=false 的
// 正常结果返回给 LLM，让宠物自己用语言表达"做不到"，而不是抛异常打断对话。
type careResult struct {
	OK      bool   `json:"ok"`
	Outcome string `json:"outcome" jsonschema:"这次行为的结果描述（定性，不含数值）"`
}

type rememberArgs struct {
	Fact string `json:"fact" jsonschema:"要记住的事情，一两句话"`
}

type recallArgs struct {
	Query string `json:"query" jsonschema:"想回忆的事情或关键词"`
}

type recallResult struct {
	Notes string `json:"notes" jsonschema:"检索到的记忆原文"`
}

type statusResult struct {
	Status string `json:"status" jsonschema:"我当前的身体感受（定性描述，不含数值）"`
}

// buildTools 装配某只宠物的全部工具：自我行为 + 记忆工具（remember/recall，落到 petfs）。
// 自我行为只开放 sleep/wake（包装 tick.Engine 的 care 动作，与 REST care 同一领域路径）；
// 喂食/玩耍/清洁是主人专属的照顾动作（care 通道），宠物只能表达需求，见 INSTRUCTIONS.md。
func (a *PetAgent) buildTools(petID string) ([]adktool.Tool, error) {
	var tools []adktool.Tool
	add := func(t adktool.Tool, err error) error {
		if err != nil {
			return err
		}
		tools = append(tools, t)
		return nil
	}

	care := func(name, desc string, action pet.Action) error {
		return add(functiontool.New(functiontool.Config{Name: name, Description: desc},
			func(ctx adkagent.Context, _ noArgs) (careResult, error) {
				return a.selfCare(ctx, petID, action)
			}))
	}

	if err := care("sleep", "自己去睡觉：睡着后精力会慢慢恢复。已经在睡时会失败。", pet.ActionSleep); err != nil {
		return nil, err
	}
	if err := care("wake", "自己醒过来。醒着时用它会失败。", pet.ActionWake); err != nil {
		return nil, err
	}

	if err := add(functiontool.New(functiontool.Config{
		Name:        "get_own_status",
		Description: "感受一下自己现在的身体状况（定性的状态描述，不含具体数值）。",
	}, func(ctx adkagent.Context, _ noArgs) (statusResult, error) {
		p, err := a.engine.Settle(ctx, petID)
		if err != nil {
			return statusResult{}, err
		}
		return statusResult{Status: statusSnapshot(p)}, nil
	})); err != nil {
		return nil, err
	}

	if err := add(functiontool.New(functiontool.Config{
		Name:        "remember",
		Description: "把一件值得记住的事写进今天的日记（主人的喜好、约定、重要的事等）。",
	}, func(_ adkagent.Context, args rememberArgs) (careResult, error) {
		fact := strings.TrimSpace(args.Fact)
		if fact == "" {
			return careResult{OK: false, Outcome: "没想好要记什么"}, nil
		}
		if err := a.fs.AppendJournal(petID, fact, time.Now()); err != nil {
			return careResult{}, err
		}
		return careResult{OK: true, Outcome: "已经记进今天的日记了"}, nil
	})); err != nil {
		return nil, err
	}

	if err := add(functiontool.New(functiontool.Config{
		Name:        "recall",
		Description: "回忆过去：读取长期记忆（MEMORY.md）和最近几天的日记原文。",
	}, func(_ adkagent.Context, args recallArgs) (recallResult, error) {
		return a.recall(petID, args.Query)
	})); err != nil {
		return nil, err
	}

	// 插件注入的全局工具（M5）：对所有宠物可见，工具内自行按当前宠物路由。
	tools = append(tools, a.opts.ExtraTools...)

	return tools, nil
}

// selfCare 执行一个自我行为动作（与 REST care 同一领域路径）。
func (a *PetAgent) selfCare(ctx context.Context, petID string, action pet.Action) (careResult, error) {
	p, err := a.engine.Care(ctx, petID, action)
	if err != nil {
		return careResult{OK: false, Outcome: domainErrText(err, p, action)}, nil
	}
	return careResult{OK: true, Outcome: actionOutcome(action, p)}, nil
}

// recall 的简单实现（M2）：全文读取 MEMORY.md + 最近 N 天日记，拼接截取返回，
// 由 LLM 自己从中找出与 query 相关的内容；不做向量检索。
func (a *PetAgent) recall(petID, _ string) (recallResult, error) {
	var sb strings.Builder
	if mem, err := a.fs.Read(petID, petfs.FileMemory); err == nil && strings.TrimSpace(mem) != "" {
		sb.WriteString("【长期记忆】\n")
		sb.WriteString(strings.TrimSpace(mem))
		sb.WriteString("\n\n")
	}
	journals, err := a.fs.ListJournals(petID)
	if err != nil {
		return recallResult{}, err
	}
	if len(journals) > recallDays {
		journals = journals[len(journals)-recallDays:]
	}
	for _, name := range journals {
		content, err := a.fs.ReadJournal(petID, name)
		if err != nil {
			continue
		}
		sb.WriteString(content)
		sb.WriteString("\n")
	}
	notes := strings.TrimSpace(sb.String())
	if notes == "" {
		notes = "（还没有任何记忆）"
	}
	return recallResult{Notes: truncateRunes(notes, recallMaxRunes)}, nil
}

// domainErrText 把领域错误翻译成宠物第一人称表达的素材。
func domainErrText(err error, p *pet.Pet, action pet.Action) string {
	switch {
	case errors.Is(err, pet.ErrSleeping):
		return "我正在睡觉，做不了这个"
	case errors.Is(err, pet.ErrAlreadySleeping):
		return "我已经在睡觉了"
	case errors.Is(err, pet.ErrNotSleeping):
		return "我现在醒着，没在睡觉"
	case errors.Is(err, pet.ErrBusy):
		if action == pet.ActionSleep {
			return narrate.SleepBusyOutcome(p)
		}
		return "我正在忙别的事，现在做不了"
	case errors.Is(err, pet.ErrLowEnergy):
		return "我太累了，实在玩不动"
	case errors.Is(err, pet.ErrDead):
		return "我已经没有力气回应了"
	default:
		return "做不到：" + err.Error()
	}
}

// actionOutcome 描述动作完成后的状态（定性词汇，供 LLM 组织第一人称语言）。
// 自我行为工具只有 sleep/wake，其余照顾动作是主人专属，不走这里。
func actionOutcome(action pet.Action, p *pet.Pet) string {
	ctx := narrate.FromPet(p)
	switch action {
	case pet.ActionSleep:
		ctx.Effect = narrate.EffectSlept
		if f := narrate.Policy("care.sleep", ctx); f.Outcome != "" {
			return f.Outcome
		}
		return "我睡着了"
	case pet.ActionWake:
		ctx.Effect = narrate.EffectWoke
		return fmt.Sprintf("我醒了。现在精力：%s", levelWord(p.Stats.Energy))
	default:
		return "做完了"
	}
}

// statusSnapshot 生成定性的状态描述（注入指令的快照与 get_own_status 共用）。
// 刻意不含数值——宠物被要求用生命化的语言表达状态。
func statusSnapshot(p *pet.Pet) string {
	return narrate.StatusBlock(p,
		fmt.Sprintf("%s（%s）", stageLabel(p.Stage), p.Stage),
		levelWord(p.Stats.Hunger), levelWord(p.Stats.Happy), levelWord(p.Stats.Clean),
		levelWord(p.Stats.Energy), levelWord(p.Stats.Health),
		moodPhrase(p))
}

// stageLabel 返回成长阶段的中文名。
func stageLabel(s pet.Stage) string {
	switch s {
	case pet.StageEgg:
		return "蛋"
	case pet.StageBaby:
		return "幼年"
	case pet.StageChild:
		return "成长期"
	case pet.StageAdult:
		return "成年"
	default:
		return string(s)
	}
}

// levelWord 把 0-100 的数值映射为定性词汇。
func levelWord(v float64) string {
	switch {
	case v >= 80:
		return "满满的"
	case v >= 60:
		return "还不错"
	case v >= 40:
		return "一般般"
	case v >= 20:
		return "有点低"
	case v > 0:
		return "快见底了"
	default:
		return "已经空了"
	}
}
