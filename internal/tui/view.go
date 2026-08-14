package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
)

// 样式统一定义在 theme.go。

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
			cursor = keyStyle.Render("▶") + " "
		}
		// 宠物名着物种色，列表即见个性。
		name := lipgloss.NewStyle().Foreground(speciesColor(p.Species)).Render(p.Name)
		line := fmt.Sprintf("%s%s（%s · %s · %s）", cursor, name, p.Species, stageLabel(p.Stage), personalityLabel(p.Personality))
		if !p.Alive {
			line += dangerStyle.Render(" †")
		}
		b.WriteString("  " + line + "\n")
	}
	b.WriteString(faintStyle.Render("\n  [↑/↓ 或 j/k] 选择    [enter] 进入    [n] 新建宠物    [q] 退出\n"))
	return b.String()
}

// renderCreate 渲染创建表单。
func (m model) renderCreate() string {
	field := func(idx int, label, value string) string {
		marker := "  "
		if m.createField == idx {
			marker = keyStyle.Render("▶") + " "
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
	b.WriteString(faintStyle.Render("\n  [tab/↑/↓] 切换项    [◀ ▶] 换选项    [enter] 开盲盒    [esc] 返回\n"))
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
	b.WriteString(accentStyle.Render(egg) + "\n\n")
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

	// 头部：宠物名加粗主题色，心情为彩色圆点徽章，活动态徽章着色。
	header := fmt.Sprintf(" %s · %s · %s · %s · %s",
		headerStyle.Foreground(accent).Render(m.pet.Name),
		m.pet.Species, stageLabel(m.pet.Stage), personalityLabel(m.pet.Personality), moodBadge(m.pet))
	// 活动态互斥展示：只跟 Snapshot.Activity（死亡 > sleeping > adventuring）。
	act := m.pet.Activity
	if act == "" {
		if m.pet.Sleeping {
			act = "sleeping"
		} else if m.adventuring {
			act = "adventuring"
		} else {
			act = "idle"
		}
	}
	switch {
	case !m.pet.Alive:
		header += dangerStyle.Render(" †")
	case act == "sleeping":
		header += sleepStyle.Render("（睡觉中）")
	case act == "adventuring":
		loc := m.advNode
		if loc == "" {
			loc = "路上"
		}
		badge := fmt.Sprintf("（探险中·%s", loc)
		if m.advChests > 0 {
			badge += fmt.Sprintf("·宝箱×%d", m.advChests)
		}
		header += accentStyle.Render(badge + "）")
	}
	b.WriteString(header + "\n")
	b.WriteString(faintStyle.Render(strings.Repeat("─", max(20, m.width/2))) + "\n")

	// 精灵卡片（圆角边框 + 物种色，见 theme.go）+ 属性
	spriteCard := m.spriteCardStyle().Render(m.renderSprite())
	statsBlock := renderStats(m.pet)
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, spriteCard, "  ", statsBlock) + "\n")

	// 日志区：固定行数窗口（长行按终端宽度软换行，最新内容优先，不足补空行），
	// 行数恒定后整屏总高度不随日志进出跳动。
	b.WriteString(faintStyle.Render("── 日志 "+strings.Repeat("─", 12)) + "\n")
	for _, l := range m.renderLogLines() {
		b.WriteString(l + "\n")
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

	// 底部帮助 / 输入行：胶囊式按键提示（按键主题色、说明暗淡）。
	if m.chatMode {
		b.WriteString(inputStyle.Render("> "+m.input+"█") + faintStyle.Render("  （enter 发送，esc 取消）") + "\n")
	} else if m.streaming {
		b.WriteString(faintStyle.Render("回复中…… esc 中断") + "\n")
	} else if !m.pet.Alive {
		b.WriteString(" " + keyHint("r", "刷新") + faintStyle.Render("  ·  ") + keyHint("q", "退出") + "\n")
	} else {
		hints := []string{
			keyHint("f", "喂食"), keyHint("p", "玩耍"), keyHint("c", "清洁"),
			keyHint("a", "探险"), keyHint("s", "睡觉"), keyHint("w", "叫醒"),
			keyHint("t", "聊天"), keyHint("r", "刷新"), keyHint("q", "退出"),
		}
		b.WriteString(" " + strings.Join(hints, faintStyle.Render(" · ")) + "\n")
	}
	return b.String()
}

// logAreaLines 是日志区的固定行数（按渲染后的行数计，而非日志条目数）。
const logAreaLines = 8

// renderLogLines 把日志渲染成固定行数的窗口：条目软换行后保留最新若干行，
// 不足补空行。旧条目被挤出窗口与 maxLogs 的淘汰语义一致。
func (m model) renderLogLines() []string {
	var lines []string
	if len(m.logs) == 0 {
		lines = append(lines, faintStyle.Render("（还没有消息）"))
	} else {
		for _, entry := range m.logs {
			for _, wl := range wrapText(entry, m.logWrapWidth()) {
				lines = append(lines, styleLog(entry, wl))
			}
		}
		if len(lines) > logAreaLines {
			lines = lines[len(lines)-logAreaLines:]
		}
	}
	for len(lines) < logAreaLines {
		lines = append(lines, "")
	}
	return lines
}

// renderSprite 渲染精灵动画区：固定盒内依次排布环境行 / 庆祝彩点 / 探险 banner、
// 精灵帧、地面线、覆盖层底部、低属性装饰；排版后按行类型逐行着色。
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

	var body []boxLine
	for _, l := range strings.Split(strings.TrimPrefix(frame, "\n"), "\n") {
		body = append(body, boxLine{l, lineSprite})
	}
	// 地面线：精灵脚下的一行点缀，填补盒子底部空行，画面更稳。
	body = append(body, boxLine{" · · · · · · ·", lineGround})

	celebrate := m.action == animCelebrate
	adventure := m.action == animAdventure || m.adventuring
	// 升级庆祝：上下各加一行交替闪烁的彩点。
	if celebrate {
		body = append([]boxLine{{sparklePattern(m.frame), lineSparkle}},
			append(body, boxLine{sparklePattern(m.frame + 1), lineSparkle})...)
	}
	// 探险中：顶部滚动地图路径，底部提示。
	if adventure {
		body = append([]boxLine{{adventurePathBanner(m.frame), lineBanner}},
			append(body, boxLine{adventureFooter(m.advNode, m.advChests), lineFooter})...)
	}
	// 环境行（仅无覆盖层时）：睡觉星光闪烁、开心音符漂浮。
	if !celebrate && !adventure {
		if a := m.ambientLine(); a != "" {
			body = append([]boxLine{{a, lineAmbient}}, body...)
		}
	}
	// 低属性提示装饰。
	if m.pet.Alive {
		if d := decorations(m.pet); d != "" {
			body = append(body, boxLine{d, lineDecor})
		}
	}

	// 固定盒子：高度、宽度逐帧恒定——动画只换字符、绝不动布局，
	// 这是动画期间界面不抖的关键。先排版（fitBoxLines）后逐行着色，
	// ANSI 序列不参与显示宽度计算。
	body = fitBoxLines(body, spriteBoxH, spriteBoxW)
	lines := make([]string, len(body))
	for i, bl := range body {
		lines[i] = "  " + m.styleBoxLine(bl)
	}
	return strings.Join(lines, "\n")
}

