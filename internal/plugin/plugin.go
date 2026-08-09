// Package plugin 是 PocketPet 的 Go 插件框架（设计文档 8.3/8.4）：
// 核心接口 + 可选能力接口集，插件按需实现，装配时由 Registry 类型断言发现。
//
// 信任边界：插件是编译期引入的可信代码，权限接近内核（能注册迁移、调数值），
// 不支持第三方二进制热插拔。
package plugin

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	adkagent "google.golang.org/adk/v2/agent"
	adktool "google.golang.org/adk/v2/tool"

	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
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
	EventSubscriber interface {
		OnEvent(ctx context.Context, e pet.Event)
	}
	// TickHook 在每个 tick 周期结算后被调用。
	TickHook = tick.TickHook
	// RouteProvider 注册 REST 路由（统一挂载在 /v1/plugins/<name>/ 前缀下）。
	RouteProvider interface {
		Routes() []Route
	}
	// SchemaProvider 声明插件自己的 SQLite 表/迁移（独立版本命名空间）。
	SchemaProvider interface {
		Migrations() []store.Migration
	}
)

// Route 是插件注册的一条 REST 路由。Pattern 是相对插件命名空间的路径
// （如 "/pets/{id}/inventory"），挂载时统一加 "/v1/plugins/<plugin>" 前缀，
// 避免与核心端点及其他插件冲突。类型定义在契约层（本包），api 包以别名引用。
type Route struct {
	Method  string
	Pattern string
	Handler http.HandlerFunc
}

// PluginContext 是给插件的受控能力面：跨宠物访问、数值调整、事件、文件、存储、日志。
// 刻意保持小巧——所有状态操作都经 tick.Engine 的加锁/补算路径。
type PluginContext struct {
	engine *tick.Engine
	fs     *petfs.FS
	db     *sql.DB
	logger *slog.Logger
}

// NewPluginContext 构造插件上下文（由 main 在装配期调用）。
func NewPluginContext(eng *tick.Engine, fs *petfs.FS, db *sql.DB, logger *slog.Logger) PluginContext {
	return PluginContext{engine: eng, fs: fs, db: db, logger: logger}
}

// Logger 返回插件日志器（带 plugin=<name> 字段由 Init 包装）。
func (c PluginContext) Logger() *slog.Logger { return c.logger }

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

// DB 返回插件表所在的数据库连接（插件自己的表，经 SchemaProvider 迁移建立）。
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
}

// NewRegistry 创建注册表（注册顺序即装配顺序）。
func NewRegistry(plugins ...Plugin) *Registry {
	return &Registry{plugins: plugins}
}

// Plugins 返回全部已注册插件。
func (r *Registry) Plugins() []Plugin { return r.plugins }

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

// InitAll 按注册顺序初始化全部插件。
func (r *Registry) InitAll(ctx PluginContext) error {
	for _, p := range r.plugins {
		c := ctx
		c.logger = ctx.logger.With("plugin", p.Name())
		if err := p.Init(c); err != nil {
			return fmt.Errorf("plugin %s init: %w", p.Name(), err)
		}
	}
	return nil
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
			out = append(out, subscriberSink{es})
		}
	}
	return out
}

// subscriberSink 把 EventSubscriber（带 ctx）适配为 EventSink（无 ctx）。
type subscriberSink struct{ es EventSubscriber }

func (s subscriberSink) Publish(e pet.Event) { s.es.OnEvent(context.Background(), e) }

// TickHooks 收集全部 TickHook 插件。
func (r *Registry) TickHooks() []tick.TickHook {
	var out []tick.TickHook
	for _, p := range r.plugins {
		if th, ok := p.(TickHook); ok {
			out = append(out, th)
		}
	}
	return out
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
