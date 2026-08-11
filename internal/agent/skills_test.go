package agent

import (
	"context"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/genai"

	adkagent "google.golang.org/adk/v2/agent"
	adkmodel "google.golang.org/adk/v2/model"
	adktool "google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/lalolv/PocketPet/internal/config"
	"github.com/lalolv/PocketPet/internal/llm"
	"github.com/lalolv/PocketPet/internal/petfs"
	"github.com/lalolv/PocketPet/internal/plugin"
)

// fakeModel 是测试用的 model.LLM：记录最近一次请求（指令与工具声明），
// 返回固定文本回复。用于在无 key 环境验证装配结果。
type fakeModel struct {
	reply string

	mu      sync.Mutex
	lastReq *adkmodel.LLMRequest
}

func (f *fakeModel) Name() string { return "fake" }

func (f *fakeModel) GenerateContent(_ context.Context, req *adkmodel.LLMRequest, _ bool) iter.Seq2[*adkmodel.LLMResponse, error] {
	f.mu.Lock()
	f.lastReq = req
	f.mu.Unlock()
	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		yield(&adkmodel.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: f.reply}}},
		}, nil)
	}
}

func (f *fakeModel) instruction() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastReq == nil || f.lastReq.Config == nil || f.lastReq.Config.SystemInstruction == nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range f.lastReq.Config.SystemInstruction.Parts {
		sb.WriteString(p.Text)
	}
	return sb.String()
}

