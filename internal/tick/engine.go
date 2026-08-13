// Package tick 是 tick 引擎：按固定间隔对全部存活宠物结算衰减，
// 加载时按 LastTickAt 离线补算（带时长上限），并把领域事件落库、推送给订阅方。
// 每只宠物的操作经 petID 锁串行化，与 HTTP 层的并发访问安全。
package tick

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petstate"
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
// 实现方应快速返回（建议 <100ms）；耗时操作请自行异步化或分批，
// 否则会拉长全局 tick（Registry 会对慢钩子打 Warn）。
type TickHook interface {
	OnTick(ctx context.Context, now time.Time)
}

// TraitsLoader 按宠物 ID 返回当前 SOUL 特质；返回中性值表示不修饰。
type TraitsLoader func(petID string) pet.Traits

// AfterMutateFunc 在 Care/Adjust 等写路径成功落库后调用（已释放 pet 锁），供 AutoSleep 等即时补救。
type AfterMutateFunc func(ctx context.Context, p *pet.Pet)

// Engine 驱动宠物的周期结算与状态变更，是 pet/store 之上的协调层。
// 活动态互斥委托 petstate.Manager（docs/07）。
type Engine struct {
	store      *store.Store
	sink       EventSink // 可为 nil（仅落库不推送）
	stateSink  StateSink // 可为 nil（不推状态快照）
	traits     TraitsLoader
	interval   time.Duration
	offlineMax time.Duration
	clock      pet.Clock
	state      *petstate.Manager

	mu          sync.Mutex
	locks       map[string]*sync.Mutex // 每只宠物一把锁
	hooks       []TickHook             // 每周期结算后调用（启动前注册完毕）
	afterMutate []AfterMutateFunc
}

// NewEngine 创建 tick 引擎。interval 为结算间隔，offlineMax 为离线补算时长上限。
func NewEngine(st *store.Store, sink EventSink, interval, offlineMax time.Duration, clock pet.Clock) *Engine {
	e := &Engine{
		store:      st,
		sink:       sink,
		interval:   interval,
		offlineMax: offlineMax,
		clock:      clock,
		locks:      make(map[string]*sync.Mutex),
	}
	e.state = petstate.New(e)
	return e
}

// State 返回统一状态管理器（插件注册 Kind / Apply）。
func (e *Engine) State() *petstate.Manager { return e.state }

// RequestSleep 供 proactive 调用：立刻睡或排队意图。
func (e *Engine) RequestSleep(ctx context.Context, id, reason string) (petstate.Result, error) {
	res, err := e.state.RequestSleep(ctx, id, reason)
	if err != nil {
		return res, err
	}
	if res.Err != nil {
		if errors.Is(res.Err, pet.ErrAlready) {
			res.Err = pet.ErrAlreadySleeping
		}
		return res, nil
	}
	return res, nil
}

// --- petstate.Host ---

func (e *Engine) LockPet(id string) func() { return e.lock(id) }

func (e *Engine) LoadPet(ctx context.Context, id string) (*pet.Pet, error) {
	return e.store.GetPet(ctx, id)
}

func (e *Engine) SavePet(ctx context.Context, p *pet.Pet) error {
	p.SyncSleepingFromActivity()
	return e.store.SavePet(ctx, p)
}

func (e *Engine) TraitsOf(id string) pet.Traits { return e.traitsOf(id) }

func (e *Engine) Now() time.Time { return e.clock.Now() }

func (e *Engine) OfflineMax() time.Duration { return e.offlineMax }

// Emit 实现 petstate.Host（公开 Emit 仍保留）。
func (e *Engine) Emit(ctx context.Context, evs ...pet.Event) {
	e.emit(ctx, evs)
}

func (e *Engine) PublishState(p *pet.Pet) { e.publishState(p) }

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

