package metaagent

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	mrand "math/rand/v2"

	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
)

// NewSeed 生成 16 字节 hex 种子。
func NewSeed() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("metaagent: generate seed: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func rngFromSeed(seed string) *mrand.Rand {
	sum := sha256.Sum256([]byte(seed))
	u1 := binary.LittleEndian.Uint64(sum[0:8])
	u2 := binary.LittleEndian.Uint64(sum[8:16])
	return mrand.New(mrand.NewPCG(u1, u2))
}

// SampleTraits 从种子采样特质权重（高方差 + playfulness/timidity 软反相关）。
func SampleTraits(seed string) map[string]float64 {
	r := rngFromSeed(seed + ":traits")
	out := make(map[string]float64, len(TraitKeys))
	for _, k := range TraitKeys {
		out[k] = round2(sampleUnit(r))
	}
	// 软反相关：高活泼略压胆怯，仍可被噪声打破。
	if out["playfulness"] > 0.6 && out["timidity"] > 0.55 && r.Float64() < 0.7 {
		out["timidity"] = round2(clamp01(out["timidity"] - 0.25))
	}
	if out["timidity"] > 0.7 && out["sociability"] > 0.6 && r.Float64() < 0.7 {
		out["sociability"] = round2(clamp01(out["sociability"] - 0.2))
	}
	return out
}

func sampleUnit(r *mrand.Rand) float64 {
	// 两均匀平均 → 偏中间；15% 概率全幅极端。
	if r.Float64() < 0.15 {
		return r.Float64()
	}
	return (r.Float64() + r.Float64()) / 2
}

// SampleStats 按特质在安全带内采样初始属性（Health 固定 100，EXP=0）。
func SampleStats(seed string, traits map[string]float64) pet.Stats {
	r := rngFromSeed(seed + ":stats")
	appetite := traits["appetite"]
	play := traits["playfulness"]
	soc := traits["sociability"]

	hunger := 70 - (appetite-0.5)*30 + (r.Float64()-0.5)*8
	happy := 75 + (play+soc-1)*12 + (r.Float64()-0.5)*8
	clean := 70 + (r.Float64()-0.5)*20
	energy := 88 + play*10 + (r.Float64()-0.5)*6

	return pet.Stats{
		Hunger: clampBand(hunger, 50, 90),
		Happy:  clampBand(happy, 55, 95),
		Clean:  clampBand(clean, 50, 95),
		Energy: clampBand(energy, 70, 100),
		Health: 100,
		EXP:    0,
	}
}

func clampBand(v, lo, hi float64) float64 {
	return math.Round(math.Min(hi, math.Max(lo, v)))
}

// ClampStatsInput 把 LLM/调用方提交的数值钳入安全带；Health 强制 100。
func ClampStatsInput(in pet.Stats) pet.Stats {
	return pet.Stats{
		Hunger: clampBand(in.Hunger, 50, 90),
		Happy:  clampBand(in.Happy, 55, 95),
		Clean:  clampBand(in.Clean, 50, 95),
		Energy: clampBand(in.Energy, 70, 100),
		Health: 100,
		EXP:    0,
	}
}

// ClampTraits 只保留已知键并钳制到 [0,1]。
func ClampTraits(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(TraitKeys))
	for _, k := range TraitKeys {
		if v, ok := in[k]; ok {
			out[k] = round2(clamp01(v))
		}
	}
	return out
}

// MergeTraits 以 base 为先验，允许 delta 内偏移；缺失键用 base。
func MergeTraits(base, proposed map[string]float64, maxDelta float64) map[string]float64 {
	out := make(map[string]float64, len(TraitKeys))
	for _, k := range TraitKeys {
		b := base[k]
		out[k] = round2(clamp01(b))
		if proposed == nil {
			continue
		}
		p, ok := proposed[k]
		if !ok {
			continue
		}
		p = clamp01(p)
		if p > b+maxDelta {
			p = b + maxDelta
		}
		if p < b-maxDelta {
			p = b - maxDelta
		}
		out[k] = round2(clamp01(p))
	}
	return out
}

// TraitsFromTemplate 从性格模板解析 traits；失败则均匀 0.5。
func TraitsFromTemplate(personality string) (map[string]float64, string, error) {
	per, err := petfs.ResolvePersonality(personality)
	if err != nil {
		return nil, "", err
	}
	doc := petfs.ParseSoul(per.Soul)
	if len(doc.Traits) == 0 {
		out := make(map[string]float64, len(TraitKeys))
		for _, k := range TraitKeys {
			out[k] = 0.5
		}
		return out, per.Label, nil
	}
	return ClampTraits(doc.Traits), per.Label, nil
}
