package metaagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/genai"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"

	"github.com/lalolv/PocketPet/internal/llm"
)

// 默认诞生超时（设计文档建议 90s）。
const defaultBirthTimeout = 90 * time.Second

// ModelFactory 构造 model.LLM；测试可注入。nil 时用 llm.NewModel。
type ModelFactory func(ctx context.Context, cfg llm.Config) (adkmodel.LLM, error)

// RunBirth 执行一次诞生：优先 LLM MetaAgent，未配置或 ForceScript 时走脚本。
// 失败/超时则 EnsureComplete（fallback）。
func (m *Midwife) RunBirth(ctx context.Context, petID string) error {
	w, err := m.loadWorkshop(petID)
	if err != nil {
		return err
	}
	if w.draft.has(StageFinalized) {
		return nil
	}

	timeout := m.BirthTimeout
	if timeout <= 0 {
		timeout = defaultBirthTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	useLLM := !m.ForceScript && m.LLM.Configured()
	if useLLM {
		err = m.runLLM(runCtx, w)
		if err == nil && w.draft.has(StageFinalized) {
			return nil
		}
		reason := "incomplete after LLM run"
		if err != nil {
			reason = err.Error()
			slog.Warn("metaagent: LLM birth failed, falling back", "pet", petID, "err", err)
		} else {
			slog.Warn("metaagent: LLM finished without finalize, falling back", "pet", petID)
		}
		m.emitFailed(ctx, petID, reason, true)

		// finalize 成功会删草稿；仅在仍有草稿时 fallback。
		if _, loadErr := m.FS.LoadGenesisDraft(petID); loadErr != nil {
			if w.draft.has(StageFinalized) {
				return nil
			}
			return fmt.Errorf("metaagent: LLM failed and draft missing: %v", err)
		}
		w, err = m.loadWorkshop(petID)
		if err != nil {
			return err
		}
		res := w.EnsureComplete(ctx)
		if !res.OK {
			return fmt.Errorf("metaagent: fallback after LLM: %s", res.Error)
		}
		return nil
	}

	return m.RunScript(runCtx, petID)
}

func (m *Midwife) runLLM(ctx context.Context, w *Workshop) error {
	newModel := m.ModelFactory
	if newModel == nil {
		newModel = llm.NewModel
	}
	model, err := newModel(ctx, m.LLM)
	if err != nil {
		return err
	}
	tools, err := w.buildADKTools()
	if err != nil {
		return fmt.Errorf("build tools: %w", err)
	}
	ag, err := llmagent.New(llmagent.Config{
		Name:        "metaagent",
		Description: "PocketPet 造物主：分阶段孵化宠物",
		Model:       model,
		Instruction: metaInstruction,
		Tools:       tools,
	})
	if err != nil {
		return fmt.Errorf("create metaagent: %w", err)
	}
	rn, err := runner.New(runner.Config{
		AppName:           "pocketpet-genesis",
		Agent:             ag,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		return fmt.Errorf("create runner: %w", err)
	}

	sessionID := fmt.Sprintf("birth-%s-%d", w.draft.PetID, m.now().UnixNano())
	msg := &genai.Content{Role: "user", Parts: []*genai.Part{{Text: buildUserPrompt(w.draft)}}}
	slog.Info("metaagent: LLM birth start", "pet", w.draft.PetID, "mode", w.draft.Mode, "model", m.LLM.Model)

	var narrBuf strings.Builder
	flushNarr := func() {
		text := strings.TrimSpace(narrBuf.String())
		narrBuf.Reset()
		if text == "" {
			return
		}
		if utf8.RuneCountInString(text) > 200 {
			text = string([]rune(text)[:200]) + "…"
		}
		w.Narrate(ctx, "meta", text)
	}

	for ev, err := range rn.Run(ctx, "creator", sessionID, msg, adkagent.RunConfig{}) {
		if err != nil {
			flushNarr()
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("birth timeout: %w", err)
			}
			return err
		}
		if text := eventPlainText(ev); text != "" {
			narrBuf.WriteString(text)
		}
		// 工具在同一 Workshop 上改 draft；finalize 后文件会删，以内存标志为准。
		if w.draft.has(StageFinalized) {
			flushNarr()
			slog.Info("metaagent: LLM birth ready", "pet", w.draft.PetID, "name", w.draft.Name)
			return nil
		}
	}
	flushNarr()
	return nil
}

// eventPlainText 提取事件中的可见文本（跳过思考与工具部件）。
func eventPlainText(ev *session.Event) string {
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
