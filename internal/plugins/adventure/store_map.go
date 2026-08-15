package adventure

import (
	"context"
	"database/sql"
	"encoding/json"
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
		if sm, err := a.loadMap(ctx, id); err == nil {
			a.ctx.Logger().Info("adventure: using existing map", "map_id", id,
				"island", sm.Graph.IslandName, "nodes", len(sm.Graph.Nodes), "chests", sm.Graph.ChestCount())
			return nil
		}
	}
	_, err = a.refreshMap(ctx, time.Now().UTC())
	return err
}

// refreshMap 换图：同步段生成拓扑 + 降级主题并立即落库（毫秒级，可阻塞 tick）；
// 随后若配置了 Themer，起 goroutine 异步生成 LLM 主题并就地 UPDATE（见 upgradeTheme）。
func (a *Adventure) refreshMap(ctx context.Context, now time.Time) (*StoredMap, error) {
	if err := a.abortAllRuns(ctx, now); err != nil {
		return nil, err
	}
	g := GenerateMap(a.genConfig())
	req := a.themeRequest(g)
	if th, err := (FallbackThemer{IntN: a.IntN}).ThemeIsland(ctx, req); err == nil {
		applyTheme(&g, th)
	} else {
		a.ctx.Logger().Warn("adventure: fallback theme failed", "err", err)
	}
	id := fmt.Sprintf("map_%d", now.UnixNano())
	sm := &StoredMap{ID: id, CreatedAt: now, Graph: g}
	if err := a.saveMap(ctx, sm); err != nil {
		return nil, err
	}
	if err := a.setKV(kvCurrentMapID, id); err != nil {
		return nil, err
	}
	// 换图后旧图无任何引用（行程已在 abortAllRuns 清空），直接清掉避免累积。
	if err := a.dropMapsExcept(id); err != nil {
		a.ctx.Logger().Warn("adventure: drop old maps failed", "err", err)
	}
	a.ctx.Logger().Info("adventure: map refreshed", "map_id", id, "island", g.IslandName,
		"nodes", len(g.Nodes), "chests", g.ChestCount())
	if a.Themer != nil {
		a.themeWg.Add(1)
		go a.upgradeTheme(id, req)
	}
	return sm, nil
}

// upgradeTheme 异步生成 LLM 主题并就地更新（不换图、不动拓扑与行程）。
// 失败仅记日志，地图保持降级主题；下一次换图再试。
func (a *Adventure) upgradeTheme(mapID string, req ThemeRequest) {
	defer a.themeWg.Done()
	timeout := a.ThemeTimeout
	if timeout <= 0 {
		timeout = defaultThemeTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	th, err := a.Themer.ThemeIsland(ctx, req)
	if err != nil {
		a.ctx.Logger().Warn("adventure: llm theme failed, keep fallback", "map_id", mapID, "err", err)
		return
	}
	if err := a.updateMapTheme(mapID, th); err != nil {
		a.ctx.Logger().Warn("adventure: save llm theme failed", "map_id", mapID, "err", err)
		return
	}
	a.ctx.Logger().Info("adventure: island themed", "map_id", mapID, "island", th.IslandName)
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
		`INSERT INTO adventure_maps (id, created_at, node_count, island_name, theme) VALUES (?, ?, ?, ?, ?)`,
		sm.ID, sm.CreatedAt.UTC().Format(time.RFC3339Nano), len(sm.Graph.Nodes),
		sm.Graph.IslandName, sm.Graph.Theme); err != nil {
		return err
	}
	for _, n := range sm.Graph.Nodes {
		chest := 0
		if n.HasChest {
			chest = 1
		}
		elements, _ := json.Marshal(n.Elements)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO adventure_nodes (map_id, node_id, name, has_chest, description, zone, elements)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			sm.ID, n.ID, n.Name, chest, n.Description, n.Zone, string(elements)); err != nil {
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
	var created, islandName, theme string
	var nodeCount int
	err := a.db.QueryRowContext(ctx,
		`SELECT created_at, node_count, island_name, theme FROM adventure_maps WHERE id = ?`, mapID).
		Scan(&created, &nodeCount, &islandName, &theme)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("adventure: map %s not found", mapID)
	}
	if err != nil {
		return nil, err
	}
	createdAt, _ := time.Parse(time.RFC3339Nano, created)

	rows, err := a.db.QueryContext(ctx,
		`SELECT node_id, name, has_chest, description, zone, elements FROM adventure_nodes WHERE map_id = ? ORDER BY node_id`, mapID)
	if err != nil {
		return nil, err
	}
	nodes := make([]MapNode, 0, nodeCount)
	for rows.Next() {
		var n MapNode
		var chest int
		var elements string
		if err := rows.Scan(&n.ID, &n.Name, &chest, &n.Description, &n.Zone, &elements); err != nil {
			rows.Close()
			return nil, err
		}
		n.HasChest = chest != 0
		_ = json.Unmarshal([]byte(elements), &n.Elements)
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
		Graph:     MapGraph{IslandName: islandName, Theme: theme, Nodes: nodes, Edges: edges},
	}, nil
}

// updateMapTheme 就地更新地图主题（异步 LLM 段落库用；不动拓扑与行程）。
func (a *Adventure) updateMapTheme(mapID string, t *IslandTheme) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE adventure_maps SET island_name = ?, theme = ? WHERE id = ?`,
		t.IslandName, t.Theme, mapID); err != nil {
		return err
	}
	for _, loc := range t.Locations {
		elements, _ := json.Marshal(loc.Elements)
		if _, err := tx.Exec(
			`UPDATE adventure_nodes SET name = ?, description = ?, zone = ?, elements = ?
			 WHERE map_id = ? AND node_id = ?`,
			loc.Name, loc.Description, loc.Zone, string(elements), mapID, loc.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
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

// dropMapsExcept 删除 keep 以外的全部地图及其节点/边（换图后清理旧图）。
func (a *Adventure) dropMapsExcept(keep string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM adventure_edges WHERE map_id != ?`,
		`DELETE FROM adventure_nodes WHERE map_id != ?`,
		`DELETE FROM adventure_maps WHERE id != ?`,
	} {
		if _, err := tx.Exec(q, keep); err != nil {
			return err
		}
	}
	return tx.Commit()
}
