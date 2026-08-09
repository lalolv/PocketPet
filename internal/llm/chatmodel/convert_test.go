package chatmodel

import (
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
)

// collect 耗尽一个响应序列。
func collect(t *testing.T, seq iter.Seq2[*model.LLMResponse, error]) ([]*model.LLMResponse, error) {
	t.Helper()
	var out []*model.LLMResponse
	for resp, err := range seq {
		if err != nil {
			return out, err
		}
		out = append(out, resp)
	}
	return out, nil
}

// fakeEndpoint 记录最后一次请求体，并按 handler 给的响应回复。
type fakeEndpoint struct {
	srv *httptest.Server

	lastBody   map[string]any
	lastHeader http.Header
}

func newFakeEndpoint(t *testing.T, handler http.HandlerFunc) *fakeEndpoint {
	t.Helper()
	fe := &fakeEndpoint{}
	fe.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fe.lastHeader = r.Header
		if err := json.NewDecoder(r.Body).Decode(&fe.lastBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		handler(w, r)
	}))
	t.Cleanup(fe.srv.Close)
	return fe
}

func (fe *fakeEndpoint) model(t *testing.T) *Model {
	t.Helper()
	m, err := NewModel(context.Background(), "deepseek-chat", &ClientConfig{APIKey: "test-key", BaseURL: fe.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return m.(*Model)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// completion 构造一个非流式响应。
func completion(content, finishReason string) map[string]any {
	return map[string]any{
		"choices": []any{map[string]any{
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": finishReason,
		}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	}
}

// TestNonStreamText 验证非流式文本：请求结构（system+user、模型名、无 stream）、
// 响应文本与 usage 映射。
func TestNonStreamText(t *testing.T) {
	fe := newFakeEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, completion("你好，主人", "stop"))
	})

	req := &model.LLMRequest{
		Config: &genai.GenerateContentConfig{
			SystemInstruction: &genai.Content{Role: "user", Parts: []*genai.Part{{Text: "你是一只猫"}}},
		},
		Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "你好"}}}},
	}
	resps, err := collect(t, fe.model(t).GenerateContent(context.Background(), req, false))
	if err != nil {
		t.Fatal(err)
	}
	if len(resps) != 1 {
		t.Fatalf("responses = %d", len(resps))
	}
	got := resps[0].Content.Parts[0].Text
	if got != "你好，主人" {
		t.Fatalf("text = %q", got)
	}
	if resps[0].FinishReason != genai.FinishReasonStop {
		t.Fatalf("finish = %v", resps[0].FinishReason)
	}
	if resps[0].UsageMetadata == nil || resps[0].UsageMetadata.TotalTokenCount != 15 {
		t.Fatalf("usage = %+v", resps[0].UsageMetadata)
	}

	// 请求体结构
	if fe.lastHeader.Get("Authorization") != "Bearer test-key" {
		t.Fatalf("auth = %q", fe.lastHeader.Get("Authorization"))
	}
	if fe.lastBody["model"] != "deepseek-chat" {
		t.Fatalf("model = %v", fe.lastBody["model"])
	}
	if _, ok := fe.lastBody["stream"]; ok && fe.lastBody["stream"] == true {
		t.Fatalf("stream should be false/omitted: %v", fe.lastBody["stream"])
	}
	msgs, _ := fe.lastBody["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v", msgs)
	}
	m0, _ := msgs[0].(map[string]any)
	m1, _ := msgs[1].(map[string]any)
	if m0["role"] != "system" || m0["content"] != "你是一只猫" {
		t.Fatalf("system msg = %v", m0)
	}
	if m1["role"] != "user" || m1["content"] != "你好" {
		t.Fatalf("user msg = %v", m1)
	}
}

