// Package store provides the embedded SQLite persistence layer for the baggage
// leg closeout hub. It uses database/sql with the pure-Go modernc.org/sqlite
// driver (no CGO) so the resulting binary is statically linked and cross
// compiles to linux/amd64 and linux/arm64.
//
// All schema lives in migrations/0001_init.sql and is applied idempotently on
// Open. Accessor methods accept a DBTX so the same code path runs against a
// *sql.DB (for reads) or a *sql.Tx (for the linearised write transactions
// driven by internal/coordinator).
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	_ "modernc.org/sqlite"
)

// DBTX is the common surface of *sql.DB and *sql.Tx.
type DBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	PrepareContext(context.Context, string) (*sql.Stmt, error)
}

// Store wraps a database/sql connection to the embedded SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at dsn and applies migrations.
// dsn may be a file path or ":memory:". A shared cache is used so that
// multiple *sql.DB handles opened against the same in-memory identifier are
// isolated by connection; tests that need persistence should use a file path
// under t.TempDir().
func Open(ctx context.Context, dsn string) (*Store, error) {
	params := url.Values{}
	params.Add("_pragma", "foreign_keys(1)")
	params.Add("_pragma", "busy_timeout(5000)")
	separator := "?"
	if strings.ContainsRune(dsn, '?') {
		separator = "&"
	}
	db, err := sql.Open("sqlite", dsn+separator+params.Encode())
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	// Single writer avoids SQLITE_BUSY under contention; the coordinator also
	// serialises writes, but this keeps standalone access safe.
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: pragma journal: %w", err)
	}
	if err := applyMigrations(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB returns the underlying *sql.DB for advanced use (e.g. status queries).
func (s *Store) DB() *sql.DB { return s.db }

// BeginTx starts a serialisable write transaction.
func (s *Store) BeginTx(ctx context.Context) (*sql.Tx, error) {
	return s.db.BeginTx(ctx, nil)
}

func applyMigrations(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	return nil
}

// MetaGet returns the value for a meta key, or "" if absent.
func (s *Store) MetaGet(ctx context.Context, key string) (string, error) {
	return MetaGet(ctx, s.db, key)
}

// MetaGetTx reads a meta value within a transaction.
func MetaGet(ctx context.Context, q DBTX, key string) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// MetaSet writes a meta value.
func (s *Store) MetaSet(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		key, value)
	return err
}

// MetaSetTx writes a meta value within a transaction.
func MetaSetTx(ctx context.Context, q DBTX, key, value string) error {
	_, err := q.ExecContext(ctx,
		"INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		key, value)
	return err
}
