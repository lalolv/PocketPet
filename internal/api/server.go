// Package api 提供 REST API（/v1 前缀）与 SSE 事件流。
package api

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lalolv/PocketPet/internal/agent"
	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
	"github.com/lalolv/PocketPet/internal/store"
	"github.com/lalolv/PocketPet/internal/tick"
)

// sseReplayCount 是 SSE 连接建立时回放的历史事件条数。
const sseReplayCount = 20

// Server 是 HTTP API 服务。
type Server struct {
	store  *store.Store
	engine *tick.Engine
	hub    *Hub
	fs     *petfs.FS
	agent  *agent.PetAgent
	mux    *http.ServeMux

	a2aMu       sync.Mutex
	a2aHandlers map[string]*a2aEntry // petID → A2A 协议 handler（按 agent 实例缓存）
}

// NewServer 装配路由。
func NewServer(st *store.Store, engine *tick.Engine, hub *Hub, fs *petfs.FS, ag *agent.PetAgent) *Server {
	s := &Server{store: st, engine: engine, hub: hub, fs: fs, agent: ag,
		mux: http.NewServeMux(), a2aHandlers: make(map[string]*a2aEntry)}
	// 状态快照推送：结算/动作落库后经 hub 广播 SSE state 帧。
	engine.SetStateSink(stateSink{hub})
	s.mux.HandleFunc("POST /v1/pets", s.handleCreatePet)
	s.mux.HandleFunc("GET /v1/pets", s.handleListPets)
	s.mux.HandleFunc("GET /v1/pets/{id}", s.handleGetPet)
	s.mux.HandleFunc("POST /v1/pets/{id}/care", s.handleCare)
	s.mux.HandleFunc("GET /v1/pets/{id}/events", s.handleEvents)
	s.mux.HandleFunc("POST /v1/pets/{id}/chat", s.handleChat)
	s.mux.HandleFunc("GET /v1/pets/{id}/soul", s.handleSoul)
	s.mux.HandleFunc("POST /v1/pets/{id}/soul/lock", s.handleSoulLock)
	s.mux.HandleFunc("GET /v1/pets/{id}/memory", s.handleMemory)
	s.mux.HandleFunc("GET /v1/pets/{id}/skills", s.handleSkills)
	s.mux.HandleFunc("/a2a/pets/{id}/", s.handleA2A)
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	return s
}

// Handler 返回根 HTTP handler（含请求日志中间件）。
func (s *Server) Handler() http.Handler { return logRequests(s.mux) }

// statsView 是对外展示的数值视图（内部 float 取整）。
type statsView struct {
	Hunger int `json:"hunger"`
	Happy  int `json:"happy"`
	Clean  int `json:"clean"`
	Energy int `json:"energy"`
	Health int `json:"health"`
	EXP    int `json:"exp"`
}

// petView 是 API 返回的宠物完整状态。
type petView struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Species    string    `json:"species"`
	Stage      pet.Stage `json:"stage"`
	Sleeping   bool      `json:"sleeping"`
	Alive      bool      `json:"alive"`
	Stats      statsView `json:"stats"`
	BornAt     time.Time `json:"born_at"`
	LastTickAt time.Time `json:"last_tick_at"`
	// Personality 是性格模板键（M2）。仅在创建/单只查询时附带，列表为空。
	Personality string `json:"personality,omitempty"`
}

// stateView 是 SSE state 帧的载荷：随时间变化的字段快照（每 tick/动作后推送）。
// 与 petView 分离：名字、物种等不变字段不重复推，客户端合并到本地状态。
type stateView struct {
	ID       string    `json:"id"`
	Stage    pet.Stage `json:"stage"`
	Sleeping bool      `json:"sleeping"`
	Alive    bool      `json:"alive"`
	Stats    statsView `json:"stats"`
}

// statsViewOf 计算对外展示的数值视图（内部 float 取整）。
func statsViewOf(p *pet.Pet) statsView {
	return statsView{
		Hunger: int(math.Round(p.Stats.Hunger)),
		Happy:  int(math.Round(p.Stats.Happy)),
		Clean:  int(math.Round(p.Stats.Clean)),
		Energy: int(math.Round(p.Stats.Energy)),
		Health: int(math.Round(p.Stats.Health)),
		EXP:    p.Stats.EXP,
	}
}

