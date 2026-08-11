package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lalolv/PocketPet/internal/llm"
)

// writeYAML 写一个临时配置文件并返回路径。
func writeYAML(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pocketpet.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultsPureEnv(t *testing.T) {
	// 无 -config、无 POCKETPET_CONFIG、探测路径不存在 → 默认值
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfigPath != "" {
		t.Fatalf("ConfigPath = %q, want empty (pure env)", cfg.ConfigPath)
	}
	if cfg.ListenAddr != ":8080" || cfg.TickInterval != 60*time.Second ||
		cfg.OfflineMax != 24*time.Hour || cfg.DBPath != "data/pocketpet.db" ||
		cfg.DataRoot != "data" || cfg.SkillsDir != "skills" {
		t.Fatalf("defaults = %+v", cfg)
	}
	if len(cfg.MCPServers) != 0 {
		t.Fatalf("mcp = %+v", cfg.MCPServers)
	}
	if cfg.LLM != (llm.Config{}) {
		t.Fatalf("llm = %+v", cfg.LLM)
	}
	if cfg.Genesis.Timeout != 90*time.Second || cfg.Genesis.ScriptPace != 80*time.Millisecond ||
		cfg.Genesis.LegacyCreate != LegacyCreateInstant {
		t.Fatalf("genesis defaults = %+v", cfg.Genesis)
	}
}

func TestFileLoading(t *testing.T) {
	path := writeYAML(t, `
server:
  listen: ":9999"
data_dir: /tmp/pp-data
skills_dir: /tmp/pp-skills
tick:
  interval_seconds: 5
  offline_catchup_max_hours: 12
llm:
  model: deepseek-v4-flash
  base_url: https://api.deepseek.com/v1
  api_key: sk-test-123
mcp:
  servers:
    - name: weather
      command: /bin/weather
      args: ["--units", "metric"]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfigPath != path {
		t.Fatalf("ConfigPath = %q", cfg.ConfigPath)
	}
	if cfg.ListenAddr != ":9999" || cfg.DataRoot != "/tmp/pp-data" || cfg.SkillsDir != "/tmp/pp-skills" {
		t.Fatalf("file values = %+v", cfg)
	}
	if cfg.TickInterval != 5*time.Second || cfg.OfflineMax != 12*time.Hour {
		t.Fatalf("tick = %v %v", cfg.TickInterval, cfg.OfflineMax)
	}
	if cfg.LLM.Model != "deepseek-v4-flash" || cfg.LLM.BaseURL != "https://api.deepseek.com/v1" ||
		cfg.LLM.APIKey != "sk-test-123" {
		t.Fatalf("llm = %+v", cfg.LLM)
	}
	if len(cfg.MCPServers) != 1 || cfg.MCPServers[0].Name != "weather" ||
		cfg.MCPServers[0].Command != "/bin/weather" || len(cfg.MCPServers[0].Args) != 2 {
		t.Fatalf("mcp = %+v", cfg.MCPServers)
	}
}

func TestPriorityEnvOverFile(t *testing.T) {
	path := writeYAML(t, `
server:
  listen: ":9999"
data_dir: /tmp/pp-data
tick:
  interval_seconds: 5
mcp:
  servers:
    - name: weather
      command: /bin/weather
`)
	t.Setenv(EnvListenAddr, ":7777")
	t.Setenv(EnvTickInterval, "2s")
	t.Setenv(EnvMCPServers, `[{"name":"home","command":"/bin/home"}]`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":7777" {
		t.Fatalf("listen = %q, env should win", cfg.ListenAddr)
	}
	if cfg.TickInterval != 2*time.Second {
		t.Fatalf("tick = %v, env should win", cfg.TickInterval)
	}
	if cfg.DataRoot != "/tmp/pp-data" {
		t.Fatalf("data_dir = %q, file should apply (no env)", cfg.DataRoot)
	}
	// env JSON 整体覆盖文件 MCP 列表
	if len(cfg.MCPServers) != 1 || cfg.MCPServers[0].Name != "home" {
		t.Fatalf("mcp = %+v, env should override file", cfg.MCPServers)
	}
}

func TestConfigPathPriority(t *testing.T) {
	a := writeYAML(t, "server:\n  listen: ':1111'\n")
	b := writeYAML(t, "server:\n  listen: ':2222'\n")
	t.Setenv(EnvConfigPath, b)
	// flag > env
	cfg, err := Load(a)
	if err != nil || cfg.ListenAddr != ":1111" {
		t.Fatalf("flag path should win: %q %v", cfg.ListenAddr, err)
	}
	// env 生效
	cfg, err = Load("")
	if err != nil || cfg.ListenAddr != ":2222" || cfg.ConfigPath != b {
		t.Fatalf("env path: %q %q %v", cfg.ListenAddr, cfg.ConfigPath, err)
	}
}

func TestBadFileErrors(t *testing.T) {
	// 解析错误：报错且不静默
	path := writeYAML(t, "server:\n  listen: [broken\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "pocketpet.yaml") {
		t.Fatalf("bad yaml err = %v", err)
	}
	// 显式指定但不存在：报错（不是静默忽略）
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	if _, err := Load(missing); err == nil || !strings.Contains(err.Error(), "nope.yaml") {
		t.Fatalf("missing file err = %v", err)
	}
}


func TestProactiveConfig(t *testing.T) {
	// 默认全开（文件未写 proactive 段时保持默认）。
	path := writeYAML(t, "server:\n  listen: ':9999'\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := (ProactiveConfig{Enabled: true, AutoSleep: true, AutoWake: true, Messages: true}); cfg.Proactive != want {
		t.Fatalf("proactive defaults = %+v, want %+v", cfg.Proactive, want)
	}

	// 显式关闭部分开关；未写的项保持默认 true。
	path = writeYAML(t, `
proactive:
  auto_sleep: false
  messages: false
`)
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := (ProactiveConfig{Enabled: true, AutoSleep: false, AutoWake: true, Messages: false}); cfg.Proactive != want {
		t.Fatalf("proactive = %+v, want %+v", cfg.Proactive, want)
	}
}

func TestGenesisConfig(t *testing.T) {
	path := writeYAML(t, `
genesis:
  timeout_seconds: 30
  script_pace_ms: 0
  legacy_create: birth
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Genesis.Timeout != 30*time.Second || cfg.Genesis.ScriptPace != 0 ||
		cfg.Genesis.LegacyCreate != LegacyCreateBirth {
		t.Fatalf("genesis file = %+v", cfg.Genesis)
	}

	t.Setenv(EnvGenesisTimeout, "45s")
	t.Setenv(EnvGenesisScriptPace, "10ms")
	t.Setenv(EnvGenesisLegacyCreate, "instant")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Genesis.Timeout != 45*time.Second || cfg.Genesis.ScriptPace != 10*time.Millisecond ||
		cfg.Genesis.LegacyCreate != LegacyCreateInstant {
		t.Fatalf("genesis env override = %+v", cfg.Genesis)
	}
}

func TestPluginsConfig(t *testing.T) {
	// 缺省：全部启用
	path := writeYAML(t, "server:\n  listen: ':9999'\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AdventureEnabled() || !cfg.FriendsEnabled() {
		t.Fatalf("plugins should default enabled: %+v", cfg.Plugins)
	}

	path = writeYAML(t, `
plugins:
  adventure:
    enabled: false
    map_refresh_ticks: 5
    energy_cost: 20
  friends:
    enabled: true
    visit_affinity: 7
`)
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdventureEnabled() {
		t.Fatal("adventure should be disabled")
	}
	if !cfg.FriendsEnabled() {
		t.Fatal("friends should stay enabled")
	}
	if cfg.Plugins.Adventure.MapRefreshTicks == nil || *cfg.Plugins.Adventure.MapRefreshTicks != 5 {
		t.Fatalf("map_refresh_ticks = %+v", cfg.Plugins.Adventure.MapRefreshTicks)
	}
	if cfg.Plugins.Adventure.EnergyCost == nil || *cfg.Plugins.Adventure.EnergyCost != 20 {
		t.Fatalf("energy_cost = %+v", cfg.Plugins.Adventure.EnergyCost)
	}
	if cfg.Plugins.Friends.VisitAffinity == nil || *cfg.Plugins.Friends.VisitAffinity != 7 {
		t.Fatalf("visit_affinity = %+v", cfg.Plugins.Friends.VisitAffinity)
	}
}
