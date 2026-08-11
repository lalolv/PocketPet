package metaagent

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/lalolv/PocketPet/internal/pet"
)

// emitJSON 把 payload 序列化为 Event.Message 并推送。
func (m *Midwife) emitJSON(ctx context.Context, petID, typ string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		slog.Error("metaagent: marshal genesis payload", "type", typ, "err", err)
		return
	}
	m.emit(ctx, pet.Event{
		PetID:     petID,
		Type:      typ,
		Message:   string(raw),
		CreatedAt: m.now(),
	})
}

func (m *Midwife) emitStarted(ctx context.Context, d *Draft) {
	m.emitJSON(ctx, d.PetID, pet.EventGenesisStarted, map[string]any{
		"pet_id":  d.PetID,
		"seed":    d.Seed,
		"species": d.Species,
		"mode":    d.Mode,
	})
}

func (m *Midwife) emitFailed(ctx context.Context, petID, reason string, fallback bool) {
	m.emitJSON(ctx, petID, pet.EventGenesisFailed, map[string]any{
		"reason":   reason,
		"fallback": fallback,
	})
}
