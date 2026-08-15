package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// 节奏与容量常量。
const (
	frameDur        = 250 * time.Millisecond // 动画帧间隔
	reconnectDur    = 2 * time.Second        // SSE 断线重连等待
	maxLogs         = 50                     // 日志区保留条数（回看缓冲）
	animActionTicks = 6                      // eat/play/clean 动作帧数
	celebrateTicks  = 8                      // 升级庆祝帧数
)

// screen 是界面状态机。
type screen int

const (
	screenLoading screen = iota // 启动加载中
	screenSelect                // 选宠
	screenCreate                // 创建表单
	screenBirth                 // MetaAgent 诞生剧场
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
	animAdventure // 探险中（循环直到结束）
)

// 创建表单的选项。
var (
	speciesOptions     = []string{"cat", "dog", "blob"}
	personalityOptions = []string{"", "lively", "quiet", "tsundere"}
	personalityLabels  = map[string]string{
		"": "盲盒", "lively": "活泼", "quiet": "安静", "tsundere": "傲娇",
		"genesis": "天生",
	}
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
	birthLog             []string // 诞生剧场旁白/阶段行
	birthReady           bool     // 已收到 genesis.ready，避免重复 get

	// 主界面
	pet         Pet
	action      animAction
	frame       int
	logs        []string
	logOffset   int // 日志回看的向上偏移行数（0 = 贴底看最新）
	chatMode    bool
	input       string
	sseCh       chan Event
	stateCh     chan PetState
	sseCancel   context.CancelFunc
	petLoading  bool // 首次进入主界面尚未拿到状态
	adventuring bool // 探险进行中（动画循环）
	advNode     string
	advIsland   string            // 探险所在岛名（主题化后随事件/run 快照更新）
	advChests   int
	island      string            // 当前岛名（idle 展示用；空 = 未拉取或无地图）
	mapDescs    map[string]string // 当前地图：地点名 → 描述（moved 事件日志附带）

	// 流式聊天
	streaming  bool               // 正在接收回复流
	streamBuf  string             // 已收到的部分回复
	chatCh     chan chatEvent     // 聊天流通道
	chatCancel context.CancelFunc // esc 中断流

	offline string // 离线原因（screenOffline 展示）

	width, height int
}

// NewModel 创建 TUI 模型。
func NewModel(client *Client) model {
	return model{client: client, screen: screenLoading}
}

// 消息类型。
type (
	tickMsg       time.Time   // 动画帧
	petsMsg       []Pet       // 宠物列表
	petMsg        Pet         // 宠物状态（get/care 响应）
	createdMsg    Pet         // 创建成功（旧路径）
	birthStartMsg BirthResult // MetaAgent 诞生开始
	chatEventMsg  chatEvent   // 聊天流事件
	eventMsg      Event       // SSE 事件
	stateMsg      PetState    // SSE 状态快照
	errMsg        errorWrap   // 错误（含来源）
	careResultMsg struct {
		pet    Pet
		action string
		before Stats // 动作前的属性快照（算增量用）
	}
	adventureResultMsg AdventureRun
	adventureStatusMsg AdventureRun
	currentMapMsg      CurrentMap // 当前地图（拉取失败时 IslandName 为空，静默忽略）
)

// chatEvent 是聊天流上的一条消息：文本块、结束（含完整回复）或错误。
type chatEvent struct {
	chunk string // 文本块
	reply string // done 时的完整回复
	done  bool   // 流正常结束
	err   error  // 流失败
}

type errorWrap struct {
	where string
	err   error
}

// Init 实现 tea.Model。
func (m model) Init() tea.Cmd {
	return tea.Batch(loadPetsCmd(m.client), frameTick())
}

func frameTick() tea.Cmd {
	return tea.Tick(frameDur, func(t time.Time) tea.Msg { return tickMsg(t) })
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
		res, err := c.BirthPet(context.Background(), name, species, personality)
		if err != nil {
			return errMsg{"create", err}
		}
		return birthStartMsg(res)
	}
}

// careCmd 执行照顾动作；before 是按键瞬间的属性快照，用于回执日志的增量文案。
func careCmd(c *Client, id, action string, before Stats) tea.Cmd {
	return func() tea.Msg {
		p, err := c.Care(context.Background(), id, action)
		if err != nil {
			return errMsg{"care", err}
		}
		return careResultMsg{pet: p, action: action, before: before}
	}
}

