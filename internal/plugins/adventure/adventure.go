// Package adventure 是"探险"玩法插件（设计文档 8.3 的回测案例，M5 验证）：
// 纯插件实现，不改 tick/store/pet 领域层——工具经 ToolProvider 注入，结算走 TickHook，
// 背包是自己的 SQLite 表（SchemaProvider），背包查询走 RouteProvider。
// 仍需在 composition root 注册并重编译。
//
// 数值规则（确定性，写死在代码里）：
//   - adventure_start：需清醒且 Energy >= 15，消耗 Energy -15，进入探险（默认 3 个 tick）
//   - 探险结束（TickHook 结算）：随机掉落 1 件物品、EXP +10、Happy +5、25% 概率 Health -5
//   - 掉落表：松果 50% / 闪亮石头 30% / 羽毛 15% / 神秘种子 5%
package adventure

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	adkagent "google.golang.org/adk/v2/agent"
	adktool "google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/lalolv/PocketPet/internal/httpx"
	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/plugin"
	"github.com/lalolv/PocketPet/internal/store"
)

// 插件产生的领域事件（插件事件名由插件自己拥有，不进 pet 包）。
const (
	EventStarted  = "pet.adventure_started"
	EventFinished = "pet.adventure_finished"
)

// 默认数值参数。
const (
	defaultTicks        = 3
	defaultEnergyCost   = 15.0
	defaultEXPReward    = 10
	defaultHappyReward  = 5.0
	defaultInjuryChance = 0.25
	defaultInjuryHealth = 5.0
)

// drop 是一件掉落物品。
type drop struct {
	ID    string // 物品标识（背包表里的 key）
	Label string // 中文名（事件/展示用）
}

// dropTable 是掉落表：按 [0,1) 区间命中。
var dropTable = []struct {
	chance float64
	drop   drop
}{
	{0.50, drop{"pinecone", "松果"}},
	{0.30, drop{"shiny-stone", "闪亮石头"}},
	{0.15, drop{"feather", "羽毛"}},
	{0.05, drop{"mystery-seed", "神秘种子"}},
}

// ErrNoInventory 表示探险插件未注册/背包表不存在（friends 等插件优雅降级用）。
var ErrNoInventory = errors.New("adventure: no inventory (adventure plugin not active)")

// ErrItemNotFound 表示背包里没有该物品。
var ErrItemNotFound = errors.New("adventure: item not in inventory")

// Adventure 是探险插件实例。数值字段在 Init 前可覆盖（测试/调参用）。
type Adventure struct {
	Ticks        int
	EnergyCost   float64
	EXPReward    int
	HappyReward  float64
	InjuryChance float64
	InjuryHealth float64

	// Roll 是随机源（返回 [0,1)），默认 rand.Float64；测试注入固定值保证确定性。
	Roll func() float64

	ctx   plugin.PluginContext
	db    *sql.DB
	tools []adktool.Tool
}

// New 创建默认参数的探险插件。
func New() *Adventure {
	return &Adventure{
		Ticks:        defaultTicks,
		EnergyCost:   defaultEnergyCost,
		EXPReward:    defaultEXPReward,
		HappyReward:  defaultHappyReward,
		InjuryChance: defaultInjuryChance,
		InjuryHealth: defaultInjuryHealth,
		Roll:         rand.Float64,
	}
}

// Name 实现 plugin.Plugin。
func (a *Adventure) Name() string { return "adventure" }

// Migrations 实现 plugin.SchemaProvider：探险状态表 + 背包表。
func (a *Adventure) Migrations() []store.Migration {
	return []store.Migration{
		`CREATE TABLE adventure_active (
			pet_id     TEXT PRIMARY KEY,
			ticks_left INTEGER NOT NULL,
			started_at TEXT NOT NULL
		);
		CREATE TABLE adventure_items (
			pet_id TEXT NOT NULL,
			item   TEXT NOT NULL,
			count  INTEGER NOT NULL,
			PRIMARY KEY (pet_id, item)
		);`,
	}
}

// Init 实现 plugin.Plugin：构建工具。
func (a *Adventure) Init(ctx plugin.PluginContext) error {
	a.ctx = ctx
	a.db = ctx.DB()

	start, err := functiontool.New(functiontool.Config{
		Name:        "adventure_start",
		Description: fmt.Sprintf("出门去探险：消耗精力（-%d），过一段时间后带回随机物品。需要醒着且精力充足。", int(a.EnergyCost)),
	}, func(actx adkagent.Context, _ struct{}) (plugin.ToolResult, error) {
		return a.start(actx, plugin.PetIDOf(actx))
	})
	if err != nil {
		return err
	}
	status, err := functiontool.New(functiontool.Config{
		Name:        "adventure_status",
		Description: "查看探险状态与背包里的物品。",
	}, func(actx adkagent.Context, _ struct{}) (plugin.ToolResult, error) {
		return a.status(actx, plugin.PetIDOf(actx))
	})
	if err != nil {
		return err
	}
	a.tools = []adktool.Tool{start, status}
	return nil
}

