package agent

import (
	"context"
	"testing"

	adkmodel "google.golang.org/adk/v2/model"

	"pocketpet/internal/llm"
	"pocketpet/internal/petfs"
)

// TestNamedProviderResolution 验证 AGENT.md provider 字段的解析规则：
// 命名 provider 优先按名匹配（含 model 覆盖），匹配不到按类型名回退默认连接参数。
func TestNamedProviderResolution(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "团团", "quiet")

	resolver, err := llm.NewResolver(map[string]llm.ProviderConfig{
		"deepseek": {Provider: llm.ProviderOpenAIChat, Model: "deepseek-chat",
			BaseURL: "http://ds.example", APIKey: "ds-key"},
	}, "", llm.ProviderConfig{Provider: llm.ProviderGemini, Model: "gemini-2.5-flash", APIKey: "env-key"})
	if err != nil {
		t.Fatal(err)
	}

	var captured []llm.ProviderConfig
	env.agent.opts = Options{
		Resolver: resolver,
		ModelFactory: func(_ context.Context, cfg llm.ProviderConfig) (adkmodel.LLM, error) {
			captured = append(captured, cfg)
			return &fakeModel{reply: "ok"}, nil
		},
	}
	writeAgentMD := func(provider, model string) {
		t.Helper()
		if err := env.fs.Write(p.ID, petfs.FileAgent,
			"---\nprovider: \""+provider+"\"\nmodel: \""+model+"\"\nmcp: \"\"\n---\n"); err != nil {
			t.Fatal(err)
		}
	}
	chat := func() llm.ProviderConfig {
		t.Helper()
		if _, err := env.agent.Chat(context.Background(), p.ID, "hi"); err != nil {
			t.Fatal(err)
		}
		return captured[len(captured)-1]
	}

	// 1. 无覆盖：全局默认（env 单 provider）。
	got := chat()
	if got.Provider != llm.ProviderGemini || got.Model != "gemini-2.5-flash" || got.APIKey != "env-key" {
		t.Fatalf("default = %+v", got)
	}

	// 2. 命名 provider 按名匹配。
	writeAgentMD("deepseek", "")
	got = chat()
	if got.Provider != llm.ProviderOpenAIChat || got.Model != "deepseek-chat" || got.APIKey != "ds-key" {
		t.Fatalf("named = %+v", got)
	}

	// 3. model 覆盖命名 provider。
	writeAgentMD("deepseek", "deepseek-reasoner")
	got = chat()
	if got.Model != "deepseek-reasoner" || got.BaseURL != "http://ds.example" {
		t.Fatalf("model override = %+v", got)
	}

	// 4. 存量写法（类型名）：回退默认连接参数（保留其 key）。
	writeAgentMD("openai-compatible", "")
	got = chat()
	if got.Provider != llm.ProviderOpenAI || got.APIKey != "env-key" {
		t.Fatalf("type fallback = %+v", got)
	}

	// 5. 改回空 provider：恢复默认（runner 按指纹重建）。
	writeAgentMD("", "")
	got = chat()
	if got.Provider != llm.ProviderGemini {
		t.Fatalf("back to default = %+v", got)
	}
}
