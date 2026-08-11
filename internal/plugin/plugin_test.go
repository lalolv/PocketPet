package plugin

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	adktool "google.golang.org/adk/v2/tool"

	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/store"
)

// fakePlugin 实现全部能力接口，用于验证 Registry 的发现与收集。
type fakePlugin struct {
	name       string
	inited     bool
	shutdown   bool
	migrations []store.Migration
	tools      []adktool.Tool
	events     []pet.Event
	eventCtxs  []context.Context
	ticks      int
	routes     []Route
	dependsOn  []string
}

func (f *fakePlugin) Name() string                 { return f.name }
func (f *fakePlugin) Init(ctx PluginContext) error { f.inited = true; return nil }
func (f *fakePlugin) Shutdown(context.Context) error {
	f.shutdown = true
	return nil
}
func (f *fakePlugin) Migrations() []store.Migration { return f.migrations }
func (f *fakePlugin) Tools() []adktool.Tool         { return f.tools }
func (f *fakePlugin) OnEvent(ctx context.Context, e pet.Event) {
	f.events = append(f.events, e)
	f.eventCtxs = append(f.eventCtxs, ctx)
}
func (f *fakePlugin) OnTick(context.Context, time.Time) { f.ticks++ }
func (f *fakePlugin) Routes() []Route                   { return f.routes }
func (f *fakePlugin) DependsOn() []string               { return f.dependsOn }

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

	if !r.Has("full") || !r.Has("plain") || r.Has("missing") {
		t.Fatalf("Has mismatch: full=%v plain=%v missing=%v", r.Has("full"), r.Has("plain"), r.Has("missing"))
	}

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

	if err := r.InitAll(NewPluginContext(nil, nil, nil, slog.Default(), r)); err != nil {
		t.Fatal(err)
	}
	if !full.inited {
		t.Fatal("full plugin not inited")
	}

	if got := len(r.Tools()); got != 0 {
		t.Fatalf("tools = %d", got)
	}
	if got := len(r.EventSinks()); got != 1 {
		t.Fatalf("sinks = %d, want 1", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.SetEventContext(ctx)
	r.EventSinks()[0].Publish(pet.Event{PetID: "p1", Type: "test"})
	if len(full.events) != 1 {
		t.Fatal("subscriber not receiving events via sink adapter")
	}
	if full.eventCtxs[0] != ctx {
		t.Fatal("subscriber did not receive registry event context")
	}
	cancel()

	if got := len(r.TickHooks()); got != 1 {
		t.Fatalf("hooks = %d, want 1", got)
	}
	routes := r.Routes()
	if len(routes) != 1 || routes[0].Plugin != "full" || len(routes[0].Routes) != 1 {
		t.Fatalf("routes = %+v", routes)
	}

	if err := r.ShutdownAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !full.shutdown {
		t.Fatal("full plugin not shut down")
	}
}

func TestRegistryHardDeps(t *testing.T) {
	dependent := &fakePlugin{name: "needs-x", dependsOn: []string{"x"}}
	r := NewRegistry(dependent)
	err := r.InitAll(NewPluginContext(nil, nil, nil, slog.Default(), r))
	if err == nil {
		t.Fatal("expected missing dependency error")
	}
	if !strings.Contains(err.Error(), "needs-x") || !strings.Contains(err.Error(), `"x"`) {
		t.Fatalf("err = %v", err)
	}

	ok := NewRegistry(&plainPlugin{name: "x"}, dependent)
	if err := ok.InitAll(NewPluginContext(nil, nil, nil, slog.Default(), ok)); err != nil {
		t.Fatal(err)
	}
}

func TestHasPluginSoftDep(t *testing.T) {
	adv := &plainPlugin{name: "adventure"}
	r := NewRegistry(adv)
	ctx := NewPluginContext(nil, nil, nil, slog.Default(), r)
	if !ctx.HasPlugin("adventure") {
		t.Fatal("expected HasPlugin adventure")
	}
	if ctx.HasPlugin("friends") {
		t.Fatal("unexpected friends")
	}
	empty := NewPluginContext(nil, nil, nil, slog.Default(), nil)
	if empty.HasPlugin("adventure") {
		t.Fatal("nil registry should report false")
	}
}

type namedInitPlugin struct {
	name string
	got  string
}

func (p *namedInitPlugin) Name() string { return p.name }
func (p *namedInitPlugin) Init(ctx PluginContext) error {
	p.got = ctx.PluginName()
	return nil
}

func TestInitSetsPluginName(t *testing.T) {
	p := &namedInitPlugin{name: "demo"}
	r := NewRegistry(p)
	if err := r.InitAll(NewPluginContext(nil, nil, nil, slog.Default(), r)); err != nil {
		t.Fatal(err)
	}
	if p.got != "demo" {
		t.Fatalf("PluginName = %q", p.got)
	}
}

func TestTimedTickHookRunsInner(t *testing.T) {
	fast := &fakePlugin{name: "fast"}
	r := NewRegistry(fast)
	hooks := r.TickHooks()
	if len(hooks) != 1 {
		t.Fatalf("hooks = %d", len(hooks))
	}
	hooks[0].OnTick(context.Background(), time.Now())
	if fast.ticks != 1 {
		t.Fatalf("ticks = %d", fast.ticks)
	}
}
