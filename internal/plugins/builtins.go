// Package plugins 聚合内置玩法插件的构造与 YAML 配置应用。
//
// 新增内置插件时：在本包 Build 注册，并（如需）扩展 config.PluginsConfig；
// cmd/pocketpetd 只调用 Build，不必再写装配细节。仍是编译期可信模型，
// 不支持第三方二进制热插拔。
package plugins

import (
	"log/slog"
	"time"

	"github.com/lalolv/PocketPet/internal/config"
	"github.com/lalolv/PocketPet/internal/plugin"
	"github.com/lalolv/PocketPet/internal/plugins/adventure"
	"github.com/lalolv/PocketPet/internal/plugins/friends"
)

// Build 按配置构造已启用的内置插件（返回顺序即 Registry 装配顺序）。
// 段缺失或 enabled 未写时默认启用，数值用各插件代码内默认值。
func Build(cfg config.Config) []plugin.Plugin {
	var out []plugin.Plugin
	if cfg.AdventureEnabled() {
		adv := adventure.New()
		applyAdventure(adv, cfg.Plugins.Adventure)
		out = append(out, adv)
		slog.Info("plugin enabled", "name", "adventure")
	} else {
		slog.Info("plugin disabled", "name", "adventure")
	}
	if cfg.FriendsEnabled() {
		fr := friends.New()
		applyFriends(fr, cfg.Plugins.Friends)
		out = append(out, fr)
		slog.Info("plugin enabled", "name", "friends")
	} else {
		slog.Info("plugin disabled", "name", "friends")
	}
	return out
}

func applyAdventure(a *adventure.Adventure, c config.AdventurePluginConfig) {
	if c.MapRefreshTicks != nil && *c.MapRefreshTicks > 0 {
		a.MapRefreshTicks = *c.MapRefreshTicks
	}
	if c.NodeCount != nil && *c.NodeCount > 0 {
		a.NodeCount = *c.NodeCount
	}
	if c.MaxBranches != nil && *c.MaxBranches > 0 {
		a.MaxBranches = *c.MaxBranches
	}
	if c.ChestMinPct != nil {
		a.ChestMinPct = *c.ChestMinPct
	}
	if c.ChestMaxPct != nil {
		a.ChestMaxPct = *c.ChestMaxPct
	}
	if c.EnergyCost != nil {
		a.EnergyCost = *c.EnergyCost
	}
	if c.StepIntervalSeconds != nil {
		if *c.StepIntervalSeconds <= 0 {
			a.StepInterval = 0
		} else {
			a.StepInterval = time.Duration(*c.StepIntervalSeconds) * time.Second
		}
	}
}

func applyFriends(f *friends.Friends, c config.FriendsPluginConfig) {
	if c.VisitAffinity != nil {
		f.VisitAffinity = *c.VisitAffinity
	}
	if c.VisitHappy != nil {
		f.VisitHappy = *c.VisitHappy
	}
	if c.PeekAffinity != nil {
		f.PeekAffinity = *c.PeekAffinity
	}
	if c.GiftAffinity != nil {
		f.GiftAffinity = *c.GiftAffinity
	}
	if c.GiftHappy != nil {
		f.GiftHappy = *c.GiftHappy
	}
}
