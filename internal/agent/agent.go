// Package agent 是 PetAgent 运行时：每只宠物惰性装配一个 ADK llmagent，
// 指令由 petfs 文件 + 实时状态快照动态拼装；LLM 不可用（未配置/调用失败）
// 时走基于 SOUL 模板的预置降级文案，保证 chat 永远有回应。
package agent

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/genai"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	adktool "google.golang.org/adk/v2/tool"

	"github.com/lalolv/PocketPet/internal/adkx"
	"github.com/lalolv/PocketPet/internal/config"
	"github.com/lalolv/PocketPet/internal/llm"
	"github.com/lalolv/PocketPet/internal/narrate"
	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
	"github.com/lalolv/PocketPet/internal/tick"
)

// runner 与会话的固定标识：每只宠物一个 runner、一条主会话（进程内工作记忆）。
const (
	appName     = "pocketpet"
	chatUser    = "master"
	chatSession = "main"
)

// 指令装配的截取上限（M2 简单实现，M3 由梦境整理接管记忆压缩）。
const (
	memoryExcerptRunes = 1500 // MEMORY.md 摘要截取（按字符，避免截断 UTF-8）
	journalListMax     = 10   // 指令中列出的近期日记条数
)

// Options 是 PetAgent 的可选装配项。
type Options struct {
	// SkillsDir 是全局技能目录（SKILL.md 技能包，对全部宠物可见）。
	SkillsDir string
	// MCPServers 是全局可用的 MCP server 声明；宠物在 AGENT.md 里按名启用。
	MCPServers []config.MCPServer

	// 以下为测试/定制接缝：nil 时使用生产实现。
	// ModelFactory 替换 model.LLM 的构造（测试注入 fake model 以验证装配结果）。
	ModelFactory func(ctx context.Context, cfg llm.Config) (adkmodel.LLM, error)
	// MCPTransport 替换 MCP server 的传输构造（默认 stdio CommandTransport）。
	MCPTransport func(spec config.MCPServer) (mcp.Transport, error)
	// ExtraTools 是注入全部宠物的全局工具（M5 插件体系，由 plugin.Registry.Tools 收集）。
	// 对所有宠物可见；工具内用 plugin.PetIDOf(ctx) 取当前宠物 ID（agent 名约定 pet_<id>）。
	ExtraTools []adktool.Tool
}

// PetAgent 管理全部宠物的 Agent 运行时（petID → runner，惰性创建，线程安全）。
type PetAgent struct {
	engine *tick.Engine
	fs     *petfs.FS
	cfg    llm.Config // 全局 LLM 配置（运行期可被替换，如测试注入假端点）
	opts   Options

	mu        sync.Mutex
	runners   map[string]*cachedRunner
	seq       map[string]int    // 每只宠物的降级文案轮换序号
	wakeNotes map[string]string // 每只宠物待注入指令的"睡醒便签"（单次对话有效）

	chatMu    sync.Mutex
	chatLocks map[string]*sync.Mutex // 每只宠物的对话串行锁（保护会话历史）
}

// cachedRunner 记录 runner、其 llmagent 及装配指纹；
// AGENT.md 变更（model/mcp 声明）导致指纹变化时自动重建。
// 技能不在指纹里：skilltoolset 每次请求实时读盘，新技能自动生效。
type cachedRunner struct {
	r           *runner.Runner
	ag          adkagent.Agent
	fingerprint string
}

// New 创建 PetAgent。cfg 为全局 LLM 配置（配置文件 llm 段）。
func New(eng *tick.Engine, fs *petfs.FS, cfg llm.Config, opts ...Options) *PetAgent {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	return &PetAgent{
		engine:    eng,
		fs:        fs,
		cfg:       cfg,
		opts:      o,
		runners:   make(map[string]*cachedRunner),
		seq:       make(map[string]int),
		wakeNotes: make(map[string]string),
		chatLocks: make(map[string]*sync.Mutex),
	}
}

// EnsureFiles 确保宠物拥有 petfs 文件（出生时未创建的存量宠物惰性补齐）。
// 已存在时直接返回 false；并发创建竞争时 ErrExists 视为成功。
func (a *PetAgent) EnsureFiles(p *pet.Pet) (bool, error) {
	if a.fs.Exists(p.ID) {
		return false, nil
	}
	_, err := a.fs.CreatePet(p.ID, petfs.Identity{
		Name: p.Name, Species: p.Species, Stage: string(p.Stage), BornAt: p.BornAt,
	})
	if err != nil && !errors.Is(err, petfs.ErrExists) {
		return false, err
	}
	return true, nil
}

// Chat 是与宠物对话的非流式入口，返回完整回复。
// LLM 不可用或调用失败时返回降级文案而非错误；error 只用于宠物不存在等前置失败。
func (a *PetAgent) Chat(ctx context.Context, petID, message string) (string, error) {
	var sb strings.Builder
	for chunk, err := range a.run(ctx, petID, message, false) {
		if err != nil {
			return "", err
		}
		sb.WriteString(chunk)
	}
	return sb.String(), nil
}