// Shutdown 实现 plugin.Shutdowner（当前无资源，占位对称）。
func (a *Adventure) Shutdown(context.Context) error { return nil }

// Tools 实现 plugin.ToolProvider。
func (a *Adventure) Tools() []adktool.Tool { return a.tools }

// start 处理 adventure_start：检查状态、扣精力、登记探险中状态、发事件。
func (a *Adventure) start(ctx context.Context, petID string) (plugin.ToolResult, error) {
	p, err := a.ctx.GetPet(ctx, petID)
	if err != nil {
		return plugin.ToolResult{}, err
	}
	if p.Sleeping {
		return plugin.ToolResult{OK: false, Outcome: "我正在睡觉，没法出门探险"}, nil
	}
	if p.Stats.Energy < a.EnergyCost {
		return plugin.ToolResult{OK: false, Outcome: "我太累了，没力气去探险"}, nil
	}
	if left, ok, err := a.activeTicks(petID); err != nil {
		return plugin.ToolResult{}, err
	} else if ok {
		return plugin.ToolResult{OK: false, Outcome: fmt.Sprintf("我还在外面探险呢（再过 %d 个 tick 回来）", left)}, nil
	}

	if _, err := a.ctx.AdjustStats(ctx, petID, pet.Stats{Energy: -a.EnergyCost}); err != nil {
		return plugin.ToolResult{}, err
	}
	if _, err := a.db.ExecContext(ctx,
		`INSERT INTO adventure_active (pet_id, ticks_left, started_at) VALUES (?, ?, ?)`,
		petID, a.Ticks, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return plugin.ToolResult{}, err
	}
	a.ctx.Emit(ctx, pet.Event{PetID: petID, Type: EventStarted,
		Message: p.Name + " 去探险了", CreatedAt: time.Now()})
	return plugin.ToolResult{OK: true, Outcome: "我出发去探险了！过一阵子才回来"}, nil
}

// status 处理 adventure_status：返回探险状态与背包内容的文本。
func (a *Adventure) status(ctx context.Context, petID string) (plugin.ToolResult, error) {
	var sb strings.Builder
	if left, ok, err := a.activeTicks(petID); err != nil {
		return plugin.ToolResult{}, err
	} else if ok {
		fmt.Fprintf(&sb, "我正在外面探险，预计再过 %d 个 tick 回来。", left)
	} else {
		sb.WriteString("我现在没有出门。")
	}
	items, err := Inventory(a.db, petID)
	if err != nil {
		return plugin.ToolResult{}, err
	}
	if len(items) == 0 {
		sb.WriteString("背包空空的。")
	} else {
		sb.WriteString("背包里有：")
		for item, count := range items {
			fmt.Fprintf(&sb, "%s×%d ", itemLabel(item), count)
		}
	}
	return plugin.ToolResult{OK: true, Outcome: strings.TrimSpace(sb.String())}, nil
}

// OnTick 实现 plugin.TickHook：推进探险倒计时，归零时结算掉落与奖励。
func (a *Adventure) OnTick(ctx context.Context, now time.Time) {
	rows, err := a.db.QueryContext(ctx, `SELECT pet_id, ticks_left FROM adventure_active`)
	if err != nil {
		a.ctx.Logger().Warn("adventure: list active failed", "err", err)
		return
	}
	type active struct {
		id   string
		left int
	}
	var actives []active
	for rows.Next() {
		var ac active
		if err := rows.Scan(&ac.id, &ac.left); err == nil {
			actives = append(actives, ac)
		}
	}
	rows.Close()

	for _, ac := range actives {
		ac.left--
		if ac.left > 0 {
			if _, err := a.db.ExecContext(ctx,
				`UPDATE adventure_active SET ticks_left = ? WHERE pet_id = ?`, ac.left, ac.id); err != nil {
				a.ctx.Logger().Warn("adventure: tick update failed", "pet", ac.id, "err", err)
			}
			continue
		}
		a.finish(ctx, ac.id, now)
	}
}

