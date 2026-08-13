package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lalolv/PocketPet/internal/pet"
)

var t0 = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

func openMemory(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSaveGetRoundTrip(t *testing.T) {
	s := openMemory(t)
	ctx := context.Background()

	p := pet.New("p1", "团团", "cat", t0)
	p.Stats.Hunger = 66.5 // 确认小数衰减值在快照中不丢失
	p.Activity = pet.ActivitySleeping
	p.SyncSleepingFromActivity()
	p.Alerts.Hungry = true
	p.Intents = []string{pet.IntentSleep}
	p.StateSeq = 3
	if err := s.SavePet(ctx, p); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetPet(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, p) {
		t.Fatalf("got %+v, want %+v", got, p)
	}
}

func TestGetNotFound(t *testing.T) {
	s := openMemory(t)
	_, err := s.GetPet(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSaveUpdateAndList(t *testing.T) {
	s := openMemory(t)
	ctx := context.Background()

	for _, id := range []string{"a", "b"} {
		if err := s.SavePet(ctx, pet.New(id, id, "cat", t0)); err != nil {
			t.Fatal(err)
		}
	}
	updated := pet.New("a", "a", "cat", t0)
	updated.Stats.EXP = 42
	updated.Stage = pet.StageBaby
	if err := s.SavePet(ctx, updated); err != nil {
		t.Fatal(err)
	}

	pets, err := s.ListPets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pets) != 2 {
		t.Fatalf("list len = %d, want 2 (update must not duplicate)", len(pets))
	}
	got, err := s.GetPet(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Stats.EXP != 42 || got.Stage != pet.StageBaby {
		t.Fatalf("update not applied: %+v", got)
	}
}

func TestEventsAppendAndRecent(t *testing.T) {
	s := openMemory(t)
	ctx := context.Background()

	for i, typ := range []string{"pet.born", "pet.hungry", "pet.dirty"} {
		id, err := s.AppendEvent(ctx, pet.Event{
			PetID: "p1", Type: typ, Message: "m", CreatedAt: t0.Add(time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		if id != int64(i+1) {
			t.Fatalf("event id = %d, want %d", id, i+1)
		}
	}
	// 其它宠物的事件不应混入
	if _, err := s.AppendEvent(ctx, pet.Event{PetID: "p2", Type: "pet.born", Message: "x", CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}

	evs, err := s.RecentEvents(ctx, "p1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Type != "pet.hungry" || evs[1].Type != "pet.dirty" {
		t.Fatalf("recent events = %+v", evs)
	}
	if !evs[0].CreatedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("created_at roundtrip failed: %v", evs[0].CreatedAt)
	}
}

// TestMigrateIdempotent 用临时文件库验证迁移可重复执行（重开不报错、数据保留）。
func TestMigrateIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	ctx := context.Background()

	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.SavePet(ctx, pet.New("p1", "团团", "cat", t0)); err != nil {
		t.Fatal(err)
	}
	s1.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after migrate: %v", err)
	}
	defer s2.Close()
	if _, err := s2.GetPet(ctx, "p1"); err != nil {
		t.Fatalf("data lost after reopen: %v", err)
	}
}

// TestPluginMigrations 验证插件迁移的独立版本命名空间（M5）。
func TestPluginMigrations(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	migs := []Migration{`CREATE TABLE adv_test (id TEXT PRIMARY KEY);`}
	if err := st.RunPluginMigrations("adv", migs); err != nil {
		t.Fatal(err)
	}
	// 表已建
	if _, err := st.DB().Exec(`INSERT INTO adv_test (id) VALUES ('x')`); err != nil {
		t.Fatal(err)
	}
	// 重复执行幂等
	if err := st.RunPluginMigrations("adv", migs); err != nil {
		t.Fatal(err)
	}
	// 另一插件独立计版本
	if err := st.RunPluginMigrations("friends", []Migration{`CREATE TABLE fr_test (id TEXT PRIMARY KEY);`}); err != nil {
		t.Fatal(err)
	}
	// 版本号互不相干：kv_meta 里各有记录，核心 schema_version 仍是核心迁移数
	var v string
	if err := st.DB().QueryRow(`SELECT value FROM kv_meta WHERE key = 'plugin:adv'`).Scan(&v); err != nil || v != "1" {
		t.Fatalf("plugin:adv version = %q, %v", v, err)
	}
	var core string
	if err := st.DB().QueryRow(`SELECT value FROM kv_meta WHERE key = 'schema_version'`).Scan(&core); err != nil {
		t.Fatal(err)
	}
	if core != "1" {
		t.Fatalf("core schema_version = %q, want 1", core)
	}
	// 插件迁移追加第二条：只执行增量
	migs2 := append(migs, `ALTER TABLE adv_test ADD COLUMN n INTEGER DEFAULT 0;`)
	if err := st.RunPluginMigrations("adv", migs2); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO adv_test (id, n) VALUES ('y', 1)`); err != nil {
		t.Fatal(err)
	}
}
