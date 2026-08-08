package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	adka2a "google.golang.org/adk/v2/server/adka2a/v2"
	"google.golang.org/adk/v2/session"

	"pocketpet/internal/llm"
	"pocketpet/internal/store"
)

// a2aPrefix 是 A2A 端点的 URL 前缀；宠物 A2A 暴露在 /a2a/pets/{id}/ 下：
//   - GET  .well-known/agent-card.json  agent card（发现，无需 LLM）
//   - POST message:send / message:stream / tasks/...  A2A 协议端点（HTTP+JSON 绑定）
const a2aPrefix = "/a2a/pets/"

// a2aEntry 缓存一只宠物的 A2A 协议 handler；agent 重建（AGENT.md 变更）后随之重建。
type a2aEntry struct {
	ag      adkagent.Agent
	handler http.Handler
}

// handleA2A 把 /a2a/pets/{id}/ 子树分发给该宠物的 A2A handler。
func (s *Server) handleA2A(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rest := strings.TrimPrefix(r.URL.Path, a2aPrefix+id)

	// agent card：发现元信息，不要求 LLM 可用。
	if rest == "/.well-known/agent-card.json" {
		s.serveAgentCard(w, r, id)
		return
	}

	// 协议端点：需要装配出宠物的 llmagent。
	ag, err := s.agent.AgentFor(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeDomainError(w, err)
		return
	case errors.Is(err, llm.ErrNotConfigured):
		writeError(w, http.StatusServiceUnavailable, codeLLMMissing,
			"a2a messaging requires an LLM provider, which is not configured")
		return
	case err != nil:
		writeDomainError(w, err)
		return
	}

	h := s.a2aHandlerFor(id, ag)
	http.StripPrefix(a2aPrefix+id, h).ServeHTTP(w, r)
}

// serveAgentCard 输出该宠物的 A2A agent card。
func (s *Server) serveAgentCard(w http.ResponseWriter, r *http.Request, id string) {
	p, err := s.store.GetPet(r.Context(), id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	base := scheme + "://" + r.Host + a2aPrefix + id
	card := &a2a.AgentCard{
		Name:        p.Name,
		Description: fmt.Sprintf("PocketPet 口袋宠物「%s」（%s）：以第一人称对话的虚拟宠物 Agent。", p.Name, p.Species),
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(base, a2a.TransportProtocolHTTPJSON),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Capabilities:       a2a.AgentCapabilities{Streaming: true},
		Version:            "m4",
		Skills: []a2a.AgentSkill{{
			ID:          "first-person-chat",
			Name:        "第一人称宠物对话",
			Description: "和这只宠物直接对话；它以第一人称回应，可能自主吃饭/玩耍/睡觉，也会记住重要的事。",
			Tags:        []string{"pet", "chat"},
		}},
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(card); err != nil {
		// 头已写出，仅记录。
		_ = err
	}
}

// a2aHandlerFor 返回该宠物的 A2A 协议 handler（executor + REST 绑定），按 agent 实例缓存。
func (s *Server) a2aHandlerFor(id string, ag adkagent.Agent) http.Handler {
	s.a2aMu.Lock()
	defer s.a2aMu.Unlock()
	if e, ok := s.a2aHandlers[id]; ok && e.ag == ag {
		return e.handler
	}
	executor := adka2a.NewExecutor(adka2a.ExecutorConfig{
		RunnerConfig: runner.Config{
			AppName:        "pocketpet-a2a",
			Agent:          ag,
			SessionService: session.InMemoryService(),
		},
	})
	mux := http.NewServeMux()
	mux.Handle("/", a2asrv.NewRESTHandler(a2asrv.NewHandler(executor)))
	s.a2aHandlers[id] = &a2aEntry{ag: ag, handler: mux}
	return mux
}
