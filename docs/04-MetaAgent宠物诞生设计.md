# MetaAgent 宠物诞生设计

> 版本：v0.1  
> 前置文档：`docs/03-架构设计方案.md`（尤其 §4 性格系统、§9 API）  
> 状态：G0–G2、**G4 已落地**；G3 待细化

## 0. 动机与目标

当前创建宠物时：

- 数值属性由 `pet.New` 写死常量；
- 性格从 3 个硬编码模板中选择或均匀随机；
- `SOUL.md` 的 traits 只写入、不参与数值修饰。

这与架构文档 4.1「模板 / 自然语言 / 盲盒」及「充分利用 LLM」的方向不符。

**目标**：引入 **MetaAgent（造物主 Agent）**，通过工具调用分阶段生成一只完整的宠物 Agent；每一阶段经 SSE 推到前端，玩家可实时观看诞生过程；SOUL 正文、外貌、气质标签、癖好、基础数值等均可高随机生成，同时由 **规则提示词 + Go 工具护栏** 保证合理与自洽。

**非目标（本设计不包含）**：

- 让 LLM 绕过领域层直接改运行中宠物的 Hunger 等养成公式；
- 用多 Agent 辩论或向量检索做出生（过重）；
- 一次 JSON 吐出全部字段替代分阶段工具流（会失去 SSE 剧场价值与逐步护栏）。

## 1. 核心理念

```
数值护栏在 Go 工具里；叙事与随机创意在 MetaAgent；
LLM 不可用时仍能降级出生（养成闭环不堵死）。
```

| | PetAgent | MetaAgent |
|---|---|---|
| 身份 | 宠物本人 | 造物主 / 孵化器 |
| 生命周期 | 常驻，可反复对话 | **每次创建一次**，结束后销毁 session |
| 工具作用对象 | 自己（sleep / wake / remember…） | **正在诞生的宠物档案** |
| 输出 | 第一人称聊天 | 结构化诞生步骤 + 旁白（给玩家看） |

与梦境整理器（`internal/dream`）的关系：

- Dream Reflector：睡眠时一次性 JSON 整理（无工具或弱工具）；
- MetaAgent：出生时 **多步 tool-call 状态机**，强依赖工具落盘与分阶段 SSE。

可复用：`llm.Config` / model 工厂、SSE Hub、`petfs.WriteSoulWithHistory`、JSON 容错解析模式。

## 2. 总体流程

```
POST /v1/pets/birth
        │
        ▼
  分配 petID + seed
  写入「蛋」骨架（genesis_status = incubating）
  立即 201 返回 { pet_id, seed, ... }
        │
        ▼
  启动 MetaAgent runner（独立 session，一次性）
        │
        ├─ roll_genes          → SSE genesis.genes
        ├─ set_temperament     → SSE genesis.temperament
        ├─ set_appearance      → SSE genesis.appearance
        ├─ set_quirks          → SSE genesis.quirks
        ├─ write_soul          → SSE genesis.soul
        ├─ set_base_stats      → SSE genesis.stats（可同步 state 帧）
        ├─ write_identity      → SSE genesis.identity
        ├─ finalize_birth      → SSE genesis.ready + pet.born
        │
        └─ 全程旁白文本        → SSE genesis.narration
```

前端订阅该宠物的事件流（或 birth 专用流），即可播放「开盲盒 / 破壳」剧场。

## 3. 创建模式

| mode | 含义 | MetaAgent 输入侧重 |
|---|---|---|
| `random` | 开盲盒（默认） | 高随机；用户 `prompt` 仅作软愿望 |
| `describe` | 自然语言描述 | `prompt` 为主，种子 traits 为先验 |
| `template` | 预设性格模板 | 模板 traits 为先验；LLM 润色叙事与 quirks |

兼容旧 API：`POST /v1/pets` 仅传 `personality` → 视为 `template`；`personality` 为空的快速创建可继续走「无 MetaAgent 的即时模板/采样」路径，或内部转发到 `birth`（实施时二选一，见 §12）。

