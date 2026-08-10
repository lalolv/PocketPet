package api

import (
	"log/slog"
	"sync"

	"github.com/lalolv/PocketPet/internal/pet"
)

// sseFrame 是 SSE 推送的一帧：领域事件（name 为事件类型）或状态快照（name 为 "state"）。
type sseFrame struct {
	name string // SSE event 名
	data any    // pet.Event 或 stateView
}

// Hub 是 SSE 推送中心：按 petID 维护订阅者，Publish/PublishState 非阻塞广播。
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan sseFrame]struct{}
}

// NewHub 创建空的推送中心。
func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[chan sseFrame]struct{})}
}

// Subscribe 订阅某宠物的推送（事件 + 状态快照），返回缓冲 channel 与取消函数。
func (h *Hub) Subscribe(petID string) (<-chan sseFrame, func()) {
	ch := make(chan sseFrame, 16)
	h.mu.Lock()
	if h.subs[petID] == nil {
		h.subs[petID] = make(map[chan sseFrame]struct{})
	}
	h.subs[petID][ch] = struct{}{}
	n := len(h.subs[petID])
	h.mu.Unlock()
	slog.Debug("sse: subscriber connected", "pet", petID, "subscribers", n)
	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if set, ok := h.subs[petID]; ok {
			if _, ok := set[ch]; ok {
				delete(set, ch)
				close(ch)
				slog.Debug("sse: subscriber disconnected", "pet", petID, "subscribers", len(set))
			}
			if len(set) == 0 {
				delete(h.subs, petID)
			}
		}
	}
	return ch, cancel
}

// Publish 把领域事件广播给该宠物的全部订阅者；订阅者消费缓慢时丢弃而非阻塞。
// 实现 tick.EventSink。
func (h *Hub) Publish(e pet.Event) {
	h.broadcast(e.PetID, sseFrame{name: e.Type, data: e})
}

// PublishState 把状态快照广播给该宠物的全部订阅者（同样非阻塞、可丢弃）。
// 快照不落库、不回放：订阅建立时服务端会补发当前快照，断线重连天然拿到最新态。
func (h *Hub) PublishState(v stateView) {
	h.broadcast(v.ID, sseFrame{name: "state", data: v})
}

// broadcast 非阻塞地向该宠物的全部订阅者投递一帧。
func (h *Hub) broadcast(petID string, f sseFrame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[petID] {
		select {
		case ch <- f:
		default:
		}
	}
}
