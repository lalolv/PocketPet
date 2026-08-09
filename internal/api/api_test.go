package api

import (
	"bufio"
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	adkmodel "google.golang.org/adk/v2/model"

	"pocketpet/internal/agent"
	"pocketpet/internal/llm"
	"pocketpet/internal/pet"
	"pocketpet/internal/petfs"
	"pocketpet/internal/store"
	"pocketpet/internal/tick"
)

var t0 = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

// testEnv 是一套内存态的测试服务（LLM 未配置，chat 走降级文案）。
type testEnv struct {
	srv   *httptest.Server
	clock *pet.FakeClock
}

func setup(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clock := pet.NewFakeClock(t0)
	hub := NewHub()
	fs := petfs.New(filepath.Join(t.TempDir(), "data"))
	engine := tick.NewEngine(st, tick.MultiSink{hub, agent.NewStageSync(fs, st)}, time.Minute, 24*time.Hour, clock)
	ag := agent.New(engine, fs, llm.ProviderConfig{})
	srv := httptest.NewServer(NewServer(st, engine, hub, fs, ag).Handler())
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv, clock: clock}
}

func doJSON(t *testing.T, method, url, body string) (int, map[string]any) {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response (status %d): %v", resp.StatusCode, err)
	}
	return resp.StatusCode, out
}

func createPet(t *testing.T, env *testEnv) string {
	t.Helper()
	status, body := doJSON(t, "POST", env.srv.URL+"/v1/pets", `{"name":"团团","species":"cat"}`)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, body = %v", status, body)
	}
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("no id in response: %v", body)
	}
	return id
}

func statsOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	s, ok := body["stats"].(map[string]any)
	if !ok {
		t.Fatalf("no stats in body: %v", body)
	}
	return s
}

func TestCreateCareGetFlow(t *testing.T) {
	env := setup(t)
	id := createPet(t, env)

	// feed：Hunger 70→90，EXP+2
	status, body := doJSON(t, "POST", env.srv.URL+"/v1/pets/"+id+"/care", `{"action":"feed"}`)
	if status != http.StatusOK {
		t.Fatalf("care status = %d, body = %v", status, body)
	}
	s := statsOf(t, body)
	if s["hunger"] != 90.0 || s["exp"] != 2.0 {
		t.Fatalf("stats after feed = %v", s)
	}

	// play：Happy 80→95，Energy 100→90，Hunger 90→85
	status, body = doJSON(t, "POST", env.srv.URL+"/v1/pets/"+id+"/care", `{"action":"play"}`)
	if status != http.StatusOK {
		t.Fatalf("care status = %d, body = %v", status, body)
	}
	s = statsOf(t, body)
	if s["happy"] != 95.0 || s["energy"] != 90.0 || s["hunger"] != 85.0 {
		t.Fatalf("stats after play = %v", s)
	}

	// GET 单只：确认状态落库
	status, body = doJSON(t, "GET", env.srv.URL+"/v1/pets/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("get status = %d", status)
	}
	if got := statsOf(t, body); got["exp"] != 5.0 {
		t.Fatalf("persisted stats = %v", got)
	}

	// 列表
	status, body = doJSON(t, "GET", env.srv.URL+"/v1/pets", "")
	if status != http.StatusOK {
		t.Fatalf("list status = %d", status)
	}
	if pets, _ := body["pets"].([]any); len(pets) != 1 {
		t.Fatalf("list = %v", body)
	}
}