## 4. 诞生阶段（严格串行）

阶段 **严格串行**，便于前端时间线与动画；MetaAgent 系统提示词规定推荐顺序，`finalize_birth` 校验清单。

| 序号 | 阶段 | 工具 | 必须产物 | 前端可展示 |
|---|---|---|---|---|
| 0 | 破壳启动 | （系统）`genesis.started` | petID、seed、species、mode | 「一颗蛋开始发光…」 |
| 1 | 基因 | `roll_genes` | traits 权重 | 特质条 / 雷达图 |
| 2 | 气质 | `set_temperament` | label + 一句话 blurb | 气质大标题 |
| 3 | 外貌 | `set_appearance` | 外貌段落 | 形象文案（预留绘图） |
| 4 | 癖好 | `set_quirks` | 2–5 条 quirks | 标签逐条弹出 |
| 5 | 灵魂 | `write_soul` | SOUL 正文 | 打字机展示性格 |
| 6 | 体质 | `set_base_stats` | 初始 Stats（Health 除外） | 生命力条 |
| 7 | 身份 | `write_identity` | 名、主人称呼、PET.md 字段 | 「它叫 XXX」 |
| 8 | 诞生 | `finalize_birth` | 校验齐全 → ready | 破壳完成，可聊天 |

规则：

- MetaAgent **必须通过工具落盘**；仅在对话里「宣布」性格/数值无效。
- 孵化中（`incubating`）禁止正式 `chat`（或返回固定「还在蛋里」文案）；`care` 是否允许实施时再定，默认禁止直至 `ready`。
- 旁白（narration）不能替代任何工具调用。

## 5. 工具契约与 Go 护栏

每个工具统一行为：**校验 → 写入孵化草稿 → 发 SSE → 返回 `{ ok, summary | error }` 给 LLM**。

草稿存储建议：`data/pets/<pet-id>/.genesis.json`（或内存 + 关键节点落盘），与正式 `PET.md` / `SOUL.md` 分离，直至 `finalize`。

### 5.1 `roll_genes`

**入参（可选）**：`bias`（describe 模式下的软约束说明，工具可忽略数值、只记备注）。

**出参示例**：

```json
{
  "ok": true,
  "traits": {
    "playfulness": 0.73,
    "timidity": 0.28,
    "appetite": 0.61,
    "sociability": 0.44
  }
}
```

**护栏**：

- 仅允许已知特质键（首期：`playfulness` / `timidity` / `appetite` / `sociability`；扩展须版本化）；
- 值钳制到 `[0, 1]`；未知键丢弃；
- **推荐实现**：工具内用 `seed` 做高方差采样（如 Beta / 三角分布 + 软反相关），LLM 调用后主要「阅读结果」再叙事——随机性稳定、可复现；
- 若允许 LLM 提交 traits：相对种子采样值单键偏移 ≤ `±0.15`（`describe` 可放宽到 `±0.25`）。

### 5.2 `set_temperament`

```json
{ "label": "粘人社恐", "blurb": "外表冷淡，其实一刻都离不开你" }
```

- `label` 建议 ≤ 8 个汉字；避免空洞词（「可爱」「普通」）；
- 不强制落在旧三模板（活泼/安静/傲娇），三模板仅作 `template` 模式先验或 fallback 锚点。

### 5.3 `set_appearance`

```json
{ "appearance": "毛色、体型、标志性特征……" }
```

- 写入草稿，finalize 时进入 `PET.md` 正文或预留的 `avatar.md`。

### 5.4 `set_quirks`

```json
{ "quirks": ["打雷钻纸箱", "只吃碗左边的粮", "被夸会假装走开"] }
```

- 条数 2–5；每条建议 ≤ 30 字；
- **纯叙事**，不进入数值公式；可写入 `SOUL.md` 正文附属或独立 frontmatter 列表（实施时定一种，保持解析稳定）。

