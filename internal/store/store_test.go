package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestEveryStoreConnectionEnforcesForeignKeys(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	s.DB().SetMaxOpenConns(3)
	connections := make([]*sql.Conn, 3)
	for i := range connections {
		connections[i], err = s.DB().Conn(ctx)
		if err != nil {
			t.Fatalf("Conn(%d) error = %v", i, err)
		}
		defer connections[i].Close()

		var foreignKeys int
		if err := connections[i].QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatalf("query foreign_keys on connection %d: %v", i, err)
		}
		if foreignKeys != 1 {
			t.Errorf("connection %d foreign_keys = %d, want 1", i, foreignKeys)
		}
	}

	if _, err := InsertLogicalMessage(ctx, connections[1], LogicalMessageRow{
		Device:       "orphan-device",
		ControlNo:    "orphan-control",
		RawIngressID: 999999,
		MsgHash:      "orphan-hash",
		ParseStatus:  "OK",
		ResultStatus: "ACCEPTED",
		CreatedAt:    "2026-08-16T00:00:00Z",
	}); err == nil {
		t.Error("InsertLogicalMessage() with a missing raw_ingress parent succeeded")
	}

	rawIngressID, err := InsertRawIngress(ctx, connections[2],
		"2026-08-16T00:00:01Z", "connection-2", []byte("valid"), "OK", "")
	if err != nil {
		t.Fatalf("InsertRawIngress() error = %v", err)
	}
	if _, err := InsertLogicalMessage(ctx, connections[2], LogicalMessageRow{
		Device:       "valid-device",
		ControlNo:    "valid-control",
		RawIngressID: rawIngressID,
		MsgHash:      "valid-hash",
		ParseStatus:  "OK",
		ResultStatus: "ACCEPTED",
		CreatedAt:    "2026-08-16T00:00:01Z",
	}); err != nil {
		t.Fatalf("InsertLogicalMessage() with a valid raw_ingress parent error = %v", err)
	}
}
