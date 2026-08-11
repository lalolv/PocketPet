// Package adkx 提供 ADK llmagent 的薄脚手架：事件文本提取、一次性 runner。
//
// 各用例包（agent / metaagent / dream / proactive）按领域职责拆分；
// 共享的是装配样板，不是把它们收编到同一个 agents/ 目录。
package adkx

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/genai"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"

	"github.com/lalolv/PocketPet/internal/llm"
)

// EventText 提取事件中的可见文本（跳过思考部件与工具调用/响应）。
func EventText(ev *session.Event) string {
	if ev == nil || ev.Content == nil {
		return ""
	}
	var sb strings.Builder
	for _, part := range ev.Content.Parts {
		if part == nil || part.Text == "" || part.Thought {
			continue
		}
		if part.FunctionCall != nil || part.FunctionResponse != nil {
			continue
		}
		sb.WriteString(part.Text)
	}
	return sb.String()
}

// Ephemeral 描述一次无历史、无工具的 llmagent 调用（梦境整理、主动消息等）。
type Ephemeral struct {
	Cfg           llm.Config
	AppName       string
	AgentName     string
	Description   string
	Instruction   string
	UserID        string
	SessionPrefix string // 会话 ID 前缀；空则用 AgentName
	Prompt        string
	// ModelFactory 可选；nil 时用 llm.NewModel。
	ModelFactory func(ctx context.Context, cfg llm.Config) (adkmodel.LLM, error)
}

// Run 执行一次性调用，返回拼接后的可见文本。
func (e Ephemeral) Run(ctx context.Context) (string, error) {
	newModel := e.ModelFactory
	if newModel == nil {
		newModel = llm.NewModel
	}
	m, err := newModel(ctx, e.Cfg)
	if err != nil {
		return "", err
	}
	ag, err := llmagent.New(llmagent.Config{
		Name:        e.AgentName,
		Description: e.Description,
		Model:       m,
		Instruction: e.Instruction,
	})
	if err != nil {
		return "", fmt.Errorf("create agent %s: %w", e.AgentName, err)
	}
	rn, err := runner.New(runner.Config{
		AppName:           e.AppName,
		Agent:             ag,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		return "", fmt.Errorf("create runner %s: %w", e.AppName, err)
	}

	prefix := e.SessionPrefix
	if prefix == "" {
		prefix = e.AgentName
	}
	userID := e.UserID
	if userID == "" {
		userID = "system"
	}
	sessionID := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	msg := &genai.Content{Role: "user", Parts: []*genai.Part{{Text: e.Prompt}}}

	var sb strings.Builder
	for ev, err := range rn.Run(ctx, userID, sessionID, msg, adkagent.RunConfig{}) {
		if err != nil {
			return "", err
		}
		sb.WriteString(EventText(ev))
	}
	return sb.String(), nil
}