// Run 启动周期结算循环，直到 ctx 取消。启动时立即 TickAll 一次，避免重启后空等一个 interval。
func (e *Engine) Run(ctx context.Context) {
	e.TickAll(ctx)
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

// AddAfterMutate 注册 Care/Adjust 成功后的回调（应在 Run 前注册；回调内可再调 Engine，勿假设持锁）。
func (e *Engine) AddAfterMutate(fn AfterMutateFunc) {
	if fn == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.afterMutate = append(e.afterMutate, fn)
}

func (e *Engine) runAfterMutate(ctx context.Context, p *pet.Pet) {
	if p == nil {
		return
	}
	e.mu.Lock()
	fns := append([]AfterMutateFunc(nil), e.afterMutate...)
	e.mu.Unlock()
	for _, fn := range fns {
		fn(ctx, p)
	}
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
	p.SyncSleepingFromActivity()
	tr := e.traitsOf(id)
	evs := p.TickTraits(e.clock.Now(), e.offlineMax, tr)
	p.SyncSleepingFromActivity()
	if err := e.store.SavePet(ctx, p); err != nil {
		return nil, err
	}
	e.emit(ctx, evs)
	e.publishState(p)
	return p, nil
}

// Care 对宠物执行照顾动作：先补算衰减到当前时刻，再应用动作。
// sleep/wake 走 petstate.Manager（活动互斥 + 意图排队）。
func (e *Engine) Care(ctx context.Context, id string, action pet.Action) (*pet.Pet, error) {
	switch action {
	case pet.ActionSleep:
		res, err := e.state.RequestSleep(ctx, id, "care")
		if err != nil {
			return nil, err
		}
		p, loadErr := e.store.GetPet(ctx, id)
		if loadErr != nil {
			return nil, loadErr
		}
		if res.Err != nil {
			if errors.Is(res.Err, pet.ErrAlready) {
				return p, pet.ErrAlreadySleeping
			}
			return p, res.Err
		}
		if res.Queued {
			return p, pet.ErrBusy
		}
		slog.Info("care action applied", "pet", id, "action", action)
		return p, nil
	case pet.ActionWake:
		res, err := e.state.Apply(ctx, id, petstate.Transition{
			To: pet.ActivityIdle, Owner: "core", Reason: "wake",
		})
		if err != nil {
			return nil, err
		}
		p, loadErr := e.store.GetPet(ctx, id)
		if loadErr != nil {
			return nil, loadErr
		}
		if res.Err != nil {
			if errors.Is(res.Err, pet.ErrAlready) && !p.Sleeping {
				return p, pet.ErrNotSleeping
			}
			return p, res.Err
		}
		slog.Info("care action applied", "pet", id, "action", action)
		return p, nil
	}

	unlock := e.lock(id)

	p, err := e.store.GetPet(ctx, id)
	if err != nil {
		unlock()
		return nil, err
	}
	p.SyncSleepingFromActivity()
	if p.Incubating() {
		unlock()
		return nil, pet.ErrIncubating
	}
	now := e.clock.Now()
	tr := e.traitsOf(id)
	evs := p.TickTraits(now, e.offlineMax, tr)
	careEvs, err := p.CareTraits(action, now, tr)
	if err != nil {
		_ = e.store.SavePet(ctx, p)
		e.emit(ctx, evs)
		e.publishState(p)
		unlock()
		return nil, err
	}
	p.SyncSleepingFromActivity()
	if err := e.store.SavePet(ctx, p); err != nil {
		unlock()
		return nil, err
	}
	slog.Info("care action applied", "pet", id, "action", action)
	e.emit(ctx, append(evs, careEvs...))
	e.publishState(p)
	unlock()
	e.runAfterMutate(ctx, p)
	return p, nil
}

// PeekActivity 根据持久化 Activity 返回统一活动态（不补算、不取锁）。
func (e *Engine) PeekActivity(p *pet.Pet) string {
	if p == nil || !p.Alive {
		return pet.ActivityIdle
	}
	p.SyncSleepingFromActivity()
	if p.Activity == "" {
		return pet.ActivityIdle
	}
	return p.Activity
}

// Activity 补算后返回统一活动态。
func (e *Engine) Activity(ctx context.Context, id string) (string, error) {
	snap, err := e.state.View(ctx, id)
	if err != nil {
		return "", err
	}
	return snap.Activity.Kind, nil
}

// Adjust 对宠物应用一次插件的确定性数值调整：先补算衰减，再加减并保存、推送事件。
// 与 Care 同级，走同一把 petID 锁与同一套领域规则。
func (e *Engine) Adjust(ctx context.Context, id string, delta pet.Stats) (*pet.Pet, error) {
	unlock := e.lock(id)

	p, err := e.store.GetPet(ctx, id)
	if err != nil {
		unlock()
		return nil, err
	}
	now := e.clock.Now()
	tr := e.traitsOf(id)
	evs := p.TickTraits(now, e.offlineMax, tr)
	evs = append(evs, p.Adjust(delta, now)...)
	if err := e.store.SavePet(ctx, p); err != nil {
		unlock()
		return nil, err
	}
	e.emit(ctx, evs)
	e.publishState(p)
	unlock()
	e.runAfterMutate(ctx, p)
	return p, nil
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
