package store

import (
	"context"
	"path/filepath"
	"testing"
)

// newTestStore opens an isolated on-disk SQLite database for a test. A file
// path (rather than a shared ":memory:" handle) is used so each test gets a
// fresh, self-contained database, matching the project guidance for tests that
// need persistence.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(context.Background(), filepath.Join(dir, "audit.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func insertAuditRecord(t *testing.T, ctx context.Context, s *Store, n int) {
	t.Helper()
	ts := "2025-01-01T00:00:00Z"
	category := "closeout"
	ref := ""
	payload := "rec"
	if err := InsertAudit(ctx, s.DB(), ts, category, ref, payload); err != nil {
		t.Fatalf("InsertAudit #%d: %v", n, err)
	}
}

func TestListAuditEmpty(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	got, err := ListAudit(ctx, s.DB(), 10)
	if err != nil {
		t.Fatalf("ListAudit empty: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListAudit empty: want 0 rows, got %d", len(got))
	}
}

func TestListAuditMultipleRecordsNewestLast(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	const n = 5
	for i := 0; i < n; i++ {
		insertAuditRecord(t, ctx, s, i)
	}

	got, err := ListAudit(ctx, s.DB(), n)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(got) != n {
		t.Fatalf("ListAudit: want %d rows, got %d", n, len(got))
	}
	// Contract: ordered by id from oldest to newest, newest last.
	for i, r := range got {
		wantID := int64(i + 1)
		if r.ID != wantID {
			t.Fatalf("row %d: want id %d, got %d", i, wantID, r.ID)
		}
	}
	last := got[len(got)-1]
	if last.ID != int64(n) {
		t.Fatalf("newest record must be last: want id %d, got %d", n, last.ID)
	}
}

func TestListAuditLimitTruncationReturnsMostRecentOldestToNewest(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	const total = 5
	for i := 0; i < total; i++ {
		insertAuditRecord(t, ctx, s, i)
	}

	const limit = 3
	got, err := ListAudit(ctx, s.DB(), limit)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(got) != limit {
		t.Fatalf("ListAudit: want %d rows, got %d", limit, len(got))
	}
	// With limit < total, the most recent `limit` records must be returned,
	// ordered oldest-to-newest so the newest is last: ids [3, 4, 5].
	wantIDs := []int64{3, 4, 5}
	for i, want := range wantIDs {
		if got[i].ID != want {
			t.Fatalf("row %d: want id %d, got %d", i, want, got[i].ID)
		}
	}
	if got[len(got)-1].ID != int64(total) {
		t.Fatalf("newest record must be last under truncation: want id %d, got %d", total, got[len(got)-1].ID)
	}
}

func TestListAuditLimitExceedsTotal(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	const n = 3
	for i := 0; i < n; i++ {
		insertAuditRecord(t, ctx, s, i)
	}

	got, err := ListAudit(ctx, s.DB(), 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(got) != n {
		t.Fatalf("ListAudit: want %d rows, got %d", n, len(got))
	}
	// Even when limit >= total, ordering must still be oldest-to-newest.
	for i, r := range got {
		if r.ID != int64(i+1) {
			t.Fatalf("row %d: want id %d, got %d", i, i+1, r.ID)
		}
	}
}

func TestListAuditPreservesFields(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	ts := "2025-06-01T12:30:45Z"
	category := "manual"
	ref := "LEG-123"
	payload := `{"k":"v"}`
	if err := InsertAudit(ctx, s.DB(), ts, category, ref, payload); err != nil {
		t.Fatalf("InsertAudit: %v", err)
	}

	got, err := ListAudit(ctx, s.DB(), 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListAudit: want 1 row, got %d", len(got))
	}
	r := got[0]
	if r.ID != 1 {
		t.Errorf("ID: want 1, got %d", r.ID)
	}
	if r.Ts != ts {
		t.Errorf("Ts: want %q, got %q", ts, r.Ts)
	}
	if r.Category != category {
		t.Errorf("Category: want %q, got %q", category, r.Category)
	}
	if r.Ref != ref {
		t.Errorf("Ref: want %q, got %q", ref, r.Ref)
	}
	if r.Payload != payload {
		t.Errorf("Payload: want %q, got %q", payload, r.Payload)
	}
}
