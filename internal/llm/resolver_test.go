package llm

import "testing"

var envCfg = ProviderConfig{
	Provider: ProviderGemini,
	Model:    "env-model",
	BaseURL:  "https://env.example",
	APIKey:   "env-key",
}

func namedProviders() map[string]ProviderConfig {
	return map[string]ProviderConfig{
		"deepseek": {Provider: ProviderOpenAIChat, Model: "deepseek-chat", BaseURL: "https://api.deepseek.com/v1", APIKey: "ds-key"},
		"gpt":      {Provider: ProviderOpenAI, Model: "gpt-4o-mini", APIKey: "oai-key"},
	}
}

func TestResolverNamedDefault(t *testing.T) {
	r, err := NewResolver(namedProviders(), "deepseek", envCfg)
	if err != nil {
		t.Fatal(err)
	}
	def := r.Default()
	if def.Provider != ProviderOpenAIChat || def.Model != "deepseek-chat" || def.APIKey != "ds-key" {
		t.Fatalf("default = %+v", def)
	}
}

func TestResolverEnvFallback(t *testing.T) {
	// 有 providers 但 llm.default 缺省 → 回退 env 单 provider
	r, err := NewResolver(namedProviders(), "", envCfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.Default() != envCfg {
		t.Fatalf("default = %+v, want env cfg", r.Default())
	}
	// 无 providers → env 单 provider（向后兼容）
	r, err = NewResolver(nil, "", envCfg)
	if err != nil || r.Default() != envCfg {
		t.Fatalf("nil providers: %v %+v", err, r.Default())
	}
}

func TestResolverBadDefault(t *testing.T) {
	if _, err := NewResolver(namedProviders(), "nope", envCfg); err == nil {
		t.Fatal("unknown default name should error")
	}
	if _, err := NewResolver(nil, "deepseek", envCfg); err == nil {
		t.Fatal("default with empty providers should error")
	}
}

func TestResolveNamedMatch(t *testing.T) {
	r, _ := NewResolver(namedProviders(), "deepseek", envCfg)

	// 按名字匹配命名 provider
	cfg := r.Resolve("gpt", "")
	if cfg.Provider != ProviderOpenAI || cfg.APIKey != "oai-key" {
		t.Fatalf("named resolve = %+v", cfg)
	}
	// model 覆盖命名 provider 的 model
	cfg = r.Resolve("deepseek", "deepseek-reasoner")
	if cfg.Model != "deepseek-reasoner" || cfg.BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("model override = %+v", cfg)
	}
	// 空 name → 默认
	if cfg := r.Resolve("", ""); cfg.Model != "deepseek-chat" {
		t.Fatalf("empty name = %+v", cfg)
	}
}

func TestResolveTypeFallback(t *testing.T) {
	r, _ := NewResolver(namedProviders(), "deepseek", envCfg)
	// 存量 AGENT.md 只写类型名：按类型覆盖默认连接参数（保留 key/base_url）
	cfg := r.Resolve("gemini", "")
	if cfg.Provider != ProviderGemini || cfg.APIKey != "ds-key" || cfg.BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("type fallback = %+v", cfg)
	}
	// 别名也走类型路径
	cfg = r.Resolve("chat", "")
	if cfg.Provider != ProviderOpenAIChat {
		t.Fatalf("alias = %+v", cfg)
	}
}
