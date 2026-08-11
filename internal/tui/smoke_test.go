package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// TestSmokeScript 是端到端冒烟（全内存、确定性）：
// 创建猫 → 喂食（吃动画）→ 睡觉（Zzz + SSE fell_asleep）→ hungry 事件 → 退出。
// 驱动方式与真实 Program 一致：执行 Update 返回的 Cmd，把产生的消息喂回模型。
func TestSmokeScript(t *testing.T) {
	ts := newTestServer(t)
	client := NewClient(ts.srv.URL)

	// 模拟用户在创建表单提交后的服务端结果。
	created, err := client.CreatePet(context.TODO(), "雪球", "cat", "傲娇")
	if err != nil {
		t.Fatal(err)
	}

	m := NewModel(client)
	m.screen = screenMain
	m.pet = created
	t.Cleanup(m.shutdown) // LIFO：先于 srv.Close 执行，避免 SSE 连接卡住清理

	// inbox 模拟 Program 的消息循环：异步执行 Cmd 并把消息喂回来。
	// tea.BatchMsg 由 Program 在真实运行时展开执行，这里同样递归展开。
	inbox := make(chan tea.Msg, 8)
	var execCmd func(tea.Cmd)
	execCmd = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		go func() {
			msg := cmd()
			if msg == nil {
				return
			}
			if batch, ok := msg.(tea.BatchMsg); ok {
				for _, c := range batch {
					execCmd(c)
				}
				return
			}
			inbox <- msg
		}()
	}
	drive := func(msg tea.Msg) {
		nm, cmd := m.Update(msg)
		m = nm.(model)
		execCmd(cmd)
	}

	// 进入主界面（等价于 createdMsg 路径）：启动 SSE 并等待事件。
	drive(createdMsg(created))

	// 等 SSE 回放 pet.born 到达模型。
	waitFor(t, inbox, func(msg tea.Msg) bool {
		drive(msg)
		return len(m.logs) > 0 && strings.Contains(m.logs[len(m.logs)-1], "出生")
	}, "born event in logs")

	// f 喂食：care 命令执行 → 状态回写 + 吃动画。
	drive(keyOf("f"))
	waitFor(t, inbox, func(msg tea.Msg) bool {
		if _, ok := msg.(careResultMsg); ok {
			drive(msg)
			return true
		}
		drive(msg)
		return false
	}, "care result")
	if m.action != animEat {
		t.Fatalf("action = %v, want eat", m.action)
	}
	if m.pet.Stats.Hunger != 100 {
		t.Fatalf("hunger = %d, want 100", m.pet.Stats.Hunger)
	}
	if v := m.renderString(); !strings.Contains(v, "[@]") && !strings.Contains(v, "[o]") {
		t.Fatalf("eat frame should show food:\n%s", v)
	}

	// s 睡觉：care → sleeping=true；SSE fell_asleep 随后到达日志。
	drive(keyOf("s"))
	waitFor(t, inbox, func(msg tea.Msg) bool {
		drive(msg)
		return m.pet.Sleeping
	}, "sleeping via care")
	waitFor(t, inbox, func(msg tea.Msg) bool {
		drive(msg)
		for _, l := range m.logs {
			if strings.Contains(l, "睡着了") {
				return true
			}
		}
		return false
	}, "fell_asleep in logs")

	// 睡觉动画帧：闭眼 + z。
	m2, _ := m.Update(tickMsg(time.Now()))
	m = m2.(model)
	if v := m.renderString(); !strings.Contains(v, "- -") || !strings.Contains(v, "z") {
		t.Fatalf("sleep frame should have closed eyes and z:\n%s", v)
	}

	// 推时钟两段 24h（睡眠中衰减减半：饱食 100 → 64 → 28，跌破预警线）→
	// 拉状态触发结算 → SSE pet.hungry 到日志。离线补算上限 24h/次，故分两段推进。
	ts.clock.Advance(24 * time.Hour)
	execCmd(getPetCmd(client, created.ID))
	waitFor(t, inbox, func(msg tea.Msg) bool {
		drive(msg)
		return m.pet.Stats.Hunger == 64
	}, "first offline settle")
	ts.clock.Advance(24 * time.Hour)
	execCmd(getPetCmd(client, created.ID))
	waitFor(t, inbox, func(msg tea.Msg) bool {
		drive(msg)
		hasHungry := false
		for _, l := range m.logs {
			if strings.Contains(l, "饿了") {
				hasHungry = true
			}
		}
		// 等事件与状态回写都到位（事件与 petMsg 顺序不定）。
		return hasHungry && m.pet.Stats.Hunger == 28
	}, "hungry event in logs")
	// 状态同步：饱食掉到 10，装饰出现。
	if v := m.renderString(); !strings.Contains(v, "流口水") {
		t.Fatalf("hungry decoration expected:\n%s", v)
	}

	// q 退出：返回 Quit 命令且 SSE 被取消。
	_, quitCmd := m.Update(keyOf("q"))
	if quitCmd == nil {
		t.Fatal("q should quit")
	}
	if m.sseCancel == nil {
		t.Fatal("sse should have been started")
	}
}

// waitFor 从 inbox 消费消息直到 want 满足（每条消息都先喂给模型）。
func waitFor(t *testing.T, inbox <-chan tea.Msg, feed func(tea.Msg) bool, what string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case msg := <-inbox:
			if feed(msg) {
				return
			}
		case <-deadline:
			t.Fatalf("timeout waiting for %s", what)
		}
	}
}