// finish 结算一次探险：掉落、奖励、可能受伤、发事件、清状态。
func (a *Adventure) finish(ctx context.Context, petID string, now time.Time) {
	p, err := a.ctx.GetPet(ctx, petID)
	if err != nil || !p.Alive {
		// 宠物没了/死了：清理状态即可。
		_, _ = a.db.ExecContext(ctx, `DELETE FROM adventure_active WHERE pet_id = ?`, petID)
		return
	}

	d := a.rollDrop()
	injured := a.Roll() < a.InjuryChance
	delta := pet.Stats{EXP: a.EXPReward, Happy: a.HappyReward}
	if injured {
		delta.Health = -a.InjuryHealth
	}
	if _, err := a.ctx.AdjustStats(ctx, petID, delta); err != nil {
		a.ctx.Logger().Warn("adventure: reward failed", "pet", petID, "err", err)
		return
	}
	if err := AddItem(a.db, petID, d.ID, 1); err != nil {
		a.ctx.Logger().Warn("adventure: grant item failed", "pet", petID, "err", err)
		return
	}
	if _, err := a.db.ExecContext(ctx, `DELETE FROM adventure_active WHERE pet_id = ?`, petID); err != nil {
		a.ctx.Logger().Warn("adventure: clear active failed", "pet", petID, "err", err)
	}

	msg := p.Name + " 探险回来了，带回了【" + d.Label + "】"
	if injured {
		msg += "，不过受了点伤"
	}
	a.ctx.Emit(ctx, pet.Event{PetID: petID, Type: EventFinished, Message: msg, CreatedAt: now})
}

// rollDrop 按掉落表随机一件物品。
func (a *Adventure) rollDrop() drop {
	roll := a.Roll()
	acc := 0.0
	for _, e := range dropTable {
		acc += e.chance
		if roll < acc {
			return e.drop
		}
	}
	return dropTable[len(dropTable)-1].drop
}

// activeTicks 返回宠物是否在探险中及剩余 tick 数。
func (a *Adventure) activeTicks(petID string) (int, bool, error) {
	var left int
	err := a.db.QueryRow(`SELECT ticks_left FROM adventure_active WHERE pet_id = ?`, petID).Scan(&left)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return left, true, nil
}

// Routes 实现 plugin.RouteProvider。
func (a *Adventure) Routes() []plugin.Route {
	return []plugin.Route{
		{Method: http.MethodGet, Pattern: "/pets/{id}/inventory", Handler: a.handleInventory},
	}
}

// handleInventory 返回背包内容（含探险中状态）。
func (a *Adventure) handleInventory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := a.ctx.GetPet(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "pet not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	items, err := Inventory(a.db, id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	type itemView struct {
		Item  string `json:"item"`
		Label string `json:"label"`
		Count int    `json:"count"`
	}
	views := make([]itemView, 0, len(items))
	for item, count := range items {
		views = append(views, itemView{Item: item, Label: itemLabel(item), Count: count})
	}
	left, adventuring, err := a.activeTicks(id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"pet_id":      id,
		"name":        p.Name,
		"adventuring": adventuring,
		"ticks_left":  left,
		"items":       views,
	})
}

// Inventory 返回宠物背包（item → count）。背包表不存在时返回 ErrNoInventory，
// 供依赖方（如 friends 插件）优雅降级。
func Inventory(db *sql.DB, petID string) (map[string]int, error) {
	rows, err := db.Query(`SELECT item, count FROM adventure_items WHERE pet_id = ?`, petID)
	if err != nil {
		if isNoSuchTable(err) {
			return nil, ErrNoInventory
		}
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var item string
		var count int
		if err := rows.Scan(&item, &count); err != nil {
			return nil, err
		}
		out[item] = count
	}
	return out, rows.Err()
}

// AddItem 往宠物背包加 count 件物品。
func AddItem(db *sql.DB, petID, item string, count int) error {
	_, err := db.Exec(
		`INSERT INTO adventure_items (pet_id, item, count) VALUES (?, ?, ?)
		 ON CONFLICT(pet_id, item) DO UPDATE SET count = count + excluded.count`,
		petID, item, count)
	if err != nil && isNoSuchTable(err) {
		return ErrNoInventory
	}
	return err
}

// TakeItem 从宠物背包取出 1 件物品；没有背包或没有该物品时返回对应错误。
func TakeItem(db *sql.DB, petID, item string) error {
	var count int
	err := db.QueryRow(`SELECT count FROM adventure_items WHERE pet_id = ? AND item = ?`, petID, item).Scan(&count)
	switch {
	case err != nil && isNoSuchTable(err):
		return ErrNoInventory
	case errors.Is(err, sql.ErrNoRows):
		return ErrItemNotFound
	case err != nil:
		return err
	}
	if count <= 1 {
		_, err = db.Exec(`DELETE FROM adventure_items WHERE pet_id = ? AND item = ?`, petID, item)
	} else {
		_, err = db.Exec(`UPDATE adventure_items SET count = count - 1 WHERE pet_id = ? AND item = ?`, petID, item)
	}
	return err
}

// isNoSuchTable 判断 SQLite 的"表不存在"错误。
func isNoSuchTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

// itemLabel 返回物品中文名；未知物品原样返回 ID。
func itemLabel(id string) string {
	for _, e := range dropTable {
		if e.drop.ID == id {
			return e.drop.Label
		}
	}
	return id
}
