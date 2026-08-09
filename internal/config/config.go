// Package config 集中管理 pocketpetd 的运行配置：YAML 配置文件 + 环境变量覆盖。
//
// 优先级：启动参数（-config 指定文件本身）> 环境变量 > 配置文件 > 默认值。
// 文件定位：-config 启动参数 → POCKETPET_CONFIG → ./pocketpet.yaml → ./configs/pocketpet.yaml；
// 都找不到则为纯 env 模式（向后兼容 M2 之前的行为）。
// 文件存在但解析失败时返回错误（调用方应报错退出），不静默忽略。
package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"pocketpet/internal/llm"
)

// 环境变量名。
const (
	EnvConfigPath   = "POCKETPET_CONFIG"        // 配置文件路径
	EnvListenAddr   = "POCKETPET_LISTEN"        // HTTP 监听地址
	EnvTickInterval = "POCKETPET_TICK_INTERVAL" // tick 间隔，如 "60s"、"1s"
	EnvOfflineMax   = "POCKETPET_OFFLINE_MAX"   // 离线结算补算时长上限，如 "24h"
	EnvDBPath       = "POCKETPET_DB_PATH"       // SQLite 数据库文件路径，":memory:" 表示内存库
	EnvDataDir      = "POCKETPET_DATA_DIR"      // 数据根目录（petfs 宠物文件在其 pets/ 子目录下）
	EnvSkillsDir    = "POCKETPET_SKILLS_DIR"    // 全局技能目录（SKILL.md 技能包，对所有宠物可见）
	EnvMCPServers   = "POCKETPET_MCP_SERVERS"   // 全局可用 MCP servers，JSON 数组
)

// 默认探测的配置文件路径（按顺序）。
var defaultConfigPaths = []string{"pocketpet.yaml", "configs/pocketpet.yaml"}

// MCPServer 是一个可供宠物启用的 MCP server 声明（仅 stdio 传输）。
// 可来自 YAML（mcp.servers）或 POCKETPET_MCP_SERVERS JSON 数组。
type MCPServer struct {
	Name    string            `json:"name" yaml:"name"`
	Command string            `json:"command" yaml:"command"`
	Args    []string          `json:"args,omitempty" yaml:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
}

// Config 是 pocketpetd 的全部运行配置。
type Config struct {
	// ListenAddr 是 HTTP 服务监听地址，默认 ":8080"。
	ListenAddr string
	// TickInterval 是 tick 引擎的结算间隔，默认 60s。
	TickInterval time.Duration
	// OfflineMax 是离线结算的补算时长上限，默认 24h，
	// 防止久未开机时一次性补算直接把宠物饿死。
	OfflineMax time.Duration
	// DBPath 是 SQLite 文件路径，默认 "data/pocketpet.db"。
	DBPath string
	// DataRoot 是数据根目录，默认 "data"；宠物文件在 <DataRoot>/pets/<id>/。
	DataRoot string
	// SkillsDir 是全局技能目录，默认 "skills"（仓库根的技能包目录）。
	SkillsDir string
	// MCPServers 是全局可用的 MCP server 列表；宠物在 AGENT.md 里按名启用。
	// env JSON 非空时整体覆盖文件列表（与"环境变量 > 配置文件"优先级一致）。
	MCPServers []MCPServer

	// LLMDefault 是命名 provider 名（yaml llm.default）；空 = env 单 provider 模式。
	LLMDefault string
	// LLMProviders 是命名 provider 表（yaml llm.providers），key 为名字。
	// api_key_env 已在加载时解析为 APIKey；指向的环境变量缺失则 APIKey 为空（按未配置降级）。
	LLMProviders map[string]llm.ProviderConfig

	// ConfigPath 是实际使用的配置文件路径（纯 env 模式为空）。
	ConfigPath string
}

// fileConfig 是 YAML 文件的镜像结构。
type fileConfig struct {
	Server struct {
		Listen string `yaml:"listen"`
	} `yaml:"server"`
	DataDir   string `yaml:"data_dir"`
	SkillsDir string `yaml:"skills_dir"`
	DBPath    string `yaml:"db_path"`
	Tick      struct {
		IntervalSeconds        int `yaml:"interval_seconds"`
		OfflineCatchupMaxHours int `yaml:"offline_catchup_max_hours"`
	} `yaml:"tick"`
	LLM struct {
		Default   string                    `yaml:"default"`
		Providers map[string]fileProviderKV `yaml:"providers"`
	} `yaml:"llm"`
	MCP struct {
		Servers []MCPServer `yaml:"servers"`
	} `yaml:"mcp"`
}

// fileProviderKV 是 yaml 里一个命名 provider 的字段。
type fileProviderKV struct {
	Provider  string `yaml:"provider"`
	Model     string `yaml:"model"`
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"`
	APIKey    string `yaml:"api_key"`
}

