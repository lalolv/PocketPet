package agent

import (
	"context"
	"testing"

	adkmodel "google.golang.org/adk/v2/model"

	"github.com/lalolv/PocketPet/internal/llm"
	"github.com/lalolv/PocketPet/internal/petfs"
)

// TestAgentModelOverride 验证 AGENT.md model 字段的按宠物覆盖：
// 空 = 跟随全局配置，填写后仅对该宠物生效（runner 按指纹重建）。
func TestAgentModelOverride(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "团团", "quiet")
	env.agent.cfg = llm.Config{Model: "global-model", BaseURL: "http://ds.example", APIKey: "k"}

	var captured []llm.Config
	env.agent.opts = Options{
		ModelFactory: func(_ context.Context, cfg llm.Config) (adkmodel.LLM, error) {
			captured = append(captured, cfg)
			return &fakeModel{reply: "ok"}, nil
		},
	}
	writeAgentMD := func(model string) {
		t.Helper()
		if err := env.fs.Write(p.ID, petfs.FileAgent,
			"---\nmodel: \""+model+"\"\nmcp: \"\"\n---\n"); err != nil {
			t.Fatal(err)
		}
	}
	chat := func() llm.Config {
		t.Helper()
		if _, err := env.agent.Chat(context.Background(), p.ID, "hi"); err != nil {
			t.Fatal(err)
		}
		return captured[len(captured)-1]
	}

	// 1. 无覆盖：跟随全局 model。
	if got := chat(); got.Model != "global-model" {
		t.Fatalf("default = %+v", got)
	}
	// 2. model 覆盖（base_url/api_key 继承全局）。
	writeAgentMD("pet-model")
	got := chat()
	if got.Model != "pet-model" || got.BaseURL != "http://ds.example" || got.APIKey != "k" {
		t.Fatalf("model override = %+v", got)
	}
	// 3. 改回空：恢复全局（runner 按指纹重建）。
	writeAgentMD("")
	if got := chat(); got.Model != "global-model" {
		t.Fatalf("back to default = %+v", got)
	}
}
