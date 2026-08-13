// Package plugin 是 PocketPet 的 Go 插件框架（设计文档 8.3/8.4）：
// 核心接口 + 可选能力接口集，插件按需实现，装配时由 Registry 类型断言发现。
//
// 信任边界：插件是编译期引入的可信代码，权限接近内核（能注册迁移、调数值），
// 不支持第三方二进制热插拔。新增玩法不改 tick/store/pet 领域层；内置插件在
// internal/plugins.Build 注册，cmd/pocketpetd 只做 NewRegistry(plugins.Build(cfg)...) 。
package plugin

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	adkagent "google.golang.org/adk/v2/agent"
	adktool "google.golang.org/adk/v2/tool"

	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
	"github.com/lalolv/PocketPet/internal/petstate"
	"github.com/lalolv/PocketPet/internal/store"
	"github.com/lalolv/PocketPet/internal/tick"
)

// Plugin 是核心接口：所有插件必须实现。
type Plugin interface {
	Name() string
	// Init 在装配期调用一次（迁移已就绪），传入受控能力上下文。
	Init(ctx PluginContext) error
}

// 可选能力接口集——按需实现，Registry 装配时类型断言发现：
type (
	// ToolProvider 向 PetAgent 注册新工具（对所有宠物可见）。
	ToolProvider interface {
		Tools() []adktool.Tool
	}
	// EventSubscriber 订阅领域事件（接入 Engine 的事件流水）。
	// OnEvent 必须快速返回且不得对同一宠物重入 Engine（emit 可能持有 petID 锁）；
	// 耗时逻辑请自行异步化。
	EventSubscriber interface {
		OnEvent(ctx context.Context, e pet.Event)
	}
	// TickHook 在每个 tick 周期结算后被调用。
	// 契约：必须快速返回（通常 < SlowTickWarn）；重活请异步或分批。
	// Registry.TickHooks 会包装慢钩子告警（带 plugin 名）。
	TickHook = tick.TickHook
	// RouteProvider 注册 REST 路由（统一挂载在 /v1/plugins/<name>/ 前缀下）。
	RouteProvider interface {
		Routes() []Route
	}
	// SchemaProvider 声明插件自己的 SQLite 表/迁移（独立版本命名空间）。
	// 表名建议以插件名或明确前缀开头（如 adventure_*），避免与核心表及他插件冲突。
	SchemaProvider interface {
		Migrations() []store.Migration
	}
	// Depender 声明硬依赖的其他插件名；InitAll 前校验，缺失则失败。
	// 软依赖请用 PluginContext.HasPlugin 在 Init/运行时自行降级。
	Depender interface {
		DependsOn() []string
	}
	// Shutdowner 在进程退出时做清理（与 Init 对称，可选）。
	Shutdowner interface {
		Shutdown(ctx context.Context) error
	}
)

// SlowTickWarn 是插件 TickHook 的慢执行告警阈值；超过则打 Warn 日志，不中断 tick。
const SlowTickWarn = 100 * time.Millisecond

// CoreTables 是核心存储表名（插件经 DB() 拿的是共享连接，约定禁止改写这些表；
// 数值/事件请走 AdjustStats / Care / Emit）。
var CoreTables = []string{"pets", "pet_events", "kv_meta"}

// Route 是插件注册的一条 REST 路由。Pattern 是相对插件命名空间的路径
// （如 "/pets/{id}/inventory"），挂载时统一加 "/v1/plugins/<plugin>" 前缀，
// 避免与核心端点及其他插件冲突。类型定义在契约层（本包），api 包以别名引用。
type Route struct {
	Method  string
	Pattern string
	Handler http.HandlerFunc
}

// PluginContext 是给插件的受控能力面：跨宠物访问、数值调整、事件、文件、存储、日志。
// 刻意保持小巧——所有宠物状态操作都经 tick.Engine 的加锁/补算路径。
type PluginContext struct {
	engine   *tick.Engine
	fs       *petfs.FS
	db       *sql.DB
	logger   *slog.Logger
	registry *Registry
	plugin   string // InitAll 注入的当前插件名
}

// NewPluginContext 构造插件上下文（由 main 在装配期调用）。
// registry 可为 nil（单测）；非 nil 时插件可通过 HasPlugin 查软依赖。
func NewPluginContext(eng *tick.Engine, fs *petfs.FS, db *sql.DB, logger *slog.Logger, registry *Registry) PluginContext {
	return PluginContext{engine: eng, fs: fs, db: db, logger: logger, registry: registry}
}

// Logger 返回插件日志器（带 plugin=<name> 字段由 Init 包装）。
func (c PluginContext) Logger() *slog.Logger { return c.logger }

// PluginName 返回当前上下文所属插件名（Init 期内非空）。
func (c PluginContext) PluginName() string { return c.plugin }

// HasPlugin 报告名为 name 的插件是否已注册（软依赖检查）。
func (c PluginContext) HasPlugin(name string) bool {
	if c.registry == nil {
		return false
	}
	return c.registry.Has(name)
}

