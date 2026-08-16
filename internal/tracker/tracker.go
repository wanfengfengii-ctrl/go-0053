// Package tracker persists valid scans as immutable facts and maintains the
// per-(leg, bag) departure and arrival projections.
//
// Projection model
//
// For every milestone slot the effective fact is the one with the maximum
// (scan_time, device, control_no, fact_id) ordering. Picking the most recent
// valid fact means a fact that arrives late but carries an earlier scan time
// can never regress a stage already reached or overturn a mutually-exclusive
// conclusion (security clear/hold, load/offload); such shadowed facts remain
// auditable in scan_facts. Because the ordering is total and independent of
// arrival order, the final projection is identical whether facts are submitted
// in time order or out of order.
//
// Late evidence
//
// After a leg's departure is closed at fence seq F, an outbound-domain fact
// (CHECKIN, SORT, SCREEN, LOAD, outbound OFFLOAD) committed with seq > F is
// flagged is_late_evidence: it is saved as an immutable audit fact but never
// touches the frozen departure projection or the baseline report. Inbound
// facts (UNLOAD, TRANSFER, CLAIM) always advance the independent arrival
// projection.
//
// Evidence facts (wrong zone or unknown bag) are saved but, like late facts,
// do not mutate the normal bag projection; they surface in the close report as
// WRONG_ZONE_EVIDENCE / UNKNOWN_BAG_EVIDENCE rows.
package tracker

import (
	"context"
	"fmt"

	"github.com/baghub/baggage-leg-closeout-hub/internal/clock"
	"github.com/baghub/baggage-leg-closeout-hub/internal/model"
	"github.com/baghub/baggage-leg-closeout-hub/internal/store"
)

// Tracker saves scan facts and maintains the departure/arrival projections.
type Tracker struct {
	store   *store.Store
	clock   clock.Clock
}

// New returns a Tracker backed by s and clock.
func New(s *store.Store, clock clock.Clock) *Tracker {
	return &Tracker{store: s, clock: clock}
}

// ApplyFact persists an immutable scan fact (assigning its created_at) and, when
// the fact is a normal valid scan, recomputes the affected projection. It
// decides late-evidence status by consulting the leg's close fence within the
// same transaction. factID of the inserted row is returned.
func (t *Tracker) ApplyFact(ctx context.Context, tx store.DBTX, fact store.ScanFactRow) (int64, error) {
	fact.CreatedAt = clock.ISOFrom(t.clock)
	if fact.ScanType.Domain() == model.DomainOutbound {
		closed, fence, err := store.IsDepartureClosed(ctx, tx, fact.LegKey)
		if err != nil {
			return 0, fmt.Errorf("tracker: check closed: %w", err)
		}
		if closed && fact.CommitSeq > fence {
			fact.IsLateEvidence = true
		}
	}
	factID, err := store.InsertScanFact(ctx, tx, fact)
	if err != nil {
		return 0, err
	}
	// Evidence facts (wrong zone / unknown bag) and late outbound facts never
	// mutate the normal projection.
	if fact.IsLateEvidence || fact.IsWrongZone || fact.IsUnknownBag {
		return factID, nil
	}
	switch fact.ScanType.Domain() {
	case model.DomainOutbound:
		if err := t.recomputeDeparture(ctx, tx, fact.LegKey, fact.BagTag); err != nil {
			return 0, err
		}
	case model.DomainInbound:
		if err := t.recomputeArrival(ctx, tx, fact.LegKey, fact.BagTag); err != nil {
			return 0, err
		}
	}
	return factID, nil
}