func TestInvalidActionReturns4xx(t *testing.T) {
	env := setup(t)
	id := createPet(t, env)

	status, body := doJSON(t, "POST", env.srv.URL+"/v1/pets/"+id+"/care", `{"action":"dance"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj["code"] != codeInvalidAction {
		t.Fatalf("error body = %v", body)
	}
}

func TestStateConflictReturns4xx(t *testing.T) {
	env := setup(t)
	id := createPet(t, env)

	// 睡着后 play 被拒（409 invalid_state）
	if status, _ := doJSON(t, "POST", env.srv.URL+"/v1/pets/"+id+"/care", `{"action":"sleep"}`); status != http.StatusOK {
		t.Fatalf("sleep status = %d", status)
	}
	status, body := doJSON(t, "POST", env.srv.URL+"/v1/pets/"+id+"/care", `{"action":"play"}`)
	if status != http.StatusConflict {
		t.Fatalf("play-while-sleeping status = %d, want 409", status)
	}
	if errObj, _ := body["error"].(map[string]any); errObj["code"] != codeInvalidState {
		t.Fatalf("error body = %v", body)
	}
}

func TestNotFoundAndBadBody(t *testing.T) {
	env := setup(t)

	status, body := doJSON(t, "GET", env.srv.URL+"/v1/pets/nope", "")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if errObj, _ := body["error"].(map[string]any); errObj["code"] != codeNotFound {
		t.Fatalf("error body = %v", body)
	}

	status, _ = doJSON(t, "POST", env.srv.URL+"/v1/pets", `{"name":""}`)
	if status != http.StatusBadRequest {
		t.Fatalf("empty create status = %d, want 400", status)
	}
}

// TestChatFallback 验证 M2 的对话端点：未配置 LLM 时 chat 返回 200 + 降级文案，
// 而不是 M1 的 501 占位。
func TestChatFallback(t *testing.T) {
	env := setup(t)
	id := createPet(t, env)

	status, body := doJSON(t, "POST", env.srv.URL+"/v1/pets/"+id+"/chat", `{"message":"你好"}`)
	if status != http.StatusOK {
		t.Fatalf("chat status = %d, body = %v", status, body)
	}
	reply, _ := body["reply"].(string)
	if reply == "" {
		t.Fatalf("empty reply: %v", body)
	}

	// 不存在的宠物仍走 404。
	status, body = doJSON(t, "POST", env.srv.URL+"/v1/pets/nope/chat", `{"message":"hi"}`)
	if status != http.StatusNotFound {
		t.Fatalf("chat-unknown-pet status = %d, want 404", status)
	}
	if errObj, _ := body["error"].(map[string]any); errObj["code"] != codeNotFound {
		t.Fatalf("error body = %v", body)
	}

	// 空消息 400。
	status, _ = doJSON(t, "POST", env.srv.URL+"/v1/pets/"+id+"/chat", `{"message":"  "}`)
	if status != http.StatusBadRequest {
		t.Fatalf("empty-message status = %d, want 400", status)
	}
}

// TestChatStream 验证 chat 的 SSE 变体：收到若干 chunk 事件后以 done 收尾，
// done 里的完整回复等于各 chunk 的拼接。
func TestChatStream(t *testing.T) {
	env := setup(t)
	id := createPet(t, env)

	resp, err := http.Post(env.srv.URL+"/v1/pets/"+id+"/chat?stream=true", "application/json",
		strings.NewReader(`{"message":"在吗"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %v", ct)
	}

	var chunks []string
	var doneReply string
	lines := bufio.NewReader(resp.Body)
	var event string
	for {
		line, err := lines.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE: %v (chunks=%v)", err, chunks)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data := strings.TrimPrefix(line, "data: ")
			var payload map[string]string
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				t.Fatalf("bad SSE data %q: %v", data, err)
			}
			switch event {
			case "chunk":
				chunks = append(chunks, payload["text"])
			case "done":
				doneReply = payload["reply"]
			case "error":
				t.Fatalf("stream error event: %v", payload)
			}
		case line == "":
			if event == "done" {
				if len(chunks) == 0 {
					t.Fatal("no chunk events before done")
				}
				if doneReply != strings.Join(chunks, "") {
					t.Fatalf("done reply %q != joined chunks %q", doneReply, strings.Join(chunks, ""))
				}
				return
			}
		}
	}
}