// 盒内行类型：决定排版后的着色方式（见 theme.go 的 styleBoxLine）。
type boxLineKind int

const (
	lineBlank boxLineKind = iota
	lineSprite
	lineGround  // 精灵脚下的地面线
	lineSparkle // 升级庆祝彩点
	lineBanner  // 探险滚动路径
	lineFooter  // 探险底部提示
	lineDecor   // 低属性提示装饰
	lineAmbient // 环境行（睡觉星光 / 开心音符）
)

// boxLine 是固定盒里的一行：裸文本 + 行类型。
type boxLine struct {
	text string
	kind boxLineKind
}

// sparklePattern 庆祝彩点图案（奇偶帧交错闪烁）。
func sparklePattern(frame int) string {
	if frame%2 == 0 {
		return "* · * · *"
	}
	return "· * · * ·"
}

// adventureFooter 探险卡片的底部提示行。
func adventureFooter(node string, chests int) string {
	footer := "→ 探险中"
	if node != "" {
		footer = "→ " + node
	}
	if chests > 0 {
		footer += fmt.Sprintf(" ★×%d", chests)
	}
	return footer
}

// ambientLine 返回空闲态的环境行：睡觉星光、开心音符；其余为空（不占行）。
func (m model) ambientLine() string {
	switch {
	case m.pet.Sleeping:
		if m.frame%2 == 0 {
			return " ✦      ·"
		}
		return " ·      ✦"
	case m.pet.Alive && m.pet.Stats.Happy >= 70:
		if m.frame%2 == 0 {
			return "  ♪"
		}
		return "   ♪"
	}
	return ""
}

// 精灵区固定盒子尺寸（宽为显示列数，高为行数）。
const (
	spriteBoxW = 24 // 覆盖最宽内容：帧 ≤14、探险 banner 13、双项低属性装饰约 23
	spriteBoxH = 8  // 帧 5 + 覆盖层 2 + 地面 1；低属性装饰在极端组合下让位截断
)

// fitBoxLines 把盒内行装进固定盒子：超高截断、不足补空行、每行等宽（裸文本排版）。
func fitBoxLines(body []boxLine, height, width int) []boxLine {
	if len(body) > height {
		body = body[:height]
	}
	out := make([]boxLine, 0, height)
	for _, bl := range body {
		bl.text = fitWidth(bl.text, width)
		out = append(out, bl)
	}
	for len(out) < height {
		out = append(out, boxLine{strings.Repeat(" ", width), lineBlank})
	}
	return out
}

// fitWidth 把单行按显示宽度截断或右侧补齐到 width 列（CJK 按双宽计）。
func fitWidth(s string, width int) string {
	if runewidth.StringWidth(s) > width {
		s = runewidth.Truncate(s, width, "…")
	}
	return s + strings.Repeat(" ", width-runewidth.StringWidth(s))
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

// bar 渲染 10 格进度条：按值着色（≥60 绿 / 30-59 黄 / <30 红），空格部分暗淡。
func bar(v int) string {
	v = max(0, min(100, v))
	filled := v / 10
	style := successStyle
	switch {
	case v < alertWarn:
		style = dangerStyle
	case v < 60:
		style = warnStyle
	}
	return style.Render(strings.Repeat("█", filled)) + faintStyle.Render(strings.Repeat("░", 10-filled))
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
