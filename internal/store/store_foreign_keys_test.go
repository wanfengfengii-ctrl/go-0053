package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/baghub/baggage-leg-closeout-hub/internal/store"
)

// TestForeignKeysEnforcedOnEveryPoolConnection is the regression test for the
// bug where Store.Open set PRAGMA foreign_keys=ON via ExecContext. Because
// database/sql runs ExecContext on a single pooled connection, every connection
// the pool opened afterwards had foreign_keys=0: once the first connection was
// occupied, writes landing on a second pool connection silently accepted orphan
// logical_messages rows referencing nonexistent raw_ingress.
//
// The fix applies foreign_keys (and busy_timeout) through _pragma DSN
// parameters, which the modernc.org/sqlite driver applies to every connection
// in its connect path. This test forces the pool onto at least two distinct
// physical connections, then asserts orphan writes are rejected and legitimate
// associated writes succeed.
func TestForeignKeysEnforcedOnEveryPoolConnection(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "fk.db")
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	db := s.DB()
	// Let the pool grow to a second physical connection.
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)

	// Check out and HOLD the first physical connection so the next checkout is
	// forced onto a distinct second physical connection.
	conn1, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire conn1: %v", err)
	}
	defer func() { _ = conn1.Close() }()

	conn2, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire conn2: %v", err)
	}
	defer func() { _ = conn2.Close() }()

	// Prove conn1 and conn2 are distinct physical connections. SQLite temp
	// tables are connection-local, so a temp table created on conn1 must be
	// invisible on conn2; if conn2 saw it, both *sql.Conn would be backed by
	// the same physical connection and the test would not exercise a second
	// pool connection at all.
	if _, err := conn1.ExecContext(ctx, "CREATE TEMP TABLE _conn_marker(x INTEGER)"); err != nil {
		t.Fatalf("create temp marker on conn1: %v", err)
	}
	if _, err := conn1.ExecContext(ctx, "INSERT INTO _conn_marker VALUES(1)"); err != nil {
		t.Fatalf("seed temp marker on conn1: %v", err)
	}
	if err := conn2.QueryRowContext(ctx, "SELECT COUNT(*) FROM _conn_marker").Scan(new(int)); err == nil {
		t.Fatal("conn2 saw conn1's connection-local temp table; expected two distinct physical connections")
	}

	// Both physical connections must enforce foreign keys. The old bug left the
	// second pool connection with foreign_keys=0.
	for name, c := range map[string]*sql.Conn{"conn1": conn1, "conn2": conn2} {
		var fk int
		if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatalf("%s: PRAGMA foreign_keys: %v", name, err)
		}
		if fk != 1 {
			t.Fatalf("%s: foreign_keys=%d, want 1; FK protection missing on a pool connection", name, fk)
		}
	}

	// Orphan write on the second pool connection must be REJECTED. raw_ingress
	// is empty at this point, so raw_ingress_id=999999 references nothing.
	_, err = store.InsertLogicalMessage(ctx, conn2, store.LogicalMessageRow{
		Device:       "DEV-ORPHAN",
		ControlNo:    "CTRL-ORPHAN",
		RawIngressID: 999999,
		MsgHash:      "deadbeef",
		ParseStatus:  "OK",
		ResultStatus: "ACCEPTED",
		CreatedAt:    "20260101T000000",
	})
	if err == nil {
		t.Fatal("orphan logical_messages insert succeeded on conn2; foreign-key constraint not enforced on a pool connection")
	}

	// Legitimate associated write must SUCCEED. Create raw_ingress on conn1
	// (auto-committed and visible to conn2), then a logical_message on conn2
	// referencing it.
	rawID, err := store.InsertRawIngress(ctx, conn1, "20260101T000000", "conn-a", []byte("hello"), "OK", "")
	if err != nil {
		t.Fatalf("InsertRawIngress on conn1: %v", err)
	}
	if _, err := store.InsertLogicalMessage(ctx, conn2, store.LogicalMessageRow{
		Device:       "DEV-1",
		ControlNo:    "CTRL-1",
		RawIngressID: rawID,
		MsgHash:      store.HashFrame([]byte("hello")),
		ParseStatus:  "OK",
		ResultStatus: "ACCEPTED",
		CreatedAt:    "20260101T000000",
	}); err != nil {
		t.Fatalf("legitimate InsertLogicalMessage referencing valid raw_ingress failed on conn2: %v", err)
	}
}
