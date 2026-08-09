package dream

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"pocketpet/internal/llm"
	"pocketpet/internal/pet"
	"pocketpet/internal/petfs"
	"pocketpet/internal/store"
	"pocketpet/internal/tick"
)

var t0 = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

// testEnv 是一套无 LLM 的整理器测试环境。
type testEnv struct {
	st     *store.Store
	fs     *petfs.FS
	engine *tick.Engine
	org    *Organizer
	clock  *pet.FakeClock

	mu     sync.Mutex
	events []pet.Event
}

func setup(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clock := pet.NewFakeClock(t0)
	fs := petfs.New(t.TempDir())
	engine := tick.NewEngine(st, nil, time.Minute, 24*time.Hour, clock)
	env := &testEnv{st: st, fs: fs, engine: engine, clock: clock}
	org := NewOrganizer(fs, st, llm.ProviderConfig{})
	org.Now = clock.Now
	org.Emitter = func(_ context.Context, evs ...pet.Event) {
		env.mu.Lock()
		env.events = append(env.events, evs...)
		env.mu.Unlock()
	}
	env.org = org
	return env
}

func (env *testEnv) newPet(t *testing.T, personality string) *pet.Pet {
	t.Helper()
	p, err := env.engine.CreatePet(context.Background(), "雪球", "cat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.fs.CreatePet(p.ID, petfs.Identity{
		Name: p.Name, Species: p.Species, Personality: personality,
		Stage: string(p.Stage), BornAt: p.BornAt,
	}); err != nil {
		t.Fatal(err)
	}
	return p
}

// seedJournals 写 n 条当天日记。
func (env *testEnv) seedJournals(t *testing.T, id string, facts ...string) {
	t.Helper()
	for _, f := range facts {
		if err := env.fs.AppendJournal(id, f, t0); err != nil {
			t.Fatal(err)
		}
	}
}

func (env *testEnv) eventTypes() []string {
	env.mu.Lock()
	defer env.mu.Unlock()
	var out []string
	for _, e := range env.events {
		out = append(out, e.Type)
	}
	return out
}

// fakeReflector 是测试用的 Reflector。
type fakeReflector struct {
	res ReflectResult
	err error

	mu      sync.Mutex
	calls   int
	lastReq ReflectRequest

	started chan struct{} // 非 nil 时 Reflect 进入后通知并阻塞，直到 release 关闭
	release chan struct{}
}

func (f *fakeReflector) Reflect(_ context.Context, req ReflectRequest) (ReflectResult, error) {
	f.mu.Lock()
	f.calls++
	f.lastReq = req
	f.mu.Unlock()
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
		<-f.release
	}
	return f.res, f.err
}

func (f *fakeReflector) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fullResult 是一份"全产物"的整理结果。
func fullResult() ReflectResult {
	return ReflectResult{
		MemoryUpdate:  "# 长期记忆\n\n- 主人叫阿洛，常熬夜。\n",
		SoulNarrative: "我嘴硬心软。最近越来越信任主人了。",
		TraitDeltas:   map[string]float64{"playfulness": 0.5, "appetite": -0.05, "evil": 0.9},
		Skill:         &SkillDraft{Name: "goodnight-ritual", Description: "晚安仪式", Instructions: "每晚和主人说晚安。"},
		Dream:         "梦见小鱼干堆积成山，主人在山顶朝我招手。",
	}
}

