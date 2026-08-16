package tracker_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/baghub/baggage-leg-closeout-hub/internal/clock"
	"github.com/baghub/baggage-leg-closeout-hub/internal/model"
	"github.com/baghub/baggage-leg-closeout-hub/internal/store"
	"github.com/baghub/baggage-leg-closeout-hub/internal/tracker"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// applyFact runs Tracker.ApplyFact inside a committed transaction. Before the
// UpsertDeparture fix, the transaction could not commit for a normal outbound
// fact because the departure upsert returned "18 values for 16 columns".
func applyFact(t *testing.T, tr *tracker.Tracker, s *store.Store, fact store.ScanFactRow) {
	t.Helper()
	tx, err := s.BeginTx(context.Background())
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if _, err := tr.ApplyFact(context.Background(), tx, fact); err != nil {
		_ = tx.Rollback()
		t.Fatalf("ApplyFact (%s): %v", fact.ScanType, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit (%s): %v", fact.ScanType, err)
	}
}

// TestApplyFact_NormalOutboundPersistsDepartureProjection verifies that applying
// a normal outbound scan fact through the Tracker persists the departure
// projection and lets the transaction commit. Multiple milestones exercise the
// conflict-update path on the same (leg, bag) row, and the arrival projection
// must remain untouched.
func TestApplyFact_NormalOutboundPersistsDepartureProjection(t *testing.T) {
	s := openTestStore(t)
	clk := clock.NewVirtual(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	tr := tracker.New(s, clk)
	ctx := context.Background()

	legKey := model.LegKey("BA", "1234", "20260101", "LHR", "JFK", "1")
	bagTag := "0001BA1234567"

	rawID, err := store.InsertRawIngress(ctx, s.DB(), clk.ISO(), "test-conn", []byte("raw-1"), "OK", "")
	if err != nil {
		t.Fatalf("InsertRawIngress: %v", err)
	}

	// A normal CHECKIN fact must commit and create the outbound projection.
	// Before the fix this failed with "18 values for 16 columns".
	checkin := store.ScanFactRow{
		CommitSeq:    1,
		LegKey:       legKey,
		BagTag:       bagTag,
		ScanType:     model.ScanCheckin,
		Device:       "DEV1",
		ControlNo:    "0000000001",
		ScanTime:     "20260101120000",
		WorkZone:     "CHK",
		Operator:     "OP1",
		RawIngressID: rawID,
	}
	applyFact(t, tr, s, checkin)

	got, ok, err := store.GetDeparture(ctx, s.DB(), legKey, bagTag)
	if err != nil {
		t.Fatalf("GetDeparture after CHECKIN: %v", err)
	}
	if !ok {
		t.Fatalf("departure projection missing after CHECKIN fact")
	}
	if got.CheckinTime != "20260101120000" {
		t.Fatalf("checkin time not persisted: got %q", got.CheckinTime)
	}
	if got.CheckinFactID == 0 {
		t.Fatalf("checkin fact id not persisted")
	}
	if got.LatestSeq != 1 {
		t.Fatalf("LatestSeq = %d, want 1", got.LatestSeq)
	}
	if got.Closed {
		t.Fatalf("projection must not be closed after a normal scan")
	}

	// Subsequent milestones rewrite the same row via ON CONFLICT DO UPDATE and
	// must persist alongside the surviving checkin milestone.
	screen := store.ScanFactRow{
		CommitSeq:     2,
		LegKey:        legKey,
		BagTag:        bagTag,
		ScanType:      model.ScanScreen,
		Device:        "DEV1",
		ControlNo:     "0000000002",
		ScanTime:      "20260101120100",
		WorkZone:      "SCR",
		Operator:      "OP1",
		ScreenOutcome: model.ScreenClear,
		RawIngressID:  rawID,
	}
	load := store.ScanFactRow{
		CommitSeq:    3,
		LegKey:       legKey,
		BagTag:       bagTag,
		ScanType:     model.ScanLoad,
		Device:       "DEV1",
		ControlNo:    "0000000003",
		ScanTime:     "20260101120200",
		WorkZone:     "LDZ",
		Operator:     "OP1",
		RawIngressID: rawID,
	}
	applyFact(t, tr, s, screen)
	applyFact(t, tr, s, load)

	got2, ok, err := store.GetDeparture(ctx, s.DB(), legKey, bagTag)
	if err != nil {
		t.Fatalf("GetDeparture after milestones: %v", err)
	}
	if !ok {
		t.Fatalf("departure projection missing after milestones")
	}
	if got2.CheckinTime != "20260101120000" {
		t.Fatalf("checkin time regressed after conflict updates: %q", got2.CheckinTime)
	}
	if got2.ScreenOutcome != model.ScreenClear {
		t.Fatalf("screen outcome not persisted: got %q", got2.ScreenOutcome)
	}
	if got2.ScreenFactID == 0 {
		t.Fatalf("screen fact id not persisted")
	}
	if got2.LoadTime != "20260101120200" {
		t.Fatalf("load time not persisted: got %q", got2.LoadTime)
	}
	if got2.LoadFactID == 0 {
		t.Fatalf("load fact id not persisted")
	}
	if got2.LatestSeq != 3 {
		t.Fatalf("LatestSeq = %d, want 3", got2.LatestSeq)
	}

	// Exactly one departure row for the (leg, bag): updates replaced, not
	// duplicated, the projection.
	rows, err := store.DeparturesForLeg(ctx, s.DB(), legKey)
	if err != nil {
		t.Fatalf("DeparturesForLeg: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 departure row, got %d", len(rows))
	}

	// The arrival projection must remain untouched by outbound facts.
	arrRows, err := store.ArrivalsForLeg(ctx, s.DB(), legKey)
	if err != nil {
		t.Fatalf("ArrivalsForLeg: %v", err)
	}
	if len(arrRows) != 0 {
		t.Fatalf("arrival projection must be empty for outbound facts, got %d rows", len(arrRows))
	}
}

// TestApplyFact_InboundPersistsArrivalProjection guards that the departure fix
// did not regress the independent arrival projection path.
func TestApplyFact_InboundPersistsArrivalProjection(t *testing.T) {
	s := openTestStore(t)
	clk := clock.NewVirtual(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	tr := tracker.New(s, clk)
	ctx := context.Background()

	legKey := model.LegKey("BA", "1234", "20260101", "LHR", "JFK", "1")
	bagTag := "0001BA7654321"

	rawID, err := store.InsertRawIngress(ctx, s.DB(), clk.ISO(), "test-conn", []byte("raw-2"), "OK", "")
	if err != nil {
		t.Fatalf("InsertRawIngress: %v", err)
	}

	unload := store.ScanFactRow{
		CommitSeq:    1,
		LegKey:       legKey,
		BagTag:       bagTag,
		ScanType:     model.ScanUnload,
		Device:       "DEV2",
		ControlNo:    "0000000010",
		ScanTime:     "20260101130000",
		WorkZone:     "UNL",
		Operator:     "OP2",
		RawIngressID: rawID,
	}
	applyFact(t, tr, s, unload)

	got, ok, err := store.GetArrival(ctx, s.DB(), legKey, bagTag)
	if err != nil {
		t.Fatalf("GetArrival after UNLOAD: %v", err)
	}
	if !ok {
		t.Fatalf("arrival projection missing after UNLOAD fact")
	}
	if got.UnloadTime != "20260101130000" {
		t.Fatalf("unload time not persisted: got %q", got.UnloadTime)
	}
	if got.LatestSeq != 1 {
		t.Fatalf("LatestSeq = %d, want 1", got.LatestSeq)
	}

	// Outbound projection must remain untouched by inbound facts.
	depRows, err := store.DeparturesForLeg(ctx, s.DB(), legKey)
	if err != nil {
		t.Fatalf("DeparturesForLeg: %v", err)
	}
	if len(depRows) != 0 {
		t.Fatalf("departure projection must be empty for inbound facts, got %d rows", len(depRows))
	}
}
