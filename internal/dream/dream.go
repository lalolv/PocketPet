// Package dream 是梦境整理器：宠物入睡（pet.fell_asleep）后异步触发，
// 读取近期日记与长期记忆，经 Reflector（LLM 抽象）凝练 MEMORY.md、
// 小幅演化 SOUL.md（带护栏与历史归档）、沉淀反复经验为 Skill、生成梦境独白。
// LLM 不可用或调用失败时静默跳过，不影响睡眠与精力恢复。
package dream

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"pocketpet/internal/llm"
	"pocketpet/internal/pet"
	"pocketpet/internal/petfs"
	"pocketpet/internal/store"
)

// 整理参数与护栏常量。
const (
	journalDays        = 7               // 吸收的近期日记天数
	minEntriesForSkill = 3               // 顿悟门槛：日记条目数下限（少于则不允许产技能）
	maxTraitStep       = 0.1             // SOUL 特质权重单步变化上限（护栏）
	dreamNoteMaxRunes  = 200             // 睡醒便签中梦境摘要的字符上限
	organizeTimeout    = 2 * time.Minute // 单次整理的超时
)

// basicWakeNote 是入睡后立即写好的睡醒便签（无 LLM 时也能让宠物醒后有话说）。
const basicWakeNote = "你刚睡了一觉，感觉精力恢复了一些。"

// Reflector 是梦境整理的 LLM 抽象：一次调用返回全部整理产物。
// 生产实现为 LLMReflector（ADK）；测试注入 fake。
type Reflector interface {
	Reflect(ctx context.Context, req ReflectRequest) (ReflectResult, error)
}

// ReflectRequest 是一次整理所需的全部输入。
type ReflectRequest struct {
	Name, Species, Stage string
	Soul                 string             // SOUL.md 现状全文
	Traits               map[string]float64 // 当前特质权重
	Memory               string             // MEMORY.md 现状
	Journals             []string           // 最近 journalDays 天的日记全文
	JournalEntries       int                // 日记条目总数（"- " 行数，顿悟门槛判定用）
	ExistingSkills       []string           // 已学会的技能名（避免重复产出）
}

// SkillDraft 是一个待沉淀的技能（遵循 ADK SKILL.md 规范：name/description/正文指令）。
type SkillDraft struct {
	Name         string // kebab-case
	Description  string
	Instructions string
}

// ReflectResult 是一次整理的全部产物；零值字段表示"该项无变化"。
type ReflectResult struct {
	MemoryUpdate  string             // 完整的新版 MEMORY.md；空 = 不更新
	SoulNarrative string             // 新的 SOUL.md 正文；空 = 不变
	TraitDeltas   map[string]float64 // 特质权重调整量（会被 maxTraitStep 钳制）
	Skill         *SkillDraft        // nil = 本次不沉淀技能
	Dream         string             // 第一人称梦境独白；空 = 无
}

// Organizer 监听领域事件并驱动整理流程；实现 tick.EventSink。
type Organizer struct {
	fs  *petfs.FS
	st  *store.Store
	cfg llm.ProviderConfig

	// Emitter 输出整理产生的领域事件（落库 + SSE），通常接 tick.Engine.Emit；
	// 必须在启动前设置（main 里在 Engine 创建后接线）。
	Emitter func(ctx context.Context, evs ...pet.Event)
	// Reflector 为 nil 时按 LLM 配置（全局 + AGENT.md 覆盖）惰性构造 LLMReflector，
	// 配置缺失则静默跳过整理。测试注入 fake。
	Reflector Reflector
	// Resolver 是命名 provider 解析器（YAML 配置体系）；nil 时以 cfg 走单 provider 兼容路径。
	Resolver *llm.Resolver
	// Now 返回当前时间（日记命名、事件时间戳）；nil 时用 time.Now。测试注入假时钟。
	Now func() time.Time

	mu      sync.Mutex
	pending map[string]bool // 每只宠物的"整理中"标志，防并发触发
}

// NewOrganizer 创建整理器。
func NewOrganizer(fs *petfs.FS, st *store.Store, cfg llm.ProviderConfig) *Organizer {
	return &Organizer{fs: fs, st: st, cfg: cfg, pending: make(map[string]bool)}
}

