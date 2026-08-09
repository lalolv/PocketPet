package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// 节奏与容量常量。
const (
	frameDur        = 250 * time.Millisecond // 动画帧间隔
	pollDur         = 4 * time.Second        // 状态轮询间隔（SSE 之外的兜底）
	reconnectDur    = 2 * time.Second        // SSE 断线重连等待
	maxLogs         = 7                      // 日志区保留条数
	animActionTicks = 6                      // eat/play/clean 动作帧数
	celebrateTicks  = 8                      // 升级庆祝帧数
)

// screen 是界面状态机。
type screen int

const (
	screenLoading screen = iota // 启动加载中
	screenSelect                // 选宠
	screenCreate                // 创建表单
	screenMain                  // 主界面
	screenOffline               // 服务器不可达
)

// animAction 是动画状态机（表现层，不阻塞操作）。
type animAction int

const (
	animIdle animAction = iota
	animEat
	animPlay
	animClean
	animCelebrate // stage_up 庆祝
)

// 创建表单的选项。
var (
	speciesOptions     = []string{"cat", "dog", "blob"}
	personalityOptions = []string{"", "lively", "quiet", "tsundere"}
	personalityLabels  = map[string]string{"": "随机", "lively": "活泼", "quiet": "安静", "tsundere": "傲娇"}
)

// model 是 Bubble Tea 主模型。
type model struct {
	client *Client
	screen screen

	// 选宠 / 创建
	pets                 []Pet
	cursor               int
	createField          int // 0 名字 1 物种 2 性格
	createName           string
	createSpeciesIdx     int
	createPersonalityIdx int

	// 主界面
	pet        Pet
	action     animAction
	frame      int
	logs       []string
	chatMode   bool
	input      string
	sseCh      chan Event
	sseCancel  context.CancelFunc
	petLoading bool // 首次进入主界面尚未拿到状态

	offline string // 离线原因（screenOffline 展示）

	width, height int
}

// NewModel 创建 TUI 模型。
func NewModel(client *Client) model {
	return model{client: client, screen: screenLoading}
}

// 消息类型。
type (
	tickMsg       time.Time // 动画帧
	pollMsg       time.Time // 状态轮询
	petsMsg       []Pet     // 宠物列表
	petMsg        Pet       // 宠物状态（get/care 响应）
	createdMsg    Pet       // 创建成功
	replyMsg      string    // chat 回复
	eventMsg      Event     // SSE 事件
	errMsg        errorWrap // 错误（含来源）
	careResultMsg struct {
		pet    Pet
		action string
	}
)

type errorWrap struct {
	where string
	err   error
}

// Init 实现 tea.Model。
func (m model) Init() tea.Cmd {
	return tea.Batch(loadPetsCmd(m.client), frameTick(), pollTick())
}

func frameTick() tea.Cmd {
	return tea.Tick(frameDur, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func pollTick() tea.Cmd {
	return tea.Tick(pollDur, func(t time.Time) tea.Msg { return pollMsg(t) })
}

// 异步命令。
func loadPetsCmd(c *Client) tea.Cmd {
	return func() tea.Msg {
		pets, err := c.ListPets(context.Background())
		if err != nil {
			return errMsg{"connect", err}
		}
		return petsMsg(pets)
	}
}

func getPetCmd(c *Client, id string) tea.Cmd {
	return func() tea.Msg {
		p, err := c.GetPet(context.Background(), id)
		if err != nil {
			return errMsg{"get", err}
		}
		return petMsg(p)
	}
}

func createCmd(c *Client, name, species, personality string) tea.Cmd {
	return func() tea.Msg {
		p, err := c.CreatePet(context.Background(), name, species, personality)
		if err != nil {
			return errMsg{"create", err}
		}
		return createdMsg(p)
	}
}

func careCmd(c *Client, id, action string) tea.Cmd {
	return func() tea.Msg {
		p, err := c.Care(context.Background(), id, action)
		if err != nil {
			return errMsg{"care", err}
		}
		return careResultMsg{pet: p, action: action}
	}
}

func chatCmd(c *Client, id, message string) tea.Cmd {
	return func() tea.Msg {
		reply, err := c.Chat(context.Background(), id, message)
		if err != nil {
			return errMsg{"chat", err}
		}
		return replyMsg(reply)
	}
}

// waitEventCmd 从 SSE 通道取一条事件；通道关闭时返回 nil（不产生消息）。
func waitEventCmd(ch <-chan Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return eventMsg(ev)
	}
}

// Update 实现 tea.Model。
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyPressMsg:
		return m.onKey(msg)

	case tickMsg:
		m.frame++
		if m.action != animIdle && m.action != animCelebrate && m.frame >= animActionTicks {
			m.action, m.frame = animIdle, 0
		}
		if m.action == animCelebrate && m.frame >= celebrateTicks {
			m.action, m.frame = animIdle, 0
		}
		return m, frameTick()

	case pollMsg:
		if m.screen == screenMain && m.pet.ID != "" && m.pet.Alive {
			return m, tea.Batch(getPetCmd(m.client, m.pet.ID), pollTick())
		}
		return m, pollTick()

	case petsMsg:
		m.pets = msg
		if len(msg) == 0 {
			m.screen = screenCreate
		} else {
			m.screen = screenSelect
			m.cursor = 0
		}
		return m, nil

	case createdMsg:
		m.pet = Pet(msg)
		m.petLoading = false
		m.screen = screenMain
		m.logf("欢迎来到这个世界，%s！", m.pet.Name)
		return m, m.startSSE()

	case petMsg:
		m.pet = Pet(msg)
		m.petLoading = false
		if m.screen == screenLoading {
			m.screen = screenMain
		}
		if !m.pet.Alive {
			m.logf("%s 已经不在了……（RIP）", m.pet.Name)
		}
		return m, nil

	case careResultMsg:
		m.pet = msg.pet
		m.logf("✔ %s", actionLabel(msg.action))
		return m, nil

	case replyMsg:
		m.logf("%s：%s", m.pet.Name, string(msg))
		return m, nil

	case eventMsg:
		return m.onEvent(Event(msg))

	case errMsg:
		return m.onError(msg)
	}
	return m, nil
}

