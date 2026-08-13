package proactive

import (
	"context"
	"fmt"
	"strings"

	"github.com/lalolv/PocketPet/internal/adkx"
	"github.com/lalolv/PocketPet/internal/llm"
	"github.com/lalolv/PocketPet/internal/narrate"
)

// Messager 是主动消息的文案生成抽象。生产实现为 LLMMessager（ADK）；测试注入 fake。
type Messager interface {
	Compose(ctx context.Context, req ComposeRequest) (string, error)
}

// ComposeRequest 是一条主动消息所需的全部输入。
type ComposeRequest struct {
	Name, Species, Stage string
	Soul                 string // SOUL.md 现状全文
	Trigger              string // 触发事件类型（pet.EventHungry 等）
	Frame                narrate.Frame
}

// LLMMessager 是 Messager 的生产实现：独立的一次性 llmagent（无工具、无历史），
// 不占用宠物的主对话会话——主动消息不污染聊天上下文。
type LLMMessager struct {
	Cfg llm.Config
}

// Compose 实现 Messager。Frame 不允许开口时返回空串。
func (r *LLMMessager) Compose(ctx context.Context, req ComposeRequest) (string, error) {
	if !req.Frame.MaySpeak {
		return "", nil
	}
	text, err := adkx.Ephemeral{
		Cfg:           r.Cfg,
		AppName:       "pocketpet-proactive",
		AgentName:     "proactive_messager",
		Description:   "宠物状态驱动的主动消息生成器",
		Instruction:   composeInstruction,
		UserID:        "pet",
		SessionPrefix: "proactive",
		Prompt:        buildComposePrompt(req),
	}.Run(ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

// composeInstruction 是消息生成器的 system prompt：角色 + 输出契约。
const composeInstruction = `你扮演一只虚拟宠物，正在用第一人称给主人发一条主动消息。

规则：
- 一两句话，简短自然，像发消息一样；
- 语气符合下面给出的身份与性格；
- 必须遵守「必须遵守的事实」，不得声称「禁止声称」里的内容；
- 直接输出消息正文：不加引号、不加"（动作）"式舞台描述、不解释原因、不要自称名字。`

// buildComposePrompt 把消息输入装配为 user prompt。
func buildComposePrompt(req ComposeRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 宠物身份\n名字：%s；物种：%s；成长阶段：%s\n\n", req.Name, req.Species, req.Stage)
	b.WriteString("# 性格（SOUL.md）\n")
	if s := strings.TrimSpace(req.Soul); s != "" {
		b.WriteString(s + "\n\n")
	} else {
		b.WriteString("（空）\n\n")
	}
	b.WriteString(req.Frame.PromptBlock())
	return b.String()
}