// TestOrganizeFullFlow 验证一次完整整理：记忆凝练、SOUL 演化（含历史与钳制）、
// 技能沉淀、梦境（日记 + 事件 + 睡醒便签）。
func TestOrganizeFullFlow(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "tsundere")
	env.seedJournals(t, p.ID, "主人叫阿洛", "主人又熬夜了", "主人晚上跟我说晚安", "主人给我小鱼干")

	fake := &fakeReflector{res: fullResult()}
	env.org.Reflector = fake

	if err := env.org.Organize(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}

	// 记忆凝练
	mem, err := env.fs.Read(p.ID, petfs.FileMemory)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mem, "主人叫阿洛") {
		t.Fatalf("MEMORY.md not updated:\n%s", mem)
	}

	// SOUL 演化：正文替换；playfulness 0.6 + 0.5 → 钳制单步 +0.1 → 0.7；
	// appetite 0.7 - 0.05 → 0.65；编造的 evil 特质被忽略。
	doc, err := env.fs.ReadSoulDoc(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Traits["playfulness"] != 0.7 {
		t.Fatalf("playfulness = %v, want 0.7 (clamped step)", doc.Traits["playfulness"])
	}
	if doc.Traits["appetite"] != 0.65 {
		t.Fatalf("appetite = %v, want 0.65", doc.Traits["appetite"])
	}
	if _, ok := doc.Traits["evil"]; ok {
		t.Fatalf("invented trait should be ignored: %v", doc.Traits)
	}
	if !strings.Contains(doc.Body, "越来越信任主人") {
		t.Fatalf("soul body not updated:\n%s", doc.Body)
	}
	if doc.Template != "tsundere" || doc.Label != "傲娇" {
		t.Fatalf("template/label lost: %+v", doc)
	}

	// 历史归档 = 旧版 SOUL
	hist, err := env.fs.SoulHistory(p.ID)
	if err != nil || len(hist) != 1 {
		t.Fatalf("history = %v, %v", hist, err)
	}

	// 技能沉淀
	skills, err := env.fs.ListSkills(p.ID)
	if err != nil || len(skills) != 1 || skills[0] != "goodnight-ritual" {
		t.Fatalf("skills = %v, %v", skills, err)
	}

	// 梦境进日记
	journals, _ := env.fs.ListJournals(p.ID)
	if len(journals) != 1 {
		t.Fatalf("journals = %v", journals)
	}
	journal, _ := env.fs.ReadJournal(p.ID, journals[0])
	if !strings.Contains(journal, "做梦：梦见小鱼干堆积成山") {
		t.Fatalf("dream not in journal:\n%s", journal)
	}

	// 睡醒便签含梦境与整理记录
	note, err := env.fs.TakeWakeNote(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "你做了一个梦：梦见小鱼干") || !strings.Contains(note, "整理进了长期记忆") {
		t.Fatalf("wake note:\n%s", note)
	}

	// 事件：soul_changed + skill_learned + dream
	types := env.eventTypes()
	want := []string{pet.EventSoulChanged, pet.EventSkillLearned, pet.EventDream}
	if len(types) != len(want) {
		t.Fatalf("events = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("events = %v, want %v", types, want)
		}
	}
}

// TestOrganizeSoulLocked 验证护栏：SOUL 被锁定时跳过演化，其余产物照常。
func TestOrganizeSoulLocked(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "tsundere")
	env.seedJournals(t, p.ID, "a", "b", "c")
	if err := env.fs.SetSoulLocked(p.ID, true); err != nil {
		t.Fatal(err)
	}
	before, _ := env.fs.Read(p.ID, petfs.FileSOUL)

	env.org.Reflector = &fakeReflector{res: fullResult()}
	if err := env.org.Organize(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}

	after, _ := env.fs.Read(p.ID, petfs.FileSOUL)
	if before != after {
		t.Fatal("locked SOUL should not change")
	}
	hist, _ := env.fs.SoulHistory(p.ID)
	if len(hist) != 0 {
		t.Fatalf("locked SOUL should have no history: %v", hist)
	}
	// 其余产物不受影响：记忆更新 + 梦境事件
	if mem, _ := env.fs.Read(p.ID, petfs.FileMemory); !strings.Contains(mem, "阿洛") {
		t.Fatal("memory should still update when soul locked")
	}
	for _, typ := range env.eventTypes() {
		if typ == pet.EventSoulChanged {
			t.Fatal("soul_changed should not fire when locked")
		}
	}
}