// ListPets 返回全部宠物（读存储层，不触发补算）。
func (c PluginContext) ListPets(ctx context.Context) ([]*pet.Pet, error) {
	return c.engine.ListPets(ctx)
}

// GetPet 读取一只宠物的当前状态（先离线补算）。
func (c PluginContext) GetPet(ctx context.Context, id string) (*pet.Pet, error) {
	return c.engine.Settle(ctx, id)
}

// FindPetByName 按名字找同实例的宠物（交友等跨宠物玩法用）；找不到返回 store.ErrNotFound。
func (c PluginContext) FindPetByName(ctx context.Context, name string) (*pet.Pet, error) {
	pets, err := c.engine.ListPets(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range pets {
		if p.Name == name {
			return p, nil
		}
	}
	return nil, store.ErrNotFound
}

// Care 对宠物执行照顾动作（与 REST care / 自我行为工具同一领域路径）。
func (c PluginContext) Care(ctx context.Context, id string, action pet.Action) (*pet.Pet, error) {
	return c.engine.Care(ctx, id, action)
}

// AdjustStats 对宠物应用一次确定性数值增减（负值即扣减），
// 走与照顾动作相同的钳制、晋升与告警规则。
func (c PluginContext) AdjustStats(ctx context.Context, id string, delta pet.Stats) (*pet.Pet, error) {
	return c.engine.Adjust(ctx, id, delta)
}

// Activity 返回统一活动态（idle / sleeping / adventuring /…）。
func (c PluginContext) Activity(ctx context.Context, id string) (string, error) {
	return c.engine.Activity(ctx, id)
}

// View 返回 Settle 后的原子状态快照。
func (c PluginContext) View(ctx context.Context, id string) (petstate.Snapshot, error) {
	return c.engine.State().View(ctx, id)
}

// Apply 经 petstate.Manager 原子切换活动（插件主路径）。
func (c PluginContext) Apply(ctx context.Context, id string, t petstate.Transition) (petstate.Result, error) {
	return c.engine.State().Apply(ctx, id, t)
}

// RegisterActivity 注册玩法活动 Kind。
func (c PluginContext) RegisterActivity(k petstate.ActivityKind) error {
	return c.engine.State().RegisterKind(k)
}

// RestoreActivity 重启时对齐占用（不跑 CanEnter）。
func (c PluginContext) RestoreActivity(ctx context.Context, id, kind, owner string) error {
	return c.engine.State().Restore(ctx, id, kind, owner)
}

// Emit 把插件产生的领域事件落库并推送（pet_events 表 + SSE）。
func (c PluginContext) Emit(ctx context.Context, evs ...pet.Event) {
	c.engine.Emit(ctx, evs...)
}

// ReadFile 读宠物的 petfs 顶层文件（PET.md/SOUL.md/... 白名单内）。
func (c PluginContext) ReadFile(id, name string) (string, error) {
	return c.fs.Read(id, name)
}

// AppendJournal 往某只宠物的当天日记追加一条记录——跨宠物玩法让对方
// "感知"到事情的主要落点（它聊天时 recall 可达）。
func (c PluginContext) AppendJournal(id, text string, now time.Time) error {
	return c.fs.AppendJournal(id, text, now)
}

// DB 返回共享 SQLite 连接（信任模型下与核心同库）。
//
// 约定（契约，非强制沙箱）：
//   - 只读写本插件 SchemaProvider 声明的表；
//   - 禁止改写 CoreTables（pets / pet_events / kv_meta）；宠物数值与事件走 AdjustStats/Care/Emit；
//   - 跨插件共享数据默认禁止；确需协作须经对方导出的包级 API，勿直接 SQL 耦合他插件表。
//     玩法插件应彼此独立（见 docs/06）。
//
// SQLite 无独立 schema 命名空间，故以约定 + 迁移版本隔离（plugin:<name>）代替物理隔离。
func (c PluginContext) DB() *sql.DB { return c.db }

// ToolResult 是插件工具的统一返回结构（与内置自我行为工具同风格）：
// 领域性拒绝走 OK=false 正常返回，让宠物自己组织语言，而不是抛异常打断对话。
type ToolResult struct {
	OK      bool   `json:"ok"`
	Outcome string `json:"outcome" jsonschema:"这次行为的结果描述（定性，不含数值）"`
}

// PetIDOf 从工具调用的 agent.Context 解析当前宠物 ID
// （PetAgent 的 agent 名约定为 pet_<id>；插件工具对所有宠物可见，靠它路由）。
func PetIDOf(ctx adkagent.Context) string {
	return strings.TrimPrefix(ctx.AgentName(), "pet_")
}

// Registry 持有插件实例并做能力发现。
type Registry struct {
	plugins []Plugin

	mu       sync.RWMutex
	eventCtx context.Context // SetEventContext 注入；供 EventSubscriber 取消传播
}

// NewRegistry 创建注册表（注册顺序即装配顺序）。
func NewRegistry(plugins ...Plugin) *Registry {
	return &Registry{plugins: plugins, eventCtx: context.Background()}
}

// Plugins 返回全部已注册插件。
func (r *Registry) Plugins() []Plugin { return r.plugins }

// Has 报告名为 name 的插件是否已注册。
func (r *Registry) Has(name string) bool {
	for _, p := range r.plugins {
		if p.Name() == name {
			return true
		}
	}
	return false
}

// SetEventContext 设置 EventSubscriber 收到的 ctx（通常为进程信号 ctx）。
// 可在 EventSinks 收集之后、Run 之前调用；Publish 时动态读取。
func (r *Registry) SetEventContext(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	r.eventCtx = ctx
}

func (r *Registry) eventContext() context.Context {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.eventCtx == nil {
		return context.Background()
	}
	return r.eventCtx
}

// checkDeps 校验全部 Depender 的硬依赖是否已注册。
func (r *Registry) checkDeps() error {
	for _, p := range r.plugins {
		d, ok := p.(Depender)
		if !ok {
			continue
		}
		for _, dep := range d.DependsOn() {
			if !r.Has(dep) {
				return fmt.Errorf("plugin %s depends on %q, which is not registered", p.Name(), dep)
			}
		}
	}
	return nil
}

// RunMigrations 执行全部 SchemaProvider 插件的迁移（各自独立版本命名空间）。
func (r *Registry) RunMigrations(st *store.Store) error {
	for _, p := range r.plugins {
		sp, ok := p.(SchemaProvider)
		if !ok {
			continue
		}
		if err := st.RunPluginMigrations(p.Name(), sp.Migrations()); err != nil {
			return fmt.Errorf("plugin %s migrations: %w", p.Name(), err)
		}
	}
	return nil
}

// InitAll 先校验硬依赖，再按注册顺序初始化全部插件。
func (r *Registry) InitAll(ctx PluginContext) error {
	if err := r.checkDeps(); err != nil {
		return err
	}
	ctx.registry = r
	for _, p := range r.plugins {
		c := ctx
		c.plugin = p.Name()
		c.logger = ctx.logger.With("plugin", p.Name())
		if err := p.Init(c); err != nil {
			return fmt.Errorf("plugin %s init: %w", p.Name(), err)
		}
	}
	return nil
}

// ShutdownAll 按注册逆序调用 Shutdowner（后启先停）。
func (r *Registry) ShutdownAll(ctx context.Context) error {
	var first error
	for i := len(r.plugins) - 1; i >= 0; i-- {
		p := r.plugins[i]
		s, ok := p.(Shutdowner)
		if !ok {
			continue
		}
		if err := s.Shutdown(ctx); err != nil && first == nil {
			first = fmt.Errorf("plugin %s shutdown: %w", p.Name(), err)
		}
	}
	return first
}

// Tools 收集全部 ToolProvider 插件的工具。
func (r *Registry) Tools() []adktool.Tool {
	var out []adktool.Tool
	for _, p := range r.plugins {
		if tp, ok := p.(ToolProvider); ok {
			out = append(out, tp.Tools()...)
		}
	}
	return out
}

// EventSinks 把全部 EventSubscriber 插件适配为 tick.EventSink（供 MultiSink 使用）。
func (r *Registry) EventSinks() []tick.EventSink {
	var out []tick.EventSink
	for _, p := range r.plugins {
		if es, ok := p.(EventSubscriber); ok {
			out = append(out, subscriberSink{es: es, reg: r})
		}
	}
	return out
}

// subscriberSink 把 EventSubscriber（带 ctx）适配为 EventSink（无 ctx）。
type subscriberSink struct {
	es  EventSubscriber
	reg *Registry
}

func (s subscriberSink) Publish(e pet.Event) {
	s.es.OnEvent(s.reg.eventContext(), e)
}

// TickHooks 收集全部 TickHook 插件，并包装慢执行告警（带 plugin 名）。
func (r *Registry) TickHooks() []tick.TickHook {
	var out []tick.TickHook
	for _, p := range r.plugins {
		if th, ok := p.(TickHook); ok {
			out = append(out, timedTickHook{name: p.Name(), inner: th})
		}
	}
	return out
}

// timedTickHook 在钩子耗时超过 SlowTickWarn 时打 Warn，不打断后续钩子。
type timedTickHook struct {
	name  string
	inner tick.TickHook
}

func (h timedTickHook) OnTick(ctx context.Context, now time.Time) {
	start := time.Now()
	h.inner.OnTick(ctx, now)
	if d := time.Since(start); d >= SlowTickWarn {
		slog.Warn("plugin tick hook slow", "plugin", h.name, "duration", d, "warn_after", SlowTickWarn)
	}
}

// PluginRoutes 是一个插件的路由集合。
type PluginRoutes struct {
	Plugin string
	Routes []Route
}

// Routes 收集全部 RouteProvider 插件的路由。
func (r *Registry) Routes() []PluginRoutes {
	var out []PluginRoutes
	for _, p := range r.plugins {
		if rp, ok := p.(RouteProvider); ok {
			out = append(out, PluginRoutes{Plugin: p.Name(), Routes: rp.Routes()})
		}
	}
	return out
}
