package plugin

import (
	"context"
	"log/slog"
	"testing"
	"time"

	adktool "google.golang.org/adk/v2/tool"

	"pocketpet/internal/pet"
	"pocketpet/internal/store"
)

// fakePlugin 实现全部能力接口，用于验证 Registry 的发现与收集。
type fakePlugin struct {
	name       string
	inited     bool
	migrations []store.Migration
	tools      []adktool.Tool
	events     []pet.Event
	ticks      int
	routes     []Route
}

func (f *fakePlugin) Name() string                  { return f.name }
func (f *fakePlugin) Init(ctx PluginContext) error  { f.inited = true; return nil }
func (f *fakePlugin) Migrations() []store.Migration { return f.migrations }
func (f *fakePlugin) Tools() []adktool.Tool         { return f.tools }
func (f *fakePlugin) OnEvent(_ context.Context, e pet.Event) {
	f.events = append(f.events, e)
}
func (f *fakePlugin) OnTick(context.Context, time.Time) { f.ticks++ }
func (f *fakePlugin) Routes() []Route                   { return f.routes }

// plainPlugin 只实现核心接口。
type plainPlugin struct{ name string }

func (p *plainPlugin) Name() string                 { return p.name }
func (p *plainPlugin) Init(ctx PluginContext) error { return nil }

func TestRegistryDiscovery(t *testing.T) {
	full := &fakePlugin{
		name:       "full",
		migrations: []store.Migration{"CREATE TABLE x (id TEXT);"},
		routes:     []Route{{Method: "GET", Pattern: "/ping"}},
	}
	plain := &plainPlugin{name: "plain"}
	r := NewRegistry(full, plain)

	// 迁移：只收集实现了 SchemaProvider 的
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := r.RunMigrations(st); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO x (id) VALUES ('1')`); err != nil {
		t.Fatal("plugin migration not applied")
	}

	// InitAll
	if err := r.InitAll(NewPluginContext(nil, nil, nil, slog.Default())); err != nil {
		t.Fatal(err)
	}
	if !full.inited {
		t.Fatal("full plugin not inited")
	}

	// 能力收集
	if got := len(r.Tools()); got != 0 { // fakePlugin.Tools 返回 nil
		t.Fatalf("tools = %d", got)
	}
	if got := len(r.EventSinks()); got != 1 {
		t.Fatalf("sinks = %d, want 1", got)
	}
	r.EventSinks()[0].Publish(pet.Event{PetID: "p1", Type: "test"})
	if len(full.events) != 1 {
		t.Fatal("subscriber not receiving events via sink adapter")
	}
	if got := len(r.TickHooks()); got != 1 {
		t.Fatalf("hooks = %d, want 1", got)
	}
	routes := r.Routes()
	if len(routes) != 1 || routes[0].Plugin != "full" || len(routes[0].Routes) != 1 {
		t.Fatalf("routes = %+v", routes)
	}
}
