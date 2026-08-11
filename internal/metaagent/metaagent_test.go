package metaagent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
	"github.com/lalolv/PocketPet/internal/store"
	"github.com/lalolv/PocketPet/internal/tick"
)

func TestSampleTraitsDeterministic(t *testing.T) {
	a := SampleTraits("seed-alpha")
	b := SampleTraits("seed-alpha")
	c := SampleTraits("seed-beta")
	for _, k := range TraitKeys {
		if a[k] != b[k] {
			t.Fatalf("same seed diverged on %s: %v vs %v", k, a[k], b[k])
		}
	}
	diff := false
	for _, k := range TraitKeys {
		if a[k] != c[k] {
			diff = true
			break
		}
	}
	if !diff {
		t.Fatal("different seeds produced identical traits")
	}
}

func TestClampStatsInput(t *testing.T) {
	s := ClampStatsInput(pet.Stats{Hunger: 10, Happy: 200, Clean: 70, Energy: 50, Health: 1, EXP: 9})
	if s.Hunger != 50 || s.Happy != 95 || s.Energy != 70 || s.Health != 100 || s.EXP != 0 {
		t.Fatalf("clamped = %+v", s)
	}
}

func TestScriptBirthEndToEnd(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	var events []pet.Event
	fs := petfs.New(filepath.Join(t.TempDir(), "data"))
	engine := tick.NewEngine(st, eventCollector{&events}, time.Minute, 24*time.Hour, pet.RealClock{})
	m := &Midwife{
		Engine: engine,
		FS:     fs,
		Emit:   engine.Emit,
		Sync:   true,
		ForceScript: true,
		Now:    func() time.Time { return time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC) },
	}

	res, err := m.Start(ctx, Request{
		Name: "团团", Species: "cat", Mode: ModeRandom, Seed: "test-seed-001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.GenesisStatus != pet.GenesisIncubating {
		// Sync 跑完后 Start 返回时已是 incubating 快照；宠物本身应已 ready
		t.Fatalf("birth result status = %q", res.GenesisStatus)
	}

	p, err := st.GetPet(ctx, res.PetID)
	if err != nil {
		t.Fatal(err)
	}
	if p.GenesisStatus != pet.GenesisReady {
		t.Fatalf("pet genesis_status = %q, want ready", p.GenesisStatus)
	}
	if p.Name != "团团" {
		t.Fatalf("name = %q", p.Name)
	}
	if p.Stats.Health != 100 || p.Stats.Hunger < 50 || p.Stats.Hunger > 90 {
		t.Fatalf("stats = %+v", p.Stats)
	}

	soul, err := fs.Read(res.PetID, petfs.FileSOUL)
	if err != nil {
		t.Fatal(err)
	}
	doc := petfs.ParseSoul(soul)
	if doc.Template != "genesis" || doc.Label == "" || len(doc.Traits) != 4 {
		t.Fatalf("soul doc = %+v\n%s", doc, soul)
	}
	if !strings.Contains(doc.Body, "## 癖好") {
		t.Fatalf("soul body missing quirks:\n%s", doc.Body)
	}

	petMD, err := fs.Read(res.PetID, petfs.FilePET)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(petMD, "seed: test-seed-001") {
		t.Fatalf("PET.md missing seed:\n%s", petMD)
	}

	types := map[string]bool{}
	for _, e := range events {
		types[e.Type] = true
		if strings.HasPrefix(e.Type, "genesis.") && e.Type != pet.EventGenesisNarration {
			var payload map[string]any
			if err := json.Unmarshal([]byte(e.Message), &payload); err != nil {
				t.Fatalf("event %s message not JSON: %q", e.Type, e.Message)
			}
		}
	}
	for _, want := range []string{
		pet.EventGenesisStarted, pet.EventGenesisGenes, pet.EventGenesisTemperament,
		pet.EventGenesisAppearance, pet.EventGenesisQuirks, pet.EventGenesisSoul,
		pet.EventGenesisStats, pet.EventGenesisIdentity, pet.EventGenesisReady,
		pet.EventBorn,
	} {
		if !types[want] {
			t.Fatalf("missing event %s; got %v", want, types)
		}
	}
}

func TestToolOrderEnforced(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fs := petfs.New(filepath.Join(t.TempDir(), "data"))
	engine := tick.NewEngine(st, nil, time.Minute, 24*time.Hour, pet.RealClock{})
	m := &Midwife{Engine: engine, FS: fs, Emit: func(context.Context, ...pet.Event) {}, Sync: true}

	p, err := engine.BeginBirth(ctx, "x", "cat")
	if err != nil {
		t.Fatal(err)
	}
	d := &Draft{PetID: p.ID, Seed: "s", Species: "cat", Mode: ModeRandom, Master: "主人", BornAt: p.BornAt}
	raw, _ := encodeDraft(d)
	if err := fs.SaveGenesisDraft(p.ID, raw); err != nil {
		t.Fatal(err)
	}
	w, err := m.loadWorkshop(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res := w.SetTemperament(ctx, "x", "y"); res.OK {
		t.Fatal("expected set_temperament to fail before genes")
	}
	if res := w.RollGenes(ctx, nil); !res.OK {
		t.Fatal(res.Error)
	}
	if res := w.SetQuirks(ctx, []string{"a", "b"}); res.OK {
		t.Fatal("expected quirks to fail before appearance")
	}
}

func TestFallbackCompletes(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fs := petfs.New(filepath.Join(t.TempDir(), "data"))
	engine := tick.NewEngine(st, nil, time.Minute, 24*time.Hour, pet.RealClock{})
	m := &Midwife{Engine: engine, FS: fs, Emit: func(context.Context, ...pet.Event) {}}

	p, err := engine.BeginBirth(ctx, "", "dog")
	if err != nil {
		t.Fatal(err)
	}
	d := &Draft{PetID: p.ID, Seed: "fb-seed", Species: "dog", Mode: ModeRandom, Master: "主人", BornAt: p.BornAt}
	raw, _ := encodeDraft(d)
	_ = fs.SaveGenesisDraft(p.ID, raw)
	w, _ := m.loadWorkshop(p.ID)
	res := w.EnsureComplete(ctx)
	if !res.OK {
		t.Fatal(res.Error)
	}
	got, err := st.GetPet(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GenesisStatus != pet.GenesisReady || got.Name == "" {
		t.Fatalf("pet = %+v", got)
	}
}

type eventCollector struct{ evs *[]pet.Event }

func (c eventCollector) Publish(e pet.Event) { *c.evs = append(*c.evs, e) }

