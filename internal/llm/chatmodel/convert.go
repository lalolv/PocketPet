package chatmodel

// convert.go —— ADK LLMRequest/LLMResponse 与 Chat Completions 线格式之间的映射。
//
// 取舍说明：
//   - thought parts（思考内容）不发给端点（国产端点普遍不支持 reasoning 字段）。
//   - tool_call_id：FunctionCall.ID 缺失时合成 "adk-chat-call-N"；FunctionResponse
//     无 ID 时对应最老的未完成调用（与 openaimodel 的 callTracker 一致）；
//     找不到对应调用的孤儿 response 不报错（容错优先，记 debug 日志）。
//   - genai.Schema → JSON Schema 用一个小型递归转换器（type/properties/required/
//     items/enum/description）；优先使用 ParametersJsonSchema（已是标准 JSON Schema）。
//   - 不支持的参数（top_k、candidate_count>1）忽略并记 debug 日志——
//     面向容错差的端点，比 openaimodel 的报错策略更宽（有意偏离，见汇报）。

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
)

// ---------- 线格式（最小类型，全 omitempty 容错） ----------

// chatRequest 是 POST /chat/completions 的请求体。
type chatRequest struct {
	Model          string              `json:"model"`
	Messages       []chatMessage       `json:"messages"`
	Tools          []chatTool          `json:"tools,omitempty"`
	ToolChoice     any                 `json:"tool_choice,omitempty"`
	Temperature    *float64            `json:"temperature,omitempty"`
	TopP           *float64            `json:"top_p,omitempty"`
	MaxTokens      *int64              `json:"max_tokens,omitempty"`
	Stop           []string            `json:"stop,omitempty"`
	Stream         bool                `json:"stream,omitempty"`
	ResponseFormat *chatResponseFormat `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

type chatToolCall struct {
	Index    *int   `json:"index,omitempty"` // 仅流式分片出现
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"` // "function"
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type chatTool struct {
	Type     string `json:"type"` // "function"
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Parameters  any    `json:"parameters,omitempty"`
	} `json:"function"`
}

type chatResponseFormat struct {
	Type string `json:"type"` // "json_object"
}