// onKey 分发按键。
func (m model) onKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// 全局退出：ctrl+c 永远退出；q 在非输入态退出。
	if k.Mod == tea.ModCtrl && k.Code == 'c' {
		m.shutdown()
		return m, tea.Quit
	}
	if m.chatMode || m.screen == screenCreate {
		if k.Code == tea.KeyEscape {
			m.chatMode = false
			m.input = ""
			if m.screen == screenCreate && len(m.pets) > 0 {
				m.screen = screenSelect
			}
			return m, nil
		}
	} else if k.Text == "q" {
		m.shutdown()
		return m, tea.Quit
	}

	switch m.screen {
	case screenSelect:
		return m.onSelectKey(k)
	case screenCreate:
		return m.onCreateKey(k)
	case screenMain:
		return m.onMainKey(k)
	case screenOffline:
		if k.Text == "r" {
			m.screen = screenLoading
			m.offline = ""
			return m, loadPetsCmd(m.client)
		}
	}
	return m, nil
}

// onSelectKey 处理选宠界面。
func (m model) onSelectKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case k.Code == tea.KeyUp || k.Text == "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case k.Code == tea.KeyDown || k.Text == "j":
		if m.cursor < len(m.pets)-1 {
			m.cursor++
		}
	case k.Text == "n":
		m.screen = screenCreate
		m.createField = 0
	case k.Code == tea.KeyEnter:
		p := m.pets[m.cursor]
		m.pet = p
		m.petLoading = true
		m.screen = screenMain
		m.logf("见到了 %s！", p.Name)
		return m, tea.Batch(getPetCmd(m.client, p.ID), m.startSSE())
	}
	return m, nil
}

// onCreateKey 处理创建表单。
func (m model) onCreateKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.Code {
	case tea.KeyTab, tea.KeyDown:
		m.createField = min(2, m.createField+1)
		return m, nil
	case tea.KeyUp:
		m.createField = max(0, m.createField-1)
		return m, nil
	case tea.KeyLeft:
		m.cycleOption(-1)
		return m, nil
	case tea.KeyRight:
		m.cycleOption(1)
		return m, nil
	case tea.KeyBackspace:
		if m.createField == 0 && m.createName != "" {
			r := []rune(m.createName)
			m.createName = string(r[:len(r)-1])
		}
		return m, nil
	case tea.KeyEnter:
		if strings.TrimSpace(m.createName) == "" {
			m.logf("先给宠物起个名字吧")
			return m, nil
		}
		m.logf("正在等待 %s 诞生……", m.createName)
		return m, createCmd(m.client, strings.TrimSpace(m.createName),
			speciesOptions[m.createSpeciesIdx], personalityOptions[m.createPersonalityIdx])
	}
	// 名字输入：只接收可打印字符。
	if m.createField == 0 && k.Text != "" {
		m.createName += k.Text
	}
	return m, nil
}

// cycleOption 在创建表单里循环物种/性格选项。
func (m *model) cycleOption(delta int) {
	switch m.createField {
	case 1:
		m.createSpeciesIdx = (m.createSpeciesIdx + delta + len(speciesOptions)) % len(speciesOptions)
	case 2:
		m.createPersonalityIdx = (m.createPersonalityIdx + delta + len(personalityOptions)) % len(personalityOptions)
	}
}