// TestToolRound 验证工具回合：声明映射、tool_calls 响应转 function_call、
// function_response 回填 tool 消息（tool_call_id 对应）。
func TestToolRound(t *testing.T) {
	var sawTools bool
	fe := newFakeEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		if !sawTools {
			sawTools = true
			writeJSON(w, map[string]any{
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
			return
		}
		writeJSON(w, completion("记住了", "stop"))
	})
	m := fe.model(t)

	// 第一回合：带工具声明
	req := &model.LLMRequest{
		Config: &genai.GenerateContentConfig{
			Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{
				Name:        "remember",
				Description: "记事",
				ParametersJsonSchema: map[string]any{
					"type":       "object",
					"properties": map[string]any{"fact": map[string]any{"type": "string"}},
					"required":   []any{"fact"},
				},
			}}}},
		},
		Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "记住：主人叫阿洛"}}}},
	}
	resps, err := collect(t, m.GenerateContent(context.Background(), req, false))
	if err != nil {
		t.Fatal(err)
	}
	fc := resps[0].Content.Parts[0].FunctionCall
	if fc == nil || fc.ID != "call_1" || fc.Name != "remember" || fc.Args["fact"] != "主人叫阿洛" {
		t.Fatalf("function call = %+v", fc)
	}
	if resps[0].FinishReason != genai.FinishReasonStop {
		t.Fatalf("finish = %v", resps[0].FinishReason)
	}

	// 请求体里的 tools 结构
	tools, _ := fe.lastBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", tools)
	}
	tool0, _ := tools[0].(map[string]any)
	fn, _ := tool0["function"].(map[string]any)
	if tool0["type"] != "function" || fn["name"] != "remember" {
		t.Fatalf("tool = %v", tool0)
	}
	params, _ := fn["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Fatalf("parameters = %v", params)
	}

	// 第二回合：历史带 function_call 与 function_response
	req2 := &model.LLMRequest{Contents: []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "记住：主人叫阿洛"}}},
		{Role: "model", Parts: []*genai.Part{{FunctionCall: fc}}},
		{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			ID: "call_1", Name: "remember", Response: map[string]any{"ok": true, "outcome": "已记进日记"},
		}}}},
	}}
	if _, err := collect(t, m.GenerateContent(context.Background(), req2, false)); err != nil {
		t.Fatal(err)
	}
	msgs, _ := fe.lastBody["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %v", msgs)
	}
	assistant, _ := msgs[1].(map[string]any)
	tcs, _ := assistant["tool_calls"].([]any)
	tc0, _ := tcs[0].(map[string]any)
	tcFn, _ := tc0["function"].(map[string]any)
	if tc0["id"] != "call_1" || tcFn["name"] != "remember" || !strings.Contains(tcFn["arguments"].(string), "阿洛") {
		t.Fatalf("assistant tool_call = %v", tc0)
	}
	toolMsg, _ := msgs[2].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_1" || toolMsg["name"] != "remember" {
		t.Fatalf("tool msg = %v", toolMsg)
	}
	if !strings.Contains(toolMsg["content"].(string), "已记进日记") {
		t.Fatalf("tool content = %v", toolMsg["content"])
	}
}

// TestSynthesizedCallIDs 验证无 ID 的 function_call/response 配对：合成 ID + 最老 pending。
func TestSynthesizedCallIDs(t *testing.T) {
	fe := newFakeEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, completion("ok", "stop"))
	})
	m := fe.model(t)

	req := &model.LLMRequest{Contents: []*genai.Content{
		{Role: "model", Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "eat"}},
			{FunctionCall: &genai.FunctionCall{Name: "play"}},
		}},
		{Role: "user", Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{Name: "eat", Response: map[string]any{"ok": true}}},
			{FunctionResponse: &genai.FunctionResponse{Name: "play", Response: map[string]any{"ok": true}}},
		}},
	}}
	if _, err := collect(t, m.GenerateContent(context.Background(), req, false)); err != nil {
		t.Fatal(err)
	}
	msgs, _ := fe.lastBody["messages"].([]any)
	toolMsg1, _ := msgs[1].(map[string]any)
	toolMsg2, _ := msgs[2].(map[string]any)
	if toolMsg1["tool_call_id"] != "adk-chat-call-0" || toolMsg1["name"] != "eat" {
		t.Fatalf("tool msg1 = %v", toolMsg1)
	}
	if toolMsg2["tool_call_id"] != "adk-chat-call-1" || toolMsg2["name"] != "play" {
		t.Fatalf("tool msg2 = %v", toolMsg2)
	}
}

