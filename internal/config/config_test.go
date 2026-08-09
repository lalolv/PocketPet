package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if len(cfg.LLMProviders) != 0 || cfg.LLMDefault != "" {
		t.Fatalf("llm = %q %+v", cfg.LLMDefault, cfg.LLMProviders)
	}
}

func TestFileLoading(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-test-123")
	path := writeYAML(t, `
server:
  listen: ":9999"
data_dir: /tmp/pp-data
skills_dir: /tmp/pp-skills
tick:
  interval_seconds: 5
  offline_catchup_max_hours: 12
llm:
  default: deepseek
  providers:
    deepseek:
      provider: openai-chat
      model: deepseek-chat
      base_url: https://api.deepseek.com/v1
      api_key_env: DEEPSEEK_API_KEY
    gpt:
      provider: openai-compatible
      model: gpt-4o-mini
      api_key_env: NO_SUCH_ENV_VAR
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
	if cfg.LLMDefault != "deepseek" {
		t.Fatalf("LLMDefault = %q", cfg.LLMDefault)
	}
	ds := cfg.LLMProviders["deepseek"]
	if ds.Provider != "openai-chat" || ds.Model != "deepseek-chat" ||
		ds.BaseURL != "https://api.deepseek.com/v1" || ds.APIKey != "sk-test-123" {
		t.Fatalf("deepseek = %+v", ds)
	}
	// api_key_env 指向缺失变量 → APIKey 为空（按未配置降级）
	if cfg.LLMProviders["gpt"].APIKey != "" {
		t.Fatalf("gpt api key should be empty: %+v", cfg.LLMProviders["gpt"])
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

func TestAPIKeyDirectWarns(t *testing.T) {
	path := writeYAML(t, `
llm:
  providers:
    x:
      provider: gemini
      model: gemini-2.5-flash
      api_key: sk-direct
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// 直写 api_key 支持但告警（行为上 APIKey 生效）
	if cfg.LLMProviders["x"].APIKey != "sk-direct" {
		t.Fatalf("api_key direct = %+v", cfg.LLMProviders["x"])
	}
}
