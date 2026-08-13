// pocketpetd 是 PocketPet 的后端守护进程。
// 配置：YAML 配置文件 + 环境变量覆盖（启动参数 > env > 文件 > 默认值）；
// 未配置 LLM 时 chat 走性格化降级文案，养成闭环不受影响。
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lalolv/PocketPet/internal/agent"
	"github.com/lalolv/PocketPet/internal/api"
	"github.com/lalolv/PocketPet/internal/config"
	"github.com/lalolv/PocketPet/internal/dream"
	"github.com/lalolv/PocketPet/internal/metaagent"
	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
	"github.com/lalolv/PocketPet/internal/plugin"
	"github.com/lalolv/PocketPet/internal/plugins"
	"github.com/lalolv/PocketPet/internal/proactive"
	"github.com/lalolv/PocketPet/internal/store"
	"github.com/lalolv/PocketPet/internal/tick"
)

func main() {
	configPath := flag.String("config", "", "YAML 配置文件路径（默认探测 ./pocketpet.yaml、./configs/pocketpet.yaml）")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}
	setupLogging(cfg.LogLevel)
	llmCfg := cfg.LLM

	slog.Info("starting pocketpetd",
		"listen", cfg.ListenAddr,
		"config", orNone(cfg.ConfigPath),
		"tick_interval", cfg.TickInterval,
		"offline_max", cfg.OfflineMax,
		"db", cfg.DBPath,
		"data_dir", cfg.DataRoot,
		"log_level", orNone(cfg.LogLevel),
		"llm_model", orNone(llmCfg.Model),
		"llm_base_url", orNone(llmCfg.BaseURL),
	)
	if !llmCfg.Configured() {
		slog.Info("llm not configured, chat will use personality fallback lines " +
			"(set llm.model / llm.api_key in config file to enable real LLM chat)")
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		slog.Error("open store failed", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	pfs := petfs.New(cfg.DataRoot)
	hub := api.NewHub()

	// M5 插件体系：内置插件由 internal/plugins.Build 按 YAML 过滤/调参后注册。
	registry := plugin.NewRegistry(plugins.Build(cfg)...)
	if err := registry.RunMigrations(st); err != nil {
		slog.Error("plugin migrations failed", "err", err)
		os.Exit(1)
	}

	// 事件订阅方：SSE hub + 阶段同步器（stage_up 回写 PET.md）+ 梦境整理器（入睡触发）
	// + 主动行为器（状态告警 → 主动消息/自动入睡）+ 插件 EventSubscriber。
	stageSync := agent.NewStageSync(pfs, st)
	organizer := dream.NewOrganizer(pfs, st, llmCfg)
	monitor := proactive.NewMonitor(st, pfs, llmCfg, proactive.Options{
		Enabled:   cfg.Proactive.Enabled,
		AutoSleep: cfg.Proactive.AutoSleep,
		AutoWake:  cfg.Proactive.AutoWake,
		Messages:  cfg.Proactive.Messages,
	})
	sink := tick.MultiSink{hub, stageSync, organizer, monitor}
	sink = append(sink, registry.EventSinks()...)
	engine := tick.NewEngine(st, sink, cfg.TickInterval, cfg.OfflineMax, pet.RealClock{})
	engine.SetTraitsLoader(func(id string) pet.Traits {
		doc, err := pfs.ReadSoulDoc(id)
		if err != nil {
			return pet.NeutralTraits()
		}
		return pet.TraitsFromMap(doc.Traits)
	})
	organizer.Emitter = engine.Emit
	monitor.Engine = engine
	monitor.Emitter = engine.Emit

	// 插件 Init（受控上下文）→ tick 钩子 → 工具 → 路由。
	if err := registry.InitAll(plugin.NewPluginContext(engine, pfs, st.DB(), slog.Default(), registry)); err != nil {
		slog.Error("plugin init failed", "err", err)
		os.Exit(1)
	}
	for _, h := range registry.TickHooks() {
		engine.AddTickHook(h)
	}
	// 主动行为器的 tick 侧：睡饱自动醒来；Care/Adjust 后即时补 AutoSleep。
	engine.AddTickHook(monitor)
	engine.AddAfterMutate(func(ctx context.Context, p *pet.Pet) {
		monitor.EnsureAutoSleep(ctx, p)
	})

	petAgent := agent.New(engine, pfs, llmCfg, agent.Options{
		SkillsDir:  cfg.SkillsDir,
		MCPServers: cfg.MCPServers,
		ExtraTools: registry.Tools(),
	})
	midwife := &metaagent.Midwife{
		Engine:       engine,
		FS:           pfs,
		Emit:         engine.Emit,
		LLM:          llmCfg,
		BirthTimeout: cfg.Genesis.Timeout,
		ScriptPace:   cfg.Genesis.ScriptPace,
	}
	server := api.NewServer(st, engine, hub, pfs, petAgent, midwife)
	server.LegacyCreate = cfg.Genesis.LegacyCreate
	for _, pr := range registry.Routes() {
		server.RegisterPluginRoutes(pr.Plugin, pr.Routes)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	registry.SetEventContext(ctx)

	go engine.Run(ctx)

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		slog.Info("shutting down pocketpetd")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			slog.Error("http shutdown failed", "err", err)
		}
		if err := registry.ShutdownAll(shutdownCtx); err != nil {
			slog.Error("plugin shutdown failed", "err", err)
		}
	}()

	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("http serve failed", "err", err)
		os.Exit(1)
	}
	slog.Info("pocketpetd stopped")
}

func orNone(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}
