package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
)

// 样式。
var (
	headerStyle = lipgloss.NewStyle().Bold(true)
	faintStyle  = lipgloss.NewStyle().Faint(true)
	inputStyle  = lipgloss.NewStyle().Bold(true)
)

// logWrapWidth 返回日志区的软换行宽度；尚未拿到终端宽度时不换行。
func (m model) logWrapWidth() int {
	if m.width <= 2 {
		return 0
	}
	return m.width - 2
}

// wrapText 按显示宽度把文本软换行（CJK 字符按双宽计），保留原有硬换行。
// bubbletea 标准渲染器会把超出终端宽度的行直接裁掉，长回复必须先换行再输出。
// width <= 0 表示不换行。
func wrapText(s string, width int) []string {
	if width <= 0 {
		return strings.Split(s, "\n")
	}
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		var b strings.Builder
		w := 0
		for _, r := range para {
			rw := runewidth.RuneWidth(r)
			if w+rw > width && b.Len() > 0 {
				lines = append(lines, b.String())
				b.Reset()
				w = 0
			}
			b.WriteRune(r)
			w += rw
		}
		lines = append(lines, b.String())
	}
	return lines
}

// View 实现 tea.Model。
func (m model) View() tea.View {
	return tea.NewView(m.renderString())
}

// renderString 按界面状态渲染整屏（测试可直接断言它）。
func (m model) renderString() string {
	switch m.screen {
	case screenLoading:
		return "\n  正在连接口袋宠物服务器……\n"
	case screenOffline:
		return fmt.Sprintf("\n  %s\n\n  连不上服务器：%s\n\n  [r] 重试    [q] 退出\n",
			headerStyle.Render("＞人＜ 呜……"), m.offline)
	case screenSelect:
		return m.renderSelect()
	case screenCreate:
		return m.renderCreate()
	case screenBirth:
		return m.renderBirth()
	case screenMain:
		return m.renderMain()
	}
	return ""
}

// renderSelect 渲染选宠列表。
func (m model) renderSelect() string {
	var b strings.Builder
	b.WriteString("\n  " + headerStyle.Render("选择你的宠物") + "\n\n")
	for i, p := range m.pets {
		cursor := "  "
		if i == m.cursor {
			cursor = "▶ "
		}
		line := fmt.Sprintf("%s%s（%s · %s · %s）", cursor, p.Name, p.Species, stageLabel(p.Stage), personalityLabel(p.Personality))
		if !p.Alive {
			line += " †"
		}
		b.WriteString("  " + line + "\n")
	}
	b.WriteString("\n  [↑/↓ 或 j/k] 选择    [enter] 进入    [n] 新建宠物    [q] 退出\n")
	return b.String()
}

// renderCreate 渲染创建表单。
func (m model) renderCreate() string {
	field := func(idx int, label, value string) string {
		marker := "  "
		if m.createField == idx {
			marker = "▶ "
		}
		return fmt.Sprintf("  %s%s：%s\n", marker, label, value)
	}
	var b strings.Builder
	b.WriteString("\n  " + headerStyle.Render("迎接新宠物") + "\n\n")
	name := m.createName
	if m.createField == 0 {
		name += "█"
	}
	b.WriteString(field(0, "名字", name))
	b.WriteString(field(1, "物种", "◀ "+speciesOptions[m.createSpeciesIdx]+" ▶"))
	b.WriteString(field(2, "气质", "◀ "+personalityLabels[personalityOptions[m.createPersonalityIdx]]+" ▶"))
	b.WriteString("\n  [tab/↑/↓] 切换项    [◀ ▶] 换选项    [enter] 开盲盒    [esc] 返回\n")
	if len(m.logs) > 0 {
		b.WriteString(faintStyle.Render("\n  " + m.logs[len(m.logs)-1] + "\n"))
	}
	return b.String()
}

// renderBirth 渲染 MetaAgent 诞生剧场：蛋动画 + 阶段旁白。
func (m model) renderBirth() string {
	eggs := []string{
		"    (  )\n   (    )\n  (  ··  )",
		"    (  )\n   ( ·  )\n  (  ··  )",
		"    (· )\n   (  · )\n  (  ··  )",
		"    (  )\n   (  · )\n  ( ··*  )",
	}
	egg := eggs[m.frame%len(eggs)]
	var b strings.Builder
	b.WriteString("\n  " + headerStyle.Render("诞生中") + "  " + faintStyle.Render(m.createName) + "\n\n")
	b.WriteString(egg + "\n\n")
	for _, line := range m.birthLog {
		b.WriteString("  " + faintStyle.Render(line) + "\n")
	}
	if m.birthReady {
		b.WriteString("\n  " + faintStyle.Render("正在睁开眼睛……") + "\n")
	} else {
		b.WriteString("\n  " + faintStyle.Render("造物主书写中，请稍候……  [q] 退出") + "\n")
	}
	return b.String()
}

