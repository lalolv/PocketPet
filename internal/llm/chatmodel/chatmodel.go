// Package chatmodel 是 OpenAI Chat Completions API（/v1/chat/completions）的
// model.LLM 适配器——给只实现了 Chat Completions 的端点（DeepSeek/Moonshot/通义/
// 老 vLLM/Ollama 等）兜底。OpenAI 官方及新版端点请用 openai-compatible（Responses API）。
//
// 实现刻意不用 openai-go SDK：手写最小线格式（全部 omitempty 字段），
// 对非严格实现（缺字段、usage 缺失、错误体形状不一）的容错更好。
package chatmodel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
)

// ClientConfig 配置适配器。BaseURL 为空时用 OpenAI 官方地址；
// APIKey 为空时回退 OPENAI_API_KEY 环境变量。
type ClientConfig struct {
	APIKey     string
	BaseURL    string       // 如 https://api.deepseek.com/v1（也可直接给到 /chat/completions）
	HTTPClient *http.Client // 可选，测试注入
}

// Model 实现 model.LLM。
type Model struct {
	name     string
	endpoint string
	apiKey   string
	hc       *http.Client
}

// NewModel 构造 Chat Completions 适配的 model.LLM。modelName 必填。
func NewModel(_ context.Context, modelName string, cfg *ClientConfig) (model.LLM, error) {
	if modelName == "" {
		return nil, fmt.Errorf("chatmodel: model name required")
	}
	if cfg == nil {
		cfg = &ClientConfig{}
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	endpoint := base
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint += "/chat/completions"
	}
	apiKey := cfg.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	return &Model{name: modelName, endpoint: endpoint, apiKey: apiKey, hc: hc}, nil
}

// Name 实现 model.LLM。
func (m *Model) Name() string { return m.name }

// GenerateContent 实现 model.LLM。stream=true 走 SSE 流：
// 增量文本以 partial 事件产出，结束时发一次聚合的完整事件（含拼合的 tool_calls）。
func (m *Model) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if req == nil {
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(nil, fmt.Errorf("chatmodel: request is nil"))
		}
	}
	body, err := buildChatRequest(m.name, req, stream)
	if err != nil {
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(nil, err)
		}
	}
	if stream {
		return m.generateStream(ctx, body)
	}
	return m.generate(ctx, body)
}

// httpError 是端点返回的非 2xx 错误。
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return fmt.Sprintf("chatmodel: http %d: %s", e.status, e.msg) }

// do 发起请求；非 2xx 解析错误体返回 *httpError（不静默吞错）。
func (m *Model) do(ctx context.Context, body any) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
	}
	resp, err := m.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(b))
		var eb struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(b, &eb) == nil && eb.Error.Message != "" {
			msg = eb.Error.Message
		}
		return nil, &httpError{status: resp.StatusCode, msg: msg}
	}
	return resp, nil
}

// generate 非流式：一次完整响应。
func (m *Model) generate(ctx context.Context, body any) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		resp, err := m.do(ctx, body)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()
		var cr chatResponse
		if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
			yield(nil, fmt.Errorf("chatmodel: decode response: %w", err))
			return
		}
		if len(cr.Choices) == 0 {
			yield(nil, fmt.Errorf("chatmodel: response has no choices"))
			return
		}
		ch := cr.Choices[0]
		yield(&model.LLMResponse{
			Content:       messageContent(ch.Message.Role, ch.Message.Content, ch.Message.ToolCalls),
			FinishReason:  mapFinishReason(ch.FinishReason),
			UsageMetadata: convertUsage(cr.Usage),
		}, nil)
	}
}

// generateStream 流式：逐行解析 SSE（data: {...} / data: [DONE]）。
// 文本增量即时产出 partial；tool_calls 分片按 index 拼合，最后产出一次完整聚合事件。
func (m *Model) generateStream(ctx context.Context, body any) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		resp, err := m.do(ctx, body)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()

		var textAgg strings.Builder
		toolAgg := newToolCallAggregator()
		finishReason := ""
		var usage *chatUsage

		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil && err != io.EOF {
				yield(nil, fmt.Errorf("chatmodel: read stream: %w", err))
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if err == io.EOF && line == "" {
				break
			}
			if strings.HasPrefix(line, "data:") {
				data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if data == "[DONE]" {
					break
				}
				var chunk chatChunk
				if json.Unmarshal([]byte(data), &chunk) == nil {
					usage = mergeUsage(usage, chunk.Usage)
					for _, choice := range chunk.Choices {
						if d := choice.Delta.Content; d != "" {
							textAgg.WriteString(d)
							if !yield(partialTextResponse(d), nil) {
								return
							}
						}
						toolAgg.add(choice.Delta.ToolCalls)
						if choice.FinishReason != "" {
							finishReason = choice.FinishReason
						}
					}
				} else {
					slog.Debug("chatmodel: skip unparseable stream chunk", "data", data)
				}
			}
			if err == io.EOF {
				break
			}
		}

		yield(&model.LLMResponse{
			Content:       messageContent("assistant", textAgg.String(), toolAgg.calls()),
			FinishReason:  mapFinishReason(finishReason),
			UsageMetadata: convertUsage(usage),
			TurnComplete:  true,
		}, nil)
	}
}

// partialTextResponse 构造一个增量文本的 partial 响应。
func partialTextResponse(text string) *model.LLMResponse {
	return &model.LLMResponse{
		Content: &genai.Content{Role: string(genai.RoleModel),
			Parts: []*genai.Part{{Text: text}}},
		Partial: true,
	}
}

// mapFinishReason 把 chat 的 finish_reason 映射到 genai.FinishReason。
func mapFinishReason(reason string) genai.FinishReason {
	switch reason {
	case "stop", "tool_calls", "function_call", "":
		return genai.FinishReasonStop
	case "length", "max_tokens":
		return genai.FinishReasonMaxTokens
	case "content_filter":
		return genai.FinishReasonSafety
	default:
		return genai.FinishReasonOther
	}
}

// convertUsage 透传 token 用量（端点不给 usage 时为 nil）。
func convertUsage(u *chatUsage) *genai.GenerateContentResponseUsageMetadata {
	if u == nil {
		return nil
	}
	return &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:     int32(u.PromptTokens),
		CandidatesTokenCount: int32(u.CompletionTokens),
		TotalTokenCount:      int32(u.TotalTokens),
	}
}

// mergeUsage 取最后一个非 nil 的 usage（流尾才可能携带）。
func mergeUsage(old, new *chatUsage) *chatUsage {
	if new != nil {
		return new
	}
	return old
}

// messageContent 把 assistant 文本与 tool_calls 拼成 genai.Content。
func messageContent(role, text string, calls []chatToolCall) *genai.Content {
	if role == "" {
		role = string(genai.RoleModel)
	}
	content := &genai.Content{Role: role}
	if text != "" {
		content.Parts = append(content.Parts, &genai.Part{Text: text})
	}
	for _, tc := range calls {
		args := map[string]any{}
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				// 端点给了非法 JSON 参数：原样保留，让工具侧报错而不是整条失败。
				args = map[string]any{"_raw": tc.Function.Arguments}
			}
		}
		content.Parts = append(content.Parts, &genai.Part{FunctionCall: &genai.FunctionCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: args,
		}})
	}
	return content
}
