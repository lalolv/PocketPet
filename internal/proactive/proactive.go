// Package proactive 是状态驱动的主动行为器：实现 tick.EventSink 与 tick.TickHook。
//
// 事件侧：宠物饿了/脏了/困了/病了/心情低落/刚睡醒时，由 LLM 生成第一人称
// 主动消息（pet.proactive 事件，经 Engine.Emit 落库 + SSE 推送）；
// 精力过低（pet.sleepy）时额外自动入睡。
// tick 侧：睡眠中精力恢复满后自动醒来。
//
// 主动消息依赖 LLM，未配置或调用失败时静默跳过（告警事件本身的固定文案仍会推送）；
// 自动入睡/醒来不依赖 LLM。自动喂食/清洁刻意不做——照顾是主人的事，
// 宠物只负责"开口说"。
package proactive

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/lalolv/PocketPet/internal/llm"
	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
	"github.com/lalolv/PocketPet/internal/store"
)

const (
	// wakeEnergyThreshold 是"睡饱了"的精力阈值：睡眠中的宠物精力恢复到
	// 该值以上时自动醒来（满值 100，约需 4 小时从 0 睡满）。
	wakeEnergyThreshold = 95.0
	// handleTimeout 是单次事件处理（LLM 消息 + 自动动作）的超时。
	handleTimeout = time.Minute
)

// CareEngine 是 Monitor 对 tick.Engine 的最小依赖（测试注入 fake）。
type CareEngine interface {
	Care(ctx context.Context, id string, action pet.Action) (*pet.Pet, error)
	ListPets(ctx context.Context) ([]*pet.Pet, error)
}

// Options 是主动行为的开关组（配置层默认值见 internal/config）。
type Options struct {
	Enabled   bool // 总开关：false 时 Monitor 完全不动作
	AutoSleep bool // pet.sleepy 时自动入睡
	AutoWake  bool // 睡眠中精力恢复到 wakeEnergyThreshold 后自动醒来
	Messages  bool // 告警/睡醒时生成 LLM 主动消息（需 LLM 配置）
}

// Monitor 订阅领域事件并驱动主动行为；实现 tick.EventSink 与 tick.TickHook。
type Monitor struct {
	st   *store.Store
	fs   *petfs.FS
	cfg  llm.Config
	opts Options

	// Engine 执行自动入睡/醒来；必须在启动前接线（main 里在 Engine 创建后设置，
	// 与 dream.Organizer.Emitter 同一模式——Monitor 本身是 Engine 的事件订阅方）。
	Engine CareEngine
	// Emitter 输出主动消息事件（落库 + SSE），通常接 tick.Engine.Emit。
	Emitter func(ctx context.Context, evs ...pet.Event)
	// Messager 生成主动消息文案；nil 时按 LLM 配置（全局 + AGENT.md model 覆盖）
	// 现场构造 LLMMessager，未配置则静默跳过消息。测试注入 fake。
	Messager Messager
	// Now 返回当前时间（事件时间戳）；nil 时用 time.Now。测试注入假时钟。
	Now func() time.Time

	mu      sync.Mutex
	pending map[string]bool // 每只宠物的"处理中"标志，防并发触发
}

// NewMonitor 创建主动行为器。Engine 与 Emitter 需在 Engine 创建后接线。
func NewMonitor(st *store.Store, fs *petfs.FS, cfg llm.Config, opts Options) *Monitor {
	return &Monitor{st: st, fs: fs, cfg: cfg, opts: opts, pending: make(map[string]bool)}
}

// Publish 实现 tick.EventSink。该方法在引擎的 petID 锁内被调用，必须即刻返回：
// 实际处理（LLM 调用、Care 动作会再次取同一把锁）全部异步，同宠物去重。
func (m *Monitor) Publish(e pet.Event) {
	if !m.opts.Enabled {
		return
	}
	switch e.Type {
	case pet.EventHungry, pet.EventDirty, pet.EventSleepy, pet.EventSick, pet.EventSad, pet.EventWokeUp:
		// 进入处理流程；各事件关联的动作由 handle 决定
	default:
		return
	}

	m.mu.Lock()
	if m.pending[e.PetID] {
		m.mu.Unlock()
		return
	}
	m.pending[e.PetID] = true
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.pending, e.PetID)
			m.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), handleTimeout)
		defer cancel()
		m.handle(ctx, e)
	}()
}