// recomputeDeparture rebuilds the outbound projection for a (leg, bag) from all
// applicable (non-late, non-evidence) facts. The effective fact per milestone
// is the last in (scan_time, device, control_no, id) ascending order, i.e. the
// maximum stable-order key.
func (t *Tracker) recomputeDeparture(ctx context.Context, tx store.DBTX, legKey, bagTag string) error {
	facts, err := store.FactsForLegBag(ctx, tx, legKey, bagTag)
	if err != nil {
		return fmt.Errorf("tracker: load facts: %w", err)
	}
	var (
		checkin, srt, screen, load, offload *store.ScanFactRow
	)
	for i := range facts {
		f := &facts[i]
		if f.IsLateEvidence || f.IsWrongZone || f.IsUnknownBag {
			continue
		}
		switch f.ScanType {
		case model.ScanCheckin:
			checkin = lastFact(checkin, f)
		case model.ScanSort:
			srt = lastFact(srt, f)
		case model.ScanScreen:
			screen = lastFact(screen, f)
		case model.ScanLoad:
			load = lastFact(load, f)
		case model.ScanOffload:
			offload = lastFact(offload, f)
		}
	}
	existing, hasRow, err := store.GetDeparture(ctx, tx, legKey, bagTag)
	if err != nil {
		return fmt.Errorf("tracker: load departure: %w", err)
	}
	row := store.DepartureRow{LegKey: legKey, BagTag: bagTag}
	if hasRow {
		row.Closed = existing.Closed
		row.FenceSeq = existing.FenceSeq
	}
	if checkin != nil {
		row.CheckinFactID = checkin.ID
		row.CheckinTime = checkin.ScanTime
		row.LatestSeq = maxSeq(row.LatestSeq, checkin.CommitSeq)
	}
	if srt != nil {
		row.SortFactID = srt.ID
		row.SortTime = srt.ScanTime
		row.LatestSeq = maxSeq(row.LatestSeq, srt.CommitSeq)
	}
	if screen != nil {
		row.ScreenFactID = screen.ID
		row.ScreenTime = screen.ScanTime
		row.ScreenOutcome = screen.ScreenOutcome
		row.LatestSeq = maxSeq(row.LatestSeq, screen.CommitSeq)
	}
	if load != nil {
		row.LoadFactID = load.ID
		row.LoadTime = load.ScanTime
		row.LatestSeq = maxSeq(row.LatestSeq, load.CommitSeq)
	}
	if offload != nil {
		row.OffloadFactID = offload.ID
		row.OffloadTime = offload.ScanTime
		row.LatestSeq = maxSeq(row.LatestSeq, offload.CommitSeq)
	}
	return store.UpsertDeparture(ctx, tx, row)
}

// recomputeArrival rebuilds the inbound projection for a (leg, bag).
func (t *Tracker) recomputeArrival(ctx context.Context, tx store.DBTX, legKey, bagTag string) error {
	facts, err := store.FactsForLegBag(ctx, tx, legKey, bagTag)
	if err != nil {
		return fmt.Errorf("tracker: load facts: %w", err)
	}
	var unload, transfer, claim *store.ScanFactRow
	for i := range facts {
		f := &facts[i]
		if f.IsLateEvidence || f.IsWrongZone || f.IsUnknownBag {
			continue
		}
		switch f.ScanType {
		case model.ScanUnload:
			unload = lastFact(unload, f)
		case model.ScanTransfer:
			transfer = lastFact(transfer, f)
		case model.ScanClaim:
			claim = lastFact(claim, f)
		}
	}
	existing, hasRow, err := store.GetArrival(ctx, tx, legKey, bagTag)
	if err != nil {
		return fmt.Errorf("tracker: load arrival: %w", err)
	}
	row := store.ArrivalRow{LegKey: legKey, BagTag: bagTag}
	if hasRow {
		row.LatestSeq = existing.LatestSeq
	}
	if unload != nil {
		row.UnloadFactID = unload.ID
		row.UnloadTime = unload.ScanTime
		row.LatestSeq = maxSeq(row.LatestSeq, unload.CommitSeq)
	}
	if transfer != nil {
		row.TransferFactID = transfer.ID
		row.TransferTime = transfer.ScanTime
		row.LatestSeq = maxSeq(row.LatestSeq, transfer.CommitSeq)
	}
	if claim != nil {
		row.ClaimFactID = claim.ID
		row.ClaimTime = claim.ScanTime
		row.LatestSeq = maxSeq(row.LatestSeq, claim.CommitSeq)
	}
	return store.UpsertArrival(ctx, tx, row)
}

