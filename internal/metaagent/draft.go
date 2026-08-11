package metaagent

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
)

// Draft 是孵化草稿（持久化为 .genesis.json）。
type Draft struct {
	PetID    string `json:"pet_id"`
	Seed     string `json:"seed"`
	Species  string `json:"species"`
	Mode     string `json:"mode"`
	Prompt   string `json:"prompt,omitempty"`
	Master   string `json:"master"`
	ReqName  string `json:"req_name,omitempty"` // 请求里预填的名字
	Fallback bool   `json:"fallback,omitempty"`

	Done Stage `json:"done"`

	Traits           map[string]float64 `json:"traits,omitempty"`
	TemperamentLabel string             `json:"temperament_label,omitempty"`
	TemperamentBlurb string             `json:"temperament_blurb,omitempty"`
	Appearance       string             `json:"appearance,omitempty"`
	Quirks           []string           `json:"quirks,omitempty"`
	SoulNarrative    string             `json:"soul_narrative,omitempty"`
	Stats            *pet.Stats         `json:"stats,omitempty"`
	Name             string             `json:"name,omitempty"`
	SpeciesFlavor    string             `json:"species_flavor,omitempty"`

	BornAt time.Time `json:"born_at"`
}

func (d *Draft) has(s Stage) bool { return d.Done&s != 0 }

func (d *Draft) mark(s Stage) { d.Done |= s }

func (d *Draft) missing() []string {
	var out []string
	check := func(s Stage, name string) {
		if d.Done&s == 0 {
			out = append(out, name)
		}
	}
	check(StageGenes, "genes")
	check(StageTemperament, "temperament")
	check(StageAppearance, "appearance")
	check(StageQuirks, "quirks")
	check(StageSoul, "soul")
	check(StageStats, "stats")
	check(StageIdentity, "identity")
	return out
}

func (d *Draft) soulContent() string {
	doc := petfs.SoulDoc{
		Template: "genesis",
		Label:    d.TemperamentLabel,
		Locked:   false,
		Traits:   d.Traits,
		Body:     buildSoulBody(d.SoulNarrative, d.Quirks),
	}
	return petfs.RenderSoul(doc)
}

func buildSoulBody(narrative string, quirks []string) string {
	var b strings.Builder
	n := strings.TrimSpace(narrative)
	b.WriteString(n)
	if n != "" && !strings.HasSuffix(n, "\n") {
		b.WriteString("\n")
	}
	if len(quirks) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("## 癖好\n")
		for _, q := range quirks {
			fmt.Fprintf(&b, "- %s\n", q)
		}
	}
	return b.String()
}

func clamp01(v float64) float64 {
	return math.Min(1, math.Max(0, v))
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func encodeDraft(d *Draft) ([]byte, error) {
	return json.MarshalIndent(d, "", "  ")
}

func decodeDraft(raw []byte) (*Draft, error) {
	var d Draft
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return &d, nil
}
