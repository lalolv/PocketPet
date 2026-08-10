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
	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
	"github.com/lalolv/PocketPet/internal/plugin"
	"github.com/lalolv/PocketPet/internal/plugins/adventure"
	"github.com/lalolv/PocketPet/internal/plugins/friends"
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
	llmCfg := cfg.LLM

	slog.Info("starting pocketpetd",
		"listen", cfg.ListenAddr,
		"config", orNone(cfg.ConfigPath),
		"tick_interval", cfg.TickInterval,
		"offline_max", cfg.OfflineMax,
		"db", cfg.DBPath,
		"data_dir", cfg.DataRoot,
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
