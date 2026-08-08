// pocketpetd 是 PocketPet 的后端守护进程。
// M2（宠物 Agent）：在 M1 数值内核之上叠加 petfs 文件体系、PetAgent
// （第一人称对话 + 自我行为工具 + remember/recall）与 LLM provider 工厂；
// 未配置 LLM 时 chat 走性格化降级文案，养成闭环不受影响。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pocketpet/internal/agent"
	"pocketpet/internal/api"
	"pocketpet/internal/config"
	"pocketpet/internal/dream"
	"pocketpet/internal/llm"
	"pocketpet/internal/pet"
	"pocketpet/internal/petfs"
	"pocketpet/internal/plugin"
	"pocketpet/internal/plugins/adventure"
	"pocketpet/internal/plugins/friends"
	"pocketpet/internal/store"
	"pocketpet/internal/tick"
)

func main() {
	cfg := config.Load()
	llmCfg := llm.FromEnv()
	slog.Info("starting pocketpetd",
		"listen", cfg.ListenAddr,
		"tick_interval", cfg.TickInterval,
		"offline_max", cfg.OfflineMax,
		"db", cfg.DBPath,
		"data_dir", cfg.DataRoot,
		"llm_provider", orNone(llmCfg.Provider),
		"llm_model", orNone(llmCfg.Model),
	)
	if !llmCfg.Configured() {
		slog.Info("llm not configured, chat will use personality fallback lines " +
			"(set POCKETPET_LLM_PROVIDER/MODEL/API_KEY to enable real LLM chat)")
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		slog.Error("open store failed", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	pfs := petfs.New(cfg.DataRoot)
	hub := api.NewHub()

	// M5 插件体系：先建注册表并跑迁移（实例构造不依赖 engine），
	// 事件订阅者随后并入 engine 的事件扇出。
	registry := plugin.NewRegistry(adventure.New(), friends.New())
	if err := registry.RunMigrations(st); err != nil {
		slog.Error("plugin migrations failed", "err", err)
		os.Exit(1)
	}

	// 事件订阅方：SSE hub + 阶段同步器（stage_up 回写 PET.md）+ 梦境整理器（入睡触发）
	// + 插件 EventSubscriber。
	stageSync := agent.NewStageSync(pfs, st)
	organizer := dream.NewOrganizer(pfs, st, llmCfg)
	sink := tick.MultiSink{hub, stageSync, organizer}
	sink = append(sink, registry.EventSinks()...)
	engine := tick.NewEngine(st, sink, cfg.TickInterval, cfg.OfflineMax, pet.RealClock{})
	organizer.Emitter = engine.Emit

	// 插件 Init（受控上下文）→ tick 钩子 → 工具 → 路由。
	if err := registry.InitAll(plugin.NewPluginContext(engine, pfs, st.DB(), slog.Default())); err != nil {
		slog.Error("plugin init failed", "err", err)
		os.Exit(1)
	}
	for _, h := range registry.TickHooks() {
		engine.AddTickHook(h)
	}

	petAgent := agent.New(engine, pfs, llmCfg, agent.Options{
		SkillsDir:  cfg.SkillsDir,
		MCPServers: cfg.MCPServers,
		ExtraTools: registry.Tools(),
	})
	server := api.NewServer(st, engine, hub, pfs, petAgent)
	for _, pr := range registry.Routes() {
		server.RegisterPluginRoutes(pr.Plugin, pr.Routes)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go engine.Run(ctx)

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			slog.Error("http shutdown failed", "err", err)
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
