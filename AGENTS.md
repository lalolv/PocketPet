# AGENTS.md

> 面向 AI 编码代理的项目说明。读者被假定为对本项目一无所知。

## 项目概述

PocketPet 是一个 **Agent 原生的虚拟宠物**项目（Go 1.26+，模块名 `github.com/lalolv/PocketPet`，MIT License）。

核心理念：**宠物即 Agent**——一只宠物 = 一组可读文件（人格 `PET.md`/`SOUL.md`、记忆 `MEMORY.md` + 日记、技能 `SKILL.md`）+ 一份数值状态（SQLite）+ 一个 LLM Agent 运行时。主人与宠物第一人称对话；入睡触发"梦境整理"（凝练记忆、演化性格、沉淀技能）。

架构铁律：**数值在 Go，灵魂在 LLM**。

- 属性/成长/生死等规则是**确定性**实现（纯 Go，无 cgo），不经过 LLM。
- 人格与表达交给 LLM；LLM 未配置或调用失败时养成闭环照常运转（降级为性格化文案），chat 永远有回应。

## 技术栈

- 语言：Go 1.26（见 `go.mod`），无 cgo（SQLite 用纯 Go 驱动 `modernc.org/sqlite`）。
- LLM 接入：统一走 **OpenAI Chat Completions 兼容端点**（OpenAI / DeepSeek / Moonshot / vLLM / Ollama 等），基于 Google ADK（`google.golang.org/adk/v2`）装配 Agent；`internal/llm/chatmodel` 是手写的最小 Chat Completions 适配器（刻意不用 openai-go SDK）。
- 外部能力：MCP（`github.com/modelcontextprotocol/go-sdk`，仅 stdio 传输）。
- 跨实例：A2A（`github.com/a2aproject/a2a-go/v2`）。
- TUI：Bubble Tea v2 + Lip Gloss v2（`charm.land/bubbletea/v2`）。
- 日志：`log/slog`，终端下经 `lmittmann/tint` 彩色输出，重定向时退化为纯文本（遵守 `NO_COLOR`）。
- 持久化：SQLite（数值状态 JSON 快照 + 事件流水）+ 宠物文件目录（人格/记忆/技能）。

## 构建、运行与测试

```bash
# 构建（验证编译）
go build ./...

# 运行后端守护进程（REST + SSE，默认 :8080）
go run ./cmd/pocketpetd -config configs/pocketpet.yaml

# 运行 TUI 客户端（另一个终端；-server 或 POCKETPET_SERVER 指定后端地址）
go run ./cmd/pocketpet-tui

# 安装
go install github.com/lalolv/PocketPet/cmd/pocketpetd@latest
go install github.com/lalolv/PocketPet/cmd/pocketpet-tui@latest

# 全部测试（当前全部通过，无外部依赖）
go test ./...

# 常用：指定包 / 竞态检测
go test ./internal/plugins/... ./internal/plugin/
go test -race ./...
```

未配置 LLM 也能跑：`chat` 走降级文案，MetaAgent 诞生走脚本降级。仓库**没有** Makefile 和 CI 配置，测试即上述 `go test`。

## 目录结构

