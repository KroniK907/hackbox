// Package store opens host.sqlite and holds host/Lobby persistence.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// FileName is the host database file inside the data dir.
const FileName = "host.sqlite"

// ErrAdminHashExists is returned when Finish has already written a hash.
var ErrAdminHashExists = errors.New("store: admin password hash already exists")

// ErrAdminHashMissing is returned when no setup password has been stored.
var ErrAdminHashMissing = errors.New("store: admin password hash does not exist")

// DB is an open host.sqlite handle.
type DB struct {
	sql *sql.DB
	dir string
}

// DefaultDataDir is os.UserCacheDir() plus hackbox.
func DefaultDataDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("store: user cache dir: %w", err)
	}
	return filepath.Join(cache, "hackbox"), nil
}

// Open opens host.sqlite in dir with WAL, a busy timeout, foreign keys,
// and MaxOpenConns=1. It creates the file and v1 tables if they are missing.
func Open(dir string) (*DB, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, FileName)
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)", filepath.ToSlash(path))
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	db := &DB{sql: sqlDB, dir: dir}
	if err := db.ensureSchema(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// Close releases the database handle.
func (db *DB) Close() error {
	if db == nil || db.sql == nil {
		return nil
	}
	return db.sql.Close()
}

// SQL returns the shared host database handle for host-owned persistence
// packages. Callers must not close it.
func (db *DB) SQL() *sql.DB {
	return db.sql
}

// Dir is the data directory that holds host.sqlite and host.log.
func (db *DB) Dir() string {
	return db.dir
}

// HasAdminHash reports whether Finish has already stored a password hash.
func (db *DB) HasAdminHash(ctx context.Context) (bool, error) {
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_password WHERE id = 1`).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: has admin hash: %w", err)
	}
	return n > 0, nil
}

// AdminHash returns the stored admin password hash.
func (db *DB) AdminHash(ctx context.Context) (string, error) {
	var hash string
	err := db.sql.QueryRowContext(ctx, `SELECT hash FROM admin_password WHERE id = 1`).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrAdminHashMissing
	}
	if err != nil {
		return "", fmt.Errorf("store: read admin hash: %w", err)
	}
	return hash, nil
}

// FinishSetup stores the first admin hash and its initial server-side session
// in one transaction. A later call fails with ErrAdminHashExists.
func (db *DB) FinishSetup(ctx context.Context, hash, sessionID string) error {
	return db.FinishSetupWith(ctx, hash, sessionID, nil)
}

// FinishSetupWith is FinishSetup plus extra work in the same transaction,
// used to persist setup form knobs next to the password hash.
func (db *DB) FinishSetupWith(ctx context.Context, hash, sessionID string, extra func(*sql.Tx) error) error {
	if hash == "" {
		return errors.New("store: empty admin hash")
	}
	if sessionID == "" {
		return errors.New("store: empty admin session ID")
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin finish setup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_password WHERE id = 1`).Scan(&n); err != nil {
		return fmt.Errorf("store: check admin hash: %w", err)
	}
	if n > 0 {
		return ErrAdminHashExists
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO admin_password (id, hash) VALUES (1, ?)`, hash); err != nil {
		return fmt.Errorf("store: insert admin hash: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO admin_session (id, kind) VALUES (?, 'operator')`, sessionID); err != nil {
		return fmt.Errorf("store: insert admin session: %w", err)
	}
	if extra != nil {
		if err := extra(tx); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit finish setup: %w", err)
	}
	return nil
}

// KVGet returns an opaque value for gameID. ok is false when the key is missing.
func (db *DB) KVGet(ctx context.Context, gameID, key string) (value []byte, ok bool, err error) {
	var stored []byte
	err = db.sql.QueryRowContext(
		ctx,
		`SELECT value FROM game_kv WHERE game_id = ? AND key = ?`,
		gameID,
		key,
	).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: kv get: %w", err)
	}
	return stored, true, nil
}

// KVSet writes an opaque value for gameID. Host does not parse the bytes.
func (db *DB) KVSet(ctx context.Context, gameID, key string, value []byte) error {
	if gameID == "" || key == "" {
		return errors.New("store: empty game kv key")
	}
	if value == nil {
		value = []byte{}
	}
	_, err := db.sql.ExecContext(
		ctx,
		`INSERT INTO game_kv (game_id, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(game_id, key) DO UPDATE SET value = excluded.value`,
		gameID,
		key,
		value,
	)
	if err != nil {
		return fmt.Errorf("store: kv set: %w", err)
	}
	return nil
}

// KVDeleteGame removes every key for gameID. Other game ids stay.
func (db *DB) KVDeleteGame(ctx context.Context, gameID string) error {
	if gameID == "" {
		return errors.New("store: empty game id")
	}
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM game_kv WHERE game_id = ?`, gameID); err != nil {
		return fmt.Errorf("store: kv delete game: %w", err)
	}
	return nil
}

// HasAdminSession reports whether sessionID identifies a stored admin session.
func (db *DB) HasAdminSession(ctx context.Context, sessionID string) (bool, error) {
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_session WHERE id = ?`, sessionID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: has admin session: %w", err)
	}
	return n > 0, nil
}

func (db *DB) ensureSchema() error {
	_, err := db.sql.Exec(`
CREATE TABLE IF NOT EXISTS schema_version (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	version INTEGER NOT NULL
);
INSERT OR IGNORE INTO schema_version (id, version) VALUES (1, 1);
CREATE TABLE IF NOT EXISTS admin_password (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	hash TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS admin_session (
	id TEXT PRIMARY KEY CHECK (length(id) > 0),
	kind TEXT NOT NULL CHECK (kind IN ('operator', 'host-phone'))
);
CREATE TABLE IF NOT EXISTS game_kv (
	game_id TEXT NOT NULL CHECK (length(game_id) > 0),
	key TEXT NOT NULL CHECK (length(key) > 0),
	value BLOB NOT NULL,
	PRIMARY KEY (game_id, key)
);
`)
	if err != nil {
		return fmt.Errorf("store: schema: %w", err)
	}
	return nil
}
