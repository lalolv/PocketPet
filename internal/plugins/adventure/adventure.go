// Package adventure 是「探险」玩法插件（docs/06）：
// 系统周期性生成共享图结构地图；宠物沿分支步进；宝箱仅展示信息。
// 与其它玩法插件彼此独立，不共享背包、不要求对方订阅事件。
package adventure

import (
	"context"
	"database/sql"
	"math/rand/v2"
	"sync"
	"time"

	adktool "google.golang.org/adk/v2/tool"

	"github.com/lalolv/PocketPet/internal/plugin"
)

// 领域事件（仅供本玩法展示 / SSE，不作跨插件契约）。
const (
	EventStarted  = "pet.adventure_started"
	EventMoved    = "pet.adventure_moved"
	EventChest    = "pet.adventure_chest"
	EventFinished = "pet.adventure_finished"
	EventAborted  = "pet.adventure_aborted"
)

const (
	defaultMapRefreshTicks = 60
	defaultNodeCount       = 12
	defaultMaxBranches     = 5
	defaultChestMinPct     = 0.15
	defaultChestMaxPct     = 0.25
	defaultEnergyCost      = 15.0
	defaultStepInterval    = 5 * time.Second // 与养成 tick（默认 60s）解耦，适合 TUI 观感

	kvCurrentMapID   = "current_map_id"
	kvRefreshCounter = "refresh_counter"
)

// Adventure 是探险插件实例。数值字段在 Init 前可覆盖。
type Adventure struct {
	MapRefreshTicks int
	NodeCount       int
	MaxBranches     int
	ChestMinPct     float64
	ChestMaxPct     float64
	EnergyCost      float64
	// StepInterval 是行程步进墙钟间隔；<=0 表示不启后台步进（单测可手动 advanceAllRuns）。
	StepInterval time.Duration

	// IntN / Float64 可注入，保证单测确定性；nil 用全局 rand。
	IntN    func(n int) int
	Float64 func() float64

	ctx        plugin.PluginContext
	db         *sql.DB
	tools      []adktool.Tool
	stepCancel context.CancelFunc
	stepMu     sync.Mutex // 串行化步进，避免与 start/换图交错
}

// New 创建默认参数的探险插件。
func New() *Adventure {
	return &Adventure{
		MapRefreshTicks: defaultMapRefreshTicks,
		NodeCount:       defaultNodeCount,
		MaxBranches:     defaultMaxBranches,
		ChestMinPct:     defaultChestMinPct,
		ChestMaxPct:     defaultChestMaxPct,
		EnergyCost:      defaultEnergyCost,
		StepInterval:    defaultStepInterval,
		IntN:            rand.IntN,
		Float64:         rand.Float64,
	}
}

// Name 实现 plugin.Plugin。
func (a *Adventure) Name() string { return "adventure" }

// Init 实现 plugin.Plugin：注册工具；若无当前地图则立即生成一张；启动步进循环。
func (a *Adventure) Init(pctx plugin.PluginContext) error {
	a.ctx = pctx
	a.db = pctx.DB()
	if a.IntN == nil {
		a.IntN = rand.IntN
	}
	if a.Float64 == nil {
		a.Float64 = rand.Float64
	}
	if err := a.ensureCurrentMap(context.Background()); err != nil {
		return err
	}
	if err := a.initTools(); err != nil {
		return err
	}
	a.startStepLoop()
	return nil
}

// Shutdown 实现 plugin.Shutdowner：停止步进循环。
func (a *Adventure) Shutdown(context.Context) error {
	if a.stepCancel != nil {
		a.stepCancel()
		a.stepCancel = nil
	}
	return nil
}

// Tools 实现 plugin.ToolProvider。
func (a *Adventure) Tools() []adktool.Tool { return a.tools }

func (a *Adventure) genConfig() GenConfig {
	return GenConfig{
		NodeCount:   a.NodeCount,
		MaxBranches: a.MaxBranches,
		ChestMinPct: a.ChestMinPct,
		ChestMaxPct: a.ChestMaxPct,
		IntN:        a.IntN,
		Float64:     a.Float64,
	}
}

func (a *Adventure) startStepLoop() {
	if a.StepInterval <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.stepCancel = cancel
	interval := a.StepInterval
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				a.advanceAllRuns(context.Background(), now.UTC())
			}
		}
	}()
}