// lastFact returns the greater of cur and cand by the stable milestone order
// (scan_time, device, control_no, fact_id). The maximum is the effective fact;
// an earlier fact arriving later (lower order key) can never replace it.
func lastFact(cur, cand *store.ScanFactRow) *store.ScanFactRow {
	if cur == nil {
		return cand
	}
	if factOrderKey(cand) > factOrderKey(cur) {
		return cand
	}
	return cur
}

// factOrderKey renders a scan fact's stable ordering key as a comparable
// string. scan_time is ISO-ish YYYYMMDDHHMMSS (lexicographically chronological);
// control_no is the wire's zero-padded 10 digits; fact_id is zero-padded so
// numeric ordering matches string ordering.
func factOrderKey(f *store.ScanFactRow) string {
	return f.ScanTime + "\x00" + f.Device + "\x00" + f.ControlNo + "\x00" + fmt.Sprintf("%020d", f.ID)
}

func maxSeq(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

// MilestoneFactID is the fact-id of a single milestone in a bag timeline.
type MilestoneFactID struct {
	Type     model.ScanType
	FactID   int64
	ScanTime string
	Device   string
	ControlNo string
}

// Timeline is a bag's full journey across both projections, with every
// milestone traceable to its fact, leg, zone, device, control number and raw
// message.
type Timeline struct {
	LegKey   string
	BagTag   string
	Outbound []store.ScanFactRow
	Inbound  []store.ScanFactRow
}

// BagTimeline returns every fact (including evidence and late facts) for a
// (leg, bag), split by domain, in commit order.
func (t *Tracker) BagTimeline(ctx context.Context, legKey, bagTag string) (Timeline, error) {
	facts, err := store.FactsForLegBag(ctx, t.store.DB(), legKey, bagTag)
	if err != nil {
		return Timeline{}, err
	}
	tl := Timeline{LegKey: legKey, BagTag: bagTag}
	for _, f := range facts {
		switch f.ScanType.Domain() {
		case model.DomainOutbound:
			tl.Outbound = append(tl.Outbound, f)
		case model.DomainInbound:
			tl.Inbound = append(tl.Inbound, f)
		}
	}
	return tl, nil
}

// LegLoadView summarises the real-time outbound load state of a leg.
type LegLoadView struct {
	LegKey       string
	CheckedIn    int
	Sorted       int
	ScreenCleared int
	ScreenHeld   int
	Loaded       int
	Offloaded    int
	Rows         []store.DepartureRow
}

// LegLoad returns the real-time outbound load view for a leg.
func (t *Tracker) LegLoad(ctx context.Context, legKey string) (LegLoadView, error) {
	rows, err := store.DeparturesForLeg(ctx, t.store.DB(), legKey)
	if err != nil {
		return LegLoadView{}, err
	}
	v := LegLoadView{LegKey: legKey, Rows: rows}
	for _, r := range rows {
		if r.CheckinFactID != 0 {
			v.CheckedIn++
		}
		if r.SortFactID != 0 {
			v.Sorted++
		}
		switch r.ScreenOutcome {
		case model.ScreenClear:
			v.ScreenCleared++
		case model.ScreenHold:
			v.ScreenHeld++
		}
		if EffectiveLoaded(r) {
			v.Loaded++
		}
		if r.OffloadFactID != 0 && !EffectiveLoaded(r) {
			v.Offloaded++
		}
	}
	return v, nil
}

// EffectiveLoaded reports whether a departure row represents a bag that is
// currently loaded (load effective and not superseded by a later offload).
func EffectiveLoaded(r store.DepartureRow) bool {
	if r.LoadFactID == 0 {
		return false
	}
	if r.OffloadFactID == 0 {
		return true
	}
	return r.LoadTime >= r.OffloadTime
}
