package adventure

// LLM 主题生成器：经 adkx.Ephemeral 一次性调用，要求模型输出 JSON（契约见 docs/08 §7）。
// 输出必须过 validateTheme；失败时附原因重试一次，仍失败则返回错误由调用方降级。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	adkmodel "google.golang.org/adk/v2/model"

	"github.com/lalolv/PocketPet/internal/adkx"
	"github.com/lalolv/PocketPet/internal/llm"
)

// LLMThemer 是用 LLM 生成地图主题的 Themer。
type LLMThemer struct {
	Cfg llm.Config
	// ModelFactory 可选（测试注入假模型）；nil 时用 llm.NewModel。
	ModelFactory func(ctx context.Context, cfg llm.Config) (adkmodel.LLM, error)
}

// themeInstruction 是系统提示：生成规范（docs/08 §3–§7 的浓缩）。
const themeInstruction = `你是一座虚拟宠物探险小岛的地图设计师。根据输入的图结构，为岛屿生成主题，只输出 JSON，不要输出任何其他文字。

输出 JSON 结构：
{"island_name":"岛名","theme":"一句话岛屿主题","locations":[{"id":节点编号,"name":"地点名","description":"一句话描述","zone":"地带","elements":["要素"]}]}

规范：
- island_name：2-8 个汉字，"特征+通名"（岛/屿/礁/渚/沙洲/群岛），呼应主题种子；不用现实地名、人名、数字、现代词汇。
- theme：不超过 30 字，概括全岛基调。
- locations 必须恰好覆盖输入的每个节点 id（0..N-1），一一对应。
- name：2-6 字，全图唯一，呼应所属地带；id=0 是登陆点；宝箱节点（输入给出）的名字可暗示藏宝。
- description：一句话 15-40 字，句号结尾，不换行；客观旁白口吻，不用人称代词；写具体；越深越神秘，危险用暗示不用血腥；人工物须以"遗迹/旧"形式出现。
- zone：必须原样使用输入中该节点下发的地带值。
- elements：从词表【地貌/风景/渊源/环境/气候/生物/危险】中选 1-3 项，至少含前四项之一；"危险"只允许深处带；标注的要素必须在描述中真实出现。
- 相邻（有边相连）地点的地形过渡要符合常识：同地带内地貌直接过渡；跨地带沿"海岸带→内陆带→深处带"递进；洞穴的前驱须是山体岩壁类；水域地形的前驱须有水源；沿边海拔不得骤降。`

// ThemeIsland 实现 Themer：调用 LLM 生成并校验，失败附原因重试一次。
func (t LLMThemer) ThemeIsland(ctx context.Context, req ThemeRequest) (*IslandTheme, error) {
	prompt, err := buildThemePrompt(req)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		p := prompt
		if lastErr != nil {
			p += "\n\n上一次输出的问题：" + lastErr.Error() + "，请修正后重新输出完整 JSON。"
		}
		raw, err := t.run(ctx, p)
		if err != nil {
			return nil, err // 调用失败（网络/超时）不重试，直接走降级
		}
		theme, err := parseTheme(raw)
		if err == nil {
			err = validateTheme(req, theme)
		}
		if err == nil {
			return theme, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("adventure: llm theme invalid after retry: %w", lastErr)
}

// run 发起一次性 LLM 调用（超时由调用方 ctx 控制）。
func (t LLMThemer) run(ctx context.Context, prompt string) (string, error) {
	return adkx.Ephemeral{
		Cfg:           t.Cfg,
		AppName:       "pocketpet-adventure",
		AgentName:     "island-cartographer",
		Description:   "探险小岛地图主题设计师",
		Instruction:   themeInstruction,
		SessionPrefix: "theme",
		Prompt:        prompt,
		ModelFactory:  t.ModelFactory,
	}.Run(ctx)
}

// buildThemePrompt 把请求结构渲染为用户提示（JSON + 要素词表说明）。
func buildThemePrompt(req ThemeRequest) (string, error) {
	type input struct {
		Seed       string    `json:"seed"`
		NodeCount  int       `json:"node_count"`
		ChestNodes []int     `json:"chest_nodes"`
		Edges      []MapEdge `json:"edges"`
		Zones      []string  `json:"zones"`
	}
	body, err := json.Marshal(input{
		Seed: req.Seed, NodeCount: req.NodeCount,
		ChestNodes: req.ChestNodes, Edges: req.Edges, Zones: req.Zones,
	})
	if err != nil {
		return "", err
	}
	return "输入（seed 为主题种子，zones 按下标对应各节点必须使用的地带，edges 为道路 from/to，chest_nodes 为藏宝箱节点）：\n" + string(body), nil
}

// parseTheme 从模型输出提取 JSON 并解析（容忍 ```json 围栏与前后杂散文本）。
func parseTheme(raw string) (*IslandTheme, error) {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("输出中未找到 JSON")
	}
	var t IslandTheme
	if err := json.Unmarshal([]byte(raw[start:end+1]), &t); err != nil {
		return nil, fmt.Errorf("JSON 解析失败: %w", err)
	}
	return &t, nil
}