```
├── cmd/
│   ├── pocketpetd/      # 后端守护进程（装配全部组件，REST + SSE）
│   └── pocketpet-tui/   # 终端互动客户端（Bubble Tea）
├── internal/
│   ├── pet/             # 领域层：属性/成长阶段/tick 衰减规则/领域事件。纯 Go：不 import net/http、不 import 数据库驱动
│   ├── petstate/        # 统一宠物状态管理器：活动态互斥、意图排队、插件 Kind 注册（docs/07）
│   ├── tick/            # tick 引擎：周期衰减、离线补算、事件落库与扇出；petID 锁串行化
│   ├── store/           # SQLite 持久化（pets 快照 / pet_events 流水 / kv_meta 迁移版本）
│   ├── petfs/           # 宠物文件系统：<data>/pets/<id>/ 下 PET/SOUL/INSTRUCTIONS/AGENT/MEMORY.md、memory/ 日记、skills/
│   ├── agent/           # PetAgent 运行时：每宠物惰性装配 ADK llmagent，指令由 petfs 文件 + 实时状态动态拼装
│   ├── metaagent/       # MetaAgent 宠物诞生流程（工具/脚本 → LLM → 超时降级，docs/04）
│   ├── dream/           # 梦境整理器：入睡后异步凝练记忆、演化 SOUL、沉淀 Skill
│   ├── proactive/       # 状态驱动主动行为：主动消息、自动入睡/醒来
│   ├── narrate/         # 叙事层：主人可见文案只能复述 pet 快照 / Apply 结果，禁止臆造活动态
│   ├── llm/             # LLM 连接工厂（chatmodel/ 为 Chat Completions 适配器）
│   ├── adkx/            # ADK 薄脚手架（一次性 runner、事件文本提取）
│   ├── api/             # REST（/v1）+ SSE 事件流 + A2A 端点
│   ├── httpx/           # 统一 JSON 响应助手（api 与插件路由共用，避免 plugins → api 反向依赖）
│   ├── plugin/          # Go 插件契约（核心接口 + 可选能力接口集）与 Registry
│   ├── plugins/         # 内置插件聚合 Build() + 实现：adventure（探险）/ friends（交友）
│   └── tui/             # TUI 客户端实现
├── skills/              # 全局 SKILL.md 技能包（对全部宠物可见）
├── configs/             # 配置示例（pocketpet.example.yaml）
├── data/                # 运行时数据（SQLite + pets/），已在 .gitignore
└── docs/                # 设计文档 01-07（中文）
```

## 运行时架构（pocketpetd 装配顺序，见 `cmd/pocketpetd/main.go`）

1. `config.Load`：`-config` 参数 > 环境变量（`POCKETPET_*`）> YAML 文件 > 默认值。
2. `store.Open` 打开 SQLite 并跑核心迁移；`plugin.Registry` 先跑插件迁移。
3. `tick.Engine` 是协调核心：周期结算 + 事件扇出（`MultiSink` 分发给 SSE hub、阶段同步器、梦境整理器、主动行为器、插件 `EventSubscriber`）。
4. `agent.PetAgent` 按需为每只宠物装配 ADK llmagent：`AGENT.md` 变更（model/mcp）触发重建，技能实时读盘自动生效。
5. 插件 `InitAll` → tick 钩子 → 工具 → 路由（挂载于 `/v1/plugins/<name>/`）。

关键并发约定：

- 每只宠物的所有状态操作经 `tick.Engine` 的 petID 锁串行化；`EventSink`/`TickHook` 必须快速返回（慢钩子 >100ms 打 Warn），**不得对同一宠物重入 Engine**（可能持有 petID 锁）。
- 活动态切换一律经 `petstate.Manager.Apply`；文案复述经 `narrate.Policy`。

## 配置

- 示例：`configs/pocketpet.example.yaml`（复制为 `configs/pocketpet.yaml` 使用；真实配置已在 `.gitignore`，**不要提交**）。
- 配置文件定位：`-config` 参数 → `POCKETPET_CONFIG` → `./pocketpet.yaml` → `./configs/pocketpet.yaml`；都找不到则为纯 env 模式。
- LLM：配置文件 `llm` 段的 `model` + `api_key` 必填，`base_url` 留空 = OpenAI 官方；每只宠物可经自己的 `AGENT.md` 的 `model` 字段覆盖模型。
- 常用环境变量：`POCKETPET_LISTEN`、`POCKETPET_TICK_INTERVAL`、`POCKETPET_DB_PATH`（`:memory:` 为内存库）、`POCKETPET_DATA_DIR`、`POCKETPET_LOG_LEVEL`、`POCKETPET_GENESIS_*`、`POCKETPET_MCP_SERVERS`（JSON 整体覆盖）。

## API 概览

- 核心：`POST /v1/pets`（即时创建）｜`POST /v1/pets/birth`（MetaAgent 诞生，SSE）｜`GET /v1/pets[/{id}]`｜`POST /v1/pets/{id}/chat`（`?stream=true` 流式）｜`POST /v1/pets/{id}/care`（feed/play/clean）｜`GET /v1/pets/{id}/events`（SSE）｜`GET /v1/pets/{id}/soul|memory|skills`｜`/healthz`。
- 插件路由统一挂载在 `/v1/plugins/<name>/` 前缀下。
- 错误响应统一为 `{"error":{"code","message"}}`（`internal/httpx`）。

## 三层扩展体系（改需求先选层）

铁律：**规则进插件，叙事进 Skill，外部进 MCP**。