// Load 加载配置：flagPath 为 -config 启动参数（空则按规则探测）。
// 文件存在但解析失败时返回错误；找不到任何文件时返回纯 env 配置。
func Load(flagPath string) (Config, error) {
	cfg := Config{
		ListenAddr:   ":8080",
		TickInterval: 60 * time.Second,
		OfflineMax:   24 * time.Hour,
		DBPath:       "data/pocketpet.db",
		DataRoot:     "data",
		SkillsDir:    "skills",
	}

	// 1. 定位并应用配置文件。
	path := locateConfig(flagPath)
	if path != "" {
		if err := cfg.applyFile(path); err != nil {
			return Config{}, err
		}
		cfg.ConfigPath = path
	}

	// 2. 环境变量覆盖（后应用，优先级更高）。
	cfg.applyEnv()
	return cfg, nil
}

// locateConfig 按优先级定位配置文件：启动参数 > 环境变量 > 默认探测。
// 显式指定（flag/env）但文件不存在时按"存在但加载失败"处理——返回该路径让上层报错。
func locateConfig(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if v := os.Getenv(EnvConfigPath); v != "" {
		return v
	}
	for _, p := range defaultConfigPaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// applyFile 读取并应用 YAML 配置文件。
func (cfg *Config) applyFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config file %s: %w", path, err)
	}
	var fc fileConfig
	if err := yaml.Unmarshal(b, &fc); err != nil {
		return fmt.Errorf("config file %s: parse: %w", path, err)
	}

	if fc.Server.Listen != "" {
		cfg.ListenAddr = fc.Server.Listen
	}
	if fc.DataDir != "" {
		cfg.DataRoot = fc.DataDir
	}
	if fc.SkillsDir != "" {
		cfg.SkillsDir = fc.SkillsDir
	}
	if fc.DBPath != "" {
		cfg.DBPath = fc.DBPath
	}
	if fc.Tick.IntervalSeconds > 0 {
		cfg.TickInterval = time.Duration(fc.Tick.IntervalSeconds) * time.Second
	}
	if fc.Tick.OfflineCatchupMaxHours > 0 {
		cfg.OfflineMax = time.Duration(fc.Tick.OfflineCatchupMaxHours) * time.Hour
	}
	if len(fc.MCP.Servers) > 0 {
		cfg.MCPServers = fc.MCP.Servers
	}

	// 命名 provider：解析 api_key_env；直写 api_key 支持但告警（secrets 不宜入库）。
	for name, kv := range fc.LLM.Providers {
		pc := llm.ProviderConfig{
			Provider: llm.NormalizeProvider(kv.Provider),
			Model:    kv.Model,
			BaseURL:  kv.BaseURL,
		}
		switch {
		case kv.APIKeyEnv != "":
			pc.APIKey = os.Getenv(kv.APIKeyEnv)
		case kv.APIKey != "":
			slog.Warn("config: api_key written directly in config file is discouraged; prefer api_key_env",
				"file", path, "provider", name)
			pc.APIKey = kv.APIKey
		}
		if cfg.LLMProviders == nil {
			cfg.LLMProviders = map[string]llm.ProviderConfig{}
		}
		cfg.LLMProviders[name] = pc
	}
	cfg.LLMDefault = fc.LLM.Default
	return nil
}

// applyEnv 应用环境变量覆盖（优先级高于配置文件）。
func (cfg *Config) applyEnv() {
	if v := os.Getenv(EnvListenAddr); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv(EnvTickInterval); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.TickInterval = d
		}
	}
	if v := os.Getenv(EnvOfflineMax); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.OfflineMax = d
		}
	}
	if v := os.Getenv(EnvDBPath); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv(EnvDataDir); v != "" {
		cfg.DataRoot = v
	}
	if v := os.Getenv(EnvSkillsDir); v != "" {
		cfg.SkillsDir = v
	}
	if v := os.Getenv(EnvMCPServers); v != "" {
		var servers []MCPServer
		if err := json.Unmarshal([]byte(v), &servers); err == nil {
			cfg.MCPServers = servers // 整体覆盖文件列表
		} else {
			slog.Warn("config: ignore malformed POCKETPET_MCP_SERVERS", "err", err)
		}
	}
}
