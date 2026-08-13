package adventure

import (
	"errors"
	"net/http"
	"strings"

	"github.com/lalolv/PocketPet/internal/httpx"
	"github.com/lalolv/PocketPet/internal/plugin"
	"github.com/lalolv/PocketPet/internal/store"
)

// Routes 实现 plugin.RouteProvider。
func (a *Adventure) Routes() []plugin.Route {
	return []plugin.Route{
		{Method: http.MethodGet, Pattern: "/maps/current", Handler: a.handleCurrentMap},
		{Method: http.MethodGet, Pattern: "/pets/{id}/run", Handler: a.handleRun},
		{Method: http.MethodPost, Pattern: "/pets/{id}/start", Handler: a.handleStart},
	}
}

// handleStart 供 TUI/客户端直接出发探险（不经 LLM 工具）。
func (a *Adventure) handleStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := a.ctx.GetPet(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "pet not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	res, err := a.start(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if !res.OK {
		code, status := adventureReject(res.Outcome)
		httpx.WriteError(w, status, code, res.Outcome)
		return
	}
	a.writeRunJSON(w, r, id)
}

func adventureReject(outcome string) (code string, status int) {
	switch {
	case strings.Contains(outcome, "睡觉"):
		return "invalid_state", http.StatusConflict
	case strings.Contains(outcome, "太困"):
		return "invalid_state", http.StatusConflict
	case strings.Contains(outcome, "忙着"):
		return "invalid_state", http.StatusConflict
	case strings.Contains(outcome, "太累"):
		return "low_energy", http.StatusConflict
	case strings.Contains(outcome, "还在外面"):
		return "already_adventuring", http.StatusConflict
	case strings.Contains(outcome, "没有探险地图"):
		return "no_map", http.StatusNotFound
	default:
		return "rejected", http.StatusConflict
	}
}

func (a *Adventure) handleCurrentMap(w http.ResponseWriter, r *http.Request) {
	sm, err := a.currentMap(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "no current map")
		return
	}
	type nodeView struct {
		ID       int    `json:"id"`
		Name     string `json:"name"`
		HasChest bool   `json:"has_chest"`
	}
	type edgeView struct {
		From int `json:"from"`
		To   int `json:"to"`
	}
	nodes := make([]nodeView, 0, len(sm.Graph.Nodes))
	for _, n := range sm.Graph.Nodes {
		nodes = append(nodes, nodeView{ID: n.ID, Name: n.Name, HasChest: n.HasChest})
	}
	edges := make([]edgeView, 0, len(sm.Graph.Edges))
	for _, e := range sm.Graph.Edges {
		edges = append(edges, edgeView{From: e.From, To: e.To})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"map_id":      sm.ID,
		"created_at":  sm.CreatedAt.UTC(),
		"node_count":  len(nodes),
		"chest_count": sm.Graph.ChestCount(),
		"nodes":       nodes,
		"edges":       edges,
	})
}

func (a *Adventure) handleRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := a.ctx.GetPet(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "pet not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	a.writeRunJSON(w, r, id)
}

func (a *Adventure) writeRunJSON(w http.ResponseWriter, r *http.Request, id string) {
	run, ok, err := a.getRun(id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if !ok {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"pet_id": id, "adventuring": false,
		})
		return
	}
	nodeName := ""
	branches := 0
	if sm, err := a.loadMap(r.Context(), run.MapID); err == nil {
		if n, ok := nodeByID(sm.Graph, run.NodeID); ok {
			nodeName = n.Name
		}
		branches = len(sm.Graph.OutNeighbors(run.NodeID))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"pet_id":       id,
		"adventuring":  true,
		"map_id":       run.MapID,
		"node_id":      run.NodeID,
		"node_name":    nodeName,
		"branches":     branches,
		"chests_found": run.ChestsFound,
		"started_at":   run.StartedAt.UTC(),
	})
}
