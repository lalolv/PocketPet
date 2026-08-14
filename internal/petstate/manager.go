// Package petstate 是统一宠物状态管理器：活动态互斥、意图排队、插件 Kind 注册。
// 所有活动切换经 Manager.Apply；详见 docs/07-宠物状态管理器.md。
package petstate

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lalolv/PocketPet/internal/pet"
)

// Life 是生命周期三态。
type Life string

const (
	LifeIncubating Life = "incubating"
	LifeAlive      Life = "alive"
	LifeDead       Life = "dead"
)

// Conditions 是可叠加条件态快照。
type Conditions struct {
	Hungry bool
	Dirty  bool
	Sleepy bool
	Sick   bool
	Sad    bool
}

// Activity 是当前互斥活动。
type Activity struct {
	Kind  string
	Owner string
}

// Snapshot 是对外唯一可读真相（一次锁内拷贝）。
type Snapshot struct {
	Life       Life
	Stage      pet.Stage
	Activity   Activity
	Conditions Conditions
	Intents    []string
	Stats      pet.Stats
	Seq        uint64
	Sleeping   bool
	Alive      bool
	Name       string
	ID         string
}

// Guard 是额外准入条件。
type Guard func(before Snapshot) error

// Transition 是 Apply 的入参。
type Transition struct {
	To       string // 目标 Activity.Kind；空表示只改意图
	Owner    string // 插件名；内核用 "core"
	Reason   string
	Guards   []Guard
	OnCommit func(ctx context.Context, before, after Snapshot) error
	// QueueIfBusy：目标忙时把 Intent 入队而非返回 Busy（需设 IntentKind）。
	QueueIfBusy bool
	IntentKind  string    // 如 pet.IntentSleep
	StatsDelta  pet.Stats // 同锁内 Adjust；失败随活动一并回滚
}

// Result 是 Apply 结果。
type Result struct {
	OK       bool
	Snapshot Snapshot
	Err      error
	Queued   bool
}

// ActivityKind 是插件注册的活动类型。
type ActivityKind struct {
	Name       string
	Owner      string
	CanEnter   func(before Snapshot) error
	AfterLeave func(ctx context.Context, id, reason string) error
}

// Host 是 Manager 对引擎/存储的依赖（由 tick.Engine 实现）。
type Host interface {
	LockPet(id string) (unlock func())
	LoadPet(ctx context.Context, id string) (*pet.Pet, error)
	SavePet(ctx context.Context, p *pet.Pet) error
	TraitsOf(id string) pet.Traits
	Now() time.Time
	OfflineMax() time.Duration
	Emit(ctx context.Context, evs ...pet.Event)
	PublishState(p *pet.Pet)
}

// Manager 是统一状态写入口。
type Manager struct {
	host Host

	mu    sync.RWMutex
	kinds map[string]ActivityKind // kind name → def
}

// New 创建 Manager。
func New(host Host) *Manager {
	return &Manager{host: host, kinds: make(map[string]ActivityKind)}
}