// stateOf 从领域对象构造状态快照。
func stateOf(p *pet.Pet) stateView {
	return stateView{ID: p.ID, Stage: p.Stage, Sleeping: p.Sleeping, Alive: p.Alive, Stats: statsViewOf(p)}
}

// stateSink 把 engine 的状态快照适配到 hub（实现 tick.StateSink）。
type stateSink struct{ hub *Hub }

// PublishState 实现 tick.StateSink。
func (s stateSink) PublishState(p *pet.Pet) { s.hub.PublishState(stateOf(p)) }

func view(p *pet.Pet) petView {
	return petView{
		ID: p.ID, Name: p.Name, Species: p.Species, Stage: p.Stage,
		Sleeping: p.Sleeping, Alive: p.Alive,
		Stats:  statsViewOf(p),
		BornAt: p.BornAt, LastTickAt: p.LastTickAt,
	}
}

type createPetRequest struct {
	Name        string `json:"name"`
	Species     string `json:"species"`
	Personality string `json:"personality"` // 性格模板名，可选；空 = 随机
}

// defaultPetName 给没起名的宠物一个合理默认名字。
func defaultPetName(species string) string {
	switch species {
	case "cat":
		return "小咪"
	case "dog":
		return "旺财"
	default:
		return "小团子"
	}
}

func (s *Server) handleCreatePet(w http.ResponseWriter, r *http.Request) {
	var req createPetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid JSON body")
		return
	}
	if req.Species == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "species is required")
		return
	}
	if req.Name == "" {
		req.Name = defaultPetName(req.Species)
	}
	// 先校验性格模板名，避免宠物出生了文件却建不起来。
	if _, err := petfs.ResolvePersonality(req.Personality); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	p, err := s.engine.CreatePet(r.Context(), req.Name, req.Species)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	// 出生即创建 petfs 文件（人格与记忆的 source of truth）。
	per, err := s.fs.CreatePet(p.ID, petfs.Identity{
		Name: p.Name, Species: p.Species, Personality: req.Personality,
		Stage: string(p.Stage), BornAt: p.BornAt,
	})
	if err != nil {
		writeDomainError(w, fmt.Errorf("create pet files: %w", err))
		return
	}
	v := view(p)
	v.Personality = per.Key
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) handleListPets(w http.ResponseWriter, r *http.Request) {
	pets, err := s.store.ListPets(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	views := make([]petView, 0, len(pets))
	for _, p := range pets {
		v := view(p)
		if per, err := s.fs.SoulTemplate(p.ID); err == nil {
			v.Personality = per
		}
		views = append(views, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"pets": views})
}

func (s *Server) handleGetPet(w http.ResponseWriter, r *http.Request) {
	// 读取前即时结算，保证返回的是当前时刻状态。
	p, err := s.engine.Settle(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	v := view(p)
	if per, err := s.fs.SoulTemplate(p.ID); err == nil {
		v.Personality = per
	}
	writeJSON(w, http.StatusOK, v)
}

type careRequest struct {
	Action string `json:"action"`
}

func (s *Server) handleCare(w http.ResponseWriter, r *http.Request) {
	var req careRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid JSON body")
		return
	}
	p, err := s.engine.Care(r.Context(), r.PathValue("id"), pet.Action(req.Action))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view(p))
}

// handleEvents 是 SSE 流：先回放 pet_events 中最近 N 条事件，订阅后补发当前状态快照，
// 随后持续推送实时事件与状态快照（state 帧）。客户端靠 state 帧刷新数值，无需轮询。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetPet(r.Context(), id); err != nil {
		writeDomainError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, codeInternal, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	send := func(name string, idLine int64, payload any) error {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if idLine > 0 {
			fmt.Fprintf(w, "id: %d\n", idLine)
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	// 回放历史事件。
	recent, err := s.store.RecentEvents(r.Context(), id, sseReplayCount)
	if err != nil {
		writeDomainError(w, err) // 注意：此时已写出 200 头，仅尽力记录
		return
	}
	for _, e := range recent {
		if err := send(e.Type, e.ID, e); err != nil {
			return
		}
	}

	// 订阅实时推送；先订阅再结算快照，避免漏掉两动作之间的帧。
	ch, cancel := s.hub.Subscribe(id)
	defer cancel()

	// 补发当前状态快照：客户端（含重连）立即拿到最新数值，不用等下一个 tick。
	if p, err := s.engine.Settle(r.Context(), id); err == nil {
		if err := send("state", 0, stateOf(p)); err != nil {
			return
		}
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case f, ok := <-ch:
			if !ok {
				return
			}
			var idLine int64
			if e, isEvent := f.data.(pet.Event); isEvent {
				idLine = e.ID
			}
			if err := send(f.name, idLine, f.data); err != nil {
				return
			}
		}
	}
}

