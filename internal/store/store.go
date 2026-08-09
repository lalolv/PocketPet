// Package store 负责 SQLite 持久化：pets 表存宠物状态 JSON 快照（外加关键标量冗余），
// pet_events 表存领域事件流水，kv_meta 表记录 schema 版本用于迁移。
// 驱动使用纯 Go 的 modernc.org/sqlite（无 cgo）。
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/lalolv/PocketPet/internal/pet"
)

// ErrNotFound 表示请求的宠物不存在。
var ErrNotFound = errors.New("pet not found")

// Store 是 SQLite 持久化层。
type Store struct {
	db *sql.DB
}

// Open 打开（必要时创建）path 处的 SQLite 数据库并执行迁移。
// path 传 ":memory:" 时使用内存库（测试用）。
func Open(path string) (*Store, error) {
	dsn := path
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
		dsn = "file:" + path
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite 单写者；串行化连接可避免 SQLITE_BUSY，对本地小库足够。
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		db.Close()
		return nil, fmt.Errorf("set pragma: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// Close 关闭数据库连接。
func (s *Store) Close() error { return s.db.Close() }

// DB 返回底层数据库连接。仅供编译期引入的可信插件使用（设计文档 8.4 信任边界）；
// 插件表的读写经此进行，建表走 RunPluginMigrations。
func (s *Store) DB() *sql.DB { return s.db }

// Migration 是一条迁移（建表/改表 SQL 语句）。
type Migration = string

// migrations 是按版本号递增的建表语句；版本号 = 已执行的迁移数量。
var migrations = []string{
	`CREATE TABLE pets (
		id         TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		species    TEXT NOT NULL,
		stage      TEXT NOT NULL,
		alive      INTEGER NOT NULL,
		exp        INTEGER NOT NULL,
		state      TEXT NOT NULL,      -- pet.Pet 完整 JSON 快照
		updated_at TEXT NOT NULL
	);
	CREATE TABLE pet_events (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		pet_id     TEXT NOT NULL,
		type       TEXT NOT NULL,
		message    TEXT NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE INDEX idx_pet_events_pet ON pet_events(pet_id, id);`,
}

// migrate 执行核心 schema 迁移（kv_meta key "schema_version"）。
func (s *Store) migrate() error {
	return s.runMigrations("schema_version", migrations)
}

// RunPluginMigrations 执行某插件的迁移。插件版本号用 kv_meta 里独立的
// "plugin:<name>" key 记录，与核心 schema 版本互不影响（M5 插件体系）。
func (s *Store) RunPluginMigrations(plugin string, migrations []Migration) error {
	return s.runMigrations("plugin:"+plugin, migrations)
}

// runMigrations 按 key 记录的版本号顺序执行未应用的迁移。
func (s *Store) runMigrations(key string, migrations []string) error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS kv_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return err
	}
	var version int
	row := s.db.QueryRow(`SELECT value FROM kv_meta WHERE key = ?`, key)
	var v string
	switch err := row.Scan(&v); {
	case errors.Is(err, sql.ErrNoRows):
		version = 0
	case err != nil:
		return err
	default:
		if _, err := fmt.Sscanf(v, "%d", &version); err != nil {
			return fmt.Errorf("bad %s %q: %w", key, v, err)
		}
	}
	for i := version; i < len(migrations); i++ {
		if _, err := s.db.Exec(migrations[i]); err != nil {
			return fmt.Errorf("migration %s %d: %w", key, i+1, err)
		}
		if _, err := s.db.Exec(
			`INSERT INTO kv_meta (key, value) VALUES (?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, fmt.Sprint(i+1)); err != nil {
			return err
		}
	}
	return nil
}

// SavePet 插入或更新一只宠物（状态以 JSON 快照整体持久化）。
func (s *Store) SavePet(ctx context.Context, p *pet.Pet) error {
	state, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO pets (id, name, species, stage, alive, exp, state, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			name = excluded.name, species = excluded.species, stage = excluded.stage,
			alive = excluded.alive, exp = excluded.exp,
			state = excluded.state, updated_at = excluded.updated_at`,
		p.ID, p.Name, p.Species, string(p.Stage), p.Alive, p.Stats.EXP,
		string(state), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// GetPet 按 ID 读取宠物。
func (s *Store) GetPet(ctx context.Context, id string) (*pet.Pet, error) {
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT state FROM pets WHERE id = ?`, id).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var p pet.Pet
	if err := json.Unmarshal([]byte(state), &p); err != nil {
		return nil, fmt.Errorf("decode pet %s: %w", id, err)
	}
	return &p, nil
}

// ListPets 返回全部宠物（按创建顺序）。
func (s *Store) ListPets(ctx context.Context) ([]*pet.Pet, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT state FROM pets ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*pet.Pet
	for rows.Next() {
		var state string
		if err := rows.Scan(&state); err != nil {
			return nil, err
		}
		var p pet.Pet
		if err := json.Unmarshal([]byte(state), &p); err != nil {
			return nil, fmt.Errorf("decode pet: %w", err)
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// AppendEvent 追加一条领域事件，返回自增 ID。
func (s *Store) AppendEvent(ctx context.Context, e pet.Event) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO pet_events (pet_id, type, message, created_at) VALUES (?, ?, ?, ?)`,
		e.PetID, e.Type, e.Message, e.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RecentEvents 返回某宠物最近 limit 条事件，按时间正序（最旧在前）。
func (s *Store) RecentEvents(ctx context.Context, petID string, limit int) ([]pet.Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, pet_id, type, message, created_at FROM (
			SELECT id, pet_id, type, message, created_at FROM pet_events
			WHERE pet_id = ? ORDER BY id DESC LIMIT ?
		) ORDER BY id`, petID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pet.Event
	for rows.Next() {
		var e pet.Event
		var created string
		if err := rows.Scan(&e.ID, &e.PetID, &e.Type, &e.Message, &created); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
			e.CreatedAt = t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
