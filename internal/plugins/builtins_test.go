package plugins_test

import (
	"testing"
	"time"

	"github.com/lalolv/PocketPet/internal/config"
	"github.com/lalolv/PocketPet/internal/plugin"
	"github.com/lalolv/PocketPet/internal/plugins"
	"github.com/lalolv/PocketPet/internal/plugins/adventure"
	"github.com/lalolv/PocketPet/internal/plugins/friends"
)

func TestBuildDefaults(t *testing.T) {
	got := plugins.Build(config.Config{})
	if len(got) != 2 || got[0].Name() != "adventure" || got[1].Name() != "friends" {
		t.Fatalf("Build defaults = %v", pluginNames(got))
	}
}

func TestBuildDisabledAndOverrides(t *testing.T) {
	off := false
	on := true
	refresh := 9
	visit := 7.0
	cfg := config.Config{
		Plugins: config.PluginsConfig{
			Adventure: config.AdventurePluginConfig{
				Enabled:                   &off,
				MapRefreshIntervalSeconds: &refresh,
			},
			Friends: config.FriendsPluginConfig{
				VisitAffinity: &visit,
			},
		},
	}
	got := plugins.Build(cfg)
	if len(got) != 1 || got[0].Name() != "friends" {
		t.Fatalf("Build = %v", pluginNames(got))
	}
	fr, ok := got[0].(*friends.Friends)
	if !ok || fr.VisitAffinity != 7 {
		t.Fatalf("friends override = %#v", got[0])
	}

	cfg.Plugins.Adventure.Enabled = &on
	got = plugins.Build(cfg)
	if len(got) != 2 {
		t.Fatalf("Build both = %v", pluginNames(got))
	}
	adv, ok := got[0].(*adventure.Adventure)
	if !ok || adv.MapRefreshInterval != 9*time.Second {
		t.Fatalf("adventure override = %#v", got[0])
	}
}

func pluginNames(ps []plugin.Plugin) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name()
	}
	return out
}