// ChatStream 是流式入口：逐段产出回复文本（供 SSE 转发）。
// 与 Chat 同样保证有回应——LLM 出错时改产降级文案，不向调用方抛 LLM 错误。
func (a *PetAgent) ChatStream(ctx context.Context, petID, message string) iter.Seq2[string, error] {
	return a.run(ctx, petID, message, true)
}

// run 是对话主流程：补算状态 → 惰性建 runner → 跑一轮对话 → 产出文本块。
func (a *PetAgent) run(ctx context.Context, petID, message string, streaming bool) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		unlock := a.lockChat(petID)
		defer unlock()

		p, err := a.engine.Settle(ctx, petID)
		if err != nil {
			yield("", err)
			return
		}
		if !p.Alive {
			yield(deadLine, nil)
			return
		}
		start := time.Now()
		slog.Info("agent: chat", "pet", petID, "streaming", streaming, "msg_runes", len([]rune(message)))
		if _, err := a.EnsureFiles(p); err != nil {
			// 文件缺失不阻断对话（指令装配会容忍缺文件），只记录。
			slog.Warn("agent: ensure petfs files failed", "pet", petID, "err", err)
		}
		// 醒来后的第一次对话：消费"睡醒便签"。仍客观困着（精力<预警）时不注入，
		// 避免「UI 显示困了、嘴上说刚醒做了梦」的撕裂。
		if !p.Sleeping && p.Stats.Energy >= pet.AlertWarn {
			if note, err := a.fs.TakeWakeNote(petID); err == nil && note != "" {
				a.mu.Lock()
				a.wakeNotes[petID] = note
				a.mu.Unlock()
				defer func() {
					a.mu.Lock()
					delete(a.wakeNotes, petID)
					a.mu.Unlock()
				}()
			}
		}

		r, err := a.runnerFor(ctx, p)
		if err != nil {
			if !errors.Is(err, llm.ErrNotConfigured) {
				slog.Warn("agent: build runner failed, use fallback", "pet", petID, "err", err)
			}
			a.yieldFallback(yield, p)
			return
		}

		mode := adkagent.StreamingModeNone
		if streaming {
			mode = adkagent.StreamingModeSSE
		}
		msg := &genai.Content{Role: "user", Parts: []*genai.Part{{Text: message}}}

		var finalText strings.Builder
		sawPartial := false
		var runErr error
		for ev, err := range r.r.Run(ctx, chatUser, chatSession, msg, adkagent.RunConfig{StreamingMode: mode}) {
			if err != nil {
				runErr = err
				break
			}
			// 部分 provider 把错误放在响应事件里而不是 Go error（如限流）。
			if ev != nil && ev.LLMResponse.ErrorMessage != "" {
				runErr = fmt.Errorf("llm error %s: %s", ev.LLMResponse.ErrorCode, ev.LLMResponse.ErrorMessage)
				break
			}
			text := adkx.EventText(ev)
			if text == "" {
				continue
			}
			if ev.LLMResponse.Partial {
				// 流式块：实时转发；完整的聚合事件随后还会来一次，只用于兜底。
				sawPartial = true
				if !yield(text, nil) {
					return
				}
			} else {
				finalText.WriteString(text)
			}
		}
		if runErr != nil {
			slog.Warn("agent: llm run failed, use fallback", "pet", petID, "err", runErr)
			a.yieldFallback(yield, p)
			return
		}
		slog.Debug("agent: llm reply", "pet", petID, "duration", time.Since(start).Round(time.Millisecond))
		// 非流式（或模型没产流式块）：回复来自最终聚合事件。
		if !sawPartial {
			if strings.TrimSpace(finalText.String()) == "" {
				a.yieldFallback(yield, p)
				return
			}
			yield(finalText.String(), nil)
		}
	}
}

// lockChat 返回 petID 对话锁的解锁函数，把同一宠物的对话串行化。
func (a *PetAgent) lockChat(id string) func() {
	a.chatMu.Lock()
	l, ok := a.chatLocks[id]
	if !ok {
		l = &sync.Mutex{}
		a.chatLocks[id] = l
	}
	a.chatMu.Unlock()
	l.Lock()
	return l.Unlock
}