// TestStreamText 验证流式文本：增量 partial + 最终聚合。
func TestStreamText(t *testing.T) {
	fe := newFakeEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte(s + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
		write(`data: {"choices":[{"delta":{"role":"assistant"},"finish_reason":""}]}`)
		write(`data: {"choices":[{"delta":{"content":"你好"},"finish_reason":""}]}`)
		write(`data: {"choices":[{"delta":{"content":"，主人"},"finish_reason":""}]}`)
		write(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`)
		write(`data: [DONE]`)
	})

	resps, err := collect(t, fe.model(t).GenerateContent(context.Background(),
		&model.LLMRequest{Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "hi"}}}}}, true))
	if err != nil {
		t.Fatal(err)
	}
	if fe.lastBody["stream"] != true {
		t.Fatalf("stream field = %v", fe.lastBody["stream"])
	}
	var partials []string
	var final *model.LLMResponse
	for _, r := range resps {
		if r.Partial {
			partials = append(partials, r.Content.Parts[0].Text)
		} else {
			final = r
		}
	}
	if len(partials) != 2 || partials[0] != "你好" || partials[1] != "，主人" {
		t.Fatalf("partials = %v", partials)
	}
	if final == nil || !final.TurnComplete {
		t.Fatalf("final = %+v", final)
	}
	if final.Content.Parts[0].Text != "你好，主人" {
		t.Fatalf("final text = %q", final.Content.Parts[0].Text)
	}
	if final.FinishReason != genai.FinishReasonStop {
		t.Fatalf("final finish = %v", final.FinishReason)
	}
}

// TestStreamToolCalls 验证流式 tool_calls 分片按 index 拼合。
func TestStreamToolCalls(t *testing.T) {
	fe := newFakeEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		write := func(s string) { _, _ = w.Write([]byte(s + "\n\n")) }
		write(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"eat"}}]},"finish_reason":""}]}`)
		write(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":"}}]},"finish_reason":""}]}`)
		write(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":"tool_calls"}]}`)
		write(`data: [DONE]`)
	})

	resps, err := collect(t, fe.model(t).GenerateContent(context.Background(),
		&model.LLMRequest{Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "吃"}}}}}, true))
	if err != nil {
		t.Fatal(err)
	}
	final := resps[len(resps)-1]
	fc := final.Content.Parts[0].FunctionCall
	if fc == nil || fc.ID != "call_9" || fc.Name != "eat" {
		t.Fatalf("function call = %+v", fc)
	}
	if fc.Args["a"] != 1.0 {
		t.Fatalf("args = %v", fc.Args)
	}
}

// TestHTTPError 验证 HTTP 错误不静默吞掉。
func TestHTTPError(t *testing.T) {
	fe := newFakeEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		writeJSON(w, map[string]any{"error": map[string]any{"message": "rate limited"}})
	})
	_, err := collect(t, fe.model(t).GenerateContent(context.Background(),
		&model.LLMRequest{Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "hi"}}}}}, false))
	if err == nil || !strings.Contains(err.Error(), "rate limited") || !strings.Contains(err.Error(), "429") {
		t.Fatalf("err = %v", err)
	}
}

// TestGenerationConfig 验证生成参数映射与不支持参数的忽略。
func TestGenerationConfig(t *testing.T) {
	fe := newFakeEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, completion("ok", "stop"))
	})
	temp := float32(0.7)
	topP := float32(0.9)
	topK := float32(40)
	req := &model.LLMRequest{
		Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "hi"}}}},
		Config: &genai.GenerateContentConfig{
			Temperature:      &temp,
			TopP:             &topP,
			TopK:             &topK, // 应被忽略
			MaxOutputTokens:  128,
			StopSequences:    []string{"###"},
			ResponseMIMEType: "application/json",
		},
	}
	if _, err := collect(t, fe.model(t).GenerateContent(context.Background(), req, false)); err != nil {
		t.Fatal(err)
	}
	if fe.lastBody["temperature"] != 0.699999988079071 && fe.lastBody["temperature"] != 0.7 {
		t.Fatalf("temperature = %v", fe.lastBody["temperature"])
	}
	if fe.lastBody["top_p"] != 0.8999999761581421 && fe.lastBody["top_p"] != 0.9 {
		t.Fatalf("top_p = %v", fe.lastBody["top_p"])
	}
	if fe.lastBody["max_tokens"] != 128.0 {
		t.Fatalf("max_tokens = %v", fe.lastBody["max_tokens"])
	}
	stop, _ := fe.lastBody["stop"].([]any)
	if len(stop) != 1 || stop[0] != "###" {
		t.Fatalf("stop = %v", stop)
	}
	if _, ok := fe.lastBody["top_k"]; ok {
		t.Fatal("top_k should be ignored")
	}
	rf, _ := fe.lastBody["response_format"].(map[string]any)
	if rf["type"] != "json_object" {
		t.Fatalf("response_format = %v", rf)
	}
}

// TestToolChoiceMapping 验证 tool_config 映射。
func TestToolChoiceMapping(t *testing.T) {
	fe := newFakeEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, completion("ok", "stop"))
	})
	req := &model.LLMRequest{
		Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "hi"}}}},
		Config: &genai.GenerateContentConfig{
			ToolConfig: &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{
				Mode: genai.FunctionCallingConfigModeAny,
			}},
		},
	}
	if _, err := collect(t, fe.model(t).GenerateContent(context.Background(), req, false)); err != nil {
		t.Fatal(err)
	}
	if fe.lastBody["tool_choice"] != "required" {
		t.Fatalf("tool_choice = %v", fe.lastBody["tool_choice"])
	}
}
