package proactive

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/genai"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"

	"github.com/lalolv/PocketPet/internal/llm"
	"github.com/lalolv/PocketPet/internal/pet"
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
	WakeNote             string // 仅 Trigger == pet.woke_up：睡醒便签内容（可含梦境摘要）
}

// LLMMessager 是 Messager 的生产实现：独立的一次性 llmagent（无工具、无历史），
// 不占用宠物的主对话会话——主动消息不污染聊天上下文。
type LLMMessager struct {
	Cfg llm.Config
}

// Compose 实现 Messager。未知触发类型返回空串。
func (r *LLMMessager) Compose(ctx context.Context, req ComposeRequest) (string, error) {
	reason := reasonFor(req)
	if reason == "" {
		return "", nil
	}
	m, err := llm.NewModel(ctx, r.Cfg)
	if err != nil {
		return "", err
	}
	ag, err := llmagent.New(llmagent.Config{
		Name:        "proactive_messager",
		Description: "宠物状态驱动的主动消息生成器",
		Model:       m,
		Instruction: composeInstruction,
	})
	if err != nil {
		return "", fmt.Errorf("create messager agent: %w", err)
	}
	rn, err := runner.New(runner.Config{
		AppName:           "pocketpet-proactive",
		Agent:             ag,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		return "", fmt.Errorf("create messager runner: %w", err)
	}

	// 一次性调用：每条消息用全新 session，不带历史。
	sessionID := fmt.Sprintf("proactive-%d", time.Now().UnixNano())
	msg := &genai.Content{Role: "user", Parts: []*genai.Part{{Text: buildComposePrompt(req, reason)}}}
	var sb strings.Builder
	for ev, err := range rn.Run(ctx, "pet", sessionID, msg, adkagent.RunConfig{}) {
		if err != nil {
			return "", err
		}
		sb.WriteString(eventText(ev))
	}
	return strings.TrimSpace(sb.String()), nil
}

// composeInstruction 是消息生成器的 system prompt：角色 + 输出契约。
const composeInstruction = `你扮演一只虚拟宠物，正在用第一人称给主人发一条主动消息。

规则：
- 一两句话，简短自然，像发消息一样；
- 语气符合下面给出的身份与性格；
- 直接输出消息正文：不加引号、不加"（动作）"式舞台描述、不解释原因、不要自称名字。`

// buildComposePrompt 把消息输入装配为 user prompt。
func buildComposePrompt(req ComposeRequest, reason string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 宠物身份\n名字：%s；物种：%s；成长阶段：%s\n\n", req.Name, req.Species, req.Stage)
	b.WriteString("# 性格（SOUL.md）\n")
	if s := strings.TrimSpace(req.Soul); s != "" {
		b.WriteString(s + "\n\n")
	} else {
		b.WriteString("（空）\n\n")
	}
	b.WriteString("# 发生了什么\n" + reason + "\n")
	return b.String()
}

// reasonFor 把触发事件映射为给 LLM 的情境描述；未知事件返回空串。
func reasonFor(req ComposeRequest) string {
	switch req.Trigger {
	case pet.EventHungry:
		return "你饿得肚子咕咕叫。跟主人说你想吃东西了。"
	case pet.EventDirty:
		return "你身上脏兮兮的，很不舒服。提醒主人该给你洗澡了。"
	case pet.EventSleepy:
		return "你困得睁不开眼，马上要去睡觉了。睡前跟主人说一声。"
	case pet.EventSick:
		return "你生病了，浑身难受。告诉主人你需要照顾。"
	case pet.EventSad:
		return "你心情低落，有点孤单。撒撒娇，让主人陪陪你。"
	case pet.EventWokeUp:
		s := "你刚睡醒，精神饱满。跟主人打个招呼。"
		if note := strings.TrimSpace(req.WakeNote); note != "" {
			s += "\n这一觉的情况：" + note + "（可以自然地提起这一觉或梦里的事。）"
		}
		return s
	}
	return ""
}

// eventText 提取事件中的文本（跳过思考部件与工具调用/响应）。
func eventText(ev *session.Event) string {
	if ev == nil || ev.Content == nil {
		return ""
	}
	var sb strings.Builder
	for _, part := range ev.Content.Parts {
		if part != nil && part.Text != "" && !part.Thought {
			sb.WriteString(part.Text)
		}
	}
	return sb.String()
}