// Publish 实现 tick.EventSink：宠物入睡时触发一次异步整理（同宠物去重）。
func (o *Organizer) Publish(e pet.Event) {
	if e.Type != pet.EventFellAsleep {
		return
	}
	o.mu.Lock()
	if o.pending[e.PetID] {
		o.mu.Unlock()
		return
	}
	o.pending[e.PetID] = true
	o.mu.Unlock()

	// 便签立即写好：即使整理失败或未配置 LLM，醒来后的第一次对话也能提及睡眠。
	if err := o.fs.WriteWakeNote(e.PetID, basicWakeNote+"\n"); err != nil {
		slog.Warn("dream: write wake note failed", "pet", e.PetID, "err", err)
	}

	go func() {
		defer func() {
			o.mu.Lock()
			delete(o.pending, e.PetID)
			o.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), organizeTimeout)
		defer cancel()
		if err := o.Organize(ctx, e.PetID); err != nil {
			slog.Warn("dream: organize failed", "pet", e.PetID, "err", err)
		}
	}()
}

// Organize 同步执行一次整理（测试可直接调用；生产由 Publish 异步触发）。
// LLM 失败只记日志并返回 nil（静默跳过）；返回的 error 是宠物缺失等硬错误。
func (o *Organizer) Organize(ctx context.Context, petID string) error {
	p, err := o.st.GetPet(ctx, petID)
	if err != nil {
		return err
	}
	if !p.Alive {
		return nil
	}

	ref := o.Reflector
	if ref == nil {
		cfg := o.resolveCfg(petID)
		if !cfg.Configured() {
			return nil // 未配置 LLM：静默跳过
		}
		ref = &LLMReflector{Cfg: cfg}
	}

	req := o.gather(petID, p)
	res, err := ref.Reflect(ctx, req)
	if err != nil {
		slog.Warn("dream: reflect failed, skip", "pet", petID, "err", err)
		return nil
	}
	o.apply(ctx, p, req, res)
	return nil
}

// resolveCfg 解析该宠物的有效 LLM 配置：有 Resolver 走命名 provider 解析
// （名字优先、类型名回退），否则走单 provider 兼容路径（全局配置 ← AGENT.md 覆盖）。
func (o *Organizer) resolveCfg(petID string) llm.ProviderConfig {
	var spec petfs.AgentSpec
	if s, err := o.fs.AgentSpec(petID); err == nil {
		spec = s
	}
	if o.Resolver != nil {
		return o.Resolver.Resolve(spec.Provider, spec.Model)
	}
	cfg := o.cfg
	if spec.Provider != "" {
		cfg.Provider = llm.NormalizeProvider(spec.Provider)
	}
	if spec.Model != "" {
		cfg.Model = spec.Model
	}
	return cfg
}

// gather 收集整理输入；单个文件读取失败只跳过对应部分。
func (o *Organizer) gather(petID string, p *pet.Pet) ReflectRequest {
	req := ReflectRequest{Name: p.Name, Species: p.Species, Stage: string(p.Stage)}
	if doc, err := o.fs.ReadSoulDoc(petID); err == nil {
		req.Traits = doc.Traits
	}
	if s, err := o.fs.Read(petID, petfs.FileSOUL); err == nil {
		req.Soul = s
	}
	if m, err := o.fs.Read(petID, petfs.FileMemory); err == nil {
		req.Memory = m
	}
	names, _ := o.fs.ListJournals(petID)
	if len(names) > journalDays {
		names = names[len(names)-journalDays:]
	}
	for _, n := range names {
		c, err := o.fs.ReadJournal(petID, n)
		if err != nil {
			continue
		}
		req.Journals = append(req.Journals, c)
		req.JournalEntries += strings.Count(c, "\n- ")
	}
	req.ExistingSkills, _ = o.fs.ListSkills(petID)
	return req
}

