// Package tick 是 tick 引擎：按固定间隔对全部存活宠物结算衰减，
// 加载时按 LastTickAt 离线补算（带时长上限），并把领域事件落库、推送给订阅方。
// 每只宠物的操作经 petID 锁串行化，与 HTTP 层的并发访问安全。
package tick

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/store"
)

// EventSink 接收领域事件（如 SSE 推送 hub）。
type EventSink interface {
	Publish(e pet.Event)
}

// StateSink 接收宠物最新状态快照：每次结算/动作落库后调用，
// 供 SSE 状态帧等订阅方使用。实现方必须非阻塞（在 petID 锁内调用）。
type StateSink interface {
	PublishState(p *pet.Pet)
}

// MultiSink 把事件扇出给多个订阅方（如 SSE hub 与 petfs 阶段同步器），
// 让 Engine 保持单订阅方接口不变。
type MultiSink []EventSink

// Publish 实现 EventSink。
func (m MultiSink) Publish(e pet.Event) {
	for _, s := range m {
		if s != nil {
			s.Publish(e)
		}
	}
}

// TickHook 在每个 tick 周期结算全部宠物后被调用（插件扩展点，M5）。
// 实现方应快速返回，耗时操作请自行异步化。
type TickHook interface {
	OnTick(ctx context.Context, now time.Time)
}

// TraitsLoader 按宠物 ID 返回当前 SOUL 特质；返回中性值表示不修饰。
type TraitsLoader func(petID string) pet.Traits

// Engine 驱动宠物的周期结算与状态变更，是 pet/store 之上的协调层。
type Engine struct {
	store      *store.Store
	sink       EventSink // 可为 nil（仅落库不推送）
	stateSink  StateSink // 可为 nil（不推状态快照）
	traits     TraitsLoader
	interval   time.Duration
	offlineMax time.Duration
	clock      pet.Clock

	mu    sync.Mutex
	locks map[string]*sync.Mutex // 每只宠物一把锁
	hooks []TickHook             // 每周期结算后调用（启动前注册完毕）
}

// NewEngine 创建 tick 引擎。interval 为结算间隔，offlineMax 为离线补算时长上限。
func NewEngine(st *store.Store, sink EventSink, interval, offlineMax time.Duration, clock pet.Clock) *Engine {
	return &Engine{
		store:      st,
		sink:       sink,
		interval:   interval,
		offlineMax: offlineMax,
		clock:      clock,
		locks:      make(map[string]*sync.Mutex),
	}
}

// lock 返回 petID 对应的锁的解锁函数。
func (e *Engine) lock(id string) func() {
	e.mu.Lock()
	l, ok := e.locks[id]
	if !ok {
		l = &sync.Mutex{}
		e.locks[id] = l
	}
	e.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Run 启动周期结算循环，直到 ctx 取消。
func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(e.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.TickAll(ctx)
		}
	}
}

// AddTickHook 注册每周期结算后调用的钩子（插件用；应在 Run 前完成注册）。
func (e *Engine) AddTickHook(h TickHook) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.hooks = append(e.hooks, h)
}

// SetStateSink 注册状态快照订阅方（如 SSE hub；应在 Run 前完成设置）。
func (e *Engine) SetStateSink(s StateSink) { e.stateSink = s }

// SetTraitsLoader 注册 SOUL 特质加载器（G4 性格数值修饰；应在 Run 前设置）。
func (e *Engine) SetTraitsLoader(fn TraitsLoader) { e.traits = fn }

func (e *Engine) traitsOf(id string) pet.Traits {
	if e.traits != nil {
		return e.traits(id)
	}
	return pet.NeutralTraits()
}

// publishState 把最新状态快照推给订阅方（若有）。
func (e *Engine) publishState(p *pet.Pet) {
	if e.stateSink != nil {
		e.stateSink.PublishState(p)
	}
}

// TickAll 对全部存活宠物结算一次（Run 每周期调用；测试可直接调用）。
func (e *Engine) TickAll(ctx context.Context) {
	pets, err := e.store.ListPets(ctx)
	if err != nil {
		slog.Error("tick: list pets failed", "err", err)
		return
	}
	for _, p := range pets {
		if !p.Alive {
			continue
		}
		if _, err := e.settle(ctx, p.ID); err != nil {
			slog.Error("tick: settle failed", "pet", p.ID, "err", err)
		}
	}
	slog.Debug("tick: settled all pets", "pets", len(pets))
	e.mu.Lock()
	hooks := append([]TickHook(nil), e.hooks...)
	e.mu.Unlock()
	for _, h := range hooks {
		h.OnTick(ctx, e.clock.Now())
	}
}

// CreatePet 创建宠物并产生 pet.born 事件。
func (e *Engine) CreatePet(ctx context.Context, name, species string) (*pet.Pet, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	unlock := e.lock(id)
	defer unlock()

	p := pet.New(id, name, species, e.clock.Now())
	p.GenesisStatus = pet.GenesisReady
	if err := e.store.SavePet(ctx, p); err != nil {
		return nil, err
	}
	slog.Info("pet born", "pet", id, "name", name, "species", species)
	e.emit(ctx, []pet.Event{{PetID: id, Type: pet.EventBorn,
		Message: name + " 出生了，是一只 " + species, CreatedAt: e.clock.Now()}})
	e.publishState(p)
	return p, nil
}