func startAdventureCmd(c *Client, id string) tea.Cmd {
	return func() tea.Msg {
		run, err := c.StartAdventure(context.Background(), id)
		if err != nil {
			return errMsg{"adventure", err}
		}
		return adventureResultMsg(run)
	}
}

func adventureStatusCmd(c *Client, id string) tea.Cmd {
	return func() tea.Msg {
		run, err := c.GetAdventureRun(context.Background(), id)
		if err != nil {
			return nil // 插件未启用等：忽略
		}
		return adventureStatusMsg(run)
	}
}

// currentMapCmd 拉当前探险地图（岛名/地点描述）；插件未启用或无地图时静默忽略。
func currentMapCmd(c *Client) tea.Cmd {
	return func() tea.Msg {
		cm, err := c.GetCurrentMap(context.Background())
		if err != nil {
			return nil
		}
		return currentMapMsg(cm)
	}
}

// startChat 启动流式聊天：goroutine 拉 SSE 写入通道，waitChatCmd 逐条喂回模型。
// 断线不重连（重发会重复扣 LLM 调用），失败时已收到的部分保留进日志。
func (m *model) startChat(text string) tea.Cmd {
	m.streaming = true
	m.streamBuf = ""
	ch := make(chan chatEvent, 16)
	m.chatCh = ch
	ctx, cancel := context.WithCancel(context.Background())
	m.chatCancel = cancel
	go func() {
		defer close(ch)
		send := func(ev chatEvent) {
			select {
			case ch <- ev:
			case <-ctx.Done():
			}
		}
		reply, err := m.client.ChatStream(ctx, m.pet.ID, text, func(chunk string) {
			send(chatEvent{chunk: chunk})
		})
		if err != nil {
			send(chatEvent{err: err})
			return
		}
		send(chatEvent{done: true, reply: reply})
	}()
	return waitChatCmd(ch)
}