// TestOrganizeReflectError 验证降级：LLM 失败 → 静默跳过，睡眠数值不受影响。
func TestOrganizeReflectError(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "lively")
	env.seedJournals(t, p.ID, "a", "b", "c")

	env.org.Reflector = &fakeReflector{err: errors.New("boom")}
	if err := env.org.Organize(context.Background(), p.ID); err != nil {
		t.Fatalf("reflect error should be swallowed, got %v", err)
	}

	// 一切照旧
	mem, _ := env.fs.Read(p.ID, petfs.FileMemory)
	if !strings.Contains(mem, "暂时空空") {
		t.Fatalf("memory should be untouched:\n%s", mem)
	}
	if len(env.eventTypes()) != 0 {
		t.Fatalf("no events expected, got %v", env.eventTypes())
	}

	// 睡眠数值不受影响：Care(sleep) 后 4h 精力恢复 25/h。
	if _, err := env.engine.Care(context.Background(), p.ID, pet.ActionSleep); err != nil {
		t.Fatal(err)
	}
	sleeping, err := env.st.GetPet(context.Background(), p.ID) // 重取（Care 已改 Sleeping）
	if err != nil {
		t.Fatal(err)
	}
	sleeping.Stats.Energy = 20 // 直接压低精力再补算
	if err := env.st.SavePet(context.Background(), sleeping); err != nil {
		t.Fatal(err)
	}
	env.clock.Advance(4 * time.Hour)
	got, err := env.engine.Settle(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stats.Energy != 100 {
		t.Fatalf("sleep energy recovery broken: %v", got.Stats.Energy)
	}
}

