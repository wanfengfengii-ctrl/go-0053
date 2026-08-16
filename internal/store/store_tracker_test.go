package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/baghub/baggage-leg-closeout-hub/internal/model"
	"github.com/baghub/baggage-leg-closeout-hub/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	// A file path (not ":memory:") keeps a single backing database across the
	// connection pool, as recommended by store.Open.
	dsn := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestUpsertDeparture_InsertAndConflictUpdate guards the regression where the
// INSERT ... ON CONFLICT upsert for bag_leg_departure declared 18 placeholders
// for only 16 columns. SQLite therefore rejected every write to an outbound
// projection with "18 values for 16 columns", so neither a first insert nor a
// conflict update could persist. Both paths must now round-trip every milestone
// field (including the closed/fence freeze fields) without disturbing other
// tables.
func TestUpsertDeparture_InsertAndConflictUpdate(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const legKey = "BA/1234/20260101/LHR/JFK/01"
	const bagTag = "0001BA1234567"

	first := store.DepartureRow{
		LegKey:        legKey,
		BagTag:        bagTag,
		CheckinFactID: 11,
		CheckinTime:   "20260101120000",
		SortFactID:    12,
		SortTime:      "20260101120100",
		ScreenFactID:  13,
		ScreenTime:    "20260101120200",
		ScreenOutcome: model.ScreenClear,
		LoadFactID:    14,
		LoadTime:      "20260101120300",
		OffloadFactID: 0,
		OffloadTime:   "",
		LatestSeq:     5,
		Closed:        false,
		FenceSeq:      0,
	}

	// First write exercises the INSERT branch.
	if err := store.UpsertDeparture(ctx, s.DB(), first); err != nil {
		t.Fatalf("UpsertDeparture (insert): %v", err)
	}
	got, ok, err := store.GetDeparture(ctx, s.DB(), legKey, bagTag)
	if err != nil {
		t.Fatalf("GetDeparture (after insert): %v", err)
	}
	if !ok {
		t.Fatalf("departure row missing after first insert")
	}
	if got != first {
		t.Fatalf("departure row mismatch after insert:\n got  %+v\n want %+v", got, first)
	}

	// Second write to the same (leg, bag) exercises ON CONFLICT DO UPDATE. Every
	// field changes, including Closed/FenceSeq so the freeze fields are covered.
	second := store.DepartureRow{
		LegKey:         legKey,
		BagTag:         bagTag,
		CheckinFactID:  21,
		CheckinTime:    "20260101121000",
		SortFactID:     22,
		SortTime:       "20260101121100",
		ScreenFactID:   23,
		ScreenTime:     "20260101121200",
		ScreenOutcome:  model.ScreenHold,
		LoadFactID:     24,
		LoadTime:       "20260101121300",
		OffloadFactID:  25,
		OffloadTime:    "20260101121400",
		LatestSeq:      9,
		Closed:         true,
		FenceSeq:       9,
	}
	if err := store.UpsertDeparture(ctx, s.DB(), second); err != nil {
		t.Fatalf("UpsertDeparture (conflict update): %v", err)
	}
	got2, ok, err := store.GetDeparture(ctx, s.DB(), legKey, bagTag)
	if err != nil {
		t.Fatalf("GetDeparture (after update): %v", err)
	}
	if !ok {
		t.Fatalf("departure row missing after conflict update")
	}
	if got2 != second {
		t.Fatalf("departure row mismatch after conflict update:\n got  %+v\n want %+v", got2, second)
	}

	// The conflict update must replace, not duplicate, the row.
	rows, err := store.DeparturesForLeg(ctx, s.DB(), legKey)
	if err != nil {
		t.Fatalf("DeparturesForLeg: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 departure row for leg, got %d", len(rows))
	}

	// The arrival projection and other tables must be untouched by departure writes.
	if arr, ok, err := store.GetArrival(ctx, s.DB(), legKey, bagTag); err != nil {
		t.Fatalf("GetArrival: %v", err)
	} else if ok {
		t.Fatalf("arrival row unexpectedly present after departure writes: %+v", arr)
	}
}
