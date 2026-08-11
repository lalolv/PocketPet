# PocketPet

[![Go Version](https://img.shields.io/github/go-mod/go-version/lalolv/PocketPet)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

> 宠物即 Agent —— 一只宠物 = 一组文件 + 一个数值状态 + 一个运行时。

PocketPet 是一个 Agent 原生的虚拟宠物项目：主人直接和宠物第一人称对话，宠物由一组可读的文件（人格、记忆、技能）定义；睡觉时不只是恢复精力，还会"做梦"——整理记忆、演化性格，甚至把反复的经验沉淀为技能。

## 特性

- **宠物即 Agent**：每只宠物是一个独立的 LLM Agent，以第一人称回应，有自己的意愿——傲娇的猫可能拒绝你，困极了会主动说想睡觉。
- **文件即存在**：人格（`PET.md`/`SOUL.md`）、记忆（`MEMORY.md` + 日记）、技能（`SKILL.md`）都是可读文件，主人可以查看、备份、版本管理宠物的"内心"。
- **数值在 Go，灵魂在 LLM**：属性/成长/生死规则确定性实现（纯 Go，无 cgo）；人格与表达交给 LLM；LLM 不可用时养成闭环照常运转（降级为性格化文案）。
- **睡眠即整理**：入睡触发"梦境整理"——日记凝练成长期记忆、性格小幅演化、反复经验沉淀为 Skill，并生成梦境独白事件。
- **状态驱动的主动行为**：饿了/脏了/病了/心情低落会主动给主人发消息（第一人称，经 SSE 推送），困了自动入睡、睡饱自动醒来——可在配置的 `proactive` 段逐项关闭。
- **三层扩展体系**：SKILL.md 技能包（叙事与行为）/ MCP（外部能力）/ Go 插件（内核级扩展）。规则进插件，叙事进 Skill，外部进 MCP。
- **OpenAI 兼容端点**：统一走 Chat Completions（OpenAI 官方 / DeepSeek / Moonshot / vLLM / Ollama 等均可），可按宠物覆盖模型。
- **双端运行**：`pocketpetd` 后端守护进程（REST + SSE）+ `pocketpet-tui` 终端客户端（Bubble Tea 动画界面）。

## 快速开始

要求：Go 1.26+

```bash
git clone https://github.com/lalolv/PocketPet.git
cd PocketPet

# 准备本地配置（该文件已在 .gitignore 中，不会被提交）
cp configs/pocketpet.example.yaml configs/pocketpet.yaml

# 启动后端（未配置 LLM 也能跑，chat 走降级文案）
go run ./cmd/pocketpetd -config configs/pocketpet.yaml

# 另一个终端，启动 TUI 客户端
go run ./cmd/pocketpet-tui
```

也可以直接安装：

```bash
go install github.com/lalolv/PocketPet/cmd/pocketpetd@latest
go install github.com/lalolv/PocketPet/cmd/pocketpet-tui@latest
```

### 通过 API 互动

```bash
# MetaAgent 分阶段诞生（已配置 LLM 时走造物主 Agent；否则脚本降级）
curl -X POST localhost:8080/v1/pets/birth \
  -H 'Content-Type: application/json' \
  -d '{"name": "小咪", "species": "cat", "mode": "random"}'

# 即时创建（旧路径；personality 可选，空 = 随机模板）
curl -X POST localhost:8080/v1/pets \
  -H 'Content-Type: application/json' \
  -d '{"name": "小咪", "species": "cat"}'

# 和它说话
curl -X POST localhost:8080/v1/pets/{id}/chat \
  -H 'Content-Type: application/json' \
  -d '{"message": "今天过得怎么样？"}'

# 照顾它（feed / play / clean）
curl -X POST localhost:8080/v1/pets/{id}/care \
  -H 'Content-Type: application/json' \
  -d '{"action": "feed"}'
```

## 配置

配置优先级：启动参数 `-config` > 环境变量 > 配置文件 > 默认值。示例见 [configs/pocketpet.example.yaml](configs/pocketpet.example.yaml)。

配置 LLM（写在配置文件的 `llm` 段，密钥直接填写）：

```yaml
llm:
  model: deepseek-chat                 # 必填
  base_url: https://api.deepseek.com/v1 # 任意 OpenAI Chat Completions 兼容端点；留空 = OpenAI 官方
  api_key: sk-...                       # 必填，直接写在这里
```

每只宠物可通过自己的 `AGENT.md` 覆盖模型（`model` 字段）。

## API 概览

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/v1/pets` | 创建宠物（默认即时模板；`genesis.legacy_create=birth` 时转发 MetaAgent） |
| POST | `/v1/pets/birth` | MetaAgent 分阶段诞生（SSE `genesis.*`；可选 `await_soul`） |
| GET | `/v1/pets` / `/v1/pets/{id}` | 列表 / 状态查询 |
| POST | `/v1/pets/{id}/chat` | 和宠物对话（`?stream=true` SSE 流式） |
| POST | `/v1/pets/{id}/care` | 照顾动作（feed / play / clean） |
| GET | `/v1/pets/{id}/events` | SSE 事件流（proactive / dream / skill_learned / stage_up…） |
| GET | `/v1/pets/{id}/soul` | 查看 SOUL.md（含演化历史） |
| GET | `/v1/pets/{id}/memory` | 查看长期记忆 / 日记 |
| GET | `/v1/pets/{id}/skills` | 技能列表（含睡眠沉淀学到的） |
| GET | `/v1/meta/providers`、`/healthz` | 元信息 / 健康检查 |

## 项目结构

```
├── cmd/
│   ├── pocketpetd/      # 后端守护进程（REST + SSE）
│   └── pocketpet-tui/   # 终端互动客户端
├── internal/
│   ├── pet/             # 领域层：属性/成长/规则（纯 Go，不依赖 LLM）
│   ├── agent/           # PetAgent 运行时：指令装配、工具、降级文案
│   ├── metaagent/       # MetaAgent 诞生：工具链 / 脚本 / fallback
│   ├── dream/           # 梦境整理：记忆凝练、SOUL 演化、Skill 沉淀
│   ├── proactive/       # 主动行为：状态触发的主动消息、自动入睡/醒来
│   ├── llm/             # LLM 连接工厂（OpenAI Chat Completions 兼容端点）
│   ├── adkx/            # ADK 薄脚手架（一次性 runner / 事件文本）
│   ├── httpx/           # 统一 JSON HTTP 响应（api 与插件共用）
│   ├── api/             # REST + SSE 接口层
│   ├── store/           # SQLite：数值状态 + 事件流水
│   ├── petfs/           # 宠物文件系统（PET/SOUL/MEMORY/skills）
│   ├── tick/            # tick 引擎：衰减、离线结算
│   ├── plugin/          # Go 插件契约与注册表
│   ├── plugins/         # 内置插件（探险 / 交友）
│   └── tui/             # TUI 客户端实现
├── skills/              # 全局技能包
├── configs/             # 配置示例
└── docs/                # 设计文档
```

## 文档

- [调研报告](docs/01-调研报告.md)
- [技术选型评估](docs/02-技术选型评估.md)
- [架构设计方案](docs/03-架构设计方案.md)
- [MetaAgent 宠物诞生设计](docs/04-MetaAgent宠物诞生设计.md)

## License

[MIT](LICENSE) © 2026 Lalo
