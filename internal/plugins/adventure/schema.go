package adventure

import "github.com/lalolv/PocketPet/internal/store"

// Migrations 实现 plugin.SchemaProvider。
// v1：旧版倒计时探险；v2：图结构地图（丢掉旧状态表与背包表）；
// v3：地图主题（岛名/主题、地点描述/地带/要素，docs/08）。
func (a *Adventure) Migrations() []store.Migration {
	return []store.Migration{
		`CREATE TABLE adventure_active (
			pet_id     TEXT PRIMARY KEY,
			ticks_left INTEGER NOT NULL,
			started_at TEXT NOT NULL
		);
		CREATE TABLE adventure_items (
			pet_id TEXT NOT NULL,
			item   TEXT NOT NULL,
			count  INTEGER NOT NULL,
			PRIMARY KEY (pet_id, item)
		);`,
		`DROP TABLE IF EXISTS adventure_active;
		DROP TABLE IF EXISTS adventure_items;
		CREATE TABLE adventure_kv (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		CREATE TABLE adventure_maps (
			id         TEXT PRIMARY KEY,
			created_at TEXT NOT NULL,
			node_count INTEGER NOT NULL
		);
		CREATE TABLE adventure_nodes (
			map_id    TEXT NOT NULL,
			node_id   INTEGER NOT NULL,
			name      TEXT NOT NULL,
			has_chest INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (map_id, node_id)
		);
		CREATE TABLE adventure_edges (
			map_id TEXT NOT NULL,
			src    INTEGER NOT NULL,
			dst    INTEGER NOT NULL,
			PRIMARY KEY (map_id, src, dst)
		);
		CREATE TABLE adventure_runs (
			pet_id       TEXT PRIMARY KEY,
			map_id       TEXT NOT NULL,
			node_id      INTEGER NOT NULL,
			chests_found TEXT NOT NULL DEFAULT '[]',
			started_at   TEXT NOT NULL
		);`,
		`ALTER TABLE adventure_maps ADD COLUMN island_name TEXT NOT NULL DEFAULT '';
		ALTER TABLE adventure_maps ADD COLUMN theme TEXT NOT NULL DEFAULT '';
		ALTER TABLE adventure_nodes ADD COLUMN description TEXT NOT NULL DEFAULT '';
		ALTER TABLE adventure_nodes ADD COLUMN zone TEXT NOT NULL DEFAULT '';
		ALTER TABLE adventure_nodes ADD COLUMN elements TEXT NOT NULL DEFAULT '[]';`,
	}
}
