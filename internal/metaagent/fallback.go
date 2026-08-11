package metaagent

import (
	"context"
	"fmt"
	"strings"
)

// FillMissing 用确定性内容补齐未完成阶段（超时 / 无 LLM / 工具失败后的降级）。
func (w *Workshop) FillMissing(ctx context.Context) {
	d := w.draft
	d.Fallback = true
	d.Via = ViaFallback
	_ = w.persist()

	if !d.has(StageGenes) {
		_ = w.RollGenes(ctx, nil)
	}
	if !d.has(StageTemperament) {
		label, blurb := fallbackTemperament(d.Traits)
		_ = w.SetTemperament(ctx, label, blurb)
	}
	if !d.has(StageAppearance) {
		_ = w.SetAppearance(ctx, fallbackAppearance(d.Species, d.Traits))
	}
	if !d.has(StageQuirks) {
		_ = w.SetQuirks(ctx, fallbackQuirks(d.Traits))
	}
	if !d.has(StageSoul) {
		_ = w.WriteSoul(ctx, fallbackSoul(d))
	}
	if !d.has(StageStats) {
		_ = w.SetBaseStats(ctx, nil)
	}
	if !d.has(StageIdentity) {
		_ = w.WriteIdentity(ctx, d.ReqName, d.Master, fallbackFlavor(d.Species))
	}
}

func fallbackTemperament(traits map[string]float64) (label, blurb string) {
	play := traits["playfulness"]
	timid := traits["timidity"]
	soc := traits["sociability"]
	switch {
	case play > 0.7 && soc > 0.6:
		return "蹦跳开心果", "一刻都闲不住，见谁都想蹭一蹭"
	case timid > 0.65:
		return "小心翼翼", "外面的世界有点吵，角落里最安心"
	case soc < 0.35 && play > 0.5:
		return "独行玩家", "自己玩得很开心，被盯着看会不好意思"
	case traits["appetite"] > 0.7:
		return "干饭选手", "心里想的十件事情有九件和吃有关"
	default:
		return "慢热软糖", "熟悉了以后会黏上来，但需要一点时间"
	}
}

func fallbackAppearance(species string, traits map[string]float64) string {
	var coat string
	switch species {
	case "dog":
		coat = "毛发微卷，耳朵会跟着心情动"
	case "cat":
		coat = "短毛顺滑，瞳孔在光线里会变圆变细"
	default:
		coat = "体型圆润，看起来很好rua"
	}
	extra := "眼神清澈"
	if traits["timidity"] > 0.6 {
		extra = "走路喜欢贴墙根"
	} else if traits["playfulness"] > 0.7 {
		extra = "尾巴总是停不下来"
	}
	return fmt.Sprintf("一只%s，%s，%s。", species, coat, extra)
}

func fallbackQuirks(traits map[string]float64) []string {
	q := []string{"被摸头时会轻轻眯眼"}
	if traits["appetite"] > 0.55 {
		q = append(q, "听见食盆响会从很远跑过来")
	} else {
		q = append(q, "吃东西喜欢先闻很久")
	}
	if traits["timidity"] > 0.55 {
		q = append(q, "打雷时会躲到家具后面")
	} else {
		q = append(q, "喜欢占据沙发正中间")
	}
	if traits["sociability"] < 0.4 {
		q = append(q, "客人来了会装作在睡觉")
	}
	if len(q) > 5 {
		q = q[:5]
	}
	return q
}

func fallbackSoul(d *Draft) string {
	label := d.TemperamentLabel
	if label == "" {
		label = "小小的"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "我是一只%s的%s。", label, d.Species)
	fmt.Fprintf(&b, "我管我的主人叫「%s」。", orMaster(d.Master))
	b.WriteString("说话不多，但心里有数。开心的时候会靠近你，害怕的时候希望你在。")
	b.WriteString("请慢慢了解我——我的喜好和雷区，都会在相处里长出来。")
	return b.String()
}

func fallbackFlavor(species string) string {
	switch species {
	case "cat":
		return "一只毛色还没完全定型的小猫"
	case "dog":
		return "一只耳朵还不听话的小狗"
	default:
		return "一只圆滚滚的小可爱"
	}
}

func orMaster(m string) string {
	if m == "" {
		return "主人"
	}
	return m
}

// EnsureComplete 补齐缺失阶段并 finalize；用于脚本失败后的兜底。
func (w *Workshop) EnsureComplete(ctx context.Context) ToolResult {
	if w.draft.has(StageFinalized) {
		return okResult("already ready", map[string]any{"pet_id": w.draft.PetID})
	}
	w.FillMissing(ctx)
	res := w.FinalizeBirth(ctx)
	if !res.OK {
		w.midwife.emitFailed(ctx, w.draft.PetID, res.Error, true)
	}
	return res
}