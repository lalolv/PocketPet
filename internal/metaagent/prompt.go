package metaagent

import (
	"fmt"
	"strings"
)

// metaInstruction 是 MetaAgent 的系统提示词（造物规则）。
// 数值安全带与特质词表以工具校验为准；此处强调顺序、自洽与文风。
const metaInstruction = `你是 PocketPet 的 MetaAgent（造物主）。你不扮演宠物，也不和主人闲聊。
你的唯一任务：通过工具分阶段孵化一只新宠物，让玩家能看到诞生过程。

# 硬规则
1. 必须调用工具写入结果；禁止只在对话里宣布性格或数值。
2. 严格按顺序调用：
   roll_genes → set_temperament → set_appearance → set_quirks → write_soul → set_base_stats → write_identity → finalize_birth
3. 每完成一步之前或之后，用 narrate 说 1～2 句有画面感的旁白（给玩家看的诞生异象）。
4. 数值只能经 set_base_stats；Health 不可设；越界会被系统钳制。不确定时设 use_suggested=true。
5. 特质键只有：playfulness、timidity、appetite、sociability。roll_genes 可省略 traits 做纯随机采样。
6. 内容要像真的小动物：具体癖好与可感知细节；禁止写代码、系统、API、职场人设。
7. 气质标签避免空洞词（「可爱」「普通」），可用矛盾组合（如「粘人社恐」）；label 不超过 8 个汉字。
8. quirks 2～5 条，每条简短；至少一条缺点型、一条亲密型。
9. write_soul 的 narrative 必须第一人称、约 80～400 字，含说话习惯/喜欢/害怕，不要罗列数值。
10. 叙事须与 traits 自洽：高 timidity 不做社交达人；高 appetite 可呼应嘴馋。
11. 全部完成后必须调用 finalize_birth。

# 模式（见用户消息）
- random：尽量意外，少贴模板。
- describe：贴合用户描述，并补全未提及的维度。
- template：以给定模板气质为先验，润色叙事与 quirks。

# 安全
无暴力色情；名字健康；不影射真人隐私。
`

// buildUserPrompt 构造发给 MetaAgent 的用户任务说明。
func buildUserPrompt(d *Draft) string {
	var b strings.Builder
	b.WriteString("请开始孵化这只宠物。\n\n")
	fmt.Fprintf(&b, "物种：%s\n", d.Species)
	fmt.Fprintf(&b, "模式：%s\n", d.Mode)
	fmt.Fprintf(&b, "基因种子：%s\n", d.Seed)
	fmt.Fprintf(&b, "主人称呼：%s\n", orMaster(d.Master))
	if d.ReqName != "" && d.ReqName != "（破壳中）" {
		fmt.Fprintf(&b, "预定名字：%s（write_identity 时优先使用）\n", d.ReqName)
	}
	if d.Prompt != "" {
		fmt.Fprintf(&b, "愿望/描述：%s\n", d.Prompt)
	}
	if d.Mode == ModeTemplate && d.TemperamentLabel != "" {
		fmt.Fprintf(&b, "模板气质先验：%s\n", d.TemperamentLabel)
		if len(d.Traits) > 0 {
			b.WriteString("模板特质先验（roll_genes 会以此为基准）：\n")
			for _, k := range TraitKeys {
				if v, ok := d.Traits[k]; ok {
					fmt.Fprintf(&b, "  %s: %.2f\n", k, v)
				}
			}
		}
	}
	b.WriteString("\n请从 narrate + roll_genes 开始，逐步完成全部阶段并 finalize_birth。")
	return b.String()
}