// TestCreateWithPersonality 验证创建时指定性格模板（中文别名），
// 响应与 soul 端点都能反映模板；不认识的模板名报 400。
func TestCreateWithPersonality(t *testing.T) {
	env := setup(t)

	status, body := doJSON(t, "POST", env.srv.URL+"/v1/pets", `{"name":"球球","species":"dog","personality":"傲娇"}`)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, body = %v", status, body)
	}
	if body["personality"] != "tsundere" {
		t.Fatalf("personality = %v", body["personality"])
	}
	id, _ := body["id"].(string)

	status, body = doJSON(t, "GET", env.srv.URL+"/v1/pets/"+id+"/soul", "")
	if status != http.StatusOK {
		t.Fatalf("soul status = %d, body = %v", status, body)
	}
	if body["template"] != "tsundere" {
		t.Fatalf("soul template = %v", body["template"])
	}
	content, _ := body["content"].(string)
	if !strings.Contains(content, "template: tsundere") {
		t.Fatalf("soul content missing template frontmatter: %q", content)
	}

	// 单只查询也带 personality。
	status, body = doJSON(t, "GET", env.srv.URL+"/v1/pets/"+id, "")
	if status != http.StatusOK || body["personality"] != "tsundere" {
		t.Fatalf("get = %d %v", status, body)
	}

	// 未知模板 → 400。
	status, body = doJSON(t, "POST", env.srv.URL+"/v1/pets", `{"name":"x","species":"cat","personality":"暴躁"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("unknown personality status = %d, want 400, body = %v", status, body)
	}
}

// TestCreateDefaults 验证缺省字段的合理默认：没起名按物种给一个，没选性格随机一个。
func TestCreateDefaults(t *testing.T) {
	env := setup(t)
	status, body := doJSON(t, "POST", env.srv.URL+"/v1/pets", `{"species":"cat"}`)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, body = %v", status, body)
	}
	if body["name"] != "小咪" {
		t.Fatalf("default name = %v", body["name"])
	}
	if per, _ := body["personality"].(string); per == "" {
		t.Fatalf("personality should be randomly assigned, body = %v", body)
	}
}

// TestMemoryEndpoint 验证 memory 端点：初始为空长期记忆 + 空日记列表。
func TestMemoryEndpoint(t *testing.T) {
	env := setup(t)
	id := createPet(t, env)

	status, body := doJSON(t, "GET", env.srv.URL+"/v1/pets/"+id+"/memory", "")
	if status != http.StatusOK {
		t.Fatalf("memory status = %d, body = %v", status, body)
	}
	if mem, _ := body["memory"].(string); !strings.Contains(mem, "长期记忆") {
		t.Fatalf("memory content = %q", mem)
	}
	if journals, _ := body["journals"].([]any); len(journals) != 0 {
		t.Fatalf("journals = %v", body["journals"])
	}

	// 不存在的宠物 404。
	status, _ = doJSON(t, "GET", env.srv.URL+"/v1/pets/nope/memory", "")
	if status != http.StatusNotFound {
		t.Fatalf("memory-unknown-pet status = %d, want 404", status)
	}
}

func TestHealthz(t *testing.T) {
	env := setup(t)
	status, body := doJSON(t, "GET", env.srv.URL+"/healthz", "")
	if status != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("healthz = %d %v", status, body)
	}
}

// TestSSEReplayAndLive 验证 SSE：连接后先回放 pet.born，随后能收到实时推送的事件。
func TestSSEReplayAndLive(t *testing.T) {
	env := setup(t)
	id := createPet(t, env)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", env.srv.URL+"/v1/pets/"+id+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %v", ct)
	}
	lines := bufio.NewReader(resp.Body)
	readEvent := func() (typ, data string) {
		t.Helper()
		for {
			line, err := lines.ReadString('\n')
			if err != nil {
				t.Fatalf("read SSE: %v", err)
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case strings.HasPrefix(line, "event: "):
				typ = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				return typ, strings.TrimPrefix(line, "data: ")
			}
		}
	}

	// 回放：创建时的 pet.born
	typ, _ := readEvent()
	if typ != pet.EventBorn {
		t.Fatalf("replayed event = %q, want pet.born", typ)
	}

	// 实时推送：推进假时钟 12h（饱食度 70→10 跌破阈值），
	// GET 触发即时结算 → pet.hungry 经 hub 推送
	env.clock.Advance(12 * time.Hour)
	if status, _ := doJSON(t, "GET", env.srv.URL+"/v1/pets/"+id, ""); status != http.StatusOK {
		t.Fatalf("get status = %d", status)
	}
	typ, data := readEvent()
	if typ != pet.EventHungry {
		t.Fatalf("live event = %q, want pet.hungry", typ)
	}
	if !strings.Contains(data, `"type":"pet.hungry"`) {
		t.Fatalf("live data = %s", data)
	}
}

// TestSoulLock 验证 SOUL 锁定端点与 soul 响应中的 locked/history 字段（M3）。
func TestSoulLock(t *testing.T) {
	env := setup(t)
	id := createPet(t, env)

	// 初始：未锁定、无演化历史
	status, body := doJSON(t, "GET", env.srv.URL+"/v1/pets/"+id+"/soul", "")
	if status != http.StatusOK {
		t.Fatalf("soul status = %d", status)
	}
	if body["locked"] != false {
		t.Fatalf("locked = %v", body["locked"])
	}
	if hist, _ := body["history"].([]any); len(hist) != 0 {
		t.Fatalf("history = %v", body["history"])
	}

	// 锁定
	status, body = doJSON(t, "POST", env.srv.URL+"/v1/pets/"+id+"/soul/lock", `{"locked":true}`)
	if status != http.StatusOK || body["locked"] != true {
		t.Fatalf("lock = %d %v", status, body)
	}
	status, body = doJSON(t, "GET", env.srv.URL+"/v1/pets/"+id+"/soul", "")
	if status != http.StatusOK || body["locked"] != true {
		t.Fatalf("soul after lock = %d %v", status, body)
	}

	// 解锁
	status, body = doJSON(t, "POST", env.srv.URL+"/v1/pets/"+id+"/soul/lock", `{"locked":false}`)
	if status != http.StatusOK || body["locked"] != false {
		t.Fatalf("unlock = %d %v", status, body)
	}

	// 不存在的宠物 404；坏 body 400
	status, _ = doJSON(t, "POST", env.srv.URL+"/v1/pets/nope/soul/lock", `{"locked":true}`)
	if status != http.StatusNotFound {
		t.Fatalf("lock-unknown-pet status = %d, want 404", status)
	}
	status, _ = doJSON(t, "POST", env.srv.URL+"/v1/pets/"+id+"/soul/lock", `not json`)
	if status != http.StatusBadRequest {
		t.Fatalf("bad-body lock status = %d, want 400", status)
	}
}

// setupOpts 是带 agent 装配选项的测试服务（M4：技能目录 / fake model 等）。
func setupOpts(t *testing.T, opts agent.Options) *testEnv {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clock := pet.NewFakeClock(t0)
	hub := NewHub()
	fs := petfs.New(filepath.Join(t.TempDir(), "data"))
	engine := tick.NewEngine(st, tick.MultiSink{hub, agent.NewStageSync(fs, st)}, time.Minute, 24*time.Hour, clock)
	ag := agent.New(engine, fs, llm.ProviderConfig{}, opts)
	srv := httptest.NewServer(NewServer(st, engine, hub, fs, ag).Handler())
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv, clock: clock}
}

// TestSkillsEndpoint 验证技能列表端点：全局技能标注 global，私有沉淀标注 learned。
func TestSkillsEndpoint(t *testing.T) {
	globalDir := t.TempDir()
	if err := writeTestSkill(globalDir, "goodnight-ritual", "晚安仪式"); err != nil {
		t.Fatal(err)
	}
	env := setupOpts(t, agent.Options{SkillsDir: globalDir})
	id := createPet(t, env)

	status, body := doJSON(t, "GET", env.srv.URL+"/v1/pets/"+id+"/skills", "")
	if status != http.StatusOK {
		t.Fatalf("skills status = %d, body = %v", status, body)
	}
	list, _ := body["skills"].([]any)
	if len(list) != 1 {
		t.Fatalf("skills = %v", body)
	}
	first, _ := list[0].(map[string]any)
	if first["name"] != "goodnight-ritual" || first["source"] != "global" {
		t.Fatalf("skill entry = %v", first)
	}

	status, _ = doJSON(t, "GET", env.srv.URL+"/v1/pets/nope/skills", "")
	if status != http.StatusNotFound {
		t.Fatalf("skills-unknown-pet status = %d, want 404", status)
	}
}

func writeTestSkill(dir, name, desc string) error {
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name, "SKILL.md"),
		[]byte("---\nname: "+name+"\ndescription: "+desc+"\n---\n正文\n"), 0o644)
}

// TestA2ACard 验证 agent card 可发现（无需 LLM）。
func TestA2ACard(t *testing.T) {
	env := setup(t)
	id := createPet(t, env)

	status, body := doJSON(t, "GET", env.srv.URL+"/a2a/pets/"+id+"/.well-known/agent-card.json", "")
	if status != http.StatusOK {
		t.Fatalf("card status = %d, body = %v", status, body)
	}
	if body["name"] != "团团" {
		t.Fatalf("card name = %v", body["name"])
	}
	ifaces, _ := body["supportedInterfaces"].([]any)
	if len(ifaces) != 1 {
		t.Fatalf("interfaces = %v", body)
	}
	iface, _ := ifaces[0].(map[string]any)
	if url, _ := iface["url"].(string); !strings.HasSuffix(url, "/a2a/pets/"+id) {
		t.Fatalf("interface url = %v", iface["url"])
	}

	status, _ = doJSON(t, "GET", env.srv.URL+"/a2a/pets/nope/.well-known/agent-card.json", "")
	if status != http.StatusNotFound {
		t.Fatalf("card-unknown-pet status = %d, want 404", status)
	}
}

// TestA2AMessageNoLLM 未配置 LLM 时消息端点返回 503 llm_not_configured（chat 走降级，A2A 不走）。
func TestA2AMessageNoLLM(t *testing.T) {
	env := setup(t)
	id := createPet(t, env)

	status, body := doJSON(t, "POST", env.srv.URL+"/a2a/pets/"+id+"/message:send",
		`{"message":{"messageId":"m1","role":"user","parts":[{"text":"你好"}]}}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body = %v", status, body)
	}
	if errObj, _ := body["error"].(map[string]any); errObj["code"] != codeLLMMissing {
		t.Fatalf("error body = %v", body)
	}
}