// onMainKey 处理主界面按键。
func (m model) onMainKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// 聊天输入态：按键进输入框。
	if m.chatMode {
		switch k.Code {
		case tea.KeyEnter:
			text := strings.TrimSpace(m.input)
			m.chatMode = false
			m.input = ""
			if text == "" {
				return m, nil
			}
			m.logf("我：%s", text)
			return m, chatCmd(m.client, m.pet.ID, text)
		case tea.KeyBackspace:
			if m.input != "" {
				r := []rune(m.input)
				m.input = string(r[:len(r)-1])
			}
		default:
			m.input += k.Text
		}
		return m, nil
	}

	if !m.pet.Alive {
		if k.Text == "r" {
			return m, getPetCmd(m.client, m.pet.ID)
		}
		return m, nil // 死亡后动作键禁用（q 在全局处理）
	}

	switch k.Text {
	case "f":
		m.action, m.frame = animEat, 0
		return m, careCmd(m.client, m.pet.ID, "feed")
	case "p":
		m.action, m.frame = animPlay, 0
		return m, careCmd(m.client, m.pet.ID, "play")
	case "c":
		m.action, m.frame = animClean, 0
		return m, careCmd(m.client, m.pet.ID, "clean")
	case "s":
		return m, careCmd(m.client, m.pet.ID, "sleep")
	case "w":
		return m, careCmd(m.client, m.pet.ID, "wake")
	case "t":
		m.chatMode = true
		m.input = ""
		return m, nil
	case "r":
		return m, getPetCmd(m.client, m.pet.ID)
	}
	return m, nil
}

// onEvent 处理一条 SSE 事件：进日志 + 同步动画/状态。
func (m model) onEvent(ev Event) (tea.Model, tea.Cmd) {
	switch ev.Type {
	case "_sys": // 客户端自身的连接提示（重连等）
		m.logf("· %s", ev.Message)
		return m, waitEventCmd(m.sseCh)
	}

	m.logf("• %s %s", ev.CreatedAt.Format("15:04"), ev.Message)
	var refresh tea.Cmd
	switch ev.Type {
	case "pet.fell_asleep":
		m.pet.Sleeping = true
		m.action, m.frame = animIdle, 0
	case "pet.woke_up":
		m.pet.Sleeping = false
		m.action, m.frame = animIdle, 0
	case "pet.stage_up":
		m.action, m.frame = animCelebrate, 0
		refresh = getPetCmd(m.client, m.pet.ID)
	case "pet.dead":
		m.pet.Alive = false
		refresh = getPetCmd(m.client, m.pet.ID)
	}
	return m, tea.Batch(waitEventCmd(m.sseCh), refresh)
}

// onError 处理异步错误：连接失败进离线页，业务错误进日志。
func (m model) onError(e errMsg) (tea.Model, tea.Cmd) {
	if e.where == "connect" {
		m.offline = e.err.Error()
		m.screen = screenOffline
		return m, nil
	}
	m.logf("✗ %s", friendlyErr(e.err))
	return m, nil
}

// startSSE 启动 SSE 订阅（断线自动重连），返回等待事件的命令。
func (m *model) startSSE() tea.Cmd {
	if m.sseCh != nil {
		return waitEventCmd(m.sseCh)
	}
	m.sseCh = make(chan Event, 32)
	ctx, cancel := context.WithCancel(context.Background())
	m.sseCancel = cancel
	ch := m.sseCh
	id := m.pet.ID
	go func() {
		defer close(ch)
		for {
			err := m.client.WatchEvents(ctx, id, ch)
			if ctx.Err() != nil {
				return
			}
			select {
			case ch <- Event{Type: "_sys", Message: fmt.Sprintf("事件流断开（%v），%v 后重连…", err, reconnectDur)}:
			case <-ctx.Done():
				return
			}
			select {
			case <-time.After(reconnectDur):
			case <-ctx.Done():
				return
			}
		}
	}()
	return waitEventCmd(ch)
}

// shutdown 释放 SSE 资源（退出时调用）。
func (m *model) shutdown() {
	if m.sseCancel != nil {
		m.sseCancel()
	}
}

// logf 追加一条日志（保留最近 maxLogs 条）。
func (m *model) logf(format string, args ...any) {
	m.logs = append(m.logs, fmt.Sprintf(format, args...))
	if len(m.logs) > maxLogs {
		m.logs = m.logs[len(m.logs)-maxLogs:]
	}
}

// friendlyErr 把错误翻译成日志区友好文案。
func friendlyErr(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		switch ae.Code {
		case "low_energy":
			return "太累了，实在玩不动"
		case "invalid_state":
			return "现在做不了这个（可能在睡觉或没睡）"
		case "pet_dead":
			return "已经没办法回应了……"
		case "not_found":
			return "宠物不存在"
		case "invalid_action":
			return "不认识这个动作"
		default:
			return ae.Message
		}
	}
	return "网络错误：" + err.Error()
}

// actionLabel 是动作的中文名（日志用）。
func actionLabel(action string) string {
	switch action {
	case "feed":
		return "喂食"
	case "play":
		return "玩耍"
	case "clean":
		return "清洁"
	case "sleep":
		return "哄睡"
	case "wake":
		return "叫醒"
	default:
		return action
	}
}
