package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// testPet 构造一只健康的测试宠物。
func testPet() Pet {
	return Pet{
		ID: "p1", Name: "团团", Species: "cat", Stage: "egg",
		Personality: "tsundere", Alive: true,
		Stats: Stats{Hunger: 70, Happy: 80, Clean: 80, Energy: 100, Health: 100},
	}
}

// update 便捷地驱动 model.Update 并取回新模型。
func update(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	nm, _ := m.Update(msg)
	return nm.(model)
}

func keyOf(s string) tea.KeyPressMsg {
	if len([]rune(s)) == 1 {
		r := []rune(s)[0]
		return tea.KeyPressMsg{Code: r, Text: s}
	}
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	}
	return tea.KeyPressMsg{}
}

// TestSelectFlow 验证启动后的选宠流程。
func TestSelectFlow(t *testing.T) {
	m := NewModel(NewClient("http://unused"))
	if m.screen != screenLoading {
		t.Fatal("should start at loading")
	}
	// 有宠物 → 选宠界面
	m = update(t, m, petsMsg{testPet(), {ID: "p2", Name: "雪球", Species: "dog", Stage: "baby", Alive: true}})
	if m.screen != screenSelect {
		t.Fatalf("screen = %v", m.screen)
	}
	// 移动光标 + 回车选中第二只
	m = update(t, m, keyOf("j"))
	m2, cmd := m.Update(keyOf("enter"))
	m = m2.(model)
	if m.screen != screenMain || m.pet.ID != "p2" || cmd == nil {
		t.Fatalf("after enter: screen=%v pet=%q cmd=%v", m.screen, m.pet.ID, cmd != nil)
	}
}

