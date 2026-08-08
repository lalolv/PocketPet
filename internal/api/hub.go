package api

import (
	"sync"

	"pocketpet/internal/pet"
)

// Hub 是 SSE 事件推送中心：按 petID 维护订阅者，Publish 非阻塞广播。
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan pet.Event]struct{}
}

// NewHub 创建空的推送中心。
func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[chan pet.Event]struct{})}
}

// Subscribe 订阅某宠物的事件，返回缓冲 channel 与取消函数。
func (h *Hub) Subscribe(petID string) (<-chan pet.Event, func()) {
	ch := make(chan pet.Event, 16)
	h.mu.Lock()
	if h.subs[petID] == nil {
		h.subs[petID] = make(map[chan pet.Event]struct{})
	}
	h.subs[petID][ch] = struct{}{}
	h.mu.Unlock()
	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if set, ok := h.subs[petID]; ok {
			if _, ok := set[ch]; ok {
				delete(set, ch)
				close(ch)
			}
			if len(set) == 0 {
				delete(h.subs, petID)
			}
		}
	}
	return ch, cancel
}

// Publish 把事件广播给该宠物的全部订阅者；订阅者消费缓慢时丢弃而非阻塞。
func (h *Hub) Publish(e pet.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[e.PetID] {
		select {
		case ch <- e:
		default:
		}
	}
}