// BeginBirth 创建一只孵化中的宠物蛋（不发 pet.born，等 MetaAgent finalize）。
func (e *Engine) BeginBirth(ctx context.Context, name, species string) (*pet.Pet, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	unlock := e.lock(id)
	defer unlock()

	if name == "" {
		name = "（破壳中）"
	}
	p := pet.New(id, name, species, e.clock.Now())
	p.GenesisStatus = pet.GenesisIncubating
	if err := e.store.SavePet(ctx, p); err != nil {
		return nil, err
	}
	slog.Info("pet incubating", "pet", id, "species", species)
	e.publishState(p)
	return p, nil
}

// FinalizeBirth 把孵化中的宠物提交为可互动状态：写入名字与初始 Stats，发 pet.born。
func (e *Engine) FinalizeBirth(ctx context.Context, id, name string, stats pet.Stats) (*pet.Pet, error) {
	unlock := e.lock(id)
	defer unlock()

	p, err := e.store.GetPet(ctx, id)
	if err != nil {
		return nil, err
	}
	if !p.Incubating() {
		return nil, pet.ErrNotIncubating
	}
	if name != "" {
		p.Name = name
	}
	p.ApplyBirthStats(stats)
	p.GenesisStatus = pet.GenesisReady
	if err := e.store.SavePet(ctx, p); err != nil {
		return nil, err
	}
	slog.Info("pet born", "pet", id, "name", p.Name, "species", p.Species, "via", "genesis")
	e.emit(ctx, []pet.Event{{PetID: id, Type: pet.EventBorn,
		Message: p.Name + " 出生了，是一只 " + p.Species, CreatedAt: e.clock.Now()}})
	e.publishState(p)
	return p, nil
}

// Settle 加载宠物并离线补算到当前时刻（含周期 tick 外的即时结算），返回最新状态。
func (e *Engine) Settle(ctx context.Context, id string) (*pet.Pet, error) {
	return e.settle(ctx, id)
}

// ListPets 返回全部宠物（读存储层，不触发补算；插件跨宠物访问用）。
func (e *Engine) ListPets(ctx context.Context) ([]*pet.Pet, error) {
	return e.store.ListPets(ctx)
}

func (e *Engine) settle(ctx context.Context, id string) (*pet.Pet, error) {
	unlock := e.lock(id)
	defer unlock()

	p, err := e.store.GetPet(ctx, id)
	if err != nil {
		return nil, err
	}
	tr := e.traitsOf(id)
	evs := p.TickTraits(e.clock.Now(), e.offlineMax, tr)
	if err := e.store.SavePet(ctx, p); err != nil {
		return nil, err
	}
	e.emit(ctx, evs)
	e.publishState(p)
	return p, nil
}

// Care 对宠物执行照顾动作：先补算衰减到当前时刻，再应用动作。
// 动作被领域规则拒绝时返回领域错误（此前结算的衰减仍然生效并保存）。
func (e *Engine) Care(ctx context.Context, id string, action pet.Action) (*pet.Pet, error) {
	unlock := e.lock(id)
	defer unlock()

	p, err := e.store.GetPet(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Incubating() {
		return nil, pet.ErrIncubating
	}
	now := e.clock.Now()
	tr := e.traitsOf(id)
	evs := p.TickTraits(now, e.offlineMax, tr)
	careEvs, err := p.CareTraits(action, now, tr)
	if err != nil {
		// 衰减部分已生效，保存后返回错误。
		_ = e.store.SavePet(ctx, p)
		e.emit(ctx, evs)
		e.publishState(p)
		return nil, err
	}
	if err := e.store.SavePet(ctx, p); err != nil {
		return nil, err
	}
	slog.Info("care action applied", "pet", id, "action", action)
	e.emit(ctx, append(evs, careEvs...))
	e.publishState(p)
	return p, nil
}

// Adjust 对宠物应用一次插件的确定性数值调整：先补算衰减，再加减并保存、推送事件。
// 与 Care 同级，走同一把 petID 锁与同一套领域规则。
func (e *Engine) Adjust(ctx context.Context, id string, delta pet.Stats) (*pet.Pet, error) {
	unlock := e.lock(id)
	defer unlock()

	p, err := e.store.GetPet(ctx, id)
	if err != nil {
		return nil, err
	}
	now := e.clock.Now()
	tr := e.traitsOf(id)
	evs := p.TickTraits(now, e.offlineMax, tr)
	evs = append(evs, p.Adjust(delta, now)...)
	if err := e.store.SavePet(ctx, p); err != nil {
		return nil, err
	}
	e.emit(ctx, evs)
	e.publishState(p)
	return p, nil
}

// Emit 把外部组件产生的领域事件（如梦境整理器的 pet.dream）落库并推送，
// 与引擎自身产生的事件走同一流水。
func (e *Engine) Emit(ctx context.Context, evs ...pet.Event) {
	e.emit(ctx, evs)
}

// emit 把事件写入 pet_events 表（回填 ID）并推送给订阅方。
func (e *Engine) emit(ctx context.Context, evs []pet.Event) {
	for _, ev := range evs {
		id, err := e.store.AppendEvent(ctx, ev)
		if err != nil {
			slog.Error("append event failed", "pet", ev.PetID, "type", ev.Type, "err", err)
			continue
		}
		ev.ID = id
		slog.Info("event", "pet", ev.PetID, "type", ev.Type, "msg", ev.Message)
		if e.sink != nil {
			e.sink.Publish(ev)
		}
	}
}

// newID 生成 16 字节随机 hex ID。
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
