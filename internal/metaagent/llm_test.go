package metaagent

import (
	"context"
	"iter"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	adkmodel "google.golang.org/adk/v2/model"

	"github.com/lalolv/PocketPet/internal/llm"
	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
	"github.com/lalolv/PocketPet/internal/store"
	"github.com/lalolv/PocketPet/internal/tick"
)

// scriptedModel 按预定轮次返回 LLM 响应（含工具调用）。
type scriptedModel struct {
	turns []*adkmodel.LLMResponse

	mu   sync.Mutex
	call int
}

func (f *scriptedModel) Name() string { return "scripted-meta" }

func (f *scriptedModel) GenerateContent(_ context.Context, _ *adkmodel.LLMRequest, _ bool) iter.Seq2[*adkmodel.LLMResponse, error] {
	f.mu.Lock()
	i := f.call
	if i >= len(f.turns) {
		i = len(f.turns) - 1
	}
	f.call++
	resp := f.turns[i]
	f.mu.Unlock()
	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		yield(resp, nil)
	}
}

func fc(name string, args map[string]any) *adkmodel.LLMResponse {
	return &adkmodel.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{
		{FunctionCall: &genai.FunctionCall{Name: name, Args: args}},
	}}}
}

func textResp(s string) *adkmodel.LLMResponse {
	return &adkmodel.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: s}}}}
}

func birthToolTurns() []*adkmodel.LLMResponse {
	soul := "我是一只有点拧巴的小猫。嘴上不承认想你，但你会发现我总在你脚边转。喜欢窗台的阳光，害怕突然的巨响。说话短短的，偶尔会用反问句。"
	return []*adkmodel.LLMResponse{
		fc("narrate", map[string]any{"stage": "genes", "text": "蛋壳浮现光纹。"}),
		fc("roll_genes", map[string]any{}),
		fc("set_temperament", map[string]any{"label": "粘人社恐", "blurb": "怕生但离不开你"}),
		fc("set_appearance", map[string]any{"appearance": "橘白短毛，耳朵尖尖，走路轻轻的。"}),
		fc("set_quirks", map[string]any{"quirks": []any{"打雷钻纸箱", "只吃碗左边的粮", "被夸会假装走开"}}),
		fc("write_soul", map[string]any{"narrative": soul}),
		fc("set_base_stats", map[string]any{"use_suggested": true}),
		fc("write_identity", map[string]any{"name": "团团", "master": "主人", "species_flavor": "橘白短毛猫"}),
		fc("finalize_birth", map[string]any{}),
		textResp("破壳完成。"),
	}
}

func TestLLMBirthEndToEnd(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	var events []pet.Event
	fs := petfs.New(filepath.Join(t.TempDir(), "data"))
	engine := tick.NewEngine(st, eventCollector{&events}, time.Minute, 24*time.Hour, pet.RealClock{})
	fake := &scriptedModel{turns: birthToolTurns()}
	m := &Midwife{
		Engine: engine,
		FS:     fs,
		Emit:   engine.Emit,
		LLM:    llm.Config{Model: "fake", APIKey: "test"},
		ModelFactory: func(context.Context, llm.Config) (adkmodel.LLM, error) {
			return fake, nil
		},
		Sync:         true,
		BirthTimeout: 30 * time.Second,
		Now:          func() time.Time { return time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC) },
	}

	res, err := m.Start(ctx, Request{
		Name: "团团", Species: "cat", Mode: ModeDescribe,
		Prompt: "一只怕打雷的橘猫", Seed: "llm-seed-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	p, err := st.GetPet(ctx, res.PetID)
	if err != nil {
		t.Fatal(err)
	}
	if p.GenesisStatus != pet.GenesisReady || p.Name != "团团" {
		t.Fatalf("pet = %+v", p)
	}

	soul, err := fs.Read(res.PetID, petfs.FileSOUL)
	if err != nil {
		t.Fatal(err)
	}
	doc := petfs.ParseSoul(soul)
	if doc.Label != "粘人社恐" || !strings.Contains(doc.Body, "拧巴") {
		t.Fatalf("soul = %+v\n%s", doc, soul)
	}

	types := map[string]int{}
	for _, e := range events {
		types[e.Type]++
	}
	for _, want := range []string{
		pet.EventGenesisStarted, pet.EventGenesisGenes, pet.EventGenesisTemperament,
		pet.EventGenesisSoul, pet.EventGenesisReady, pet.EventBorn,
	} {
		if types[want] == 0 {
			t.Fatalf("missing %s in %v", want, types)
		}
	}
	if types[pet.EventGenesisNarration] == 0 {
		t.Fatalf("expected narration events, got %v", types)
	}
}

func TestLLMFailureFallsBack(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	fs := petfs.New(filepath.Join(t.TempDir(), "data"))
	engine := tick.NewEngine(st, nil, time.Minute, 24*time.Hour, pet.RealClock{})
	// 只调一步就停：不 finalize → fallback
	fake := &scriptedModel{turns: []*adkmodel.LLMResponse{
		fc("roll_genes", map[string]any{}),
		textResp("我累了，不想继续了。"),
	}}
	m := &Midwife{
		Engine: engine,
		FS:     fs,
		Emit:   func(context.Context, ...pet.Event) {},
		LLM:    llm.Config{Model: "fake", APIKey: "test"},
		ModelFactory: func(context.Context, llm.Config) (adkmodel.LLM, error) {
			return fake, nil
		},
		Sync: true,
	}

	res, err := m.Start(ctx, Request{Species: "dog", Mode: ModeRandom, Seed: "fb"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.GetPet(ctx, res.PetID)
	if err != nil {
		t.Fatal(err)
	}
	if p.GenesisStatus != pet.GenesisReady {
		t.Fatalf("expected fallback ready, got %q", p.GenesisStatus)
	}
	if !fs.Exists(res.PetID) {
		t.Fatal("expected pet files after fallback")
	}
}

func TestUnconfiguredUsesScript(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fs := petfs.New(filepath.Join(t.TempDir(), "data"))
	engine := tick.NewEngine(st, nil, time.Minute, 24*time.Hour, pet.RealClock{})
	m := &Midwife{Engine: engine, FS: fs, Emit: func(context.Context, ...pet.Event) {}, Sync: true}

	res, err := m.Start(ctx, Request{Name: "旺财", Species: "dog", Seed: "script-only"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetPet(ctx, res.PetID)
	if p.GenesisStatus != pet.GenesisReady || p.Name != "旺财" {
		t.Fatalf("pet = %+v", p)
	}
}