### 5.5 `write_soul`

```json
{ "narrative": "第一人称性格正文……" }
```

- 组装 `SOUL.md`：frontmatter = 已 roll 的 traits + temperament label（+ locked: false）；
- 正文长度护栏（建议约 80–400 字）；
- 应与 quirks / 气质自洽（硬校验可选；主要靠提示词）。

### 5.6 `set_base_stats`

```json
{ "hunger": 72, "happy": 81, "clean": 77, "energy": 94 }
```

**护栏（硬）**：

| 属性 | 规则 |
|---|---|
| Health | **固定 100**，工具拒绝修改 |
| Hunger | 安全带 50–90 |
| Happy | 安全带 55–95 |
| Clean | 安全带 50–95 |
| Energy | 安全带 70–100 |
| EXP | 出生为 0，不可由 MetaAgent 设置 |

建议：工具按 genes 给出默认建议值，LLM 仅在带内微调；或无参调用表示「接受建议值」。Stats 只在 Go 侧写入领域对象，**禁止** LLM 在旁白中「生效」数值。

### 5.7 `write_identity`

```json
{ "name": "团团", "master": "主人", "species_flavor": "橘白短毛猫" }
```

- 请求已带 `name` 时，以请求为准或仅允许微调（实施定一种）；
- `master` 默认「主人」。

### 5.8 `finalize_birth`

- 检查阶段 1–7 均已成功；
- 提交草稿 → 正式 petfs 文件 + SQLite 数值状态；
- `genesis_status = ready`；
- 发出 `genesis.ready` 与（若尚未发过）`pet.born`；
- 不齐全则 `ok: false` 并指明缺失阶段，MetaAgent 可补调。

## 6. 随机性与合理性的分工

```
┌─────────────────────────────┐
│ MetaAgent System Prompt     │  角色、顺序、文风、自洽、安全
└──────────────┬──────────────┘
               │
┌──────────────▼──────────────┐
│ Tool schemas + Go validators│  合法键、安全带、条数、必填阶段
└──────────────┬──────────────┘
               │
┌──────────────▼──────────────┐
│ Sampler（roll_genes 等）     │  高方差先验；LLM 负责「讲圆」
└─────────────────────────────┘
```

| 维度 | 随机来源 | 合理性来源 |
|---|---|---|
| traits | seed 采样分布 | 键表 + `[0,1]` + 偏移上限 |
| 基础数值 | 带内采样 / traits 偏移 | 安全带；Health 固定 |
| 气质 / 癖好 / 外貌 / SOUL | LLM 高 temperature | 提示词自洽规则 + 长度/条数护栏 |

**不推荐**：让 LLM 直接发明任意 trait 键并进入数值修饰公式；开放词表与确定性规则无法对齐。

## 7. MetaAgent 规则提示词（纲要）

落地文件建议：`internal/metaagent/prompt.go` 内嵌，或 `prompts/metaagent.md`（便于非发版调文案）。

提示词须覆盖：

1. **角色**：造物主，不扮演宠物，不与主人闲聊；用工具孵化。
2. **硬顺序**：genes → temperament → appearance → quirks → soul → stats → identity → finalize。
3. **工具优先**：禁止只在文本里宣布结果。
4. **数值边界**：只能经 `set_base_stats`；Health 不可设；越界会被钳制。
5. **特质词表**：仅列出的键；与叙事自洽（高 timidity 不做社交达人等）。
6. **物种隐喻**：符合 species 的小动物习性，避免职场/客服人设。
7. **创意质量**：气质用具体矛盾组合；quirks 含「缺点型」+「亲密型」；SOUL 第一人称、含喜好与害怕、不罗列数值。
8. **旁白**：每阶段 1–2 句，有画面，供 SSE `genesis.narration`。
9. **模式**：random / describe / template 的输入权重说明。
10. **安全**：无色情暴力；名字健康；不影射隐私。
11. **收尾**：全部完成后必须 `finalize_birth`。

