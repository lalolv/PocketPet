package metaagent

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
)

// ToolResult 是工具统一返回（给 LLM 或脚本）。
type ToolResult struct {
	OK      bool           `json:"ok"`
	Error   string         `json:"error,omitempty"`
	Summary string         `json:"summary,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

func fail(msg string) ToolResult { return ToolResult{OK: false, Error: msg} }

func okResult(summary string, data map[string]any) ToolResult {
	return ToolResult{OK: true, Summary: summary, Data: data}
}

// Workshop 持有一只宠物的孵化草稿，暴露可被 MetaAgent 调用的工具方法。
type Workshop struct {
	midwife *Midwife
	draft   *Draft
}

func (m *Midwife) loadWorkshop(petID string) (*Workshop, error) {
	raw, err := m.FS.LoadGenesisDraft(petID)
	if err != nil {
		return nil, err
	}
	d, err := decodeDraft(raw)
	if err != nil {
		return nil, err
	}
	return &Workshop{midwife: m, draft: d}, nil
}

func (w *Workshop) persist() error {
	raw, err := encodeDraft(w.draft)
	if err != nil {
		return err
	}
	return w.midwife.FS.SaveGenesisDraft(w.draft.PetID, raw)
}

// Narrate 发送旁白（不推进阶段）。
func (w *Workshop) Narrate(ctx context.Context, stage, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	w.midwife.emitJSON(ctx, w.draft.PetID, pet.EventGenesisNarration, map[string]any{
		"stage": stage,
		"text":  text,
	})
}

// RollGenes 采样或合并特质权重。
// proposed 为 nil 时纯种子采样；非 nil 时相对先验做有限偏移。
func (w *Workshop) RollGenes(ctx context.Context, proposed map[string]float64) ToolResult {
	if w.draft.has(StageFinalized) {
		return fail("already finalized")
	}
	base := SampleTraits(w.draft.Seed)
	if w.draft.Mode == ModeTemplate && len(w.draft.Traits) > 0 {
		base = w.draft.Traits
	}
	var traits map[string]float64
	switch {
	case proposed == nil || len(proposed) == 0:
		traits = base
	case w.draft.Mode == ModeDescribe:
		traits = MergeTraits(base, proposed, 0.25)
	default:
		traits = MergeTraits(base, proposed, 0.15)
	}
	w.draft.Traits = traits
	w.draft.mark(StageGenes)
	if err := w.persist(); err != nil {
		return fail(err.Error())
	}
	w.midwife.emitJSON(ctx, w.draft.PetID, pet.EventGenesisGenes, map[string]any{"traits": traits})
	return okResult("genes rolled", map[string]any{"traits": traits})
}

// SetTemperament 写入气质标签。
func (w *Workshop) SetTemperament(ctx context.Context, label, blurb string) ToolResult {
	if !w.draft.has(StageGenes) {
		return fail("roll_genes first")
	}
	label = strings.TrimSpace(label)
	blurb = strings.TrimSpace(blurb)
	if label == "" {
		return fail("label is required")
	}
	if utf8.RuneCountInString(label) > 8 {
		label = string([]rune(label)[:8])
	}
	if blurb == "" {
		blurb = label
	}
	w.draft.TemperamentLabel = label
	w.draft.TemperamentBlurb = blurb
	w.draft.mark(StageTemperament)
	if err := w.persist(); err != nil {
		return fail(err.Error())
	}
	payload := map[string]any{"label": label, "blurb": blurb}
	w.midwife.emitJSON(ctx, w.draft.PetID, pet.EventGenesisTemperament, payload)
	return okResult("temperament set", payload)
}

// SetAppearance 写入外貌。
func (w *Workshop) SetAppearance(ctx context.Context, appearance string) ToolResult {
	if !w.draft.has(StageTemperament) {
		return fail("set_temperament first")
	}
	appearance = strings.TrimSpace(appearance)
	if appearance == "" {
		return fail("appearance is required")
	}
	if utf8.RuneCountInString(appearance) > 500 {
		appearance = string([]rune(appearance)[:500])
	}
	w.draft.Appearance = appearance
	w.draft.mark(StageAppearance)
	if err := w.persist(); err != nil {
		return fail(err.Error())
	}
	payload := map[string]any{"appearance": appearance}
	w.midwife.emitJSON(ctx, w.draft.PetID, pet.EventGenesisAppearance, payload)
	return okResult("appearance set", payload)
}

// SetQuirks 写入癖好列表。
func (w *Workshop) SetQuirks(ctx context.Context, quirks []string) ToolResult {
	if !w.draft.has(StageAppearance) {
		return fail("set_appearance first")
	}
	cleaned := make([]string, 0, len(quirks))
	for _, q := range quirks {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		if utf8.RuneCountInString(q) > 30 {
			q = string([]rune(q)[:30])
		}
		cleaned = append(cleaned, q)
	}
	if len(cleaned) < 2 {
		return fail("need 2–5 quirks")
	}
	if len(cleaned) > 5 {
		cleaned = cleaned[:5]
	}
	w.draft.Quirks = cleaned
	w.draft.mark(StageQuirks)
	if err := w.persist(); err != nil {
		return fail(err.Error())
	}
	payload := map[string]any{"quirks": cleaned}
	w.midwife.emitJSON(ctx, w.draft.PetID, pet.EventGenesisQuirks, payload)
	return okResult("quirks set", payload)
}

// WriteSoul 写入 SOUL 正文。
func (w *Workshop) WriteSoul(ctx context.Context, narrative string) ToolResult {
	if !w.draft.has(StageQuirks) {
		return fail("set_quirks first")
	}
	narrative = strings.TrimSpace(narrative)
	if narrative == "" {
		return fail("narrative is required")
	}
	n := utf8.RuneCountInString(narrative)
	if n < 40 {
		return fail("narrative too short")
	}
	if n > 400 {
		narrative = string([]rune(narrative)[:400])
	}
	w.draft.SoulNarrative = narrative
	w.draft.mark(StageSoul)
	if err := w.persist(); err != nil {
		return fail(err.Error())
	}
	preview := narrative
	if utf8.RuneCountInString(preview) > 80 {
		preview = string([]rune(preview)[:80]) + "…"
	}
	payload := map[string]any{"preview": preview}
	w.midwife.emitJSON(ctx, w.draft.PetID, pet.EventGenesisSoul, payload)
	return okResult("soul written", payload)
}

// SetBaseStats 写入初始数值（in 为 nil 时用种子建议值）。
func (w *Workshop) SetBaseStats(ctx context.Context, in *pet.Stats) ToolResult {
	if !w.draft.has(StageSoul) {
		return fail("write_soul first")
	}
	var stats pet.Stats
	if in == nil {
		stats = SampleStats(w.draft.Seed, w.draft.Traits)
	} else {
		stats = ClampStatsInput(*in)
	}
	w.draft.Stats = &stats
	w.draft.mark(StageStats)
	if err := w.persist(); err != nil {
		return fail(err.Error())
	}
	payload := map[string]any{
		"hunger": stats.Hunger, "happy": stats.Happy,
		"clean": stats.Clean, "energy": stats.Energy, "health": stats.Health,
	}
	w.midwife.emitJSON(ctx, w.draft.PetID, pet.EventGenesisStats, payload)
	return okResult("stats set", payload)
}

// WriteIdentity 写入名字与称呼。
func (w *Workshop) WriteIdentity(ctx context.Context, name, master, flavor string) ToolResult {
	if !w.draft.has(StageStats) {
		return fail("set_base_stats first")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimSpace(w.draft.ReqName)
	}
	if name == "" || name == "（破壳中）" {
		name = defaultName(w.draft.Species)
	}
	master = strings.TrimSpace(master)
	if master == "" {
		master = w.draft.Master
	}
	if master == "" {
		master = "主人"
	}
	flavor = strings.TrimSpace(flavor)
	w.draft.Name = name
	w.draft.Master = master
	w.draft.SpeciesFlavor = flavor
	w.draft.mark(StageIdentity)
	if err := w.persist(); err != nil {
		return fail(err.Error())
	}
	payload := map[string]any{"name": name, "master": master, "species_flavor": flavor}
	w.midwife.emitJSON(ctx, w.draft.PetID, pet.EventGenesisIdentity, payload)
	return okResult("identity set", payload)
}

// FinalizeBirth 校验并提交正式宠物文件与数值状态。
func (w *Workshop) FinalizeBirth(ctx context.Context) ToolResult {
	if w.draft.has(StageFinalized) {
		return fail("already finalized")
	}
	if miss := w.draft.missing(); len(miss) > 0 {
		return fail("missing stages: " + strings.Join(miss, ", "))
	}
	if w.draft.Stats == nil {
		return fail("stats missing")
	}

	_, err := w.midwife.FS.CreatePet(w.draft.PetID, petfs.Identity{
		Name:       w.draft.Name,
		Species:    w.draft.Species,
		Master:     w.draft.Master,
		BornAt:     w.draft.BornAt,
		Stage:      string(pet.StageEgg),
		Seed:       w.draft.Seed,
		Appearance: composeAppearance(w.draft),
		CustomSoul: w.draft.soulContent(),
		SoulLabel:  w.draft.TemperamentLabel,
		SoulKey:    "genesis",
	})
	if err != nil {
		return fail("create pet files: " + err.Error())
	}
	p, err := w.midwife.Engine.FinalizeBirth(ctx, w.draft.PetID, w.draft.Name, *w.draft.Stats)
	if err != nil {
		return fail(err.Error())
	}
	w.draft.mark(StageFinalized)
	_ = w.midwife.FS.RemoveGenesisDraft(w.draft.PetID)

	ready := map[string]any{
		"pet_id":   p.ID,
		"name":     p.Name,
		"fallback": w.draft.Fallback,
	}
	w.midwife.emitJSON(ctx, p.ID, pet.EventGenesisReady, ready)
	return okResult("birth complete", ready)
}

func defaultName(species string) string {
	switch species {
	case "cat":
		return "小咪"
	case "dog":
		return "旺财"
	default:
		return "小团子"
	}
}

func composeAppearance(d *Draft) string {
	var b strings.Builder
	if d.SpeciesFlavor != "" {
		fmt.Fprintf(&b, "%s。\n", d.SpeciesFlavor)
	}
	b.WriteString(d.Appearance)
	if d.Appearance != "" && !strings.HasSuffix(d.Appearance, "\n") {
		b.WriteString("\n")
	}
	if d.TemperamentBlurb != "" {
		fmt.Fprintf(&b, "\n气质：%s——%s\n", d.TemperamentLabel, d.TemperamentBlurb)
	}
	return b.String()
}
