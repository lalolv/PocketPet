// Package friends 是"同实例交友"玩法插件（M5 验证跨宠物访问）：
// 好感度存自己的 SQLite 表（SchemaProvider），工具经 ToolProvider 注入，
// 好感度查询走 RouteProvider；订阅 adventure 完成事件（EventSubscriber）与好友分享见闻。
// 跨实例交友走 A2A（M4 已就位），不在本插件范围。
//
// 数值规则（确定性，写死在代码里）：
//   - visit_friend：双方 affinity +5、双方 Happy +8、互动数 +1；
//     对方在睡觉则降级为"隔着门看了看"：仅双方 affinity +2、互动数 +1。
//   - gift：从背包送 1 件物品给对方（走 adventure 插件的背包，未注册时报业务错误），
//     双方 affinity +3、对方 Happy +5、互动数 +1。
//   - 探险归来（pet.adventure_finished）：与已有好友各 +AdventureAffinity 好感，并给对方写日记。
//   - 访问/送礼都会给对方写日记（它聊天时 recall 可达）并发领域事件。
//
// 对 adventure 是软依赖：Init 用 HasPlugin 检查并打日志；缺失时 gift 业务降级，
// 不实现 Depender（硬依赖），以便 YAML 可单独关闭探险。
package friends

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	adkagent "google.golang.org/adk/v2/agent"
	adktool "google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/lalolv/PocketPet/internal/httpx"
	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/plugin"
	"github.com/lalolv/PocketPet/internal/plugins/adventure"
	"github.com/lalolv/PocketPet/internal/store"
)

// 插件产生的领域事件。
const (
	EventFriendVisited = "pet.friend_visited" // 被访问的宠物收到（message 指明来访者）
	EventFriendGift    = "pet.friend_gift"    // 收到礼物的宠物收到
)

// 默认数值参数。
const (
	defaultVisitAffinity     = 5.0
	defaultVisitHappy        = 8.0
	defaultPeekAffinity      = 2.0 // 对方睡觉时的降级好感增量
	defaultGiftAffinity      = 3.0
	defaultGiftHappy         = 5.0
	defaultAdventureAffinity = 1.0 // 好友探险归来时的好感增量
)

// Friends 是交友插件实例。数值字段在 Init 前可覆盖（测试/调参用）。
type Friends struct {
	VisitAffinity     float64
	VisitHappy        float64
	PeekAffinity      float64
	GiftAffinity      float64
	GiftHappy         float64
	AdventureAffinity float64

	ctx          plugin.PluginContext
	db           *sql.DB
	tools        []adktool.Tool
	hasAdventure bool
}

// New 创建默认参数的交友插件。
func New() *Friends {
	return &Friends{
		VisitAffinity:     defaultVisitAffinity,
		VisitHappy:        defaultVisitHappy,
		PeekAffinity:      defaultPeekAffinity,
		GiftAffinity:      defaultGiftAffinity,
		GiftHappy:         defaultGiftHappy,
		AdventureAffinity: defaultAdventureAffinity,
	}
}

// Name 实现 plugin.Plugin。
func (f *Friends) Name() string { return "friends" }

// Migrations 实现 plugin.SchemaProvider：好感度表（双向各存一行）。
func (f *Friends) Migrations() []store.Migration {
	return []store.Migration{
		`CREATE TABLE friendships (
			pet_id       TEXT NOT NULL,
			friend_id    TEXT NOT NULL,
			affinity     REAL NOT NULL DEFAULT 0,
			interactions INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (pet_id, friend_id)
		);`,
	}
}

// Init 实现 plugin.Plugin：构建工具；软检查 adventure。
func (f *Friends) Init(ctx plugin.PluginContext) error {
	f.ctx = ctx
	f.db = ctx.DB()
	f.hasAdventure = ctx.HasPlugin("adventure")
	if !f.hasAdventure {
		ctx.Logger().Warn("friends: adventure plugin not registered; gift will report no inventory")
	}

	visit, err := functiontool.New(functiontool.Config{
		Name:        "visit_friend",
		Description: "去看望同住的另一只宠物（按名字）。双方都心情变好、感情加深；对方在睡觉的话就只隔着门看看。",
	}, func(actx adkagent.Context, args friendArgs) (plugin.ToolResult, error) {
		return f.visit(actx, plugin.PetIDOf(actx), args.FriendName)
	})
	if err != nil {
		return err
	}
	gift, err := functiontool.New(functiontool.Config{
		Name:        "gift",
		Description: "从自己背包里拿一件物品送给另一只宠物（按名字）。背包是探险玩法提供的，没探险过就没有东西可送。",
	}, func(actx adkagent.Context, args giftArgs) (plugin.ToolResult, error) {
		return f.gift(actx, plugin.PetIDOf(actx), args.FriendName, args.Item)
	})
	if err != nil {
		return err
	}
	f.tools = []adktool.Tool{visit, gift}
	return nil
}

