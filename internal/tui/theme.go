package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// theme.go —— 全站统一色板与样式（柔和粉彩，Catppuccin 风格）。
//
// 约定：
//   - 亮/暗色终端两套色值：cmd/pocketpet-tui 启动时用 lipgloss.HasDarkBackground
//     探测并调用 SetDarkBackground 切换；未探测时默认按暗色。
//   - NO_COLOR / 非终端输出时 lipgloss 自动退化为无色，界面回到纯文本。
//   - 先排版后上色：固定盒（view.go fitLines）与软换行（wrapText）只处理裸文本，
//     样式最后包整行/整块，ANSI 序列不参与显示宽度计算。

// 色板。命名按语义而非颜色，方便换主题。
var (
	accent    = lipgloss.Color("#F5A3C7") // 樱花粉：宠物名 / 发言 / 按键 / 探险中
	lavender  = lipgloss.Color("#B4A7E8") // 薰衣草紫：睡觉中
	green     = lipgloss.Color("#9ED69B") // 状态良好 / 操作成功
	yellow    = lipgloss.Color("#E8C37A") // 预警 / 宝箱
	red       = lipgloss.Color("#E87A7A") // 生病 / 危险 / 死亡
	faintGray = lipgloss.Color("#6E6E6E") // 卡片边框常态
	track     = lipgloss.Color("#484850") // 属性条轨道（未填充格）：比边框暗但可见
	catColor  = lipgloss.Color("#F0A58A") // 猫：蜜桃
	dogColor  = lipgloss.Color("#E8CF8A") // 狗：奶黄
	blobColor = lipgloss.Color("#8AC4F0") // blob：天蓝
)

// SetDarkBackground 按终端背景切换色板：亮背景换用深一档的同色系保证对比度。
// 由 cmd/pocketpet-tui 在程序启动前调用一次。
func SetDarkBackground(dark bool) {
	if dark { // 默认即暗色板
		return
	}
	accent = lipgloss.Color("#C45985")
	lavender = lipgloss.Color("#6A5CA8")
	green = lipgloss.Color("#4E8A4C")
	yellow = lipgloss.Color("#A87B2F")
	red = lipgloss.Color("#B44545")
	faintGray = lipgloss.Color("#9AA0A6")
	track = lipgloss.Color("#D8D4E0")
	catColor = lipgloss.Color("#C46A4E")
	dogColor = lipgloss.Color("#A8812F")
	blobColor = lipgloss.Color("#3F6FA8")
	buildStyles()
}

// 全站样式。颜色相关的样式由 buildStyles 构建（换色板后可重建）。
var (
	headerStyle  = lipgloss.NewStyle().Bold(true)  // 界面标题 / 宠物名
	faintStyle   = lipgloss.NewStyle().Faint(true) // 暗淡辅助信息（无色时也有效）
	inputStyle   lipgloss.Style                    // 输入行 / 流式回复
	accentStyle  lipgloss.Style                    // 主题色正文（宠物发言、探险态）
	keyStyle     lipgloss.Style                    // 按键提示
	successStyle lipgloss.Style
	warnStyle    lipgloss.Style
	dangerStyle  lipgloss.Style
	sleepStyle   lipgloss.Style
	trackStyle   lipgloss.Style // 属性条轨道（未填充格）
)

func init() { buildStyles() }

func buildStyles() {
	inputStyle = lipgloss.NewStyle().Bold(true).Foreground(accent)
	accentStyle = lipgloss.NewStyle().Foreground(accent)
	keyStyle = lipgloss.NewStyle().Bold(true).Foreground(accent)
	successStyle = lipgloss.NewStyle().Foreground(green)
	warnStyle = lipgloss.NewStyle().Foreground(yellow)
	dangerStyle = lipgloss.NewStyle().Foreground(red)
	sleepStyle = lipgloss.NewStyle().Foreground(lavender)
	trackStyle = lipgloss.NewStyle().Foreground(track)
}

// keyHint 渲染一枚胶囊式按键提示：按键主题色加粗，说明暗淡。
func keyHint(key, label string) string {
	return keyStyle.Render(key) + " " + faintStyle.Render(label)
}