// runnerFor 返回该宠物的缓存运行时，首次调用时装配：
// 全局配置 ← AGENT.md model 覆盖 → 构造 model.LLM
// → llmagent（动态指令 + 工具 + 工具集）→ runner。
func (a *PetAgent) runnerFor(ctx context.Context, p *pet.Pet) (*cachedRunner, error) {
	var spec petfs.AgentSpec
	if s, err := a.fs.AgentSpec(p.ID); err == nil {
		spec = s
	}
	// AGENT.md 可按宠物覆盖 model；a.cfg 可能在 New 之后被替换（测试注入假端点），
	// 因此每次现场基于当前 cfg 解析。
	cfg := a.cfg
	if spec.Model != "" {
		cfg.Model = spec.Model
	}
	fingerprint := cfg.Model + "|" + strings.Join(spec.MCPServers, ",")

	a.mu.Lock()
	defer a.mu.Unlock()
	if cr, ok := a.runners[p.ID]; ok && cr.fingerprint == fingerprint {
		return cr, nil
	}

	newModel := a.opts.ModelFactory
	if newModel == nil {
		newModel = llm.NewModel
	}
	m, err := newModel(ctx, cfg)
	if err != nil {
		return nil, err
	}
	tools, err := a.buildTools(p.ID)
	if err != nil {
		return nil, fmt.Errorf("build tools: %w", err)
	}
	toolsets, err := a.buildToolsets(ctx, p.ID, spec.MCPServers)
	if err != nil {
		return nil, fmt.Errorf("build toolsets: %w", err)
	}
	ag, err := llmagent.New(llmagent.Config{
		Name:        "pet_" + p.ID,
		Description: fmt.Sprintf("宠物 %s（%s）的第一人称对话 Agent", p.Name, p.Species),
		Model:       m,
		// 指令每次运行重建：文件内容 + 实时状态快照永远是最新的。
		InstructionProvider: func(rctx adkagent.ReadonlyContext) (string, error) {
			return a.assemble(rctx, p.ID)
		},
		Tools:    tools,
		Toolsets: toolsets,
	})
	if err != nil {
		return nil, fmt.Errorf("create llmagent: %w", err)
	}
	r, err := runner.New(runner.Config{
		AppName:           appName,
		Agent:             ag,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create runner: %w", err)
	}
	cr := &cachedRunner{r: r, ag: ag, fingerprint: fingerprint}
	a.runners[p.ID] = cr
	slog.Info("agent: runner built", "pet", p.ID, "model", cfg.Model,
		"mcp_servers", spec.MCPServers, "tools", len(tools))
	return cr, nil
}

// AgentFor 返回该宠物缓存的 llmagent，供 A2A 等其它通道复用同一份人格装配
// （指令、工具、技能、MCP 与 chat 完全一致；会话各自独立）。
func (a *PetAgent) AgentFor(ctx context.Context, petID string) (adkagent.Agent, error) {
	p, err := a.engine.Settle(ctx, petID)
	if err != nil {
		return nil, err
	}
	cr, err := a.runnerFor(ctx, p)
	if err != nil {
		return nil, err
	}
	return cr.ag, nil
}

// assemble 拼装 system instruction：行为准则 + 身份 + 性格 + 状态快照 + 记忆。
// 单个文件读取失败只跳过对应段落，不让对话整体失败。
func (a *PetAgent) assemble(ctx context.Context, petID string) (string, error) {
	var b strings.Builder
	write := func(title, content string) {
		if strings.TrimSpace(content) == "" {
			return
		}
		if title != "" {
			b.WriteString(title)
			b.WriteString("\n")
		}
		b.WriteString(strings.TrimSpace(content))
		b.WriteString("\n\n")
	}

	if s, err := a.fs.Read(petID, petfs.FileInstructions); err == nil {
		write("", s)
	}
	if s, err := a.fs.Read(petID, petfs.FilePET); err == nil {
		write("# 我是谁（PET.md）", s)
	}
	if s, err := a.fs.Read(petID, petfs.FileSOUL); err == nil {
		write("# 我的性格（SOUL.md）", s)
	}
	// 状态快照：Settle 触发补算后读取，保证是当前时刻的状态。
	if p, err := a.engine.Settle(ctx, petID); err == nil {
		write("# 我现在的状态", statusSnapshot(p))
		write("# 说话约束", narrate.ChatConstraint(p))
	}
	// 睡醒便签（仅醒来后的第一次对话存在）。
	a.mu.Lock()
	wakeNote := a.wakeNotes[petID]
	a.mu.Unlock()
	if wakeNote != "" {
		write("# 我刚睡醒", wakeNote+"\n如果主人提起，就用自己的方式自然地讲讲这一觉/这个梦。")
	}
	if s, err := a.fs.Read(petID, petfs.FileMemory); err == nil {
		write("# 我的长期记忆（摘要）", truncateRunes(s, memoryExcerptRunes))
	}
	if journals, err := a.fs.ListJournals(petID); err == nil && len(journals) > 0 {
		if len(journals) > journalListMax {
			journals = journals[len(journals)-journalListMax:]
		}
		var sb strings.Builder
		for _, j := range journals {
			sb.WriteString("- " + strings.TrimSuffix(j, ".md") + "\n")
		}
		sb.WriteString("（想看具体内容就用 recall 工具）")
		write("# 我近期写过的日记", sb.String())
	}
	return b.String(), nil
}

// yieldFallback 产出一条降级文案（保证 chat 永远有回应）。
func (a *PetAgent) yieldFallback(yield func(string, error) bool, p *pet.Pet) {
	yield(a.fallbackLine(p), nil)
}

// truncateRunes 按字符数截断，避免按字节截断 UTF-8。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "\n……（更多内容省略）"
}