// renderMain 渲染主界面：头部 / 精灵+属性 / 日志 / 帮助。
func (m model) renderMain() string {
	var b strings.Builder

	// 头部
	status := moodWord(m.pet)
	header := fmt.Sprintf(" %s · %s · %s · %s · %s",
		m.pet.Name, m.pet.Species, stageLabel(m.pet.Stage), personalityLabel(m.pet.Personality), status)
	if m.pet.Sleeping {
		header += "（睡觉中）"
	}
	if m.adventuring {
		loc := m.advNode
		if loc == "" {
			loc = "路上"
		}
		header += fmt.Sprintf("（探险中·%s", loc)
		if m.advChests > 0 {
			header += fmt.Sprintf("·宝箱×%d", m.advChests)
		}
		header += "）"
	}
	if !m.pet.Alive {
		header += " †"
	}
	b.WriteString(headerStyle.Render(header) + "\n")
	b.WriteString(faintStyle.Render(strings.Repeat("─", max(20, m.width/2))) + "\n")

	// 精灵 + 属性
	spriteBlock := m.renderSprite()
	statsBlock := renderStats(m.pet)
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, spriteBlock, "   ", statsBlock) + "\n")

	// 日志区（长行按终端宽度软换行，避免被渲染器裁掉右侧）
	b.WriteString(faintStyle.Render("── 日志 "+strings.Repeat("─", 12)) + "\n")
	if len(m.logs) == 0 {
		b.WriteString(faintStyle.Render("（还没有消息）") + "\n")
	}
	for _, l := range m.logs {
		for _, wl := range wrapText(l, m.logWrapWidth()) {
			b.WriteString(wl + "\n")
		}
	}
	// 流式回复：未收到的部分以光标占位。
	if m.streaming {
		line := "[" + m.pet.Name + "]: " + m.streamBuf + "▌"
		wrapped := wrapText(line, m.logWrapWidth())
		for i, wl := range wrapped {
			if i == len(wrapped)-1 {
				b.WriteString(inputStyle.Render(wl) + "\n")
			} else {
				b.WriteString(wl + "\n")
			}
		}
	}

	// 底部帮助 / 输入行
	if m.chatMode {
		b.WriteString(inputStyle.Render("> "+m.input+"█") + faintStyle.Render("  （enter 发送，esc 取消）") + "\n")
	} else if m.streaming {
		b.WriteString(faintStyle.Render("回复中…… [esc] 中断") + "\n")
	} else if !m.pet.Alive {
		b.WriteString(faintStyle.Render("[r] 刷新    [q] 退出") + "\n")
	} else {
		b.WriteString(faintStyle.Render("[f]喂食 [p]玩耍 [c]清洁 [a]探险 [s]睡觉 [w]叫醒 [t]聊天 [r]刷新 [q]退出") + "\n")
	}
	return b.String()
}

