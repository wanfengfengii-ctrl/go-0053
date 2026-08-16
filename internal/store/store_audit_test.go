package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestListAuditNewestLastContract(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	for _, category := range []string{"first", "second", "third"} {
		if err := InsertAudit(ctx, s.DB(), "2026-08-16T00:00:00Z", category, "", ""); err != nil {
			t.Fatalf("InsertAudit(%q) error = %v", category, err)
		}
	}

	for _, tt := range []struct {
		name  string
		limit int
		want  []string
	}{
		{name: "limited to newest records", limit: 2, want: []string{"second", "third"}},
		{name: "all records", limit: 10, want: []string{"first", "second", "third"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := ListAudit(ctx, s.DB(), tt.limit)
			if err != nil {
				t.Fatalf("ListAudit(limit=%d) error = %v", tt.limit, err)
			}

			got := make([]string, len(rows))
			for i, row := range rows {
				got[i] = row.Category
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ListAudit(limit=%d) order = %v, want %v (newest last)", tt.limit, got, tt.want)
			}
		})
	}
}