数值安全带与 trait 表以 **工具 description / 工具返回的 rules 摘要** 为准，避免 prompt 与代码两套真相。

## 8. SSE 协议

复用现有 `Hub` 与 `GET /v1/pets/{id}/events`（孵化期即可订阅）。事件名与载荷：

| event | data（JSON）要点 |
|---|---|
| `genesis.started` | `pet_id`, `seed`, `species`, `mode` |
| `genesis.narration` | `stage`, `text` |
| `genesis.genes` | `traits` |
| `genesis.temperament` | `label`, `blurb` |
| `genesis.appearance` | `appearance` |
| `genesis.quirks` | `quirks` |
| `genesis.soul` | `preview`（全文可再 `GET .../soul`） |
| `genesis.stats` | 各属性；可同时推 `state` 帧 |
| `genesis.identity` | `name`, `master` |
| `genesis.ready` | `pet_id` |
| `genesis.failed` | `reason`, `fallback` |

可选：`POST /v1/pets/birth?stream=true` 在同一响应里直接吐 genesis 帧（类似 chat stream）；默认仍推荐 **201 返回 pet_id + 客户端订 events**，便于断线重放（事件入 `pet_events`）。

## 9. API

### 9.1 `POST /v1/pets/birth`

请求：

```json
{
  "name": "团团",
  "species": "cat",
  "mode": "random",
  "personality": "",
  "prompt": "一只怕水但爱洗澡的橘猫",
  "seed": "",
  "master": "主人"
}
```

| 字段 | 说明 |
|---|---|
| `species` | 必填 |
| `name` | 可选；空则由 `write_identity` 或 fallback 命名 |
| `mode` | `random` \| `describe` \| `template`；默认 `random` |
| `personality` | `template` 模式的模板键或中文名 |
| `prompt` | `describe` 必填；`random` 下为可选软愿望 |
| `seed` | 可选；空则服务端生成；有则基因可复现 |
| `master` | 可选，默认「主人」 |

响应 `201`：

```json
{
  "id": "...",
  "seed": "...",
  "species": "cat",
  "mode": "random",
  "genesis_status": "incubating",
  "events_url": "/v1/pets/{id}/events"
}
```

### 9.2 查询

`GET /v1/pets/{id}` 增加 `genesis_status`：`incubating` | `ready` | `failed`（failed 且已 fallback 成功则可为 `ready` + 标记）。

### 9.3 与旧 `POST /v1/pets` 的关系

保留即时创建路径，用于无 LLM / 测试 / 兼容；文档与 README 逐步引导新产品走 `/birth`。

## 10. 失败与降级

| 情况 | 行为 |
|---|---|
| 单工具参数非法 | 返回 `ok: false` + 原因，MetaAgent 可重试 |
| MetaAgent 总超时（建议 90s） | fallback：用 seed 确定性补齐未完成阶段并 finalize；`genesis.failed` 可先发或仅日志，最终仍 `genesis.ready` 且 `fallback: true` |
| 未配置 LLM | 不跑 MetaAgent；sampler + 模板/插值叙事；可发压缩版 genesis 事件（快速播完） |
| finalize 前客户端断开 | 后台继续；重连 SSE 回放已落库事件 |
| finalize 校验失败且重试耗尽 | 同超时 fallback，避免卡在 incubating |

原则与梦境一致：**LLM 挂了，宠物仍能出生、养成照常。**

## 11. 包结构与接线

```
internal/metaagent/
  agent.go       # 构造 llmagent + runner；RunBirth(ctx, draft)
  tools.go       # roll_genes / set_* / finalize
  prompt.go      # 规则提示词
  draft.go       # 孵化草稿与阶段位图
  sampler.go     # seed → traits / 建议 Stats
  events.go      # 发 genesis.* 到 EventSink / Hub
  fallback.go    # 超时与无 LLM 补齐
```

