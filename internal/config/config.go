// Package config 集中管理 pocketpetd 的运行配置。
// M1 阶段仅支持默认值 + 环境变量覆盖，后续里程碑再引入 TOML 配置文件。
package config

import (
	"encoding/json"
	"log/slog"
	"os"
	"time"
)

// 环境变量名。
const (
	EnvListenAddr   = "POCKETPET_LISTEN"        // HTTP 监听地址
	EnvTickInterval = "POCKETPET_TICK_INTERVAL" // tick 间隔，如 "60s"、"1s"
	EnvOfflineMax   = "POCKETPET_OFFLINE_MAX"   // 离线结算补算时长上限，如 "24h"
	EnvDBPath       = "POCKETPET_DB_PATH"       // SQLite 数据库文件路径，":memory:" 表示内存库
	EnvDataDir      = "POCKETPET_DATA_DIR"      // 数据根目录（petfs 宠物文件在其 pets/ 子目录下）
	EnvSkillsDir    = "POCKETPET_SKILLS_DIR"    // 全局技能目录（SKILL.md 技能包，对所有宠物可见）
	EnvMCPServers   = "POCKETPET_MCP_SERVERS"   // 全局可用 MCP servers，JSON 数组
)

// MCPServer 是一个可供宠物启用的 MCP server 声明（M4：仅 stdio 传输）。
// 对应 POCKETPET_MCP_SERVERS 的数组元素，如：
// {"name":"weather","command":"/usr/local/bin/mcp-weather","args":["--fast"],"env":{"K":"V"}}
type MCPServer struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
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
	MCPServers []MCPServer
}

// Load 返回应用了环境变量覆盖后的配置。
func Load() Config {
	cfg := Config{
		ListenAddr:   ":8080",
		TickInterval: 60 * time.Second,
		OfflineMax:   24 * time.Hour,
		DBPath:       "data/pocketpet.db",
		DataRoot:     "data",
		SkillsDir:    "skills",
	}
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
			cfg.MCPServers = servers
		} else {
			slog.Warn("config: ignore malformed POCKETPET_MCP_SERVERS", "err", err)
		}
	}
	return cfg
}
