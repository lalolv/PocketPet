package adventure

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petstate"
)

// StoredMap 是落库后的一张地图（含 id）。
type StoredMap struct {
	ID        string
	CreatedAt time.Time
	Graph     MapGraph
}

func (a *Adventure) ensureCurrentMap(ctx context.Context) error {
	id, err := a.getKV(kvCurrentMapID)
	if err != nil {
		return err
	}
	if id != "" {
		if _, err := a.loadMap(ctx, id); err == nil {
			return nil
		}
	}
	_, err = a.refreshMap(ctx, time.Now().UTC())
	return err
}

func (a *Adventure) refreshMap(ctx context.Context, now time.Time) (*StoredMap, error) {
	if err := a.abortAllRuns(ctx, now); err != nil {
		return nil, err
	}
	g := GenerateMap(a.genConfig())
	id := fmt.Sprintf("map_%d", now.UnixNano())
	sm := &StoredMap{ID: id, CreatedAt: now, Graph: g}
	if err := a.saveMap(ctx, sm); err != nil {
		return nil, err
	}
	if err := a.setKV(kvCurrentMapID, id); err != nil {
		return nil, err
	}
	if err := a.setKV(kvRefreshCounter, "0"); err != nil {
		return nil, err
	}
	a.ctx.Logger().Info("adventure: map refreshed", "map_id", id, "nodes", len(g.Nodes), "chests", g.ChestCount())
	return sm, nil
}

func (a *Adventure) currentMap(ctx context.Context) (*StoredMap, error) {
	id, err := a.getKV(kvCurrentMapID)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, errors.New("adventure: no current map")
	}
	return a.loadMap(ctx, id)
}

func (a *Adventure) saveMap(ctx context.Context, sm *StoredMap) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO adventure_maps (id, created_at, node_count) VALUES (?, ?, ?)`,
		sm.ID, sm.CreatedAt.UTC().Format(time.RFC3339Nano), len(sm.Graph.Nodes)); err != nil {
		return err
	}
	for _, n := range sm.Graph.Nodes {
		chest := 0
		if n.HasChest {
			chest = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO adventure_nodes (map_id, node_id, name, has_chest) VALUES (?, ?, ?, ?)`,
			sm.ID, n.ID, n.Name, chest); err != nil {
			return err
		}
	}
	for _, e := range sm.Graph.Edges {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO adventure_edges (map_id, src, dst) VALUES (?, ?, ?)`,
			sm.ID, e.From, e.To); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *Adventure) loadMap(ctx context.Context, mapID string) (*StoredMap, error) {
	var created string
	var nodeCount int
	err := a.db.QueryRowContext(ctx,
		`SELECT created_at, node_count FROM adventure_maps WHERE id = ?`, mapID).
		Scan(&created, &nodeCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("adventure: map %s not found", mapID)
	}
	if err != nil {
		return nil, err
	}
	createdAt, _ := time.Parse(time.RFC3339Nano, created)

	rows, err := a.db.QueryContext(ctx,
		`SELECT node_id, name, has_chest FROM adventure_nodes WHERE map_id = ? ORDER BY node_id`, mapID)
	if err != nil {
		return nil, err
	}
	nodes := make([]MapNode, 0, nodeCount)
	for rows.Next() {
		var n MapNode
		var chest int
		if err := rows.Scan(&n.ID, &n.Name, &chest); err != nil {
			rows.Close()
			return nil, err
		}
		n.HasChest = chest != 0
		nodes = append(nodes, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	erows, err := a.db.QueryContext(ctx,
		`SELECT src, dst FROM adventure_edges WHERE map_id = ?`, mapID)
	if err != nil {
		return nil, err
	}
	var edges []MapEdge
	for erows.Next() {
		var e MapEdge
		if err := erows.Scan(&e.From, &e.To); err != nil {
			erows.Close()
			return nil, err
		}
		edges = append(edges, e)
	}
	erows.Close()
	if err := erows.Err(); err != nil {
		return nil, err
	}

	return &StoredMap{
		ID:        mapID,
		CreatedAt: createdAt,
		Graph:     MapGraph{Nodes: nodes, Edges: edges},
	}, nil
}

func (a *Adventure) abortAllRuns(ctx context.Context, now time.Time) error {
	rows, err := a.db.QueryContext(ctx, `SELECT pet_id FROM adventure_runs`)
	if err != nil {
		return err
	}
	var pets []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			pets = append(pets, id)
		}
	}
	rows.Close()
	if _, err := a.db.ExecContext(ctx, `DELETE FROM adventure_runs`); err != nil {
		return err
	}
	for _, petID := range pets {
		name := petID
		if p, err := a.ctx.GetPet(ctx, petID); err == nil {
			name = p.Name
		}
		_, _ = a.ctx.Apply(ctx, petID, petstate.Transition{
			To: pet.ActivityIdle, Owner: "adventure", Reason: "map-refresh",
		})
		a.ctx.Emit(ctx, pet.Event{
			PetID: petID, Type: EventAborted,
			Message: name + " 的探险因地图刷新中断了", CreatedAt: now,
		})
	}
	return nil
}

func (a *Adventure) getKV(key string) (string, error) {
	var v string
	err := a.db.QueryRow(`SELECT value FROM adventure_kv WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (a *Adventure) setKV(key, value string) error {
	_, err := a.db.Exec(
		`INSERT INTO adventure_kv (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (a *Adventure) bumpRefreshCounter() (int, error) {
	raw, err := a.getKV(kvRefreshCounter)
	if err != nil {
		return 0, err
	}
	n := 0
	if raw != "" {
		_, _ = fmt.Sscanf(raw, "%d", &n)
	}
	n++
	if err := a.setKV(kvRefreshCounter, fmt.Sprintf("%d", n)); err != nil {
		return 0, err
	}
	return n, nil
}
