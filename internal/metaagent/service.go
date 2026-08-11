package metaagent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lalolv/PocketPet/internal/pet"
)

// Start 创建孵化中的宠物、写入草稿、发出 genesis.started，并启动诞生流程（LLM 或脚本）。
func (m *Midwife) Start(ctx context.Context, req Request) (*BirthResult, error) {
	if m.Engine == nil || m.FS == nil {
		return nil, fmt.Errorf("metaagent: engine and fs required")
	}
	req.Species = strings.TrimSpace(req.Species)
	if req.Species == "" {
		return nil, fmt.Errorf("species is required")
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = ModeRandom
	}
	switch mode {
	case ModeRandom, ModeDescribe, ModeTemplate:
	default:
		return nil, fmt.Errorf("unknown mode %q", mode)
	}
	if mode == ModeDescribe && strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required for describe mode")
	}
	if mode == ModeTemplate {
		if _, _, err := TraitsFromTemplate(req.Personality); err != nil {
			return nil, err
		}
	}

	seed := strings.TrimSpace(req.Seed)
	if seed == "" {
		var err error
		seed, err = NewSeed()
		if err != nil {
			return nil, err
		}
	}
	master := strings.TrimSpace(req.Master)
	if master == "" {
		master = "主人"
	}
	name := strings.TrimSpace(req.Name)

	p, err := m.Engine.BeginBirth(ctx, name, req.Species)
	if err != nil {
		return nil, err
	}

	d := &Draft{
		PetID:   p.ID,
		Seed:    seed,
		Species: req.Species,
		Mode:    mode,
		Prompt:  strings.TrimSpace(req.Prompt),
		Master:  master,
		ReqName: name,
		BornAt:  p.BornAt,
	}
	if mode == ModeTemplate {
		traits, label, err := TraitsFromTemplate(req.Personality)
		if err != nil {
			return nil, err
		}
		d.Traits = traits
		d.TemperamentLabel = label
	}

	raw, err := encodeDraft(d)
	if err != nil {
		return nil, err
	}
	if err := m.FS.SaveGenesisDraft(p.ID, raw); err != nil {
		return nil, fmt.Errorf("save genesis draft: %w", err)
	}
	m.emitStarted(ctx, d)

	run := func() {
		runCtx := context.Background()
		if err := m.RunBirth(runCtx, p.ID); err != nil {
			slog.Warn("metaagent: birth failed", "pet", p.ID, "err", err)
		}
	}
	if m.Sync {
		run()
	} else {
		go run()
	}

	return &BirthResult{
		PetID:         p.ID,
		Seed:          seed,
		Species:       req.Species,
		Mode:          mode,
		GenesisStatus: pet.GenesisIncubating,
		EventsURL:     "/v1/pets/" + p.ID + "/events",
	}, nil
}
