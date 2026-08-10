package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lalolv/PocketPet/internal/llm"
)

// TestChatViaOpenAIChatProvider 是 Chat Completions 端点的端到端集成：
// 真实 PetAgent → llm 工厂 → chatmodel → 假端点，
// 走通"对话 + remember 工具回合"，且日记真的落盘。
func TestChatViaOpenAIChatProvider(t *testing.T) {
	// 假 Chat Completions 端点：第一次请求回 tool_call（remember），
	// 带 tool 消息的第二轮回文本。
	var sawSystemWithTools bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		hasToolMsg := false
		for _, m := range body.Messages {
			if m.Role == "tool" {
				hasToolMsg = true
			}
		}
		for _, tl := range body.Tools {
			if tl.Function.Name == "remember" {
				sawSystemWithTools = true
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if hasToolMsg {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"message":       map[string]any{"role": "assistant", "content": "哼，记住啦，主人叫阿洛。"},
					"finish_reason": "stop",
				}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"role": "assistant",
					"tool_calls": []any{map[string]any{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "remember",
							"arguments": `{"fact":"主人叫阿洛"}`,
						},
					}},
				},
				"finish_reason": "tool_calls",
			}},
		})
	}))
	t.Cleanup(srv.Close)

	env := setup(t)
	p := env.newPet(t, "团团", "tsundere")
	// 走生产工厂路径：配置指向假端点。
	env.agent.cfg = llm.Config{
		Model:   "deepseek-chat",
		BaseURL: srv.URL,
		APIKey:  "fake",
	}

	reply, err := env.agent.Chat(context.Background(), p.ID, "记住：我叫阿洛")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "哼，记住啦，主人叫阿洛。" {
		t.Fatalf("reply = %q", reply)
	}
	if !sawSystemWithTools {
		t.Fatal("remember tool declaration not in request")
	}
	// remember 工具真实执行：日记落盘。
	journals, err := env.fs.ListJournals(p.ID)
	if err != nil || len(journals) == 0 {
		t.Fatalf("journals = %v, %v", journals, err)
	}
	content, err := env.fs.ReadJournal(p.ID, journals[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "主人叫阿洛") {
		t.Fatalf("journal:\n%s", content)
	}
}

// TestOpenAIChatProviderStream 验证流式对话路径（ChatStream）。
func TestOpenAIChatProviderStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte(s + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
		write(`data: {"choices":[{"delta":{"content":"我在"},"finish_reason":""}]}`)
		write(`data: {"choices":[{"delta":{"content":"呢"},"finish_reason":""}]}`)
		write(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`)
		write(`data: [DONE]`)
	}))
	t.Cleanup(srv.Close)

	env := setup(t)
	p := env.newPet(t, "团团", "quiet")
	env.agent.cfg = llm.Config{
		Model:   "deepseek-chat",
		BaseURL: srv.URL,
		APIKey:  "fake",
	}

	var chunks []string
	for chunk, err := range env.agent.ChatStream(context.Background(), p.ID, "在吗") {
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) != 2 || chunks[0] != "我在" || chunks[1] != "呢" {
		t.Fatalf("chunks = %v", chunks)
	}
}

// TestLLMConfig 验证 llm.Config 的配置判定与工厂构造。
func TestLLMConfig(t *testing.T) {
	// 无模型名 → 未配置（诚实报错，不猜默认模型）
	cfg := llm.Config{APIKey: "k"}
	if cfg.Configured() {
		t.Fatal("without model should not be configured")
	}
	// 无密钥 → 未配置
	if (llm.Config{Model: "deepseek-chat"}).Configured() {
		t.Fatal("without api key should not be configured")
	}
	// 完整配置可构造
	cfg.Model = "deepseek-chat"
	if !cfg.Configured() {
		t.Fatal("should be configured")
	}
	m, err := llm.NewModel(context.Background(), cfg)
	if err != nil || m == nil {
		t.Fatalf("NewModel = %v, %v", m, err)
	}
	if m.Name() != "deepseek-chat" {
		t.Fatalf("name = %q", m.Name())
	}
	// 未配置 → ErrNotConfigured
	if _, err := llm.NewModel(context.Background(), llm.Config{}); err == nil {
		t.Fatal("unconfigured should error")
	}
}