// chatResponse 是非流式响应体（只取第一个 choice）。
type chatResponse struct {
	Choices []struct {
		Message struct {
			Role      string         `json:"role"`
			Content   string         `json:"content"`
			ToolCalls []chatToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

// chatChunk 是流式 SSE data 的 JSON。
type chatChunk struct {
	Choices []struct {
		Delta struct {
			Role      string         `json:"role"`
			Content   string         `json:"content"`
			ToolCalls []chatToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ---------- 请求构造 ----------

// buildChatRequest 把 ADK LLMRequest 转成 Chat Completions 请求体。
func buildChatRequest(modelName string, req *model.LLMRequest, stream bool) (*chatRequest, error) {
	name := modelName
	if req.Model != "" {
		name = req.Model
	}
	out := &chatRequest{Model: name, Stream: stream}

	msgs, err := convertMessages(req)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("chatmodel: request has no messages")
	}
	out.Messages = msgs

	tools, err := convertTools(req.Config)
	if err != nil {
		return nil, err
	}
	out.Tools = tools
	if tc := convertToolChoice(req.Config); tc != nil {
		out.ToolChoice = tc
	}
	applyGenerationConfig(out, req.Config)
	return out, nil
}

// convertMessages 生成 messages：system instruction 在最前，之后按会话顺序。
func convertMessages(req *model.LLMRequest) ([]chatMessage, error) {
	var msgs []chatMessage

	if req.Config != nil && req.Config.SystemInstruction != nil {
		if text := joinText(req.Config.SystemInstruction, ""); text != "" {
			msgs = append(msgs, chatMessage{Role: "system", Content: text})
		}
	}

	tracker := &callTracker{}
	for _, c := range req.Contents {
		if c == nil {
			continue
		}
		var texts []string
		var calls []*genai.FunctionCall
		var resps []*genai.FunctionResponse
		for _, p := range c.Parts {
			switch {
			case p == nil:
			case p.Text != "":
				if !p.Thought {
					texts = append(texts, p.Text)
				}
			case p.FunctionCall != nil:
				calls = append(calls, p.FunctionCall)
			case p.FunctionResponse != nil:
				resps = append(resps, p.FunctionResponse)
			default:
				slog.Debug("chatmodel: skip unsupported part", "type", fmt.Sprintf("%T", p))
			}
		}

		// 工具结果：每个 FunctionResponse 一条 tool 消息（Chat Completions 无 role 之分）。
		for _, fr := range resps {
			payload, err := json.Marshal(fr.Response)
			if err != nil {
				return nil, fmt.Errorf("chatmodel: marshal function response %q: %w", fr.Name, err)
			}
			msgs = append(msgs, chatMessage{
				Role:       "tool",
				ToolCallID: tracker.resolveResponseID(fr),
				Name:       fr.Name,
				Content:    string(payload),
			})
		}

		switch genai.Role(c.Role) {
		case genai.RoleModel, "assistant":
			if len(texts) == 0 && len(calls) == 0 {
				continue
			}
			msg := chatMessage{Role: "assistant", Content: strings.Join(texts, "")}
			for _, fc := range calls {
				msg.ToolCalls = append(msg.ToolCalls, tracker.newCall(fc))
			}
			msgs = append(msgs, msg)
		default: // user / 空 / 其他角色都按 user 处理
			if len(texts) > 0 {
				msgs = append(msgs, chatMessage{Role: "user", Content: strings.Join(texts, "")})
			}
		}
	}
	return msgs, nil
}

// joinText 拼接一个 Content 的全部可见文本 part。
func joinText(c *genai.Content, sep string) string {
	var parts []string
	for _, p := range c.Parts {
		if p != nil && p.Text != "" && !p.Thought {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, sep)
}

// callTracker 管理 tool_call_id 的对应关系（参考 openaimodel 的同名机制）。
type callTracker struct {
	nextID  int
	pending []string
}

// newCall 把 FunctionCall 转成 tool_call；无 ID 时合成。
func (t *callTracker) newCall(fc *genai.FunctionCall) chatToolCall {
	id := fc.ID
	if id == "" {
		id = fmt.Sprintf("adk-chat-call-%d", t.nextID)
		t.nextID++
	}
	t.pending = append(t.pending, id)
	args := fc.Args
	if args == nil {
		args = map[string]any{}
	}
	b, _ := json.Marshal(args)
	tc := chatToolCall{ID: id, Type: "function"}
	tc.Function.Name = fc.Name
	tc.Function.Arguments = string(b)
	return tc
}

// resolveResponseID 给 FunctionResponse 配对 tool_call_id：
// 有 ID 用 ID（并从 pending 移除）；无 ID 取最老的未完成调用；
// 无 pending 的孤儿不报错，给合成 ID 并记 debug（容错优先）。
func (t *callTracker) resolveResponseID(fr *genai.FunctionResponse) string {
	if fr.ID != "" {
		for i, id := range t.pending {
			if id == fr.ID {
				t.pending = append(t.pending[:i], t.pending[i+1:]...)
				break
			}
		}
		return fr.ID
	}
	if len(t.pending) > 0 {
		id := t.pending[0]
		t.pending = t.pending[1:]
		return id
	}
	slog.Debug("chatmodel: function response has no matching call", "name", fr.Name)
	id := fmt.Sprintf("adk-chat-orphan-%d", t.nextID)
	t.nextID++
	return id
}

// convertTools 把 genai 工具声明转成 chat tools（只支持 function 类）。
func convertTools(cfg *genai.GenerateContentConfig) ([]chatTool, error) {
	if cfg == nil || len(cfg.Tools) == 0 {
		return nil, nil
	}
	var out []chatTool
	for _, gt := range cfg.Tools {
		if gt == nil {
			continue
		}
		for _, decl := range gt.FunctionDeclarations {
			if decl == nil || decl.Name == "" {
				return nil, fmt.Errorf("chatmodel: function declaration missing name")
			}
			var tool chatTool
			tool.Type = "function"
			tool.Function.Name = decl.Name
			tool.Function.Description = decl.Description
			if decl.ParametersJsonSchema != nil {
				tool.Function.Parameters = decl.ParametersJsonSchema
			} else if decl.Parameters != nil {
				tool.Function.Parameters = genaiSchemaToMap(decl.Parameters)
			}
			out = append(out, tool)
		}
	}
	return out, nil
}

// genaiSchemaToMap 把 genai.Schema 递归转成 JSON Schema map（简化版：
// type/properties/required/items/enum/description，其他字段忽略）。
func genaiSchemaToMap(s *genai.Schema) map[string]any {
	if s == nil {
		return nil
	}
	out := map[string]any{}
	if s.Type != "" {
		out["type"] = strings.ToLower(string(s.Type))
	}
	if s.Description != "" {
		out["description"] = s.Description
	}
	if len(s.Properties) > 0 {
		props := map[string]any{}
		for k, v := range s.Properties {
			props[k] = genaiSchemaToMap(v)
		}
		out["properties"] = props
	}
	if len(s.Required) > 0 {
		out["required"] = s.Required
	}
	if s.Items != nil {
		out["items"] = genaiSchemaToMap(s.Items)
	}
	if len(s.Enum) > 0 {
		out["enum"] = s.Enum
	}
	return out
}

// convertToolChoice 映射 tool_config：ANY/VALIDATED → "required"，NONE → "none"，其余省略。
func convertToolChoice(cfg *genai.GenerateContentConfig) any {
	if cfg == nil || cfg.ToolConfig == nil || cfg.ToolConfig.FunctionCallingConfig == nil {
		return nil
	}
	switch cfg.ToolConfig.FunctionCallingConfig.Mode {
	case genai.FunctionCallingConfigModeAny, genai.FunctionCallingConfigModeValidated:
		return "required"
	case genai.FunctionCallingConfigModeNone:
		return "none"
	default: // AUTO / 未指定：让端点自己决定
		return nil
	}
}

// applyGenerationConfig 映射生成参数；不支持的忽略并记 debug。
func applyGenerationConfig(out *chatRequest, cfg *genai.GenerateContentConfig) {
	if cfg == nil {
		return
	}
	if cfg.Temperature != nil {
		v := float64(*cfg.Temperature)
		out.Temperature = &v
	}
	if cfg.TopP != nil {
		v := float64(*cfg.TopP)
		out.TopP = &v
	}
	if cfg.MaxOutputTokens > 0 {
		v := int64(cfg.MaxOutputTokens)
		out.MaxTokens = &v
	}
	if len(cfg.StopSequences) > 0 {
		// OpenAI stop 最多 4 个；截断并记录。
		stop := cfg.StopSequences
		if len(stop) > 4 {
			slog.Debug("chatmodel: truncate stop sequences to 4", "count", len(stop))
			stop = stop[:4]
		}
		out.Stop = stop
	}
	if cfg.TopK != nil {
		slog.Debug("chatmodel: top_k not supported, ignored")
	}
	if cfg.CandidateCount > 1 {
		slog.Debug("chatmodel: candidate_count > 1 not supported, ignored")
	}
	// 结构化输出：json_object（json_schema strict 兼容性差，见汇报）。
	if cfg.ResponseMIMEType == "application/json" || cfg.ResponseSchema != nil {
		out.ResponseFormat = &chatResponseFormat{Type: "json_object"}
	}
	if cfg.ResponseSchema != nil {
		// 端点只认 json_object 时，schema 以 system 提示注入（dream 的容错解析可兜底）。
		if b, err := json.Marshal(genaiSchemaToMap(cfg.ResponseSchema)); err == nil {
			schemaHint := "\n输出必须是符合以下 JSON Schema 的 JSON 对象：" + string(b)
			if len(out.Messages) > 0 && out.Messages[0].Role == "system" {
				out.Messages[0].Content += schemaHint
			} else {
				out.Messages = append([]chatMessage{{Role: "system", Content: strings.TrimSpace(schemaHint)}}, out.Messages...)
			}
		}
	}
}

// toolCallAggregator 按 index 拼合流式 tool_calls 分片。
type toolCallAggregator struct {
	byIdx map[int]*chatToolCall
	order []int
}

func newToolCallAggregator() *toolCallAggregator {
	return &toolCallAggregator{byIdx: map[int]*chatToolCall{}}
}

// add 合并一批分片：index 对齐，id/name 首次出现即锁定，arguments 追加。
func (a *toolCallAggregator) add(calls []chatToolCall) {
	for _, tc := range calls {
		idx := 0
		if tc.Index != nil {
			idx = *tc.Index
		}
		agg, ok := a.byIdx[idx]
		if !ok {
			agg = &chatToolCall{Type: "function"}
			a.byIdx[idx] = agg
			a.order = append(a.order, idx)
		}
		if tc.ID != "" {
			agg.ID = tc.ID
		}
		if tc.Function.Name != "" {
			agg.Function.Name = tc.Function.Name
		}
		agg.Function.Arguments += tc.Function.Arguments
	}
}

// calls 按出现顺序返回拼合结果。
func (a *toolCallAggregator) calls() []chatToolCall {
	var out []chatToolCall
	for _, idx := range a.order {
		out = append(out, *a.byIdx[idx])
	}
	return out
}
