// Package llm 提供 model.LLM 的 provider 工厂。
// M2 阶段配置只走环境变量（POCKETPET_LLM_*，及各家标准环境变量），
// 未配置 provider 时 NewModel 返回 ErrNotConfigured，由 agent 包走降级。
package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"google.golang.org/genai"

	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/model/openaimodel"

	"github.com/lalolv/PocketPet/internal/llm/chatmodel"
)

// 支持的 provider 类型。
const (
	ProviderGemini = "gemini"            // Google Gemini（model/gemini）
	ProviderOpenAI = "openai-compatible" // OpenAI 兼容端点（model/openaimodel，Responses API）
	// ProviderOpenAIChat 是 Chat Completions 兜底（/v1/chat/completions）：
	// DeepSeek/Moonshot/通义/老 vLLM/Ollama 等只实现该 API 的端点。
	// openai-compatible = Responses API（OpenAI 官方/新 vLLM/新 Ollama）。
	ProviderOpenAIChat = "openai-chat"
)

// 环境变量名。APIKey/BaseURL 留空时由底层 SDK 回退到各家标准环境变量
// （gemini: GOOGLE_API_KEY / GEMINI_API_KEY；openai: OPENAI_API_KEY / OPENAI_BASE_URL）。
const (
	EnvProvider = "POCKETPET_LLM_PROVIDER" // gemini | openai-compatible
	EnvModel    = "POCKETPET_LLM_MODEL"    // 模型名，空则按 provider 取默认值
	EnvBaseURL  = "POCKETPET_LLM_BASE_URL" // 仅 openai-compatible 有效
	EnvAPIKey   = "POCKETPET_LLM_API_KEY"
)

// ErrNotConfigured 表示未配置（或配置了未知的）LLM provider。
var ErrNotConfigured = errors.New("llm: provider not configured")

// 各 provider 的默认模型（未显式指定 POCKETPET_LLM_MODEL 时使用）。
const (
	defaultGeminiModel = "gemini-2.5-flash"
	defaultOpenAIModel = "gpt-4o-mini"
)

// ProviderConfig 是一次 LLM 连接所需的全部配置。
type ProviderConfig struct {
	Provider string // ProviderGemini / ProviderOpenAI，空 = 未配置
	Model    string // 空 = 按 provider 取默认模型
	BaseURL  string // 仅 openai-compatible：兼容端点地址
	APIKey   string // 空 = 回退到各家标准环境变量
}

// FromEnv 从环境变量加载配置。
func FromEnv() ProviderConfig {
	return ProviderConfig{
		Provider: NormalizeProvider(os.Getenv(EnvProvider)),
		Model:    strings.TrimSpace(os.Getenv(EnvModel)),
		BaseURL:  strings.TrimSpace(os.Getenv(EnvBaseURL)),
		APIKey:   strings.TrimSpace(os.Getenv(EnvAPIKey)),
	}
}

// NormalizeProvider 归一化 provider 名：大小写不敏感，接受常见别名。
func NormalizeProvider(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "":
		return ""
	case "gemini", "google":
		return ProviderGemini
	case "openai", "openai-compatible", "openai_compatible":
		return ProviderOpenAI
	case "openai-chat", "openai-chat-completions", "chat", "chat-completions":
		return ProviderOpenAIChat
	default:
		return strings.ToLower(strings.TrimSpace(p))
	}
}

// Configured 报告配置是否足以发起一次真实调用。
// APIKey 允许为空——此时要求对应的标准环境变量已设置。
// openai-chat 还要求显式模型名（国产端点模型名各不相同，不瞎猜默认值）。
func (c ProviderConfig) Configured() bool {
	hasKey := c.APIKey != ""
	switch c.Provider {
	case ProviderGemini:
		return hasKey || os.Getenv("GOOGLE_API_KEY") != "" || os.Getenv("GEMINI_API_KEY") != ""
	case ProviderOpenAI:
		return hasKey || os.Getenv("OPENAI_API_KEY") != ""
	case ProviderOpenAIChat:
		return c.Model != "" && (hasKey || os.Getenv("OPENAI_API_KEY") != "")
	default:
		return false
	}
}

// NewModel 按配置构造 ADK 的 model.LLM。未配置时返回 ErrNotConfigured。
func NewModel(ctx context.Context, c ProviderConfig) (adkmodel.LLM, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	switch c.Provider {
	case ProviderGemini:
		modelName := c.Model
		if modelName == "" {
			modelName = defaultGeminiModel
		}
		// APIKey 为空时 genai 内部回退到 GOOGLE_API_KEY / GEMINI_API_KEY。
		return gemini.NewModel(ctx, modelName, &genai.ClientConfig{
			APIKey:  c.APIKey,
			Backend: genai.BackendGeminiAPI,
		})
	case ProviderOpenAI:
		modelName := c.Model
		if modelName == "" {
			modelName = defaultOpenAIModel
		}
		// 注意（M2 实测）：ADK 的 openaimodel 走 OpenAI Responses API，
		// 不是 Chat Completions——只兼容实现了 /v1/responses 的端点。
		// APIKey/BaseURL 为空时 openai-go 回退到 OPENAI_API_KEY / OPENAI_BASE_URL。
		return openaimodel.NewModel(ctx, modelName, &openaimodel.ClientConfig{
			APIKey:  c.APIKey,
			BaseURL: c.BaseURL,
		})
	case ProviderOpenAIChat:
		// Chat Completions 兜底适配（/v1/chat/completions）。
		// 必须显式指定模型（Configured 已校验）——国产端点模型名各不相同，不猜默认值。
		return chatmodel.NewModel(ctx, c.Model, &chatmodel.ClientConfig{
			APIKey:  c.APIKey,
			BaseURL: c.BaseURL,
		})
	default:
		// Configured() 已过滤未知 provider，这里只是防御。
		return nil, fmt.Errorf("%w: %q", ErrNotConfigured, c.Provider)
	}
}