接线：

- `cmd/pocketpetd`：注册 birth 路由；Hub 订阅 genesis 事件；
- `internal/pet.New`：支持可选初始 Stats；
- `internal/petfs`：支持 quirks / temperament 字段写入约定；
- `PetAgent`：`incubating` 时拒绝或降级 chat；
- 后续落地架构 §4.2 **性格数值修饰器**，否则随机 traits 对手感影响有限。

`seed` 写入 `PET.md` frontmatter（或 SQLite），便于「基因码」分享与调试复现。

## 12. 实施里程碑

| 里程碑 | 内容 | 验收 |
|---|---|---|
| **G0 文档** | 本设计文档 | ✅ |
| **G1 草稿与工具骨架** | draft 状态机 + 工具 + 脚本调工具；SSE `genesis.*` | ✅ |
| **G2 MetaAgent** | 真 LLM runner + 规则 prompt；`POST /v1/pets/birth` | ✅ LLM 工具链诞生；失败/超时 fallback |
| **G3 降级与兼容** | fallback、无 LLM、旧 `POST /v1/pets` 策略 | 部分已含于 G1/G2；可再细化超时配置与文档 |
| **G4 玩法闭环** | traits → tick/care 修饰器；TUI 诞生剧场 | ✅ |

实施顺序：**先 G0（本文），再 G1 → G2 → G3 → G4**。

## 13. 前端体验（参考）

1. 选择物种 / 可选愿望文案 → 进入诞生剧场；
2. 左：蛋 → 剪影 → 成型；右：阶段时间线 + 旁白打字机；
3. traits / quirks 弹入；SOUL 滚动显现；
4. 收到 `genesis.ready` → CTA「和它说话」。

## 14. 设计决策记录

| 决策 | 选择 | 理由 |
|---|---|---|
| 出生编排者 | MetaAgent + 工具 | 分阶段 SSE、逐步护栏、可观测 |
| 阶段顺序 | 严格串行 | 动画与状态机简单 |
| Stats 来源 | Go 安全带 + 可选 LLM 带内微调 | 遵守「数值在 Go」 |
| traits 词表 | 固定键 | 才能做确定性修饰器 |
| quirks | 自由文本列表 | 表达面无限随机，不进公式 |
| 默认 API | 先 201 再 SSE | 断线重放、与现有 events 模型一致 |
| 失败策略 | fallback 仍出生 | 与全局 LLM 降级哲学一致 |

## 15. 对架构文档的修订点（实施时同步）

当 G2 落地后，应回写 `docs/03-架构设计方案.md`：

- §4.1 初始性格：改为指向本文三种 mode + MetaAgent；
- §9 API：增加 `POST /v1/pets/birth` 与 `genesis.*` 事件；
- §11 项目结构：增加 `internal/metaagent/`；
- §12 路线图：增加 G1–G4 或并入新里程碑编号。

---

**下一步**：G3 可补超时可配置与产品说明；玩法侧已可感知不同基因手感。

## 附录 A：G4 特质数值修饰（已落地）

从 `SOUL.md` frontmatter 读取 traits，经 `tick.Engine.SetTraitsLoader` 注入；`0.5` 为中性（倍率 1）。

| 特质 | 作用 |
|---|---|
| `appetite` | 饥饿衰减 ±25%；喂食 Hunger 收益 ±20% |
| `sociability` | 心情衰减由 `(1-sociability)` 驱动 ±20% |
| `timidity` | 心情衰减额外 ±10% |
| `playfulness` | 玩耍 Happy 收益 ±25%；玩耍耗能 ±15%；清醒精力消耗 ±10% |

TUI：创建表单提交走 `POST /v1/pets/birth`，`screenBirth` 订阅 `genesis.*` 播放蛋动画与旁白，`genesis.ready` 后进入主界面。
