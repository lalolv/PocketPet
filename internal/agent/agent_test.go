package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lalolv/PocketPet/internal/llm"
	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
	"github.com/lalolv/PocketPet/internal/store"
	"github.com/lalolv/PocketPet/internal/tick"
)

var t0 = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

// testEnv 是一套无 LLM 的测试环境（chat 走降级文案）。
type testEnv struct {
	engine *tick.Engine
	fs     *petfs.FS
	agent  *PetAgent
	clock  *pet.FakeClock
}

func setup(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clock := pet.NewFakeClock(t0)
	fs := petfs.New(filepath.Join(t.TempDir(), "data"))
	engine := tick.NewEngine(st, tick.MultiSink{NewStageSync(fs, st)}, time.Minute, 24*time.Hour, clock)
	// 未配置 provider：所有 chat 都必须走降级路径。
	ag := New(engine, fs, llm.Config{})
	return &testEnv{engine: engine, fs: fs, agent: ag, clock: clock}
}

func (env *testEnv) newPet(t *testing.T, name, personality string) *pet.Pet {
	t.Helper()
	p, err := env.engine.CreatePet(context.Background(), name, "cat")
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

// oneOf 报告 got 是否是 lines 中某条按 mood 填充后的结果。
func oneOf(got string, lines []string, mood string) bool {
	for _, l := range lines {
		if got == fmt.Sprintf(l, mood) {
			return true
		}
	}
	return false
}

func TestChatFallbackWithoutProvider(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "团团", "tsundere")

	reply, err := env.agent.Chat(context.Background(), p.ID, "你好呀")
	if err != nil {
		t.Fatal(err)
	}
	// 新生宠物状态健康 → 心情短语为默认值。
	if !oneOf(reply, fallbackLines["tsundere"], "精神好得很") {
		t.Fatalf("reply %q not a tsundere fallback line", reply)
	}
}

func TestFallbackVariesByPersonality(t *testing.T) {
	env := setup(t)
	lively := env.newPet(t, "欢欢", "lively")
	tsundere := env.newPet(t, "球球", "tsundere")

	r1, err := env.agent.Chat(context.Background(), lively.ID, "你好")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := env.agent.Chat(context.Background(), tsundere.ID, "你好")
	if err != nil {
		t.Fatal(err)
	}
	if r1 == r2 {
		t.Fatalf("different personalities should have different fallback lines: %q", r1)
	}
	if !oneOf(r1, fallbackLines["lively"], "精神好得很") {
		t.Fatalf("reply %q not a lively fallback line", r1)
	}
}

func TestFallbackRotation(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "团团", "quiet")

	first, err := env.agent.Chat(context.Background(), p.ID, "在吗")
	if err != nil {
		t.Fatal(err)
	}
	second, err := env.agent.Chat(context.Background(), p.ID, "还在吗")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("fallback lines should rotate, got %q twice", first)
	}
}

func TestFallbackMood(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "团团", "quiet")

	// 推进 12h：饱食度 70→10，跌破阈值 → 心情短语变成"肚子饿"。
	env.clock.Advance(12 * time.Hour)
	reply, err := env.agent.Chat(context.Background(), p.ID, "吃饭了吗")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "肚子饿得咕咕叫") {
		t.Fatalf("reply %q should carry hungry mood", reply)
	}
}

func TestChatDeadPet(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "团团", "lively")

	// 离线补算上限 24h/次：第一次推进健康掉到 30，第二次归零死亡。
	env.clock.Advance(24 * time.Hour)
	if _, err := env.engine.Settle(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	env.clock.Advance(24 * time.Hour)
	p2, err := env.engine.Settle(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Alive {
		t.Fatalf("pet should be dead after 48h neglect, health=%v", p2.Stats.Health)
	}

	reply, err := env.agent.Chat(context.Background(), p.ID, "你好")
	if err != nil {
		t.Fatal(err)
	}
	if reply != deadLine {
		t.Fatalf("dead pet reply = %q, want %q", reply, deadLine)
	}
}

func TestChatStreamFallback(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "团团", "lively")

	var chunks []string
	for chunk, err := range env.agent.ChatStream(context.Background(), p.ID, "在吗") {
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) == 0 {
		t.Fatal("no fallback chunks")
	}
	if !oneOf(strings.Join(chunks, ""), fallbackLines["lively"], "精神好得很") {
		t.Fatalf("streamed %q not a lively fallback line", strings.Join(chunks, ""))
	}
}