// TestSkillGate 验证顿悟门槛与名字规整：条目 <3 不产技能；非法名字被规整。
func TestSkillGate(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "lively")

	// 只有 2 条日记：不够门槛
	env.seedJournals(t, p.ID, "主人说晚安", "主人又说晚安")
	res := fullResult()
	res.Skill = &SkillDraft{Name: "Goodnight Ritual", Description: "晚安仪式", Instructions: "说晚安。"}
	env.org.Reflector = &fakeReflector{res: res}
	if err := env.org.Organize(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if skills, _ := env.fs.ListSkills(p.ID); len(skills) != 0 {
		t.Fatalf("skill below gate should not precipitate: %v", skills)
	}

	// 补到 3 条：名字被规整为 kebab-case 后沉淀
	env.seedJournals(t, p.ID, "主人今天也说了晚安")
	env.org.Reflector = &fakeReflector{res: res}
	if err := env.org.Organize(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	skills, _ := env.fs.ListSkills(p.ID)
	if len(skills) != 1 || skills[0] != "goodnight-ritual" {
		t.Fatalf("skills = %v", skills)
	}
}

// TestSkillDuplicate 验证同名技能不重复沉淀（静默跳过）。
func TestSkillDuplicate(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "lively")
	env.seedJournals(t, p.ID, "a", "b", "c")
	if err := env.fs.WriteSkill(p.ID, "goodnight-ritual", "old"); err != nil {
		t.Fatal(err)
	}

	env.org.Reflector = &fakeReflector{res: fullResult()}
	if err := env.org.Organize(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	for _, typ := range env.eventTypes() {
		if typ == pet.EventSkillLearned {
			t.Fatal("duplicate skill should not emit skill_learned")
		}
	}
}

// TestPublishDeduplicates 验证同一宠物并发触发只整理一次，且便签同步写好。
func TestPublishDeduplicates(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "lively")
	env.seedJournals(t, p.ID, "a", "b", "c")

	fake := &fakeReflector{
		res:     ReflectResult{Dream: "梦见在云上打滚。"},
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	env.org.Reflector = fake

	env.org.Publish(pet.Event{PetID: p.ID, Type: pet.EventFellAsleep, CreatedAt: t0})
	// 便签同步可用
	if note, _ := env.fs.TakeWakeNote(p.ID); !strings.Contains(note, "睡了一觉") {
		t.Fatalf("wake note should be written synchronously, got %q", note)
	}
	// 等第一次整理进入 Reflect（阻塞中），再触发第二次
	<-fake.started
	env.org.Publish(pet.Event{PetID: p.ID, Type: pet.EventFellAsleep, CreatedAt: t0})
	close(fake.release)

	// 等 pending 释放
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		env.org.mu.Lock()
		pending := env.org.pending[p.ID]
		env.org.mu.Unlock()
		if !pending {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if fake.callCount() != 1 {
		t.Fatalf("reflect calls = %d, want 1 (dedup)", fake.callCount())
	}

	// 便签被整理结果覆盖：含梦境
	note, _ := env.fs.TakeWakeNote(p.ID)
	if !strings.Contains(note, "云上打滚") {
		t.Fatalf("wake note after organize:\n%s", note)
	}
	// 非入睡事件不触发
	env.org.Publish(pet.Event{PetID: p.ID, Type: pet.EventHungry, CreatedAt: t0})
	if fake.callCount() != 1 {
		t.Fatal("non-sleep event should not trigger organize")
	}
}

// TestParseReflectResult 验证约定 JSON 的容错解析。
func TestParseReflectResult(t *testing.T) {
	// 带思考前缀 + markdown 代码块包裹
	text := "让我想想……\n```json\n{\"memory_update\": \"# 长期记忆\\n\\n- x\", \"trait_deltas\": {\"playfulness\": 0.05}, \"skill\": null, \"dream\": \"梦见小鱼干\"}\n```"
	res, err := parseReflectResult(text)
	if err != nil {
		t.Fatal(err)
	}
	if res.MemoryUpdate == "" || res.TraitDeltas["playfulness"] != 0.05 || res.Dream == "" {
		t.Fatalf("parsed = %+v", res)
	}
	if res.Skill != nil {
		t.Fatal("skill should be nil")
	}

	if _, err := parseReflectResult("没有 JSON"); !errors.Is(err, ErrBadReflectOutput) {
		t.Fatalf("garbage = %v", err)
	}
	if _, err := parseReflectResult("{\"memory_update\": 123}"); !errors.Is(err, ErrBadReflectOutput) {
		t.Fatalf("bad json = %v", err)
	}
}

// TestEvolveSoulGuardrails 直接验证护栏数值行为。
func TestEvolveSoulGuardrails(t *testing.T) {
	doc := petfs.SoulDoc{
		Template: "lively", Label: "活泼",
		Traits: map[string]float64{"playfulness": 0.95, "timidity": 0.05},
		Body:   "旧正文\n",
	}
	// 单步钳制：+0.5 → +0.1，但 0.95+0.1 → 钳到 1.0；-0.5 → -0.1，0.05-0.1 → 钳到 0
	out, changed := evolveSoul(doc, ReflectResult{
		TraitDeltas: map[string]float64{"playfulness": 0.5, "timidity": -0.5},
	})
	if !changed {
		t.Fatal("should change")
	}
	if out.Traits["playfulness"] != 1.0 || out.Traits["timidity"] != 0.0 {
		t.Fatalf("clamped traits = %v", out.Traits)
	}
	// 无变化 → changed=false（相同正文 + 零 delta）
	_, changed = evolveSoul(doc, ReflectResult{
		TraitDeltas:   map[string]float64{"playfulness": 0},
		SoulNarrative: "旧正文",
	})
	if changed {
		t.Fatal("no-op should report changed=false")
	}
}

// TestOrganizeUnconfiguredLLM 无 LLM 配置且未注入 Reflector：静默跳过。
func TestOrganizeUnconfiguredLLM(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "lively")
	if err := env.org.Organize(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if len(env.eventTypes()) != 0 {
		t.Fatalf("events = %v", env.eventTypes())
	}
}

// TestResolveCfgWithResolver 验证梦境整理的命名 provider 解析（与 chat 同一规则）。
func TestResolveCfgWithResolver(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "lively")

	resolver, err := llm.NewResolver(map[string]llm.ProviderConfig{
		"deepseek": {Provider: llm.ProviderOpenAIChat, Model: "deepseek-chat", APIKey: "ds-key"},
	}, "deepseek", llm.ProviderConfig{Provider: llm.ProviderGemini, APIKey: "env-key"})
	if err != nil {
		t.Fatal(err)
	}
	env.org.Resolver = resolver

	// 无 AGENT.md 覆盖 → 命名默认
	cfg := env.org.resolveCfg(p.ID)
	if cfg.Provider != llm.ProviderOpenAIChat || cfg.APIKey != "ds-key" {
		t.Fatalf("named default = %+v", cfg)
	}
	// AGENT.md 写类型名 → 回退默认连接参数
	if err := env.fs.Write(p.ID, petfs.FileAgent, "---\nprovider: gemini\nmodel: \"\"\nmcp: \"\"\n---\n"); err != nil {
		t.Fatal(err)
	}
	cfg = env.org.resolveCfg(p.ID)
	if cfg.Provider != llm.ProviderGemini || cfg.APIKey != "ds-key" {
		t.Fatalf("type fallback = %+v", cfg)
	}
}