// TestEmptyListGoesCreate 验证无宠物时直接进入创建表单。
func TestEmptyListGoesCreate(t *testing.T) {
	m := NewModel(NewClient("http://unused"))
	m = update(t, m, petsMsg{})
	if m.screen != screenCreate {
		t.Fatalf("screen = %v", m.screen)
	}
	// 输入名字 + 回车提交
	for _, ch := range []string{"团", "团"} {
		m = update(t, m, tea.KeyPressMsg{Text: ch})
	}
	if m.createName != "团团" {
		t.Fatalf("name = %q", m.createName)
	}
	m = update(t, m, keyOf("tab")) // 物种
	m = update(t, m, keyOf("tab")) // 性格
	m = update(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	if personalityOptions[m.createPersonalityIdx] != "lively" {
		t.Fatalf("personality idx = %d", m.createPersonalityIdx)
	}
	_, cmd := m.Update(keyOf("enter"))
	if cmd == nil {
		t.Fatal("enter should submit create")
	}
}

func TestBirthTheaterFlow(t *testing.T) {
	m := NewModel(NewClient("http://unused"))
	m.createName = "团团"
	m = update(t, m, birthStartMsg{
		ID: "pid1", Seed: "abcdef0123456789", Species: "cat", Mode: "random", GenesisStatus: "incubating",
	})
	if m.screen != screenBirth || m.pet.ID != "pid1" {
		t.Fatalf("screen=%v pet=%+v", m.screen, m.pet)
	}
	if len(m.birthLog) == 0 {
		t.Fatal("expected birth log")
	}
	view := m.renderString()
	if !strings.Contains(view, "诞生中") || !strings.Contains(view, "团团") {
		t.Fatalf("view = %q", view)
	}

	m2, cmd := m.Update(eventMsg{Type: "genesis.temperament", Message: `{"label":"粘人社恐","blurb":"x"}`})
	m = m2.(model)
	if cmd == nil {
		t.Fatal("should keep waiting events")
	}
	found := false
	for _, line := range m.birthLog {
		if strings.Contains(line, "粘人社恐") {
			found = true
		}
	}
	if !found {
		t.Fatalf("birthLog = %v", m.birthLog)
	}

	m2, cmd = m.Update(eventMsg{Type: "genesis.ready", Message: `{"pet_id":"pid1"}`})
	m = m2.(model)
	if !m.birthReady || cmd == nil {
		t.Fatalf("ready=%v cmd=%v", m.birthReady, cmd != nil)
	}

	m = update(t, m, petMsg(testPet()))
	if m.screen != screenMain {
		t.Fatalf("screen = %v after pet ready", m.screen)
	}
}

func TestFormatGenesisLine(t *testing.T) {
	if got := formatGenesisLine(Event{Type: "genesis.genes", Message: `{}`}); got == "" {
		t.Fatal("genes")
	}
	if got := formatGenesisLine(Event{Type: "genesis.narration", Message: `{"text":"光纹浮现"}`}); got != "光纹浮现" {
		t.Fatalf("narration = %q", got)
	}
}

// TestCareKeysAndAnim 验证照顾按键：动作动画立即播放，若干帧后回到 idle。
func TestCareKeysAndAnim(t *testing.T) {
	m := NewModel(NewClient("http://unused"))
	m.screen = screenMain
	m.pet = testPet()

	nm, cmd := m.Update(keyOf("f"))
	m = nm.(model)
	if cmd == nil {
		t.Fatal("f should issue care cmd")
	}
	m = update(t, m, tickMsg(time.Now()))
	if m.action != animEat {
		t.Fatalf("action = %v, want eat", m.action)
	}
	// 吃完动作帧后回 idle
	for i := 0; i < animActionTicks; i++ {
		m = update(t, m, tickMsg(time.Now()))
	}
	if m.action != animIdle {
		t.Fatalf("action = %v, want idle after %d ticks", m.action, animActionTicks)
	}
}

// TestAdventureEventsDriveAnimation 验证探险 SSE 驱动动画与日志。
func TestAdventureEventsDriveAnimation(t *testing.T) {
	m := NewModel(NewClient("http://unused"))
	m.screen = screenMain
	m.pet = testPet()
	m.sseCh = make(chan Event, 1)

	m = update(t, m, eventMsg(Event{Type: "pet.adventure_started", Message: "团团 从【入口】出发去探险了", CreatedAt: t0}))
	if !m.adventuring || m.action != animAdventure {
		t.Fatalf("after start: adventuring=%v action=%v", m.adventuring, m.action)
	}
	view := m.renderString()
	if !strings.Contains(view, "探险中") || !strings.Contains(view, "[a]探险") {
		t.Fatalf("view missing adventure UI:\n%s", view)
	}

	m = update(t, m, eventMsg(Event{Type: "pet.adventure_moved", Message: "团团 走到了【地点3】", CreatedAt: t0}))
	if m.advNode != "地点3" {
		t.Fatalf("advNode = %q", m.advNode)
	}
	m = update(t, m, eventMsg(Event{Type: "pet.adventure_chest", Message: "团团 在【地点3】发现了一个宝箱！", CreatedAt: t0}))
	if m.advChests != 1 {
		t.Fatalf("advChests = %d", m.advChests)
	}
	foundStar := false
	for _, l := range m.logs {
		if strings.Contains(l, "宝箱") {
			foundStar = true
		}
	}
	if !foundStar {
		t.Fatalf("logs = %v", m.logs)
	}

	// 探险动画不因普通动作帧数归零
	for i := 0; i < animActionTicks+2; i++ {
		m = update(t, m, tickMsg(time.Now()))
	}
	if m.action != animAdventure {
		t.Fatalf("adventure anim should loop, got %v", m.action)
	}

	m = update(t, m, eventMsg(Event{Type: "pet.adventure_finished", Message: "团团 探险回来了，沿途发现了 1 个宝箱", CreatedAt: t0}))
	if m.adventuring || m.action != animIdle {
		t.Fatalf("after finish: adventuring=%v action=%v", m.adventuring, m.action)
	}
}

func TestActivityHeaderExclusive(t *testing.T) {
	m := NewModel(NewClient("http://unused"))
	m.screen = screenMain
	m.pet = testPet()
	m.adventuring = true
	m.advNode = "地点2"
	m.pet.Sleeping = true
	m.pet.Activity = "sleeping" // Snapshot 为准：睡觉优先于本地 adventuring 标志
	view := m.renderString()
	if !strings.Contains(view, "睡觉中") {
		t.Fatalf("want 睡觉中:\n%s", view)
	}
	if strings.Contains(view, "探险中") {
		t.Fatalf("sleeping must not show 探险中:\n%s", view)
	}

	m.pet.Activity = "adventuring"
	m.pet.Sleeping = false
	view = m.renderString()
	if !strings.Contains(view, "探险中") {
		t.Fatalf("want 探险中:\n%s", view)
	}
	if strings.Contains(view, "睡觉中") {
		t.Fatalf("adventuring must not show 睡觉中:\n%s", view)
	}

	m.stateCh = make(chan PetState, 1)
	m = update(t, m, stateMsg(PetState{
		ID: m.pet.ID, Stage: "egg", Sleeping: true, Alive: true, Activity: "sleeping",
		Stats: m.pet.Stats,
	}))
	if m.adventuring {
		t.Fatal("state activity=sleeping should clear adventuring")
	}
}

func TestAdventureKeyStartsCmd(t *testing.T) {
	m := NewModel(NewClient("http://unused"))
	m.screen = screenMain
	m.pet = testPet()
	mod, cmd := m.onMainKey(tea.KeyPressMsg{Text: "a"})
	mm := mod.(model)
	if mm.action != animAdventure {
		t.Fatalf("action = %v", mm.action)
	}
	if cmd == nil {
		t.Fatal("expected startAdventureCmd")
	}
}

// TestSleepAndWakeEvents 验证 SSE 事件同步动画状态。
func TestSleepAndWakeEvents(t *testing.T) {
	m := NewModel(NewClient("http://unused"))
	m.screen = screenMain
	m.pet = testPet()
	m.sseCh = make(chan Event, 1) // 避免 waitEventCmd 阻塞

	m = update(t, m, eventMsg(Event{Type: "pet.fell_asleep", Message: "团团 睡着了", CreatedAt: t0}))
	if !m.pet.Sleeping {
		t.Fatal("should be sleeping after fell_asleep")
	}
	if view := m.renderString(); !strings.Contains(view, "- -") {
		t.Fatalf("sleep frame should have closed eyes:\n%s", view)
	}
	// 睡觉中喂饭按键仍会发命令（由服务端拒绝），这里验证 sleeping 帧含 z
	m = update(t, m, tickMsg(time.Now()))
	m = update(t, m, eventMsg(Event{Type: "pet.woke_up", Message: "团团 醒来了", CreatedAt: t0}))
	if m.pet.Sleeping {
		t.Fatal("should be awake after woke_up")
	}
	// 事件进日志
	found := false
	for _, l := range m.logs {
		if strings.Contains(l, "睡着") {
			found = true
		}
	}
	if !found {
		t.Fatalf("logs = %v", m.logs)
	}
}

// TestStageUpCelebrates 验证 stage_up 触发庆祝动画。
func TestStageUpCelebrates(t *testing.T) {
	m := NewModel(NewClient("http://unused"))
	m.screen = screenMain
	m.pet = testPet()
	m.sseCh = make(chan Event, 1)

	m = update(t, m, eventMsg(Event{Type: "pet.stage_up", Message: "团团 成长到了 baby 阶段", CreatedAt: t0}))
	if m.action != animCelebrate {
		t.Fatalf("action = %v, want celebrate", m.action)
	}
	if view := m.renderString(); !strings.Contains(view, "*") {
		t.Fatalf("celebrate view should have sparkles:\n%s", view)
	}
	for i := 0; i < celebrateTicks; i++ {
		m = update(t, m, tickMsg(time.Now()))
	}
	if m.action != animIdle {
		t.Fatal("celebrate should end")
	}
}

// TestChatFlow 验证流式聊天：输入 → 流式渲染 → 定稿进日志。
func TestChatFlow(t *testing.T) {
	m := NewModel(NewClient("http://unused"))
	m.screen = screenMain
	m.pet = testPet()

	m = update(t, m, keyOf("t"))
	if !m.chatMode {
		t.Fatal("t should enter chat mode")
	}
	m = update(t, m, tea.KeyPressMsg{Text: "你"})
	m = update(t, m, tea.KeyPressMsg{Text: "好"})
	m = update(t, m, keyOf("backspace"))
	if m.input != "你" {
		t.Fatalf("input = %q", m.input)
	}
	m2, cmd := m.Update(keyOf("enter"))
	m = m2.(model)
	if cmd == nil || !m.streaming {
		t.Fatal("enter should start streaming chat")
	}
	// 流式期间忽略新的聊天请求
	m = update(t, m, keyOf("t"))
	if m.chatMode {
		t.Fatal("t should be ignored while streaming")
	}
	// 文本块逐步上屏
	m = update(t, m, chatEventMsg{chunk: "哼，"})
	m = update(t, m, chatEventMsg{chunk: "你好呀"})
	if v := m.renderString(); !strings.Contains(v, "[团团]: 哼，你好呀▌") {
		t.Fatalf("streaming view:\n%s", v)
	}
	// done 定稿进日志
	m = update(t, m, chatEventMsg{done: true, reply: "哼，你好呀"})
	last := m.logs[len(m.logs)-1]
	if !strings.Contains(last, "[团团]: 哼，你好呀") {
		t.Fatalf("reply log = %q", last)
	}
	if m.streaming || m.streamBuf != "" {
		t.Fatal("stream should be finalized")
	}
}

// TestChatStreamInterrupt 验证流式聊天可中断：esc 取消，已收到部分保留进日志。
func TestChatStreamInterrupt(t *testing.T) {
	m := NewModel(NewClient("http://unused"))
	m.screen = screenMain
	m.pet = testPet()
	m.streaming = true
	ctx, cancel := context.WithCancel(context.Background())
	m.chatCancel = cancel

	m = update(t, m, chatEventMsg{chunk: "我想说"})
	m = update(t, m, keyOf("esc"))
	if ctx.Err() == nil {
		t.Fatal("esc should cancel the stream")
	}
	// 取消后 done 到达：部分内容带省略号定稿
	m = update(t, m, chatEventMsg{done: true})
	last := m.logs[len(m.logs)-1]
	if !strings.Contains(last, "[团团]: 我想说 …") {
		t.Fatalf("partial log = %q", last)
	}
	if m.streaming {
		t.Fatal("stream should be finalized after done")
	}
}

// TestDeadPet 验证死亡：RIP 画面 + 动作键禁用。
func TestDeadPet(t *testing.T) {
	m := NewModel(NewClient("http://unused"))
	m.screen = screenMain
	m.pet = testPet()
	m.pet.Alive = false
	m.pet.Stats.Health = 0

	if view := m.renderString(); !strings.Contains(view, "RIP") {
		t.Fatalf("dead view should show RIP:\n%s", view)
	}
	if _, cmd := m.Update(keyOf("f")); cmd != nil {
		t.Fatal("care keys must be disabled when dead")
	}
	if _, cmd := m.Update(keyOf("t")); cmd != nil {
		t.Fatal("chat must be disabled when dead")
	}
	if _, cmd := m.Update(keyOf("r")); cmd == nil {
		t.Fatal("r should still work when dead")
	}
}

// TestMoodFaces 验证心情表情切换与低属性装饰。
func TestMoodFaces(t *testing.T) {
	m := NewModel(NewClient("http://unused"))
	m.screen = screenMain
	m.pet = testPet()

	// 开心（Happy 80）→ ^ ^
	if v := m.renderString(); !strings.Contains(v, "^ ^") {
		t.Fatalf("happy face:\n%s", v)
	}
	// 饥饿（Hunger 10）→ 装饰提示 + 心情词
	m.pet.Stats.Hunger = 10
	if v := m.renderString(); !strings.Contains(v, "流口水") || !strings.Contains(v, "肚子饿扁了") {
		t.Fatalf("hungry decorations:\n%s", v)
	}
	// 难过（Happy 10）→ ; ;
	m.pet.Stats.Hunger = 70
	m.pet.Stats.Happy = 10
	if v := m.renderString(); !strings.Contains(v, "; ;") {
		t.Fatalf("sad face:\n%s", v)
	}
	// 生病（Health 40）→ x x + 心情词
	m.pet.Stats.Happy = 80
	m.pet.Stats.Health = 40
	if v := m.renderString(); !strings.Contains(v, "x x") || !strings.Contains(v, "不太舒服") {
		t.Fatalf("sick face:\n%s", v)
	}
}

// TestOfflineScreen 验证连接失败进入离线页，r 重试。
func TestOfflineScreen(t *testing.T) {
	m := NewModel(NewClient("http://unused"))
	m = update(t, m, errMsg{"connect", errTest})
	if m.screen != screenOffline {
		t.Fatalf("screen = %v", m.screen)
	}
	if v := m.renderString(); !strings.Contains(v, "连不上服务器") {
		t.Fatalf("offline view:\n%s", v)
	}
	nm, cmd := m.Update(keyOf("r"))
	m = nm.(model)
	if cmd == nil || m.screen != screenLoading {
		t.Fatalf("r should retry: screen=%v", m.screen)
	}
}

var errTest = errorString("connection refused")

type errorString string

func (e errorString) Error() string { return string(e) }
