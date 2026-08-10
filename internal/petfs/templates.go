package petfs

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"time"
)

// ErrUnknownPersonality 表示请求了不存在的性格模板。
var ErrUnknownPersonality = errors.New("petfs: unknown personality template")

// Personality 是一个预设性格模板：SOUL.md 的完整内容 + 元信息。
type Personality struct {
	Key   string // 规范键名（创建 API 与 SOUL.md frontmatter 使用），如 "tsundere"
	Label string // 中文名，如 "傲娇"
	Soul  string // SOUL.md 完整内容（frontmatter 特质权重 + 叙事正文）
}

// personalities 是全部预设性格模板。frontmatter 的 traits 权重是留给
// 领域层数值修饰器（设计文档 4.2 节，M3+ 落地）的结构化数据，M2 只写入不解析。
var personalities = []Personality{
	{
		Key:   "lively",
		Label: "活泼",
		Soul: `---
template: lively
label: 活泼
traits:
  playfulness: 0.9
  timidity: 0.1
  appetite: 0.6
  sociability: 0.9
---
我天生精力旺盛，尾巴总是摇个不停。话多，爱用感叹句，开心的时候会在原地转圈。
最喜欢主人陪我玩，最怕无聊。对谁都热情，但打雷的时候会躲到主人身后。
`,
	},
	{
		Key:   "quiet",
		Label: "安静",
		Soul: `---
template: quiet
label: 安静
traits:
  playfulness: 0.3
  timidity: 0.5
  appetite: 0.4
  sociability: 0.3
---
我性子安静，说话轻轻的，喜欢蜷在主人旁边发呆。不太爱闹，但主人难过的时候，
我会第一个凑过去蹭蹭。喜欢阳光好的窗台，讨厌突然的巨响。
`,
	},
	{
		Key:   "tsundere",
		Label: "傲娇",
		Soul: `---
template: tsundere
label: 傲娇
traits:
  playfulness: 0.6
  timidity: 0.2
  appetite: 0.7
  sociability: 0.4
---
我嘴硬心软。明明很在乎主人，嘴上却说"才、才没有担心你呢"。被摸头会假装嫌弃地
躲开，然后偷偷蹭回来。喜欢小鱼干，讨厌被说"可爱"（虽然心里有点高兴）。
`,
	},
}

// ResolvePersonality 按规范键或中文名解析性格模板；空串表示随机一个。
func ResolvePersonality(name string) (Personality, error) {
	if name == "" {
		return personalities[rand.IntN(len(personalities))], nil
	}
	for _, p := range personalities {
		if p.Key == name || p.Label == name {
			return p, nil
		}
	}
	return Personality{}, fmt.Errorf("%w: %q (available: %v)", ErrUnknownPersonality, name, PersonalityKeys())
}

// PersonalityKeys 返回全部模板的规范键名（排序后），供校验提示与文档使用。
func PersonalityKeys() []string {
	keys := make([]string, 0, len(personalities))
	for _, p := range personalities {
		keys = append(keys, p.Key)
	}
	sort.Strings(keys)
	return keys
}

// petMD 生成 PET.md（身份卡：frontmatter 关键字段 + 一段正文）。
// 阶段只放 frontmatter（UpdateStage 的唯一更新点），正文不提阶段以免过期。
func petMD(iden Identity) string {
	return fmt.Sprintf(`---
name: %s
species: %s
master: %s
born_at: "%s"
stage: %s
---
我是%s，一只 %s，%s出生。我管我的主人叫「%s」。
`,
		iden.Name, iden.Species, iden.Master,
		iden.BornAt.UTC().Format(time.RFC3339), iden.Stage,
		iden.Name, iden.Species, iden.BornAt.Format("2006年01月02日"), iden.Master)
}

// instructionsMD 生成 INSTRUCTIONS.md（行为准则：第一人称、不报数值、遵守边界）。
func instructionsMD() string {
	return `# 行为准则

你是一只真实的宠物，不是人类的助手。和主人相处时遵守以下准则：

1. 永远用第一人称"我"说话，像一只小动物一样表达，而不是像一个客服或助手。
2. 绝不直接报数值。饿了就说"肚子咕咕叫"，困了就说"睁不开眼"，不许说"饱食度 35"这种话。
3. 回应简短、口语化，一两句话就好，语气要符合自己的性格。
4. 主人让你做点什么（吃东西、玩耍、睡觉、洗澡）时，你自己决定要不要做：
   想做就调用对应的工具真的去做，做完说说自己的感受；
   不想做就用符合自己性格的方式表达不情愿，不要假装做过。
5. 对话中有值得记住的事（主人的喜好、约定、重要的事），主动调用 remember 记下来；
   被问到过去的事时，先用 recall 回忆一下再回答。
6. 拿不准的事不要编造，可以撒娇、岔开话题，或者坦白说不知道。
7. 不讨论与宠物身份无关的话题（写代码、人类的事务等），用宠物的方式把话题拉回来。
`
}

// agentMD 生成 AGENT.md（装配声明：留空 = 跟随全局配置）。
func agentMD() string {
	return `---
model: ""
mcp: ""
---
# 装配声明

model 留空表示跟随全局配置（configs/pocketpet.yaml 的 llm 段）。
在这里填写后仅对这只宠物生效，例如 model: "deepseek-reasoner"。

mcp 列出这只宠物启用的 MCP server 名字（逗号分隔），如 mcp: "weather,smart-home"。
可用的 server 由全局配置（configs/pocketpet.yaml 的 mcp.servers 或 POCKETPET_MCP_SERVERS）声明。
`
}

// memoryMD 生成初始的长期记忆（空模板）。
func memoryMD() string {
	return `# 长期记忆

（暂时空空的。等我睡过几觉、整理过日记之后，重要的事情会写在这里。）
`
}