func TestChatUnknownPet(t *testing.T) {
	env := setup(t)
	if _, err := env.agent.Chat(context.Background(), "nope", "hi"); err == nil {
		t.Fatal("chat with unknown pet should fail")
	}
}

func TestEnsureFiles(t *testing.T) {
	env := setup(t)
	// 只建数值不建文件（模拟 M1 存量宠物）。
	p, err := env.engine.CreatePet(context.Background(), "老宠物", "dog")
	if err != nil {
		t.Fatal(err)
	}
	if env.fs.Exists(p.ID) {
		t.Fatal("files should not exist yet")
	}
	created, err := env.agent.EnsureFiles(p)
	if err != nil || !created {
		t.Fatalf("EnsureFiles = %v, %v", created, err)
	}
	if !env.fs.Exists(p.ID) {
		t.Fatal("files not created")
	}
	// 幂等
	created, err = env.agent.EnsureFiles(p)
	if err != nil || created {
		t.Fatalf("second EnsureFiles = %v, %v", created, err)
	}
}

// TestAssemble 验证指令装配：文件内容 + 实时状态快照（定性、无数值）。
func TestAssemble(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "团团", "tsundere")

	ins, err := env.agent.assemble(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# 行为准则",             // INSTRUCTIONS.md
		"绝不直接报数值",            // 核心原则
		"# 我是谁（PET.md）",      // 身份
		"团团",                 // 名字
		"# 我的性格（SOUL.md）",    // 性格
		"template: tsundere", // 性格模板
		"# 我现在的状态",           // 状态快照
		"成长阶段：蛋",             // 阶段（定性）
		"# 我的长期记忆（摘要）",       // 记忆
	} {
		if !strings.Contains(ins, want) {
			t.Fatalf("instruction missing %q:\n%s", want, ins)
		}
	}
	// 快照不得含数值（"不直接报数值"原则）。
	if strings.Contains(ins, "饱食感：70") || strings.Contains(ins, "Hunger") {
		t.Fatalf("instruction leaks raw stats:\n%s", ins)
	}
}

// TestStageSyncWritesPETMD 验证 stage_up 时 PET.md 的阶段字段被同步更新。
func TestStageSyncWritesPETMD(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "团团", "lively")

	// feed 25 次 = 50 EXP，恰好 egg→baby；事件经 MultiSink 到 StageSync。
	for i := 0; i < 25; i++ {
		if _, err := env.engine.Care(context.Background(), p.ID, pet.ActionFeed); err != nil {
			t.Fatal(err)
		}
	}
	p2, err := env.engine.Settle(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Stage != pet.StageBaby {
		t.Fatalf("stage = %v, want baby", p2.Stage)
	}
	petMD, err := env.fs.Read(p.ID, petfs.FilePET)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(petMD, "stage: baby") {
		t.Fatalf("PET.md stage not synced:\n%s", petMD)
	}
}

// TestStatusSnapshotNoNumbers 确认状态快照是定性描述。
func TestStatusSnapshotNoNumbers(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "团团", "quiet")
	snap := statusSnapshot(p)
	if !strings.Contains(snap, "饱食感：还不错") { // 新生宠物 Hunger=70
		t.Fatalf("snapshot = %q", snap)
	}
	if strings.Contains(snap, "70") {
		t.Fatalf("snapshot leaks numbers: %q", snap)
	}
}

// TestWakeNoteInjected 验证睡醒便签注入指令（M3）。
func TestWakeNoteInjected(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "团团", "quiet")

	// 便签存在且宠物醒着：assemble 输出包含"我刚睡醒"段落
	env.agent.wakeNotes[p.ID] = "你刚睡了一觉。\n你做了一个梦：梦见窗台上的阳光。\n"
	ins, err := env.agent.assemble(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ins, "# 我刚睡醒") || !strings.Contains(ins, "窗台上的阳光") {
		t.Fatalf("wake note not injected:\n%s", ins)
	}
	delete(env.agent.wakeNotes, p.ID)
}

// TestWakeNoteConsumedOnChat 验证醒来后第一次对话消费便签（降级路径也会取走并清除）。
func TestWakeNoteConsumedOnChat(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "团团", "quiet")

	if err := env.fs.WriteWakeNote(p.ID, "你刚睡了一觉。\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.agent.Chat(context.Background(), p.ID, "早上好"); err != nil {
		t.Fatal(err)
	}
	note, err := env.fs.TakeWakeNote(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if note != "" {
		t.Fatalf("wake note should be consumed by first chat, got %q", note)
	}
}