type chatRequest struct {
	Message string `json:"message"`
}

// handleChat 与宠物对话（M2）。默认一次性返回完整 JSON；
// ?stream=true 时走 SSE：文本块事件名 chunk，结束发 done。
// LLM 不可用时 PetAgent 内部走降级文案，本端点始终返回 200 + reply。
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "message is required")
		return
	}

	if r.URL.Query().Get("stream") == "true" {
		s.handleChatStream(w, r, id, req.Message)
		return
	}
	reply, err := s.agent.Chat(r.Context(), id, req.Message)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"reply": reply})
}

// handleChatStream 是 chat 的 SSE 变体。
func (s *Server) handleChatStream(w http.ResponseWriter, r *http.Request, id, message string) {
	// 先确认宠物存在（此时还没写 SSE 头，可走标准错误格式）。
	if _, err := s.store.GetPet(r.Context(), id); err != nil {
		writeDomainError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, codeInternal, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	send := func(event string, payload any) bool {
		data, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	var full strings.Builder
	for chunk, err := range s.agent.ChatStream(r.Context(), id, message) {
		if err != nil {
			send("error", map[string]string{"message": err.Error()})
			return
		}
		full.WriteString(chunk)
		if !send("chunk", map[string]string{"text": chunk}) {
			return
		}
	}
	send("done", map[string]string{"reply": full.String()})
}

// handleSoul 返回宠物的 SOUL.md 内容与元信息（性格模板键、锁定状态、演化历史）。
func (s *Server) handleSoul(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := s.store.GetPet(r.Context(), id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if _, err := s.agent.EnsureFiles(p); err != nil {
		writeDomainError(w, fmt.Errorf("create pet files: %w", err))
		return
	}
	content, err := s.fs.Read(id, petfs.FileSOUL)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	template, _ := s.fs.SoulTemplate(id)
	history, err := s.fs.SoulHistory(id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if history == nil {
		history = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pet_id":   id,
		"template": template,
		"locked":   s.fs.SoulLocked(id),
		"history":  history,
		"content":  content,
	})
}

type soulLockRequest struct {
	Locked bool `json:"locked"`
}

// handleSoulLock 锁定/解锁 SOUL.md：锁定后梦境整理不再演化性格。
func (s *Server) handleSoulLock(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := s.store.GetPet(r.Context(), id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	var req soulLockRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid JSON body")
		return
	}
	if _, err := s.agent.EnsureFiles(p); err != nil {
		writeDomainError(w, fmt.Errorf("create pet files: %w", err))
		return
	}
	if err := s.fs.SetSoulLocked(id, req.Locked); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pet_id": id,
		"locked": s.fs.SoulLocked(id),
	})
}

// handleMemory 返回宠物的 MEMORY.md 内容与日记文件列表。
func (s *Server) handleMemory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := s.store.GetPet(r.Context(), id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if _, err := s.agent.EnsureFiles(p); err != nil {
		writeDomainError(w, fmt.Errorf("create pet files: %w", err))
		return
	}
	mem, err := s.fs.Read(id, petfs.FileMemory)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	journals, err := s.fs.ListJournals(id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if journals == nil {
		journals = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pet_id":   id,
		"memory":   mem,
		"journals": journals,
	})
}

// handleSkills 返回宠物可见的技能列表（私有 learned/custom + 全局 global，同名私有优先）。
func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := s.store.GetPet(r.Context(), id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if _, err := s.agent.EnsureFiles(p); err != nil {
		writeDomainError(w, fmt.Errorf("create pet files: %w", err))
		return
	}
	skills, err := s.agent.SkillsFor(id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if skills == nil {
		skills = []agent.SkillInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pet_id": id,
		"skills": skills,
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
