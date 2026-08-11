package adventure

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Run 是一只宠物在当前地图上的行程。
type Run struct {
	PetID       string
	MapID       string
	NodeID      int
	ChestsFound []int
	StartedAt   time.Time
}

func (a *Adventure) getRun(petID string) (*Run, bool, error) {
	var r Run
	var chests string
	var started string
	err := a.db.QueryRow(
		`SELECT pet_id, map_id, node_id, chests_found, started_at FROM adventure_runs WHERE pet_id = ?`,
		petID).Scan(&r.PetID, &r.MapID, &r.NodeID, &chests, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	_ = json.Unmarshal([]byte(chests), &r.ChestsFound)
	if r.ChestsFound == nil {
		r.ChestsFound = []int{}
	}
	r.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	return &r, true, nil
}

func (a *Adventure) listRuns() ([]Run, error) {
	rows, err := a.db.Query(`SELECT pet_id, map_id, node_id, chests_found, started_at FROM adventure_runs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var chests, started string
		if err := rows.Scan(&r.PetID, &r.MapID, &r.NodeID, &chests, &started); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(chests), &r.ChestsFound)
		if r.ChestsFound == nil {
			r.ChestsFound = []int{}
		}
		r.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (a *Adventure) insertRun(ctx context.Context, r Run) error {
	raw, _ := json.Marshal(r.ChestsFound)
	_, err := a.db.ExecContext(ctx,
		`INSERT INTO adventure_runs (pet_id, map_id, node_id, chests_found, started_at) VALUES (?, ?, ?, ?, ?)`,
		r.PetID, r.MapID, r.NodeID, string(raw), r.StartedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (a *Adventure) updateRun(ctx context.Context, r Run) error {
	raw, _ := json.Marshal(r.ChestsFound)
	_, err := a.db.ExecContext(ctx,
		`UPDATE adventure_runs SET node_id = ?, chests_found = ? WHERE pet_id = ?`,
		r.NodeID, string(raw), r.PetID)
	return err
}

func (a *Adventure) deleteRun(ctx context.Context, petID string) error {
	_, err := a.db.ExecContext(ctx, `DELETE FROM adventure_runs WHERE pet_id = ?`, petID)
	return err
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func nodeByID(g MapGraph, id int) (MapNode, bool) {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return MapNode{}, false
}
