package tui

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalolv/PocketPet/internal/agent"
	"github.com/lalolv/PocketPet/internal/api"
	"github.com/lalolv/PocketPet/internal/llm"
	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
	"github.com/lalolv/PocketPet/internal/store"
	"github.com/lalolv/PocketPet/internal/tick"
)

var t0 = time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

// testServer 是一套内存态服务端（无 LLM，chat 走降级文案）。
type testServer struct {
	srv   *httptest.Server
	clock *pet.FakeClock
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clock := pet.NewFakeClock(t0)
	hub := api.NewHub()
	fs := petfs.New(filepath.Join(t.TempDir(), "data"))
	engine := tick.NewEngine(st, tick.MultiSink{hub, agent.NewStageSync(fs, st)}, time.Minute, 24*time.Hour, clock)
	ag := agent.New(engine, fs, llm.ProviderConfig{})
	srv := httptest.NewServer(api.NewServer(st, engine, hub, fs, ag).Handler())
	t.Cleanup(srv.Close)
	return &testServer{srv: srv, clock: clock}
}

func TestClientREST(t *testing.T) {
	ts := newTestServer(t)
	c := NewClient(ts.srv.URL)
	ctx := context.Background()

	// 空列表
	pets, err := c.ListPets(ctx)
	if err != nil || len(pets) != 0 {
		t.Fatalf("list = %v, %v", pets, err)
	}

	// 创建（指定性格）
	p, err := c.CreatePet(ctx, "团团", "cat", "傲娇")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == "" || p.Personality != "tsundere" || p.Stats.Hunger != 70 {
		t.Fatalf("created = %+v", p)
	}

	// 列表 / 单查
	pets, err = c.ListPets(ctx)
	if err != nil || len(pets) != 1 {
		t.Fatalf("list = %v, %v", pets, err)
	}
	got, err := c.GetPet(ctx, p.ID)
	if err != nil || got.Name != "团团" {
		t.Fatalf("get = %+v, %v", got, err)
	}

	// care 成功
	fed, err := c.Care(ctx, p.ID, "feed")
	if err != nil || fed.Stats.Hunger != 90 {
		t.Fatalf("feed = %+v, %v", fed, err)
	}

	// care 业务错误：sleep 后再 sleep → invalid_state
	if _, err := c.Care(ctx, p.ID, "sleep"); err != nil {
		t.Fatal(err)
	}
	_, err = c.Care(ctx, p.ID, "sleep")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != "invalid_state" {
		t.Fatalf("sleep twice err = %v", err)
	}

	// chat（降级文案也有回复）
	if _, err := c.Care(ctx, p.ID, "wake"); err != nil {
		t.Fatal(err)
	}
	reply, err := c.Chat(ctx, p.ID, "你好")
	if err != nil || reply == "" {
		t.Fatalf("chat = %q, %v", reply, err)
	}

	// 不存在
	_, err = c.GetPet(ctx, "nope")
	if !errors.As(err, &ae) || ae.Code != "not_found" {
		t.Fatalf("get nope err = %v", err)
	}
}

func TestClientWatchEvents(t *testing.T) {
	ts := newTestServer(t)
	c := NewClient(ts.srv.URL)
	ctx := context.Background()

	p, err := c.CreatePet(ctx, "团团", "cat", "")
	if err != nil {
		t.Fatal(err)
	}

	watchCtx, cancel := context.WithCancel(ctx)
	ch := make(chan Event, 8)
	done := make(chan error, 1)
	go func() { done <- c.WatchEvents(watchCtx, p.ID, ch) }()

	// 回放：pet.born 应先到
	select {
	case ev := <-ch:
		if ev.Type != "pet.born" {
			t.Fatalf("first event = %q", ev.Type)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no replay event")
	}

	// 实时事件：推假时钟 12h，GetPet 触发结算 → pet.hungry
	ts.clock.Advance(12 * time.Hour)
	if _, err := c.GetPet(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		if ev.Type != "pet.hungry" || ev.Message == "" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no hungry event")
	}

	// ctx 取消后 WatchEvents 返回 nil
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watch err = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watch did not return after cancel")
	}
}