// waitChatCmd 从聊天流通道取一条消息；通道关闭时返回 nil（不产生消息）。
func waitChatCmd(ch <-chan chatEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return chatEventMsg(ev)
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

// waitStateCmd 从状态快照通道取一帧；通道关闭时返回 nil（不产生消息）。
func waitStateCmd(ch <-chan PetState) tea.Cmd {
	return func() tea.Msg {
		st, ok := <-ch
		if !ok {
			return nil
		}
		return stateMsg(st)
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
		// 探险动画持续循环，直到 SSE 结束事件清掉。
		if m.action == animAdventure {
			return m, frameTick()
		}
		if m.action != animIdle && m.action != animCelebrate && m.frame >= animActionTicks {
			m.action, m.frame = animIdle, 0
		}
		if m.action == animCelebrate && m.frame >= celebrateTicks {
			m.action, m.frame = animIdle, 0
		}
		return m, frameTick()

	case petsMsg:
		m.pets = msg
		if len(msg) == 0 {
			m.screen = screenCreate
		} else {
			m.screen = screenSelect
			m.cursor = 0
			// 从主界面 esc 返回时，光标落在刚才那只宠物上。
			for i, p := range msg {
				if p.ID == m.pet.ID {
					m.cursor = i
					break
				}
			}
		}
		return m, nil

	case createdMsg:
		m.pet = Pet(msg)
		m.petLoading = false
		m.screen = screenMain
		m.birthReady = false
		m.logf("欢迎来到这个世界，%s！", m.pet.Name)
		return m, m.startSSE()

	case birthStartMsg:
		res := BirthResult(msg)
		// 换宠订阅：停掉旧 SSE（若有）。
		m.stopSSE()
		m.screen = screenBirth
		m.birthReady = false
		m.birthLog = []string{"一颗蛋开始发光……"}
		m.pet = Pet{
			ID: res.ID, Name: m.createName, Species: res.Species,
			Stage: "egg", Alive: true, GenesisStatus: res.GenesisStatus,
		}
		m.petLoading = true
		m.logf("正在孵化 %s（种子 %s）", m.createName, shortSeed(res.Seed))
		return m, m.startSSE()

	case petMsg:
		m.pet = Pet(msg)
		m.petLoading = false
		if m.screen == screenBirth {
			m.screen = screenMain
			m.birthReady = false
			m.logf("欢迎来到这个世界，%s！", m.pet.Name)
		}
		if m.screen == screenLoading {
			m.screen = screenMain
		}
		if !m.pet.Alive {
			m.logf("%s 已经不在了……（RIP）", m.pet.Name)
		}
		return m, tea.Batch(adventureStatusCmd(m.client, m.pet.ID), currentMapCmd(m.client))

	case careResultMsg:
		m.pet = msg.pet
		if d := statDeltaText(msg.before, msg.pet.Stats); d != "" {
			m.logf("✔ %s %s", actionLabel(msg.action), d)
		} else {
			m.logf("✔ %s", actionLabel(msg.action))
		}
		return m, nil

	case adventureResultMsg:
		run := AdventureRun(msg)
		m.applyAdventureRun(run)
		if run.Adventuring {
			m.logf("✔ 出发探险")
		}
		return m, nil

	case adventureStatusMsg:
		m.applyAdventureRun(AdventureRun(msg))
		return m, nil

	case currentMapMsg:
		m.applyCurrentMap(CurrentMap(msg))
		return m, nil

	case chatEventMsg:
		ev := chatEvent(msg)
		if ev.chunk != "" {
			m.streamBuf += ev.chunk
			return m, waitChatCmd(m.chatCh)
		}
		// 流结束（done/err）：定稿回复并退出流式态。
		m.streaming = false
		m.chatCancel = nil
		switch {
		case ev.err != nil:
			if m.streamBuf != "" {
				m.logf("[%s]: %s …", m.pet.Name, m.streamBuf)
			}
			m.logf("✗ %s", friendlyErr(ev.err))
		case ev.reply != "":
			m.logf("[%s]: %s", m.pet.Name, ev.reply)
		case m.streamBuf != "":
			// 中断（esc）或空回复：保留已收到的部分。
			m.logf("[%s]: %s …", m.pet.Name, m.streamBuf)
		}
		m.streamBuf = ""
		return m, nil

	case eventMsg:
		return m.onEvent(Event(msg))

	case stateMsg:
		st := PetState(msg)
		if st.ID == m.pet.ID {
			m.pet.Stage = st.Stage
			m.pet.Sleeping = st.Sleeping
			m.pet.Alive = st.Alive
			m.pet.Activity = st.Activity
			m.pet.Stats = st.Stats
			m.petLoading = false
			// 以服务端活动态为准，避免本地 adventuring 与 sleeping 叠加。
			m.adventuring = st.Activity == "adventuring"
		}
		return m, waitStateCmd(m.stateCh)

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
	} else if k.Code == tea.KeyEscape && m.streaming {
		// 中断聊天流：取消后由 done/err 消息收尾。
		if m.chatCancel != nil {
			m.chatCancel()
		}
		return m, nil
	} else if k.Code == tea.KeyEscape && m.screen == screenMain {
		// 返回选宠列表：停掉当前宠物的事件订阅，重新拉取列表。
		m.stopSSE()
		m.screen = screenSelect
		return m, loadPetsCmd(m.client)
	} else if k.Text == "q" {
		m.shutdown()
		return m, tea.Quit
	}

	switch m.screen {
	case screenSelect:
		return m.onSelectKey(k)
	case screenCreate:
		return m.onCreateKey(k)
	case screenBirth:
		// 诞生中只允许退出；esc 不中断孵化（后台继续）。
		return m, nil
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
		m.stopSSE() // 从主界面返回再进入时，先停掉上一只的订阅
		m.pet = p
		m.petLoading = true
		m.adventuring, m.advNode, m.advChests = false, "", 0
		m.logs, m.logOffset = nil, 0 // 日志随宠物切换清空
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
		m.logf("正在唤醒造物主，孵化 %s……", m.createName)
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
			m.logOffset = 0 // 发送后贴回底部看回复
			m.logf("我：%s", text)
			return m, m.startChat(text)
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

	// 日志翻页：PgUp 回看 / PgDn 回到底部（死亡后也可翻）。
	switch k.Code {
	case tea.KeyPgUp:
		m.logOffset = min(m.logOffset+m.logAreaLines(), max(0, m.logTotalLines()-m.logAreaLines()))
		return m, nil
	case tea.KeyPgDown:
		m.logOffset = max(0, m.logOffset-m.logAreaLines())
		return m, nil
	}

	if !m.pet.Alive {
		if k.Text == "r" {
			return m, getPetCmd(m.client, m.pet.ID)
		}
		return m, nil // 死亡后动作键禁用（q 在全局处理）
	}

	// 翻页键之外的任意操作：日志视口贴回底部，看最新反馈。
	m.logOffset = 0
	switch k.Text {
	case "f":
		m.action, m.frame = animEat, 0
		return m, careCmd(m.client, m.pet.ID, "feed", m.pet.Stats)
	case "p":
		m.action, m.frame = animPlay, 0
		return m, careCmd(m.client, m.pet.ID, "play", m.pet.Stats)
	case "c":
		m.action, m.frame = animClean, 0
		return m, careCmd(m.client, m.pet.ID, "clean", m.pet.Stats)
	case "s":
		return m, careCmd(m.client, m.pet.ID, "sleep", m.pet.Stats)
	case "w":
		return m, careCmd(m.client, m.pet.ID, "wake", m.pet.Stats)
	case "t":
		if m.streaming {
			return m, nil // 流式回复中，忽略新聊天
		}
		m.chatMode = true
		m.input = ""
		return m, nil
	case "a":
		if m.adventuring {
			m.logf("· 还在探险中（%s）", m.advNode)
			return m, nil
		}
		if m.pet.Sleeping {
			m.logf("· 睡觉时没法出门探险")
			return m, nil
		}
		if m.pet.Stats.Energy < alertWarn {
			m.logf("· 太困了，走不动（先睡一觉吧）")
			return m, nil
		}
		m.action, m.frame = animAdventure, 0
		return m, startAdventureCmd(m.client, m.pet.ID)
	case "r":
		return m, tea.Batch(getPetCmd(m.client, m.pet.ID), adventureStatusCmd(m.client, m.pet.ID), currentMapCmd(m.client))
	}
	return m, nil
}

// onEvent 处理一条 SSE 事件：进日志 + 同步动画/标志。
// 数值与阶段由随后的 state 帧刷新（结算/动作后服务端必推），这里不再补发 GET。
func (m model) onEvent(ev Event) (tea.Model, tea.Cmd) {
	if strings.HasPrefix(ev.Type, "genesis.") {
		return m.onGenesisEvent(ev)
	}
	switch ev.Type {
	case "_sys": // 客户端自身的连接提示（重连等）
		m.logf("· %s", ev.Message)
		return m, waitEventCmd(m.sseCh)
	case "pet.proactive", "pet.dream": // 宠物主动说的话，与聊天回复同格式
		m.logf("[%s]: %s", m.pet.Name, ev.Message)
	case "pet.adventure_started":
		m.adventuring = true
		m.action, m.frame = animAdventure, 0
		// 文案为「X 从【出发地】出发去【岛名】探险了」：取第一个【】为地点、第二个为岛名。
		parts := bracketsIn(ev.Message)
		if len(parts) > 0 {
			m.advNode = parts[0]
		}
		if len(parts) > 1 {
			m.advIsland = parts[1]
		}
		m.logf("• %s %s", ev.CreatedAt.Local().Format("15:04"), ev.Message)
	case "pet.adventure_moved":
		m.adventuring = true
		m.action, m.frame = animAdventure, 0 // 打帧相位，避免精灵姿势瞬跳
		if name := nodeNameFromAdventureMsg(ev.Message); name != "" {
			m.advNode = name
		}
		m.logf("• %s %s", ev.CreatedAt.Local().Format("15:04"), ev.Message)
		if desc := m.mapDescs[m.advNode]; desc != "" {
			m.logf("  %s", desc)
		}
	case "pet.adventure_chest":
		m.adventuring = true
		m.action, m.frame = animAdventure, 0
		m.advChests++
		m.logf("★ %s %s", ev.CreatedAt.Local().Format("15:04"), ev.Message)
	case "pet.adventure_finished", "pet.adventure_aborted":
		m.adventuring = false
		m.action, m.frame = animIdle, 0
		m.advNode = ""
		m.advIsland = ""
		m.logf("• %s %s", ev.CreatedAt.Local().Format("15:04"), ev.Message)
		// 探险结束（尤其换图中断）后岛可能已换：顺手拉一次当前地图。
		return m, tea.Batch(waitEventCmd(m.sseCh), currentMapCmd(m.client))
	case "pet.born":
		if m.screen == screenBirth && !m.birthReady {
			m.birthReady = true
			m.appendBirth("破壳了！")
			return m, tea.Batch(waitEventCmd(m.sseCh), getPetCmd(m.client, m.pet.ID))
		}
		m.logf("• %s %s", ev.CreatedAt.Local().Format("15:04"), ev.Message)
	default: // 状态变化通知；落库时间为 UTC，显示前转本地时区
		m.logf("• %s %s", ev.CreatedAt.Local().Format("15:04"), ev.Message)
	}
	switch ev.Type {
	case "pet.fell_asleep":
		m.pet.Sleeping = true
		m.action, m.frame = animIdle, 0
	case "pet.woke_up":
		m.pet.Sleeping = false
		m.action, m.frame = animIdle, 0
	case "pet.stage_up":
		m.action, m.frame = animCelebrate, 0
	case "pet.dead":
		m.pet.Alive = false
	}
	return m, waitEventCmd(m.sseCh)
}

func (m model) onGenesisEvent(ev Event) (tea.Model, tea.Cmd) {
	if line := formatGenesisLine(ev); line != "" {
		m.appendBirth(line)
	}
	cmd := waitEventCmd(m.sseCh)
	if ev.Type == "genesis.ready" && m.screen == screenBirth && !m.birthReady {
		m.birthReady = true
		m.appendBirth("新生命睁开了眼睛。")
		return m, tea.Batch(cmd, getPetCmd(m.client, m.pet.ID))
	}
	return m, cmd
}

func (m *model) appendBirth(line string) {
	m.birthLog = append(m.birthLog, line)
	if len(m.birthLog) > 12 {
		m.birthLog = m.birthLog[len(m.birthLog)-12:]
	}
}

func shortSeed(seed string) string {
	if len(seed) <= 8 {
		return seed
	}
	return seed[:8]
}

// formatGenesisLine 把 genesis.* 事件的 JSON message 收成一行剧场文案。
func formatGenesisLine(ev Event) string {
	var payload map[string]any
	_ = json.Unmarshal([]byte(ev.Message), &payload)
	switch ev.Type {
	case "genesis.started":
		return "造物主落笔……"
	case "genesis.narration":
		if t, ok := payload["text"].(string); ok && t != "" {
			return t
		}
	case "genesis.genes":
		return "✦ 基因觉醒"
	case "genesis.temperament":
		label, _ := payload["label"].(string)
		if label != "" {
			return "✦ 气质成形：" + label
		}
		return "✦ 气质成形"
	case "genesis.appearance":
		return "✦ 外貌显现"
	case "genesis.quirks":
		return "✦ 癖好落定"
	case "genesis.soul":
		return "✦ 灵魂注入"
	case "genesis.stats":
		return "✦ 生命力铺开"
	case "genesis.identity":
		name, _ := payload["name"].(string)
		if name != "" {
			return "✦ 它叫 " + name
		}
		return "✦ 身份确认"
	case "genesis.ready":
		return "✦ 诞生完成"
	case "genesis.failed":
		return "· 造物波折，改用稳妥配方……"
	}
	return ""
}

// onError 处理异步错误：连接失败进离线页，业务错误进日志。
func (m model) onError(e errMsg) (tea.Model, tea.Cmd) {
	if e.where == "connect" {
		m.offline = e.err.Error()
		m.screen = screenOffline
		return m, nil
	}
	if e.where == "adventure" && !m.adventuring {
		m.action, m.frame = animIdle, 0
	}
	m.logf("✗ %s", friendlyErr(e.err))
	return m, nil
}

// stopSSE 停止当前宠物的事件订阅并清空通道（换宠/返回选宠列表时调用）。
func (m *model) stopSSE() {
	if m.sseCancel != nil {
		m.sseCancel()
		m.sseCancel = nil
	}
	m.sseCh = nil
	m.stateCh = nil
}

// startSSE 启动 SSE 订阅（断线自动重连），返回等待事件与状态快照的命令。
func (m *model) startSSE() tea.Cmd {
	if m.sseCh != nil {
		return tea.Batch(waitEventCmd(m.sseCh), waitStateCmd(m.stateCh))
	}
	m.sseCh = make(chan Event, 32)
	m.stateCh = make(chan PetState, 8)
	ctx, cancel := context.WithCancel(context.Background())
	m.sseCancel = cancel
	evCh, stCh := m.sseCh, m.stateCh
	id := m.pet.ID
	go func() {
		defer close(evCh)
		defer close(stCh)
		for {
			err := m.client.WatchEvents(ctx, id, evCh, stCh)
			if ctx.Err() != nil {
				return
			}
			select {
			case evCh <- Event{Type: "_sys", Message: fmt.Sprintf("事件流断开（%v），%v 后重连…", err, reconnectDur)}:
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
	return tea.Batch(waitEventCmd(evCh), waitStateCmd(stCh))
}

// shutdown 释放 SSE 与聊天流资源（退出时调用）。
func (m *model) shutdown() {
	if m.sseCancel != nil {
		m.sseCancel()
	}
	if m.chatCancel != nil {
		m.chatCancel()
	}
}

// applyAdventureRun 根据行程快照同步探险动画状态。
func (m *model) applyAdventureRun(run AdventureRun) {
	m.adventuring = run.Adventuring
	if run.Adventuring {
		m.action = animAdventure
		m.advNode = run.NodeName
		m.advIsland = run.IslandName
		m.advChests = len(run.ChestsFound)
	} else if m.action == animAdventure {
		m.action, m.frame = animIdle, 0
		m.advNode = ""
		m.advIsland = ""
		m.advChests = 0
	}
}

// applyCurrentMap 缓存当前地图：岛名（idle 展示）与地点名 → 描述（moved 日志附带）。
func (m *model) applyCurrentMap(cm CurrentMap) {
	m.mapDescs = make(map[string]string, len(cm.Nodes))
	for _, n := range cm.Nodes {
		if n.Description != "" {
			m.mapDescs[n.Name] = n.Description
		}
	}
	if cm.IslandName != m.island {
		m.island = cm.IslandName
		if cm.IslandName != "" {
			m.logf("· 当前岛屿：%s（%d 个地点 · %d 个宝箱）", cm.IslandName, cm.NodeCount, cm.ChestCount)
		}
	}
}

// bracketsIn 提取文案中所有【…】内容（顺序保留）。
func bracketsIn(msg string) []string {
	var out []string
	rest := msg
	for {
		i := strings.Index(rest, "【")
		if i < 0 {
			break
		}
		j := strings.Index(rest[i+len("【"):], "】")
		if j < 0 {
			break
		}
		out = append(out, rest[i+len("【"):i+len("【")+j])
		rest = rest[i+len("【")+j+len("】"):]
	}
	return out
}

// nodeNameFromAdventureMsg 从「…走到了【地点名】」类文案提取地点。
func nodeNameFromAdventureMsg(msg string) string {
	if parts := bracketsIn(msg); len(parts) > 0 {
		return parts[0]
	}
	return ""
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
		case "already_adventuring":
			return "还在外面探险呢"
		case "no_map":
			return "现在没有探险地图"
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

// statDeltaText 返回照顾动作前后的属性差文案（只列有变化的项），如 "饱食+14 清洁-2 EXP+2"。
func statDeltaText(before, after Stats) string {
	deltas := []struct {
		label string
		d     int
	}{
		{"饱食", after.Hunger - before.Hunger},
		{"心情", after.Happy - before.Happy},
		{"清洁", after.Clean - before.Clean},
		{"精力", after.Energy - before.Energy},
		{"健康", after.Health - before.Health},
		{"EXP", after.EXP - before.EXP},
	}
	var parts []string
	for _, d := range deltas {
		if d.d != 0 {
			parts = append(parts, fmt.Sprintf("%s%+d", d.label, d.d))
		}
	}
	return strings.Join(parts, " ")
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
