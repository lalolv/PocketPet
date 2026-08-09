package tui

import (
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

// TestChatFlow 验证聊天输入模式。
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
	_, cmd := m.Update(keyOf("enter"))
	if cmd == nil {
		t.Fatal("enter should send chat")
	}
	m = update(t, m, replyMsg("哼，你好呀"))
	last := m.logs[len(m.logs)-1]
	if !strings.Contains(last, "团团：哼，你好呀") {
		t.Fatalf("reply log = %q", last)
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
