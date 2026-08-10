// Package llm 提供 model.LLM 的构造：统一走 OpenAI Chat Completions 兼容端点
// （OpenAI 官方 / DeepSeek / Moonshot / vLLM / Ollama 等只实现该 API 的端点均可）。
// 未配置时 NewModel 返回 ErrNotConfigured，由 agent 包走降级。
package llm

import (
	"context"
	"errors"

	adkmodel "google.golang.org/adk/v2/model"

	"github.com/lalolv/PocketPet/internal/llm/chatmodel"
)

// ErrNotConfigured 表示 LLM 未配置（缺 model 或 api_key）。
var ErrNotConfigured = errors.New("llm: not configured")

// Config 是一次 LLM 连接所需的全部配置：任意 OpenAI Chat Completions 兼容端点。
type Config struct {
	Model   string // 模型名，必填（各端点模型名不同，不设默认值）
	BaseURL string // 端点地址，如 https://api.deepseek.com/v1；空 = OpenAI 官方
	APIKey  string // 密钥，必填
}

// Configured 报告配置是否足以发起一次真实调用。
func (c Config) Configured() bool {
	return c.Model != "" && c.APIKey != ""
}

// NewModel 按配置构造 ADK 的 model.LLM。未配置时返回 ErrNotConfigured。
func NewModel(ctx context.Context, c Config) (adkmodel.LLM, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	return chatmodel.NewModel(ctx, c.Model, &chatmodel.ClientConfig{
		APIKey:  c.APIKey,
		BaseURL: c.BaseURL,
	})
}