// renderSprite 渲染精灵动画区（含庆祝覆盖与低属性提示装饰）。
func (m model) renderSprite() string {
	sp := spriteFor(m.pet.Species)

	var frame string
	switch {
	case !m.pet.Alive:
		frame = sp.Dead
	case m.pet.Sleeping:
		frame = sp.Sleep[m.frame%len(sp.Sleep)]
	default:
		switch m.action {
		case animEat:
			frame = sp.Eat[m.frame%len(sp.Eat)]
		case animPlay:
			frame = sp.Play[m.frame%len(sp.Play)]
		case animClean:
			frame = sp.Clean[m.frame%len(sp.Clean)]
		case animAdventure:
			frame = sp.Play[m.frame%len(sp.Play)]
		default:
			frame = sp.Idle[m.frame%len(sp.Idle)]
		}
	}
	if m.pet.Alive {
		f := faceFor(m.pet)
		frame = strings.NewReplacer("{e}", f.eyes, "{m}", f.mouth).Replace(frame)
	}

	lines := strings.Split(strings.TrimPrefix(frame, "\n"), "\n")
	// 升级庆祝：上下各加一行交替闪烁的字符彩点。
	if m.action == animCelebrate {
		var sparkle string
		if m.frame%2 == 0 {
			sparkle = "* · * · *"
		} else {
			sparkle = "· * · * ·"
		}
		lines = append([]string{sparkle}, append(lines, sparkle)...)
	}
	// 探险中：顶部滚动地图路径，底部提示。
	if m.action == animAdventure || m.adventuring {
		banner := adventurePathBanner(m.frame)
		footer := "→ 探险中"
		if m.advNode != "" {
			footer = "→ " + m.advNode
		}
		if m.advChests > 0 {
			footer += fmt.Sprintf(" ★×%d", m.advChests)
		}
		lines = append([]string{banner}, append(lines, footer)...)
	}
	// 低属性提示装饰。
	if m.pet.Alive {
		if d := decorations(m.pet); d != "" {
			lines = append(lines, d)
		}
	}
	// 统一左边距并补齐高度，避免布局跳动。
	const padH = 7
	for len(lines) < padH {
		lines = append(lines, "")
	}
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

// adventurePathBanner 探险路径滚动条（表现层动画）。
func adventurePathBanner(frame int) string {
	paths := []string{
		"· → · · ◆ · ·",
		"· · → · ◆ · ·",
		"· · · → ◆ · ·",
		"· · · · → ★ ·",
		"· · · · ◆ → ·",
		"★ · · · ◆ · →",
	}
	return paths[frame%len(paths)]
}

// face 是表情（眼睛 3 字符 + 嘴 1 字符）。
type face struct {
	eyes, mouth string
}

// 告警阈值，与 internal/pet 的 AlertWarn / SickBelow 保持一致
//（TUI 使用自己的 JSON 类型，不 import 领域层）。
const (
	alertWarn = 30
	sickBelow = 50
)

// faceFor 按状态给出表情：开心 ^^、一般 oo、难过 ;;、生病 xx、睡觉 --、死亡 XX。
func faceFor(p Pet) face {
	switch {
	case !p.Alive:
		return face{"X X", "x"}
	case p.Sleeping:
		return face{"- -", "-"}
	case p.Stats.Health < sickBelow:
		return face{"x x", "~"}
	case p.Stats.Happy >= 70:
		return face{"^ ^", "w"}
	case p.Stats.Happy < alertWarn:
		return face{"; ;", "n"}
	default:
		return face{"o o", "o"}
	}
}

// decorations 是低属性的提示性装饰：饿了流口水、脏了长斑点、困了打瞌睡。
func decorations(p Pet) string {
	var parts []string
	if p.Stats.Hunger < alertWarn {
		parts = append(parts, "饿得流口水 ~")
	}
	if p.Stats.Clean < alertWarn {
		parts = append(parts, "脏兮兮 ,,")
	}
	if p.Stats.Energy < alertWarn && !p.Sleeping {
		parts = append(parts, "困得睁不开眼")
	}
	return strings.Join(parts, "  ")
}

// renderStats 渲染五维属性条 + EXP/阶段。
func renderStats(p Pet) string {
	rows := []struct {
		label string
		value int
	}{
		{"饱食", p.Stats.Hunger},
		{"心情", p.Stats.Happy},
		{"清洁", p.Stats.Clean},
		{"精力", p.Stats.Energy},
		{"健康", p.Stats.Health},
	}
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%s %s %3d\n", r.label, bar(r.value), r.value)
	}
	fmt.Fprintf(&b, "\nEXP %d   阶段 %s", p.Stats.EXP, stageLabel(p.Stage))
	return b.String()
}

// bar 渲染 10 格进度条。
func bar(v int) string {
	v = max(0, min(100, v))
	filled := v / 10
	return strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
}

// moodWord 是当前心情词（头部展示）。
func moodWord(p Pet) string {
	switch {
	case !p.Alive:
		return "已离世"
	case p.Sleeping:
		return "呼呼大睡"
	case p.Stats.Health < sickBelow:
		return "不太舒服"
	case p.Stats.Hunger < alertWarn:
		return "肚子饿扁了"
	case p.Stats.Happy >= 70:
		return "开心"
	case p.Stats.Happy < alertWarn:
		return "闷闷的"
	default:
		return "平静"
	}
}

// stageLabel 是成长阶段中文名。
func stageLabel(stage string) string {
	switch stage {
	case "egg":
		return "蛋"
	case "baby":
		return "幼年"
	case "child":
		return "成长期"
	case "adult":
		return "成年"
	default:
		return stage
	}
}

// personalityLabel 是性格模板中文名。
func personalityLabel(key string) string {
	if label, ok := personalityLabels[key]; ok && key != "" {
		return label
	}
	if key == "" {
		return "神秘"
	}
	return key
}