// handle 同步处理一条触发事件（测试可直接调用；生产由 Publish 异步触发）：
// 先生成并推送主动消息，再执行该事件关联的自动动作。
func (m *Monitor) handle(ctx context.Context, e pet.Event) {
	if m.opts.Messages {
		text, err := m.compose(ctx, e)
		if err != nil {
			slog.Warn("proactive: compose message failed, skip", "pet", e.PetID, "trigger", e.Type, "err", err)
		} else if text != "" {
			m.emit(ctx, pet.Event{PetID: e.PetID, Type: pet.EventProactive, Message: text, CreatedAt: m.now()})
		}
	}
	if e.Type == pet.EventSleepy && m.opts.AutoSleep {
		m.care(ctx, e.PetID, pet.ActionSleep)
	}
}

// OnTick 实现 tick.TickHook：每周期检查睡眠中的宠物是否睡饱，睡饱则自动醒来。
func (m *Monitor) OnTick(ctx context.Context, _ time.Time) {
	if !m.opts.Enabled || !m.opts.AutoWake || m.Engine == nil {
		return
	}
	pets, err := m.Engine.ListPets(ctx)
	if err != nil {
		slog.Warn("proactive: list pets failed", "err", err)
		return
	}
	for _, p := range pets {
		if p.Alive && p.Sleeping && p.Stats.Energy >= wakeEnergyThreshold {
			m.care(ctx, p.ID, pet.ActionWake)
		}
	}
}

// compose 生成一条主动消息；LLM 未配置时返回空串（静默跳过）。
func (m *Monitor) compose(ctx context.Context, e pet.Event) (string, error) {
	msger := m.Messager
	if msger == nil {
		cfg := m.resolveCfg(e.PetID)
		if !cfg.Configured() {
			slog.Debug("proactive: llm not configured, skip message", "pet", e.PetID, "trigger", e.Type)
			return "", nil
		}
		msger = &LLMMessager{Cfg: cfg}
	}

	p, err := m.st.GetPet(ctx, e.PetID)
	if err != nil {
		return "", err
	}
	if !p.Alive {
		return "", nil
	}
	req := ComposeRequest{
		Name: p.Name, Species: p.Species, Stage: string(p.Stage), Trigger: e.Type,
	}
	if s, err := m.fs.Read(e.PetID, petfs.FileSOUL); err == nil {
		req.Soul = s
	}
	if e.Type == pet.EventWokeUp {
		// 非消费式读取：便签仍留给醒来后的第一次对话。
		if note, err := m.fs.ReadWakeNote(e.PetID); err == nil {
			req.WakeNote = note
		}
	}
	return msger.Compose(ctx, req)
}

// care 执行一次自动照顾动作。领域拒绝（已在睡/已醒/已死亡）多为与
// 用户操作的正常竞争，降级为 debug；其余错误记 warn。
func (m *Monitor) care(ctx context.Context, petID string, action pet.Action) {
	if m.Engine == nil {
		return
	}
	_, err := m.Engine.Care(ctx, petID, action)
	switch {
	case err == nil:
		slog.Info("proactive: auto care applied", "pet", petID, "action", action)
	case errors.Is(err, pet.ErrAlreadySleeping), errors.Is(err, pet.ErrNotSleeping), errors.Is(err, pet.ErrDead):
		slog.Debug("proactive: auto care rejected", "pet", petID, "action", action, "err", err)
	default:
		slog.Warn("proactive: auto care failed", "pet", petID, "action", action, "err", err)
	}
}

// emit 经 Emitter 推送事件；未接线时静默丢弃。
func (m *Monitor) emit(ctx context.Context, evs ...pet.Event) {
	if m.Emitter != nil {
		m.Emitter(ctx, evs...)
	}
}

// resolveCfg 解析该宠物的有效 LLM 配置：全局配置 ← AGENT.md model 覆盖。
func (m *Monitor) resolveCfg(petID string) llm.Config {
	cfg := m.cfg
	if s, err := m.fs.AgentSpec(petID); err == nil && s.Model != "" {
		cfg.Model = s.Model
	}
	return cfg
}

func (m *Monitor) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}
