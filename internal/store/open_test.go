package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCreatesSQLiteWithWALAndForeignKeys(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := os.Stat(filepath.Join(dir, "host.sqlite")); err != nil {
		t.Fatalf("host.sqlite missing: %v", err)
	}

	var journal string
	if err := db.sql.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journal)
	}

	var fk int
	if err := db.sql.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys = %d, want 1", fk)
	}

	if n := db.sql.Stats().MaxOpenConnections; n != 1 {
		t.Fatalf("MaxOpenConns = %d, want 1", n)
	}
}

func TestFinishSetupFirstWins(t *testing.T) {
	t.Parallel()
	db := openTemp(t)
	ctx := context.Background()

	ok, err := db.HasAdminHash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected no hash on a new store")
	}

	if err := db.FinishSetup(ctx, "hash-one", "session-one"); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishSetup(ctx, "hash-two", "session-two"); !errors.Is(err, ErrAdminHashExists) {
		t.Fatalf("second write: %v, want ErrAdminHashExists", err)
	}

	ok, err = db.HasAdminHash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected hash after first write")
	}
	hash, err := db.AdminHash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "hash-one" {
		t.Fatalf("hash = %q, want hash-one", hash)
	}
	ok, err = db.HasAdminSession(ctx, "session-one")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected first session to exist")
	}
	ok, err = db.HasAdminSession(ctx, "session-two")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("second session must not exist")
	}
}

func TestFinishSetupWithRunsExtraInSameTransaction(t *testing.T) {
	t.Parallel()
	db := openTemp(t)
	ctx := context.Background()
	err := db.FinishSetupWith(ctx, "hash-one", "session-one", func(*sql.Tx) error {
		return errors.New("stop")
	})
	if err == nil || err.Error() != "stop" {
		t.Fatalf("err = %v", err)
	}
	ok, err := db.HasAdminHash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("failed extra still wrote a hash")
	}
}

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