1. **SKILL.md 技能包**（`skills/` 全局或宠物私有 `skills/`）：叙事、仪式、教宠物怎么用工具。带 YAML frontmatter（`name`/`description`）。
2. **MCP**：进程外能力（天气、硬件等）；配置 `mcp.servers`，宠物在 `AGENT.md` 里按名启用。
3. **Go 插件**（`internal/plugins/`）：确定性玩法规则（掉落、好感度、结算）。编译期可信代码，不支持热插拔，改完需重编译。

新增 Go 插件（详见 `docs/05-插件开发指南.md`）：

- 新建 `internal/plugins/<name>/` 包，必须实现 `Plugin{Name(), Init(PluginContext)}`；按需实现 `ToolProvider` / `SchemaProvider` / `TickHook` / `RouteProvider` / `EventSubscriber` / `Depender`（硬依赖）/ `Shutdowner`。
- **没有自动扫描**：必须在 `internal/plugins/builtins.go` 的 `Build` 里显式注册（可选加 `config.PluginsConfig` YAML 开关）。
- 玩法插件彼此独立：不 import、不查询、不订阅其他插件；软依赖用 `pctx.HasPlugin` 降级。
- 插件表名以插件名开头（如 `adventure_*`），禁止改写核心表（`pets`/`pet_events`/`kv_meta`）；数值操作走 `PluginContext.Care/AdjustStats/Emit`。

## 代码风格约定

- **注释与文档一律使用中文**（包注释、导出符号 doc comment、设计文档均如此）；代码标识符用英文。
- 每个包顶部有说明职责的包注释，改包职责时同步更新。
- 日志用 `log/slog` 的结构化字段风格。
- 分层约束（编译期靠自觉遵守）：
  - `pet`：纯领域逻辑，不 import `net/http`、不 import 数据库驱动。
  - `petfs`：不 import ADK，不依赖数据库。
  - 插件不依赖 `api` 包；共享 HTTP 助手放 `httpx`。
  - `adkx` 只做共享装配样板，不把各用例包收编成统一 agents 目录。
- 布尔/数值配置项常用指针（`*bool`/`*int`）区分"未配置（取默认）"与"显式设置"。
- 测试接缝：`agent.Options.ModelFactory` / `MCPTransport` 注入假实现；`store.Open(":memory:")` 内存库；`pet` 包有可注入的 Clock；`genesis.script_pace_ms: 0` 让脚本尽快跑完（测试常用）。

## 测试

- 策略：标准 `go test`，表驱动 + 子测试；几乎所有 `internal/*` 包都有 `*_test.go`（`cmd/`、`adkx`、`httpx`、`llm` 除外）。
- 测试不依赖真实 LLM/网络：用内存 SQLite、注入 fake model、假传输（`internal/agent/testdata/mcpecho` 是测试用 MCP echo server）。
- 提交前至少跑 `go test ./...`；改动并发/事件路径时加 `-race`。

## 安全注意事项

- **API 密钥直接写在配置文件里**（`llm.api_key`），因此 `pocketpet.yaml`（根目录与 `configs/` 下）与 `data/` 已在 `.gitignore`——绝不要提交真实配置或密钥。
- `petfs` 防路径穿越：petID 限定 `^[A-Za-z0-9_-]+$`，顶层文件读写有白名单（`writableFiles`），日记文件名限定 `YYYY-MM-DD.md`。
- SQLite 单写者：`SetMaxOpenConns(1)` + `busy_timeout`；不要绕过 `tick.Engine` 直接写 `pets` 表。
- 插件是编译期可信代码、权限接近内核；新增插件意味着信任其代码。
- HTTP 服务无鉴权，默认面向本机/私网使用；`ReadHeaderTimeout` 已设。

## 部署

- 无容器/CI 配置；分发方式是 `go install`（见上）或直接 `go build` 产出 `pocketpetd` / `pocketpet-tui` 两个二进制。
- 运行依赖仅为数据目录（默认 `./data`）与可选配置文件；无外部服务必需项。

## 参考文档（docs/，均为中文）

- `01-调研报告.md`、`02-技术选型评估.md`、`03-架构设计方案.md`（含插件体系设计 §8）
- `04-MetaAgent宠物诞生设计.md`、`05-插件开发指南.md`（新增玩法步骤）、`06-探险插件设计.md`、`07-宠物状态管理器.md`、`08-探险地图主题生成.md`（LLM 岛名/地点名/描述规范）