// RegisterKind 注册插件活动类型（Init 期调用）。
func (m *Manager) RegisterKind(k ActivityKind) error {
	if k.Name == "" || k.Name == pet.ActivityIdle || k.Name == pet.ActivitySleeping {
		return fmt.Errorf("petstate: invalid kind %q", k.Name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kinds[k.Name] = k
	return nil
}

func (m *Manager) kind(name string) (ActivityKind, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	k, ok := m.kinds[name]
	return k, ok
}

// SnapshotOf 从 Pet 构造快照（不取锁）。
func SnapshotOf(p *pet.Pet) Snapshot {
	if p == nil {
		return Snapshot{}
	}
	p.SyncSleepingFromActivity()
	life := LifeAlive
	if !p.Alive {
		life = LifeDead
	} else if p.Incubating() {
		life = LifeIncubating
	}
	act := p.Activity
	if act == "" {
		act = pet.ActivityIdle
	}
	intents := append([]string(nil), p.Intents...)
	return Snapshot{
		ID: p.ID, Name: p.Name, Life: life, Stage: p.Stage,
		Activity: Activity{Kind: act, Owner: p.ActivityOwner},
		Conditions: Conditions{
			Hungry: p.Alerts.Hungry, Dirty: p.Alerts.Dirty, Sleepy: p.Alerts.Sleepy,
			Sick: p.Alerts.Sick, Sad: p.Alerts.Sad,
		},
		Intents: intents, Stats: p.Stats, Seq: p.StateSeq,
		Sleeping: p.Sleeping, Alive: p.Alive,
	}
}

// View 补算后返回快照。
func (m *Manager) View(ctx context.Context, id string) (Snapshot, error) {
	unlock := m.host.LockPet(id)
	defer unlock()
	p, evs, err := m.settleLocked(ctx, id)
	if err != nil {
		return Snapshot{}, err
	}
	if err := m.host.SavePet(ctx, p); err != nil {
		return Snapshot{}, err
	}
	m.host.Emit(ctx, evs...)
	m.host.PublishState(p)
	return SnapshotOf(p), nil
}

// Apply 原子执行活动切换或意图入队。
func (m *Manager) Apply(ctx context.Context, id string, t Transition) (Result, error) {
	unlock := m.host.LockPet(id)
	defer unlock()
	return m.applyLocked(ctx, id, t)
}

// Restore 进程重启时对齐占用（不跑 CanEnter/OnCommit）。
func (m *Manager) Restore(ctx context.Context, id, kind, owner string) error {
	if kind == "" || kind == pet.ActivityIdle {
		return fmt.Errorf("petstate: invalid restore kind %q", kind)
	}
	unlock := m.host.LockPet(id)
	defer unlock()
	p, err := m.host.LoadPet(ctx, id)
	if err != nil {
		return err
	}
	p.SyncSleepingFromActivity()
	if !p.Alive {
		return pet.ErrDead
	}
	if p.Sleeping {
		return pet.ErrBusy
	}
	p.Activity = kind
	p.ActivityOwner = owner
	p.Sleeping = false
	p.StateSeq++
	p.SyncSleepingFromActivity()
	if err := m.host.SavePet(ctx, p); err != nil {
		return err
	}
	m.host.PublishState(p)
	return nil
}

func (m *Manager) applyLocked(ctx context.Context, id string, t Transition) (Result, error) {
	p, evs, err := m.settleLocked(ctx, id)
	if err != nil {
		return Result{}, err
	}
	before := SnapshotOf(p)

	if !p.Alive {
		return m.reject(ctx, p, evs, before, pet.ErrDead)
	}
	if p.Incubating() {
		return m.reject(ctx, p, evs, before, pet.ErrIncubating)
	}

	if t.To == "" {
		if t.IntentKind == "" {
			return Result{Snapshot: before, Err: fmt.Errorf("petstate: empty transition")}, nil
		}
		p.AddIntent(t.IntentKind)
		p.StateSeq++
		if err := m.host.SavePet(ctx, p); err != nil {
			return Result{}, err
		}
		m.host.Emit(ctx, evs...)
		m.host.PublishState(p)
		return Result{OK: true, Snapshot: SnapshotOf(p), Queued: true}, nil
	}

	to := t.To
	owner := t.Owner
	if to == pet.ActivityIdle {
		owner = ""
	}
	if to == pet.ActivitySleeping {
		owner = "core"
	}

	cur := p.Activity
	if cur == "" {
		cur = pet.ActivityIdle
	}
	if cur == to && (to == pet.ActivityIdle || to == pet.ActivitySleeping || p.ActivityOwner == owner) {
		return m.reject(ctx, p, evs, before, pet.ErrAlready)
	}

	// 互斥判定查转移策略表（transitions.go）；忙时可选把意图入队而非拒绝。
	if policyFor(to).blocks(cur, p.Stats.Energy) {
		if t.QueueIfBusy && t.IntentKind != "" {
			p.AddIntent(t.IntentKind)
			p.StateSeq++
			if err := m.host.SavePet(ctx, p); err != nil {
				return Result{}, err
			}
			m.host.Emit(ctx, evs...)
			m.host.PublishState(p)
			return Result{OK: true, Snapshot: SnapshotOf(p), Queued: true}, nil
		}
		return m.reject(ctx, p, evs, before, pet.ErrBusy)
	}

	for _, g := range t.Guards {
		if g == nil {
			continue
		}
		if err := g(before); err != nil {
			return m.reject(ctx, p, evs, before, err)
		}
	}

	if to != pet.ActivityIdle && to != pet.ActivitySleeping {
		if k, ok := m.kind(to); ok && k.CanEnter != nil {
			if err := k.CanEnter(before); err != nil {
				return m.reject(ctx, p, evs, before, err)
			}
		}
	}

	prevKind := cur
	leavingPlugin := prevKind != pet.ActivityIdle && prevKind != pet.ActivitySleeping && prevKind != to
	var afterLeave func(context.Context, string, string) error
	if leavingPlugin {
		if k, ok := m.kind(prevKind); ok {
			afterLeave = k.AfterLeave
		}
	}

	rollbackAct, rollbackOwner, rollbackSleep := p.Activity, p.ActivityOwner, p.Sleeping
	rollbackIntents := append([]string(nil), p.Intents...)
	rollbackSeq := p.StateSeq
	rollbackStats := p.Stats
	rollbackAlerts := p.Alerts

	p.Activity = to
	p.ActivityOwner = owner
	p.StateSeq++

	var careEvs []pet.Event
	now := m.host.Now()
	if to == pet.ActivitySleeping && !rollbackSleep {
		p.Sleeping = true
		careEvs = append(careEvs, pet.Event{PetID: p.ID, Type: pet.EventFellAsleep, Message: p.Name + " 睡着了", CreatedAt: now})
		p.RemoveIntent(pet.IntentSleep)
	}
	if to == pet.ActivityIdle && rollbackSleep {
		p.Sleeping = false
		careEvs = append(careEvs, pet.Event{PetID: p.ID, Type: pet.EventWokeUp, Message: p.Name + " 醒来了", CreatedAt: now})
	}
	p.SyncSleepingFromActivity()

	if t.StatsDelta != (pet.Stats{}) {
		careEvs = append(careEvs, p.Adjust(t.StatsDelta, now)...)
		p.SyncSleepingFromActivity()
	}

	after := SnapshotOf(p)
	if t.OnCommit != nil {
		if err := t.OnCommit(ctx, before, after); err != nil {
			p.Activity, p.ActivityOwner, p.Sleeping = rollbackAct, rollbackOwner, rollbackSleep
			p.Intents = rollbackIntents
			p.StateSeq = rollbackSeq
			p.Stats = rollbackStats
			p.Alerts = rollbackAlerts
			p.SyncSleepingFromActivity()
			return m.reject(ctx, p, evs, before, err)
		}
	}

	if afterLeave != nil {
		_ = afterLeave(ctx, id, t.Reason)
	}

	// 回 idle 后消费排队意图（如困着走完行程，回来倒头就睡）。
	careEvs = append(careEvs, consumeIntents(p, now)...)

	if err := m.host.SavePet(ctx, p); err != nil {
		return Result{}, err
	}
	m.host.Emit(ctx, append(evs, careEvs...)...)
	m.host.PublishState(p)
	return Result{OK: true, Snapshot: SnapshotOf(p)}, nil
}

// reject 统一处理"拒绝切换"：落库 settle 结果、发 settle 事件、返回 before 快照。
func (m *Manager) reject(ctx context.Context, p *pet.Pet, evs []pet.Event, before Snapshot, err error) (Result, error) {
	_ = m.host.SavePet(ctx, p)
	m.host.Emit(ctx, evs...)
	m.host.PublishState(p)
	return Result{Snapshot: before, Err: err}, nil
}

func (m *Manager) settleLocked(ctx context.Context, id string) (*pet.Pet, []pet.Event, error) {
	p, err := m.host.LoadPet(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	p.SyncSleepingFromActivity()
	tr := m.host.TraitsOf(id)
	evs := p.TickTraits(m.host.Now(), m.host.OfflineMax(), tr)
	if !p.Alive {
		// 历史遗留自愈：refresh 之前死亡的宠物可能仍挂着活动态，读/写路径顺手回收。
		p.ReclaimActivityOnDeath()
	}
	p.SyncSleepingFromActivity()
	return p, evs, nil
}

// RequestSleep 立刻睡或排队 IntentSleep。
func (m *Manager) RequestSleep(ctx context.Context, id, reason string) (Result, error) {
	return m.Apply(ctx, id, Transition{
		To: pet.ActivitySleeping, Owner: "core", Reason: reason,
		QueueIfBusy: true, IntentKind: pet.IntentSleep,
	})
}

// GoIdle 结束当前活动回到 idle（可触发意图消费）。
func (m *Manager) GoIdle(ctx context.Context, id, reason string) (Result, error) {
	return m.Apply(ctx, id, Transition{
		To: pet.ActivityIdle, Reason: reason,
	})
}

// ErrRejected 包装守卫拒绝。
var ErrRejected = errors.New("petstate: transition rejected")