func (f *fakeModel) toolNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	if f.lastReq == nil || f.lastReq.Config == nil {
		return out
	}
	for _, t := range f.lastReq.Config.Tools {
		for _, fd := range t.FunctionDeclarations {
			out = append(out, fd.Name)
		}
	}
	return out
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func writeSkillFile(t *testing.T, dir, name, desc, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSkillMountedAndHotReloaded 验证：全局技能经 skilltoolset 出现在 LLM 请求的
// 指令与工具声明里；运行中新增技能无需重建即可生效（source 每次请求实时读盘）。
func TestSkillMountedAndHotReloaded(t *testing.T) {
	globalDir := t.TempDir()
	writeSkillFile(t, globalDir, "goodnight-ritual", "晚安仪式", "主人说晚安时……")

	env := setup(t)
	p := env.newPet(t, "团团", "quiet")

	fake := &fakeModel{reply: "晚安喵。"}
	env.agent.opts = Options{SkillsDir: globalDir,
		ModelFactory: func(context.Context, llm.Config) (adkmodel.LLM, error) { return fake, nil }}

	if _, err := env.agent.Chat(context.Background(), p.ID, "晚安"); err != nil {
		t.Fatal(err)
	}
	ins := fake.instruction()
	if !strings.Contains(ins, "goodnight-ritual") || !strings.Contains(ins, "load_skill") {
		t.Fatalf("global skill not in instruction:\n%s", ins)
	}
	names := fake.toolNames()
	for _, want := range []string{"sleep", "wake", "remember", "recall", "get_own_status", "list_skills", "load_skill"} {
		if !containsStr(names, want) {
			t.Fatalf("tool %q missing, tools = %v", want, names)
		}
	}

	// 热加载：运行中往全局目录放新技能，下一次对话即在指令里（不重建 runner）。
	writeSkillFile(t, globalDir, "play-fetch", "捡球游戏", "玩捡球时……")
	if _, err := env.agent.Chat(context.Background(), p.ID, "玩球吗"); err != nil {
		t.Fatal(err)
	}
	if ins := fake.instruction(); !strings.Contains(ins, "play-fetch") {
		t.Fatalf("new skill not hot-loaded:\n%s", ins)
	}
}

// TestSkillSetSourcePrecedence 验证私有/全局同名去重（私有优先）与坏技能隔离。
func TestSkillSetSourcePrecedence(t *testing.T) {
	privDir, globalDir := t.TempDir(), t.TempDir()
	writeSkillFile(t, privDir, "same-name", "私有版", "私有正文")
	writeSkillFile(t, globalDir, "same-name", "全局版", "全局正文")
	writeSkillFile(t, globalDir, "global-only", "全局独有", "正文")
	// 坏技能：目录名与 frontmatter 名不匹配
	writeSkillFile(t, globalDir, "broken-dir", "坏技能", "正文") // dir broken-dir vs name... 见下
	// 手工构造名字不匹配的坏技能
	if err := os.WriteFile(filepath.Join(globalDir, "broken-dir", "SKILL.md"),
		[]byte("---\nname: other-name\ndescription: 不匹配\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	src := skillSetSource{roots: []string{privDir, globalDir}}
	fms, err := src.ListFrontmatters(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, fm := range fms {
		names = append(names, fm.Name)
	}
	if len(names) != 2 || !containsStr(names, "same-name") || !containsStr(names, "global-only") {
		t.Fatalf("frontmatters = %v", names)
	}
	ins, err := src.LoadInstructions(context.Background(), "same-name")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ins, "私有正文") {
		t.Fatalf("private skill should win, got %q", ins)
	}
}

// TestMCPToolMounted 验证 AGENT.md 声明的 MCP server 的工具出现在宠物工具集中，
// 且真实可调通：scripted model 发起 get_weather 调用，夹具 server 执行并返回结果。
// （用 go-sdk 内存传输挂进程内夹具 server，无需真实子进程。）
func TestMCPToolMounted(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "团团", "lively")

	// 进程内 MCP server 夹具：一个 get_weather 工具。
	srv := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "0.1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_weather", Description: "查询天气"},
		func(_ context.Context, _ *mcp.CallToolRequest, in struct {
			City string `json:"city"`
		}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "晴天，适合出门"}},
			}, nil, nil
		})
	serverT, clientT := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(context.Background(), serverT) }()

	// AGENT.md 启用 weather server。
	if err := env.fs.Write(p.ID, petfs.FileAgent,
		"---\nprovider: \"\"\nmodel: \"\"\nmcp: weather\n---\n"); err != nil {
		t.Fatal(err)
	}

	// scripted model：第一轮发起工具调用，第二轮汇报结果。
	fake := &scriptedModel{turns: []*adkmodel.LLMResponse{
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "get_weather", Args: map[string]any{"city": "上海"}}},
		}}},
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "外面是晴天，适合出门！"}}}},
	}}
	env.agent.opts = Options{
		MCPServers: []config.MCPServer{{Name: "weather", Command: "unused-in-test"}},
		MCPTransport: func(config.MCPServer) (mcp.Transport, error) {
			return clientT, nil
		},
		ModelFactory: func(context.Context, llm.Config) (adkmodel.LLM, error) { return fake, nil },
	}

	reply, err := env.agent.Chat(context.Background(), p.ID, "今天天气怎么样")
	if err != nil {
		t.Fatal(err)
	}
	// 工具声明出现
	if names := fake.toolNames(); !containsStr(names, "get_weather") {
		t.Fatalf("MCP tool not mounted, tools = %v", names)
	}
	// 第二轮请求里带着夹具 server 的真实执行结果
	if got := fake.lastFunctionResponse(); !strings.Contains(got, "晴天") {
		t.Fatalf("MCP tool result not relayed: %q", got)
	}
	if reply != "外面是晴天，适合出门！" {
		t.Fatalf("reply = %q", reply)
	}
}

// scriptedModel 按脚本逐轮返回响应的 fake model，并记录请求内容。
type scriptedModel struct {
	turns []*adkmodel.LLMResponse

	mu      sync.Mutex
	call    int
	lastReq *adkmodel.LLMRequest
}

func (f *scriptedModel) Name() string { return "scripted" }

