package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
)

// TestWrapText 验证长行按显示宽度软换行：CJK 双宽、硬换行保留、不超宽。
func TestWrapText(t *testing.T) {
	// 短行不换行
	if got := wrapText("你好", 10); len(got) != 1 || got[0] != "你好" {
		t.Fatalf("short line = %v", got)
	}
	// CJK 按双宽计：10 个汉字 = 20 宽，宽度 10 → 两行各 5 字
	got := wrapText("一二三四五六七八九十", 10)
	if len(got) != 2 || got[0] != "一二三四五" || got[1] != "六七八九十" {
		t.Fatalf("cjk wrap = %v", got)
	}
	for _, l := range got {
		if runewidth.StringWidth(l) > 10 {
			t.Fatalf("line %q exceeds width", l)
		}
	}
	// 混合中英 + 硬换行保留
	got = wrapText("ab你 cd\nef", 4)
	if len(got) != 3 || got[0] != "ab你" || got[1] != " cd" || got[2] != "ef" {
		t.Fatalf("mixed wrap = %v", got)
	}
	// width <= 0：不换行，仅按硬换行拆分
	if got := wrapText("abc\ndef", 0); len(got) != 2 || strings.Join(got, "\n") != "abc\ndef" {
		t.Fatalf("no wrap = %v", got)
	}
}

// healthyPet 返回一只各项属性健康的测试宠物。
func healthyPet() Pet {
	return Pet{
		ID: "p1", Name: "咪咪", Species: "cat", Stage: "baby", Alive: true,
		Stats: Stats{Hunger: 80, Happy: 80, Clean: 80, Energy: 80, Health: 80},
	}
}

// TestRenderSpriteFixedBox 精灵区是固定盒子：任意动作/帧号/状态组合下，
// 行数恒为 spriteBoxH、每行显示宽度恒为 2+spriteBoxW——动画只换字符、不动布局。
func TestRenderSpriteFixedBox(t *testing.T) {
	lowStats := healthyPet()
	lowStats.Stats.Hunger, lowStats.Stats.Clean, lowStats.Stats.Energy = 10, 10, 10
	sleeping := healthyPet()
	sleeping.Sleeping = true
	dead := healthyPet()
	dead.Alive = false

	pets := map[string]Pet{
		"healthy":  healthyPet(),
		"lowStats": lowStats, // 触发低属性装饰（含 CJK 宽字符）
		"sleeping": sleeping,
		"dead":     dead,
	}
	actions := []animAction{animIdle, animEat, animPlay, animClean, animCelebrate, animAdventure}

	for name, p := range pets {
		for _, action := range actions {
			for _, adventuring := range []bool{false, true} {
				for frame := 0; frame < 12; frame++ {
					m := model{pet: p, action: action, frame: frame, adventuring: adventuring}
					lines := strings.Split(m.renderSprite(), "\n")
					if len(lines) != spriteBoxH {
						t.Fatalf("%s action=%d adv=%v frame=%d: 行数 %d ≠ %d",
							name, action, adventuring, frame, len(lines), spriteBoxH)
					}
					for i, l := range lines {
						if w := runewidth.StringWidth(l); w != 2+spriteBoxW {
							t.Fatalf("%s action=%d adv=%v frame=%d 行 %d 宽 %d ≠ %d：%q",
								name, action, adventuring, frame, i, w, 2+spriteBoxW, l)
						}
					}
				}
			}
		}
	}
}

// TestSpriteCardStable 精灵卡片（圆角边框 + 物种色，见 theme.go）行列恒定：
// 边框只给固定盒外加恒定 2 行 2 列，动画期间整体尺寸不变。
func TestSpriteCardStable(t *testing.T) {
	p := healthyPet()
	wantW := spriteBoxW + 2 + 2 // 盒宽 + 盒内左缩进 + 左右边框
	for _, action := range []animAction{animIdle, animAdventure, animCelebrate} {
		for frame := 0; frame < 8; frame++ {
			m := model{pet: p, action: action, frame: frame, adventuring: true, advNode: "入口"}
			card := m.spriteCardStyle().Render(m.renderSprite())
			lines := strings.Split(card, "\n")
			if len(lines) != spriteBoxH+2 {
				t.Fatalf("action=%d frame=%d: 卡片行数 %d ≠ %d", action, frame, len(lines), spriteBoxH+2)
			}
			for i, l := range lines {
				if w := lipgloss.Width(l); w != wantW {
					t.Fatalf("action=%d frame=%d 行 %d 宽 %d ≠ %d：%q", action, frame, i, w, wantW, l)
				}
			}
		}
	}
}

// TestRenderLogLinesFixedWindow 日志区是固定行数窗口：任意日志量下行数恒定，
// 超出窗口时保留最新内容。
func TestRenderLogLinesFixedWindow(t *testing.T) {
	var m model
	// 空日志：占位文案 + 空行补齐
	got := m.renderLogLines()
	if len(got) != logAreaLines {
		t.Fatalf("空日志：行数 %d ≠ %d", len(got), logAreaLines)
	}
	if !strings.Contains(got[0], "还没有消息") {
		t.Fatalf("空日志：首行 = %q", got[0])
	}
	for i, l := range got[1:] {
		if l != "" {
			t.Fatalf("空日志：补齐行 %d = %q", i, l)
		}
	}

	// 少于窗口：内容在前、空行在后
	m.logs = []string{"a", "b", "c"}
	got = m.renderLogLines()
	if len(got) != logAreaLines || got[0] != "a" || got[2] != "c" || got[3] != "" || got[7] != "" {
		t.Fatalf("少量日志 = %v", got)
	}

	// 超出窗口（窄宽度下每条软换行为多行）：保留最新、行数仍恒定
	m.width = 12 // logWrapWidth = 10，每条日志 8 个汉字折成 2 行
	m.logs = nil
	for i := 0; i < maxLogs; i++ {
		m.logf("log%d 一二三四五六七八", i)
	}
	got = m.renderLogLines()
	if len(got) != logAreaLines {
		t.Fatalf("超窗日志：行数 %d ≠ %d", len(got), logAreaLines)
	}
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "log0") || !strings.Contains(joined, "log6") {
		t.Fatalf("超窗日志未保留最新内容：%q", joined)
	}
}