// apply 落地整理产物：凝练记忆 → 演化 SOUL → 沉淀技能 → 做梦，最后推送事件。
// 每个文件写入各自持锁、单次写完，对话侧不会读到写了一半的文件。
func (o *Organizer) apply(ctx context.Context, p *pet.Pet, req ReflectRequest, res ReflectResult) {
	now := time.Now()
	if o.Now != nil {
		now = o.Now()
	}
	var evs []pet.Event
	note := basicWakeNote

	// 1. 凝练：LLM 给出完整新版 MEMORY.md 才替换。
	if mu := strings.TrimSpace(res.MemoryUpdate); mu != "" && mu != strings.TrimSpace(req.Memory) {
		if err := o.fs.Write(p.ID, petfs.FileMemory, strings.TrimRight(res.MemoryUpdate, "\n")+"\n"); err != nil {
			slog.Warn("dream: write memory failed", "pet", p.ID, "err", err)
		} else {
			note += "\n你趁睡觉把最近的事整理进了长期记忆。"
		}
	}

	// 2. 反思：SOUL 小幅演化（锁定时跳过）。
	if len(res.TraitDeltas) > 0 || strings.TrimSpace(res.SoulNarrative) != "" {
		switch doc, err := o.fs.ReadSoulDoc(p.ID); {
		case err != nil:
			slog.Warn("dream: read soul failed", "pet", p.ID, "err", err)
		case doc.Locked:
			// 主人锁定了 SOUL：护栏，不做任何演化。
		default:
			if newDoc, changed := evolveSoul(doc, res); changed {
				if err := o.fs.WriteSoulWithHistory(p.ID, petfs.RenderSoul(newDoc), now); err != nil {
					slog.Warn("dream: write soul failed", "pet", p.ID, "err", err)
				} else {
					evs = append(evs, pet.Event{PetID: p.ID, Type: pet.EventSoulChanged,
						Message: p.Name + " 的性格发生了一些变化", CreatedAt: now})
				}
			}
		}
	}

	// 3. 顿悟：日记条目数达到门槛且 LLM 产出了技能草稿才沉淀。
	if res.Skill != nil && req.JournalEntries >= minEntriesForSkill {
		draft := *res.Skill
		draft.Name = sanitizeSkillName(draft.Name)
		err := o.fs.WriteSkill(p.ID, draft.Name, renderSkillMD(draft))
		switch {
		case err == nil:
			evs = append(evs, pet.Event{PetID: p.ID, Type: pet.EventSkillLearned,
				Message: p.Name + " 学会了新技能「" + draft.Name + "」", CreatedAt: now})
		case errors.Is(err, petfs.ErrExists), errors.Is(err, petfs.ErrInvalidName):
			// 已有同名技能或名字非法：静默跳过。
		default:
			slog.Warn("dream: write skill failed", "pet", p.ID, "err", err)
		}
	}

	// 4. 做梦：梦境独白进日记 + pet.dream 事件 + 写进睡醒便签。
	if d := strings.TrimSpace(res.Dream); d != "" {
		if err := o.fs.AppendJournal(p.ID, "做梦："+d, now); err != nil {
			slog.Warn("dream: append dream journal failed", "pet", p.ID, "err", err)
		}
		evs = append(evs, pet.Event{PetID: p.ID, Type: pet.EventDream, Message: d, CreatedAt: now})
		note += "\n你做了一个梦：" + truncateRunes(d, dreamNoteMaxRunes)
	}

	// 5. 更新睡醒便签（覆盖入睡时写的基础版）并推送事件。
	if err := o.fs.WriteWakeNote(p.ID, note+"\n"); err != nil {
		slog.Warn("dream: update wake note failed", "pet", p.ID, "err", err)
	}
	if o.Emitter != nil && len(evs) > 0 {
		o.Emitter(ctx, evs...)
	}
}

// evolveSoul 计算护栏约束下的新版 SOUL：特质只调已有键、单步钳制 ±maxTraitStep、
// 结果钳制 [0,1]；正文有变化才替换。changed 表示是否有实际变化。
func evolveSoul(doc petfs.SoulDoc, res ReflectResult) (petfs.SoulDoc, bool) {
	out := doc
	changed := false

	if len(res.TraitDeltas) > 0 && len(doc.Traits) > 0 {
		traits := make(map[string]float64, len(doc.Traits))
		for k, v := range doc.Traits {
			traits[k] = v
		}
		for k, delta := range res.TraitDeltas {
			old, ok := doc.Traits[k]
			if !ok {
				continue // 忽略 LLM 编造的新特质
			}
			delta = max(-maxTraitStep, min(maxTraitStep, delta))
			nv := old + delta
			nv = min(1, max(0, nv))
			if nv != old {
				traits[k] = nv
				changed = true
			}
		}
		out.Traits = traits
	}

	if n := strings.TrimSpace(res.SoulNarrative); n != "" && n != strings.TrimSpace(doc.Body) {
		out.Body = n + "\n"
		changed = true
	}
	return out, changed
}

// sanitizeSkillName 把 LLM 给的名字规整为 kebab-case（小写、空白转连字符、
// 去掉非法字符）；规整后仍不合法的交给 WriteSkill 拒绝。
func sanitizeSkillName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, " ", "-")
	var b strings.Builder
	prevDash := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == '-' && !prevDash:
			b.WriteRune(r)
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// renderSkillMD 生成符合 ADK SKILL.md 规范的文件（frontmatter: name/description + 正文指令）。
// metadata.origin: learned 标记它是梦境沉淀产出（区别于手工放入的技能）。
func renderSkillMD(d SkillDraft) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + d.Name + "\n")
	b.WriteString("description: " + strings.ReplaceAll(strings.TrimSpace(d.Description), "\n", " ") + "\n")
	b.WriteString("metadata:\n  origin: learned\n")
	b.WriteString("---\n")
	body := strings.TrimSpace(d.Instructions)
	if body == "" {
		body = d.Description
	}
	b.WriteString(body + "\n")
	return b.String()
}

// truncateRunes 按字符数截断，避免按字节截断 UTF-8。
func truncateRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "……"
}
