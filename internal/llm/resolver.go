package llm

import "fmt"

// Resolver 按名字解析 LLM provider 配置（YAML 命名 provider 体系）。
// 解析规则（AGENT.md 的 provider 字段同此）：
//  1. 优先按名字匹配命名 provider（如 "deepseek"）；
//  2. 匹配不到按类型名（gemini/openai-compatible/openai-chat，含别名）覆盖全局默认连接参数——
//     向后兼容存量只写类型名的 AGENT.md。
//
// 全局默认：文件配了 llm.default 时取其指向的命名 provider；否则回退 env 单 provider。
type Resolver struct {
	def   ProviderConfig
	named map[string]ProviderConfig
}

// NewResolver 构造解析器。providers 为命名表（可为空），defaultName 为 llm.default。
// providers 非空且 defaultName 非空但名字不存在时返回错误（配置写错了要早报）。
// envCfg 是 env 单 provider 配置（llm.FromEnv() 的产物），作为命名模式缺省时的回退。
func NewResolver(providers map[string]ProviderConfig, defaultName string, envCfg ProviderConfig) (*Resolver, error) {
	r := &Resolver{def: envCfg, named: providers}
	if defaultName == "" {
		return r, nil
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("llm.default %q set but llm.providers is empty", defaultName)
	}
	def, ok := providers[defaultName]
	if !ok {
		return nil, fmt.Errorf("llm.default %q not found in llm.providers", defaultName)
	}
	r.def = def
	return r, nil
}

// Default 返回全局默认配置。
func (r *Resolver) Default() ProviderConfig { return r.def }

// Resolve 解析一个覆盖声明为有效配置：name/model 为空表示不覆盖。
// name 先按命名 provider 匹配，匹配不到按类型名覆盖默认配置的 Provider（保留其 key/base_url）。
func (r *Resolver) Resolve(name, model string) ProviderConfig {
	cfg := r.def
	if name != "" {
		if named, ok := r.named[name]; ok {
			cfg = named
		} else {
			cfg.Provider = NormalizeProvider(name)
		}
	}
	if model != "" {
		cfg.Model = model
	}
	return cfg
}
