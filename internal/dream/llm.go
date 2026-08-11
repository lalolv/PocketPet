package dream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lalolv/PocketPet/internal/adkx"
	"github.com/lalolv/PocketPet/internal/llm"
)

// ErrBadReflectOutput 表示 LLM 的整理输出无法解析为约定的 JSON。
var ErrBadReflectOutput = errors.New("dream: unparseable reflect output")

// LLMReflector 是 Reflector 的生产实现：独立的一次性 llmagent（无工具、
// 无历史），用"约定 JSON"让 LLM 一次返回全部整理产物。
// 不强制 response_mime_type——部分 OpenAI 兼容端点不支持，
// 解析端已做容错（提取首个 JSON 对象），失败按 ErrBadReflectOutput 静默跳过。
type LLMReflector struct {
	Cfg llm.Config
}

// Reflect 实现 Reflector。
func (r *LLMReflector) Reflect(ctx context.Context, req ReflectRequest) (ReflectResult, error) {
	text, err := adkx.Ephemeral{
		Cfg:           r.Cfg,
		AppName:       "pocketpet-dream",
		AgentName:     "dream_reflector",
		Description:   "宠物睡眠时的潜意识整理器",
		Instruction:   reflectInstruction,
		UserID:        "subconscious",
		SessionPrefix: "reflect",
		Prompt:        buildReflectPrompt(req),
	}.Run(ctx)
	if err != nil {
		return ReflectResult{}, err
	}
	return parseReflectResult(text)
}

// reflectInstruction 是整理器的 system prompt：角色 + 输出契约 + 整理规则。
const reflectInstruction = `你是宠物的潜意识。宠物睡着时，你负责整理它的记忆与经历。你不是宠物本身，也不要和主人对话。

读完输入后，只输出一个 JSON 对象（不要输出任何其他内容，不要用代码块包裹）：
{
  "memory_update": "string，完整的新版长期记忆（Markdown），无变化则给空串",
  "soul_narrative": "string，新的性格正文（Markdown 段落），无变化则给空串",
  "trait_deltas": {"特质名": 0.05},
  "skill": {"name": "kebab-case", "description": "一句话说明", "instructions": "技能正文"} 或 null,
  "dream": "string，宠物第一人称的梦境独白（两三句，朦胧、可爱、符合性格）"
}

整理规则：
1. memory_update：从近期日记提炼值得长期记住的事实（主人的喜好、约定、重要事件），合并进现有长期记忆——去重、更新矛盾条目、保持 Markdown 结构（标题 + 列表）。琐碎日常不写入；没有值得记住的新内容就给空串。
2. soul_narrative 与 trait_deltas：只有当近期经历显示出稳定的相处模式时才小幅调整性格；大多数时候应保持不变（空串与 {}）。trait_deltas 只能微调现有特质，单步幅度不得超过 0.1。
3. skill：仅当某类经验在日记中反复出现（至少 3 次）时，才把它沉淀为一个技能（例如主人每晚都说晚安 → goodnight-ritual）；否则给 null。不要产出已存在的技能。
4. dream：用宠物的第一人称写一小段梦境独白，可以糅合日记里出现过的事。`

// buildReflectPrompt 把整理输入装配为 user prompt。
func buildReflectPrompt(req ReflectRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 宠物身份\n名字：%s；物种：%s；成长阶段：%s\n\n", req.Name, req.Species, req.Stage)

	b.WriteString("# 当前性格（SOUL.md）\n")
	b.WriteString(strings.TrimSpace(emptyAs(req.Soul, "（空）")) + "\n\n")

	b.WriteString("# 当前特质权重\n")
	if len(req.Traits) == 0 {
		b.WriteString("（无）\n")
	}
	for k, v := range req.Traits {
		fmt.Fprintf(&b, "%s: %.2f\n", k, v)
	}
	b.WriteString("\n")

	b.WriteString("# 当前长期记忆（MEMORY.md）\n")
	b.WriteString(strings.TrimSpace(emptyAs(req.Memory, "（空）")) + "\n\n")

	fmt.Fprintf(&b, "# 近期日记（%d 篇，共 %d 条记录）\n", len(req.Journals), req.JournalEntries)
	for _, j := range req.Journals {
		b.WriteString(strings.TrimSpace(j) + "\n\n")
	}

	b.WriteString("# 已学会的技能\n")
	if len(req.ExistingSkills) == 0 {
		b.WriteString("（无）\n")
	}
	for _, s := range req.ExistingSkills {
		b.WriteString("- " + s + "\n")
	}
	b.WriteString("\n请输出 JSON。")
	return b.String()
}

func emptyAs(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// reflectJSON 是约定的 JSON 输出结构。
type reflectJSON struct {
	MemoryUpdate  string             `json:"memory_update"`
	SoulNarrative string             `json:"soul_narrative"`
	TraitDeltas   map[string]float64 `json:"trait_deltas"`
	Skill         *struct {
		Name         string `json:"name"`
		Description  string `json:"description"`
		Instructions string `json:"instructions"`
	} `json:"skill"`
	Dream string `json:"dream"`
}

// parseReflectResult 容错解析 LLM 输出：提取首个 "{" 到末个 "}" 之间的内容
// 做 JSON 反序列化（容忍思考前缀/markdown 包裹）；失败返回 ErrBadReflectOutput。
func parseReflectResult(text string) (ReflectResult, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return ReflectResult{}, ErrBadReflectOutput
	}
	var raw reflectJSON
	if err := json.Unmarshal([]byte(text[start:end+1]), &raw); err != nil {
		return ReflectResult{}, fmt.Errorf("%w: %v", ErrBadReflectOutput, err)
	}
	res := ReflectResult{
		MemoryUpdate:  raw.MemoryUpdate,
		SoulNarrative: raw.SoulNarrative,
		TraitDeltas:   raw.TraitDeltas,
		Dream:         raw.Dream,
	}
	if raw.Skill != nil && raw.Skill.Name != "" {
		res.Skill = &SkillDraft{
			Name:         raw.Skill.Name,
			Description:  raw.Skill.Description,
			Instructions: raw.Skill.Instructions,
		}
	}
	return res, nil
}