func (f *scriptedModel) GenerateContent(_ context.Context, req *adkmodel.LLMRequest, _ bool) iter.Seq2[*adkmodel.LLMResponse, error] {
	f.mu.Lock()
	f.lastReq = req
	i := min(f.call, len(f.turns)-1)
	f.call++
	resp := f.turns[i]
	f.mu.Unlock()
	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		yield(resp, nil)
	}
}

// toolNames 返回最近一次请求中的函数工具声明名。
func (f *scriptedModel) toolNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	if f.lastReq == nil || f.lastReq.Config == nil {
		return out
	}
	for _, t := range f.lastReq.Config.Tools {
		for _, fd := range t.FunctionDeclarations {
			out = append(out, fd.Name)
		}
	}
	return out
}

// lastFunctionResponse 返回最近一次请求中全部函数响应的文本化内容。
func (f *scriptedModel) lastFunctionResponse() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastReq == nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range f.lastReq.Contents {
		if c == nil {
			continue
		}
		for _, part := range c.Parts {
			if part != nil && part.FunctionResponse != nil {
				fmt.Fprintf(&sb, "%v ", part.FunctionResponse.Response)
			}
		}
	}
	return sb.String()
}

// TestSkillsFor 验证技能列表的来源标注：learned / custom / global，同名私有优先。
func TestSkillsFor(t *testing.T) {
	globalDir := t.TempDir()
	writeSkillFile(t, globalDir, "goodnight-ritual", "晚安仪式", "正文")

	env := setup(t)
	p := env.newPet(t, "团团", "quiet")
	env.agent.opts = Options{SkillsDir: globalDir}

	// 私有：一个 learned（梦境沉淀的写法），一个 custom（手工放）
	if err := env.fs.WriteSkill(p.ID, "fetch",
		"---\nname: fetch\ndescription: 捡球\nmetadata:\n  origin: learned\n---\n正文\n"); err != nil {
		t.Fatal(err)
	}
	writeSkillFile(t, filepath.Join(env.fs.PetDir(p.ID), petfs.DirSkills), "roll", "打滚", "正文")

	infos, err := env.agent.SkillsFor(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	bySource := make(map[string]string)
	for _, i := range infos {
		bySource[i.Name] = i.Source
	}
	want := map[string]string{"fetch": "learned", "roll": "custom", "goodnight-ritual": "global"}
	for name, src := range want {
		if bySource[name] != src {
			t.Fatalf("skill %q source = %q, want %q (all = %v)", name, bySource[name], src, infos)
		}
	}
}

// TestExtraToolsFromPlugins 验证 M5 接缝：插件全局工具注入到每只宠物，
// 且工具内 plugin.PetIDOf(ctx) 能拿到当前宠物 ID。
func TestExtraToolsFromPlugins(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "团团", "lively")

	extra, err := functiontool.New(functiontool.Config{
		Name: "whoami_check", Description: "测试工具：回报当前宠物 ID",
	}, func(actx adkagent.Context, _ noArgs) (careResult, error) {
		return careResult{OK: true, Outcome: "当前宠物是 " + plugin.PetIDOf(actx)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	fake := &scriptedModel{turns: []*adkmodel.LLMResponse{
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "whoami_check"}},
		}}},
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "我知道了"}}}},
	}}
	env.agent.opts = Options{
		ExtraTools:   []adktool.Tool{extra},
		ModelFactory: func(context.Context, llm.Config) (adkmodel.LLM, error) { return fake, nil },
	}

	if _, err := env.agent.Chat(context.Background(), p.ID, "看看你是谁"); err != nil {
		t.Fatal(err)
	}
	if !containsStr(fake.toolNames(), "whoami_check") {
		t.Fatalf("extra tool not mounted: %v", fake.toolNames())
	}
	if got := fake.lastFunctionResponse(); !strings.Contains(got, p.ID) {
		t.Fatalf("PetIDOf did not route pet id %q: %q", p.ID, got)
	}
}