// TestA2AMessageRoundTrip 用 fake model 验证 A2A 消息往返（无 key 环境）。
func TestA2AMessageRoundTrip(t *testing.T) {
	env := setupOpts(t, agent.Options{
		ModelFactory: func(context.Context, llm.ProviderConfig) (adkmodel.LLM, error) {
			return &a2aFakeModel{reply: "我是团团，你好呀！"}, nil
		},
	})
	id := createPet(t, env)

	status, body := doJSON(t, "POST", env.srv.URL+"/a2a/pets/"+id+"/message:send",
		`{"message":{"messageId":"m1","role":"user","parts":[{"text":"你好"}]}}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %v", status, body)
	}
	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), "我是团团") {
		t.Fatalf("reply not in a2a response: %s", raw)
	}
}

// a2aFakeModel 返回固定文本的 model.LLM。
type a2aFakeModel struct{ reply string }

func (f *a2aFakeModel) Name() string { return "fake" }
func (f *a2aFakeModel) GenerateContent(_ context.Context, _ *adkmodel.LLMRequest, _ bool) iter.Seq2[*adkmodel.LLMResponse, error] {
	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		yield(&adkmodel.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: f.reply}}},
		}, nil)
	}
}

// TestListIncludesPersonality 验证列表端点也带性格模板（与单只查询一致）。
func TestListIncludesPersonality(t *testing.T) {
	env := setup(t)
	status, body := doJSON(t, "POST", env.srv.URL+"/v1/pets", `{"name":"球球","species":"dog","personality":"傲娇"}`)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d", status)
	}
	status, body = doJSON(t, "GET", env.srv.URL+"/v1/pets", "")
	if status != http.StatusOK {
		t.Fatalf("list status = %d", status)
	}
	pets, _ := body["pets"].([]any)
	if len(pets) != 1 {
		t.Fatalf("pets = %v", body)
	}
	first, _ := pets[0].(map[string]any)
	if first["personality"] != "tsundere" {
		t.Fatalf("list personality = %v", first["personality"])
	}
}