// Shutdown 实现 plugin.Shutdowner（当前无资源，占位对称）。
func (f *Friends) Shutdown(context.Context) error { return nil }

// Tools 实现 plugin.ToolProvider。
func (f *Friends) Tools() []adktool.Tool { return f.tools }

type friendArgs struct {
	FriendName string `json:"friend_name" jsonschema:"对方的名字"`
}

type giftArgs struct {
	FriendName string `json:"friend_name" jsonschema:"对方的名字"`
	Item       string `json:"item" jsonschema:"要送的物品（背包里的 ID，如 pinecone）"`
}

// visit 处理 visit_friend：结算好感与心情，给对方发事件、写日记。
func (f *Friends) visit(ctx context.Context, petID, friendName string) (plugin.ToolResult, error) {
	self, friend, res, err := f.resolveFriend(ctx, petID, friendName)
	if err != nil || res != nil {
		return *res, err
	}
	now := time.Now()

	if friend.Sleeping {
		// 对方在睡觉：隔着门看了看，只加少量好感。
		if err := f.bumpAffinity(ctx, self.ID, friend.ID, f.PeekAffinity); err != nil {
			return plugin.ToolResult{}, err
		}
		f.emitVisited(ctx, self, friend, fmt.Sprintf("%s 隔着门悄悄看了看你", self.Name), now)
		return plugin.ToolResult{OK: true, Outcome: friend.Name + " 在睡觉，我隔着门看了看它就回来了"}, nil
	}

	if err := f.bumpAffinity(ctx, self.ID, friend.ID, f.VisitAffinity); err != nil {
		return plugin.ToolResult{}, err
	}
	if _, err := f.ctx.AdjustStats(ctx, self.ID, pet.Stats{Happy: f.VisitHappy}); err != nil {
		return plugin.ToolResult{}, err
	}
	if _, err := f.ctx.AdjustStats(ctx, friend.ID, pet.Stats{Happy: f.VisitHappy}); err != nil {
		return plugin.ToolResult{}, err
	}
	f.emitVisited(ctx, self, friend, fmt.Sprintf("%s 来看望你了", self.Name), now)
	return plugin.ToolResult{OK: true, Outcome: "我去找 " + friend.Name + " 玩了一会儿，我们都很开心"}, nil
}

// gift 处理 gift：从背包取一件物品送给对方（背包由 adventure 插件提供，
// 未注册/没物品时优雅报业务错误）。
func (f *Friends) gift(ctx context.Context, petID, friendName, item string) (plugin.ToolResult, error) {
	self, friend, res, err := f.resolveFriend(ctx, petID, friendName)
	if err != nil || res != nil {
		return *res, err
	}
	if !f.hasAdventure {
		return plugin.ToolResult{OK: false, Outcome: "我还没有背包呢……要先去探险才能有东西可送"}, nil
	}

	err = adventure.TakeItem(f.db, self.ID, item)
	switch {
	case errors.Is(err, adventure.ErrNoInventory):
		return plugin.ToolResult{OK: false, Outcome: "我还没有背包呢……要先去探险才能有东西可送"}, nil
	case errors.Is(err, adventure.ErrItemNotFound):
		return plugin.ToolResult{OK: false, Outcome: "背包里没有这个物品"}, nil
	case err != nil:
		return plugin.ToolResult{}, err
	}
	if err := adventure.AddItem(f.db, friend.ID, item, 1); err != nil {
		return plugin.ToolResult{}, err
	}

	if err := f.bumpAffinity(ctx, self.ID, friend.ID, f.GiftAffinity); err != nil {
		return plugin.ToolResult{}, err
	}
	if _, err := f.ctx.AdjustStats(ctx, friend.ID, pet.Stats{Happy: f.GiftHappy}); err != nil {
		return plugin.ToolResult{}, err
	}

	now := time.Now()
	f.ctx.Emit(ctx, pet.Event{PetID: friend.ID, Type: EventFriendGift,
		Message: fmt.Sprintf("%s 送了你一个【%s】", self.Name, item), CreatedAt: now})
	if err := f.ctx.AppendJournal(friend.ID, fmt.Sprintf("%s 送了我一个【%s】。", self.Name, item), now); err != nil {
		f.ctx.Logger().Warn("friends: append journal failed", "err", err)
	}
	return plugin.ToolResult{OK: true, Outcome: "我把【" + item + "】送给了 " + friend.Name + "，它看起来很开心"}, nil
}

