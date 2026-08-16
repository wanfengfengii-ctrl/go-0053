package store_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/baghub/baggage-leg-closeout-hub/internal/clock"
	"github.com/baghub/baggage-leg-closeout-hub/internal/model"
	"github.com/baghub/baggage-leg-closeout-hub/internal/store"
	"github.com/baghub/baggage-leg-closeout-hub/internal/tracker"
)

func TestUpsertDeparturePersistsInsertUpdateAndTrackerProjection(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "tracker.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	direct := store.DepartureRow{
		LegKey: "LEG-DIRECT", BagTag: "BAG-DIRECT",
		CheckinFactID: 11, CheckinTime: "20260816010000",
		SortFactID: 12, SortTime: "20260816010500",
		ScreenFactID: 13, ScreenTime: "20260816011000", ScreenOutcome: model.ScreenClear,
		LoadFactID: 14, LoadTime: "20260816011500",
		OffloadFactID: 15, OffloadTime: "20260816012000",
		LatestSeq: 16, FenceSeq: 17,
	}
	if err := store.UpsertDeparture(ctx, s.DB(), direct); err != nil {
		t.Fatalf("insert departure: %v", err)
	}
	got, ok, err := store.GetDeparture(ctx, s.DB(), direct.LegKey, direct.BagTag)
	if err != nil {
		t.Fatalf("get inserted departure: %v", err)
	}
	if !ok || !reflect.DeepEqual(got, direct) {
		t.Fatalf("inserted departure = %#v, found %v; want %#v", got, ok, direct)
	}

	updated := store.DepartureRow{
		LegKey: direct.LegKey, BagTag: direct.BagTag,
		CheckinFactID: 21, CheckinTime: "20260816020000",
		SortFactID: 22, SortTime: "20260816020500",
		ScreenFactID: 23, ScreenTime: "20260816021000", ScreenOutcome: model.ScreenHold,
		LoadFactID: 24, LoadTime: "20260816021500",
		OffloadFactID: 25, OffloadTime: "20260816022000",
		LatestSeq: 26, Closed: true, FenceSeq: 27,
	}
	if err := store.UpsertDeparture(ctx, s.DB(), updated); err != nil {
		t.Fatalf("update departure: %v", err)
	}
	got, ok, err = store.GetDeparture(ctx, s.DB(), updated.LegKey, updated.BagTag)
	if err != nil {
		t.Fatalf("get updated departure: %v", err)
	}
	if !ok || !reflect.DeepEqual(got, updated) {
		t.Fatalf("updated departure = %#v, found %v; want %#v", got, ok, updated)
	}

	rawID, err := store.InsertRawIngress(ctx, s.DB(), "2026-08-16T02:30:00.000000Z", "conn-1", []byte("scan"), "OK", "")
	if err != nil {
		t.Fatalf("insert raw ingress: %v", err)
	}
	tx, err := s.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tracker transaction: %v", err)
	}
	defer tx.Rollback()

	const (
		trackerLeg = "LEG-TRACKER"
		trackerBag = "BAG-TRACKER"
		scanTime   = "20260816023000"
		commitSeq  = uint64(31)
	)
	tr := tracker.New(s, clock.NewVirtual(time.Date(2026, 8, 16, 2, 30, 0, 0, time.UTC)))
	factID, err := tr.ApplyFact(ctx, tx, store.ScanFactRow{
		CommitSeq: commitSeq, LegKey: trackerLeg, BagTag: trackerBag,
		ScanType: model.ScanCheckin, Device: "DEVICE-1", ControlNo: "0000000031",
		ScanTime: scanTime, WorkZone: "CHECKIN", Operator: "OP-1", RawIngressID: rawID,
	})
	if err != nil {
		t.Fatalf("apply outbound fact: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tracker transaction: %v", err)
	}

	got, ok, err = store.GetDeparture(ctx, s.DB(), trackerLeg, trackerBag)
	if err != nil {
		t.Fatalf("get tracker departure: %v", err)
	}
	wantTracker := store.DepartureRow{
		LegKey: trackerLeg, BagTag: trackerBag,
		CheckinFactID: factID, CheckinTime: scanTime, LatestSeq: commitSeq,
	}
	if !ok || !reflect.DeepEqual(got, wantTracker) {
		t.Fatalf("tracker departure = %#v, found %v; want %#v", got, ok, wantTracker)
	}
}
