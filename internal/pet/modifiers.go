package pet

// Traits 是 SOUL.md frontmatter 的特质权重（0～1）。
// 0.5 为中性（不改变基础公式）；偏离中性时对 tick 衰减 / care 增益做确定性修饰。
// 见架构文档 §4.2 与 docs/04-MetaAgent宠物诞生设计.md G4。
type Traits struct {
	Playfulness float64 // 活泼：play 收益↑、玩耍耗能↑、清醒精力消耗略↑
	Timidity    float64 // 胆怯：心情更易掉
	Appetite    float64 // 食欲：饿得更快、喂食收益↑
	Sociability float64 // 社交：心情衰减↓（高社交更耐寂寞）
}

// NeutralTraits 返回中性特质（倍率全为 1）。
func NeutralTraits() Traits {
	return Traits{
		Playfulness: 0.5,
		Timidity:    0.5,
		Appetite:    0.5,
		Sociability: 0.5,
	}
}

// TraitsFromMap 从 SOUL traits 映射构造；缺键用 0.5，值钳制到 [0,1]。
func TraitsFromMap(m map[string]float64) Traits {
	t := NeutralTraits()
	if m == nil {
		return t
	}
	if v, ok := m["playfulness"]; ok {
		t.Playfulness = clamp01(v)
	}
	if v, ok := m["timidity"]; ok {
		t.Timidity = clamp01(v)
	}
	if v, ok := m["appetite"]; ok {
		t.Appetite = clamp01(v)
	}
	if v, ok := m["sociability"]; ok {
		t.Sociability = clamp01(v)
	}
	return t
}

// Clamped 返回钳制后的副本。
func (t Traits) Clamped() Traits {
	return Traits{
		Playfulness: clamp01(t.Playfulness),
		Timidity:    clamp01(t.Timidity),
		Appetite:    clamp01(t.Appetite),
		Sociability: clamp01(t.Sociability),
	}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// mult 将特质映射为倍率：trait=0.5 → 1.0；0 → 1-amp；1 → 1+amp。
func mult(trait, amp float64) float64 {
	return 1 + (trait-0.5)*2*amp
}

// 修饰幅度（相对中性的最大偏移）。刻意温和，保证可感知但不破坏平衡。
const (
	ampAppetiteDecay   = 0.25 // 饥饿衰减 ±25%
	ampAppetiteFeed    = 0.20 // 喂食收益 ±20%
	ampSociabilityMood = 0.20 // 心情衰减（由 1-sociability 驱动）±20%
	ampTimidityMood    = 0.10 // 胆怯额外加速心情衰减 ±10%
	ampPlayHappy       = 0.25 // 玩耍开心收益 ±25%
	ampPlayEnergy      = 0.15 // 玩耍耗能 ±15%
	ampPlayfulDrain    = 0.10 // 清醒精力消耗 ±10%
)

// decayRates 是一次 Tick 实际使用的每小时速率（已含睡眠衰减因子）。
type decayRates struct {
	hunger, happy, clean float64
	energyAwake          float64
	energySleep          float64
}

func ratesFor(traits Traits, sleeping bool) decayRates {
	traits = traits.Clamped()
	decay := 1.0
	if sleeping {
		decay = sleepDecayFactor
	}
	happyMult := mult(1-traits.Sociability, ampSociabilityMood) * mult(traits.Timidity, ampTimidityMood)
	return decayRates{
		hunger:      hungerDecayPerHour * mult(traits.Appetite, ampAppetiteDecay) * decay,
		happy:       happyDecayPerHour * happyMult * decay,
		clean:       cleanDecayPerHour * decay,
		energyAwake: energyAwakeDrainPerHour * mult(traits.Playfulness, ampPlayfulDrain),
		energySleep: energySleepGainPerHour,
	}
}