// OnEvent 实现 plugin.EventSubscriber：好友探险归来时加深感情并写日记。
// 只做 DB/文件写入，不重入 Engine（避免与 emit 持锁路径死锁）。
func (f *Friends) OnEvent(ctx context.Context, e pet.Event) {
	if e.Type != adventure.EventFinished || f.db == nil || f.AdventureAffinity == 0 {
		return
	}
	rows, err := f.db.QueryContext(ctx,
		`SELECT friend_id FROM friendships WHERE pet_id = ?`, e.PetID)
	if err != nil {
		return
	}
	var friendIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			friendIDs = append(friendIDs, id)
		}
	}
	rows.Close()
	if len(friendIDs) == 0 {
		return
	}

	adventurerName := e.PetID
	if p, err := f.ctx.GetPet(ctx, e.PetID); err == nil {
		adventurerName = p.Name
	}
	now := time.Now()
	msg := fmt.Sprintf("听说 %s 探险回来了，带回了新见闻", adventurerName)
	for _, fid := range friendIDs {
		if err := f.bumpAffinityOnly(ctx, e.PetID, fid, f.AdventureAffinity); err != nil {
			f.ctx.Logger().Warn("friends: adventure affinity failed", "friend", fid, "err", err)
			continue
		}
		if err := f.ctx.AppendJournal(fid, msg+"。", now); err != nil {
			f.ctx.Logger().Warn("friends: append journal failed", "friend", fid, "err", err)
		}
	}
}

// resolveFriend 校验并找到双方宠物；返回非 nil 的 res 表示可直接返回的业务结果。
func (f *Friends) resolveFriend(ctx context.Context, petID, friendName string) (self, friend *pet.Pet, res *plugin.ToolResult, err error) {
	self, err = f.ctx.GetPet(ctx, petID)
	if err != nil {
		return nil, nil, nil, err
	}
	friend, err = f.ctx.FindPetByName(ctx, friendName)
	if errors.Is(err, store.ErrNotFound) {
		r := plugin.ToolResult{OK: false, Outcome: "这里没有叫「" + friendName + "」的小伙伴"}
		return nil, nil, &r, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	if friend.ID == self.ID {
		r := plugin.ToolResult{OK: false, Outcome: "我不能拜访我自己呀"}
		return nil, nil, &r, nil
	}
	return self, friend, nil, nil
}

// bumpAffinity 双向加好感并各记一次互动。
func (f *Friends) bumpAffinity(ctx context.Context, a, b string, delta float64) error {
	return f.bump(ctx, a, b, delta, true)
}

// bumpAffinityOnly 双向加好感，不增加互动计数（如探险见闻分享）。
func (f *Friends) bumpAffinityOnly(ctx context.Context, a, b string, delta float64) error {
	return f.bump(ctx, a, b, delta, false)
}

func (f *Friends) bump(ctx context.Context, a, b string, delta float64, countInteraction bool) error {
	inc := 0
	if countInteraction {
		inc = 1
	}
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		if _, err := f.db.ExecContext(ctx,
			`INSERT INTO friendships (pet_id, friend_id, affinity, interactions) VALUES (?, ?, ?, ?)
			 ON CONFLICT(pet_id, friend_id) DO UPDATE SET
				affinity = affinity + excluded.affinity,
				interactions = interactions + excluded.interactions`,
			pair[0], pair[1], delta, inc); err != nil {
			return err
		}
	}
	return nil
}

// emitVisited 发事件给对方并写进它的日记（聊天时 recall 可达）。
func (f *Friends) emitVisited(ctx context.Context, self, friend *pet.Pet, msg string, now time.Time) {
	f.ctx.Emit(ctx, pet.Event{PetID: friend.ID, Type: EventFriendVisited, Message: msg, CreatedAt: now})
	if err := f.ctx.AppendJournal(friend.ID, msg+"。", now); err != nil {
		f.ctx.Logger().Warn("friends: append journal failed", "err", err)
	}
}

// Routes 实现 plugin.RouteProvider。
func (f *Friends) Routes() []plugin.Route {
	return []plugin.Route{
		{Method: http.MethodGet, Pattern: "/pets/{id}/friends", Handler: f.handleFriends},
	}
}

// handleFriends 返回好感度列表（按好感降序）。
func (f *Friends) handleFriends(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := f.ctx.GetPet(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "pet not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	rows, err := f.db.QueryContext(r.Context(),
		`SELECT friend_id, affinity, interactions FROM friendships WHERE pet_id = ? ORDER BY affinity DESC`, id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	defer rows.Close()
	type friendView struct {
		Name         string  `json:"name"`
		Affinity     float64 `json:"affinity"`
		Interactions int     `json:"interactions"`
	}
	// 先把行读完再查名字：SQLite 单连接（MaxOpenConns=1），
	// 边遍历边查库会死锁。
	type row struct {
		friendID string
		view     friendView
	}
	var scanned []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.friendID, &r.view.Affinity, &r.view.Interactions); err == nil {
			scanned = append(scanned, r)
		}
	}
	rows.Close()
	views := []friendView{}
	for _, sc := range scanned {
		sc.view.Name = sc.friendID
		if fp, err := f.ctx.GetPet(r.Context(), sc.friendID); err == nil {
			sc.view.Name = fp.Name
		}
		views = append(views, sc.view)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"pet_id": id, "friends": views})
}