// speciesColor 返回物种主题色：猫蜜桃、狗奶黄、blob 天蓝。
func speciesColor(species string) color.Color {
	switch species {
	case "cat":
		return catColor
	case "dog":
		return dogColor
	default:
		return blobColor
	}
}

// spriteCardStyle 精灵卡片样式：圆角边框，边框色随状态（死亡/生病红、睡觉紫、开心粉）。
// 卡片本身即状态指示；内容由 renderSprite 逐行着色（见 styleBoxLine），
// 这里不设整体 Foreground，避免嵌套样式的 reset 冲掉行内分色。
// 边框只给固定盒外加恒定 2 行 2 列，不破坏防抖动的布局恒定约束。
func (m model) spriteCardStyle() lipgloss.Style {
	border := faintGray
	switch {
	case !m.pet.Alive, m.pet.Stats.Health < sickBelow:
		border = red
	case m.pet.Sleeping:
		border = lavender
	case m.pet.Stats.Happy >= 70:
		border = accent
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border)
}

// styleBoxLine 按行类型给固定盒内的一行着色（排版完成后调用，ANSI 不参与宽度计算）。
func (m model) styleBoxLine(bl boxLine) string {
	switch bl.kind {
	case lineSprite:
		return lipgloss.NewStyle().Foreground(speciesColor(m.pet.Species)).Render(bl.text)
	case lineGround:
		return faintStyle.Render(bl.text)
	case lineSparkle:
		return styleSparkle(bl.text, m.frame)
	case lineBanner, lineFooter:
		return styleAdventureLine(bl.text)
	case lineDecor:
		return warnStyle.Render(bl.text)
	case lineAmbient:
		if m.pet.Sleeping {
			return sleepStyle.Render(bl.text)
		}
		return accentStyle.Render(bl.text)
	default: // lineBlank
		return bl.text
	}
}

// styleSparkle 给庆祝彩点逐颗上色：星星按帧号轮换彩虹色，圆点保持默认色。
func styleSparkle(line string, frame int) string {
	rainbow := []lipgloss.Style{successStyle, warnStyle, dangerStyle, sleepStyle, accentStyle}
	var b strings.Builder
	star := 0
	for _, r := range line {
		if r == '*' {
			b.WriteString(rainbow[(frame+star)%len(rainbow)].Render("*"))
			star++
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// styleAdventureLine 给探险 banner/footer 的关键符号上色：→ 主题色、◆ 黄、★ 绿。
func styleAdventureLine(line string) string {
	return strings.NewReplacer(
		"→", accentStyle.Render("→"),
		"◆", warnStyle.Render("◆"),
		"★", successStyle.Render("★"),
	).Replace(line)
}

// moodBadge 渲染心情徽章：彩色圆点 + 心情词（绿开心 / 黄预警 / 红难受 / 紫睡觉）。
func moodBadge(p Pet) string {
	word := moodWord(p)
	style := lipgloss.NewStyle()
	switch {
	case !p.Alive, p.Stats.Health < sickBelow:
		style = dangerStyle
	case p.Stats.Hunger < alertWarn || p.Stats.Happy < alertWarn:
		style = warnStyle
	case p.Sleeping:
		style = sleepStyle
	case p.Stats.Happy >= 70:
		style = successStyle
	}
	return style.Render("● " + word)
}

// styleLog 按日志条目前缀着色：✔ 绿、✗ 红、★/✎ 黄、宠物发言主题色、事件流水暗淡。
// entry 是完整条目（前缀判定用），line 是折行后的其中一段（被着色对象）。
func styleLog(entry, line string) string {
	switch {
	case strings.HasPrefix(entry, "✔"):
		return successStyle.Render(line)
	case strings.HasPrefix(entry, "✗"):
		return dangerStyle.Render(line)
	case strings.HasPrefix(entry, "★"), strings.HasPrefix(entry, "✎"):
		return warnStyle.Render(line)
	case strings.HasPrefix(entry, "["):
		return accentStyle.Render(line)
	case strings.HasPrefix(entry, "•"), strings.HasPrefix(entry, "·"):
		return faintStyle.Render(line)
	default:
		return line
	}
}
