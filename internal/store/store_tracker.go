package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/baghub/baggage-leg-closeout-hub/internal/model"
)

// GetDeparture loads the outbound projection row for a (leg, bag). It returns
// (row, false, nil) when no projection exists yet.
func GetDeparture(ctx context.Context, q DBTX, legKey, bagTag string) (DepartureRow, bool, error) {
	var r DepartureRow
	var screen string
	var closed, latest, fence int64
	err := q.QueryRowContext(ctx, `SELECT leg_key, bag_tag, checkin_fact_id, checkin_time, sort_fact_id, sort_time,
		screen_fact_id, screen_time, screen_outcome, load_fact_id, load_time, offload_fact_id, offload_time,
		latest_seq, closed, fence_seq FROM bag_leg_departure WHERE leg_key=? AND bag_tag=?`, legKey, bagTag).
		Scan(&r.LegKey, &r.BagTag, &r.CheckinFactID, &r.CheckinTime, &r.SortFactID, &r.SortTime,
			&r.ScreenFactID, &r.ScreenTime, &screen, &r.LoadFactID, &r.LoadTime, &r.OffloadFactID, &r.OffloadTime,
			&latest, &closed, &fence)
	if err == sql.ErrNoRows {
		return DepartureRow{}, false, nil
	}
	if err != nil {
		return DepartureRow{}, false, err
	}
	r.ScreenOutcome = model.ScreenOutcome(screen)
	r.LatestSeq = uint64(latest)
	r.Closed = closed == 1
	r.FenceSeq = uint64(fence)
	return r, true, nil
}

// DeparturesForLeg returns all outbound projection rows for a leg.
func DeparturesForLeg(ctx context.Context, q DBTX, legKey string) ([]DepartureRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT leg_key, bag_tag, checkin_fact_id, checkin_time, sort_fact_id, sort_time,
		screen_fact_id, screen_time, screen_outcome, load_fact_id, load_time, offload_fact_id, offload_time,
		latest_seq, closed, fence_seq FROM bag_leg_departure WHERE leg_key=? ORDER BY bag_tag`, legKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DepartureRow
	for rows.Next() {
		var r DepartureRow
		var screen string
		var closed, latest, fence int64
		if err := rows.Scan(&r.LegKey, &r.BagTag, &r.CheckinFactID, &r.CheckinTime, &r.SortFactID, &r.SortTime,
			&r.ScreenFactID, &r.ScreenTime, &screen, &r.LoadFactID, &r.LoadTime, &r.OffloadFactID, &r.OffloadTime,
			&latest, &closed, &fence); err != nil {
			return nil, err
		}
		r.ScreenOutcome = model.ScreenOutcome(screen)
		r.LatestSeq = uint64(latest)
		r.Closed = closed == 1
		r.FenceSeq = uint64(fence)
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpsertDeparture inserts or replaces an outbound projection row.
func UpsertDeparture(ctx context.Context, q DBTX, r DepartureRow) error {
	closed := 0
	if r.Closed {
		closed = 1
	}
	_, err := q.ExecContext(ctx, `INSERT INTO bag_leg_departure(leg_key, bag_tag, checkin_fact_id, checkin_time,
		sort_fact_id, sort_time, screen_fact_id, screen_time, screen_outcome, load_fact_id, load_time,
		offload_fact_id, offload_time, latest_seq, closed, fence_seq)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(leg_key, bag_tag) DO UPDATE SET
			checkin_fact_id=excluded.checkin_fact_id, checkin_time=excluded.checkin_time,
			sort_fact_id=excluded.sort_fact_id, sort_time=excluded.sort_time,
			screen_fact_id=excluded.screen_fact_id, screen_time=excluded.screen_time,
			screen_outcome=excluded.screen_outcome,
			load_fact_id=excluded.load_fact_id, load_time=excluded.load_time,
			offload_fact_id=excluded.offload_fact_id, offload_time=excluded.offload_time,
			latest_seq=excluded.latest_seq, closed=excluded.closed, fence_seq=excluded.fence_seq`,
		r.LegKey, r.BagTag, r.CheckinFactID, r.CheckinTime, r.SortFactID, r.SortTime, r.ScreenFactID, r.ScreenTime,
		string(r.ScreenOutcome), r.LoadFactID, r.LoadTime, r.OffloadFactID, r.OffloadTime, r.LatestSeq, closed, r.FenceSeq)
	if err != nil {
		return fmt.Errorf("store: upsert departure: %w", err)
	}
	return nil
}

// MarkDepartureClosed freezes every outbound row for a leg with the fence seq.
func MarkDepartureClosed(ctx context.Context, q DBTX, legKey string, fenceSeq uint64) error {
	_, err := q.ExecContext(ctx, `UPDATE bag_leg_departure SET closed=1, fence_seq=? WHERE leg_key=?`, fenceSeq, legKey)
	if err != nil {
		return fmt.Errorf("store: mark closed: %w", err)
	}
	return nil
}

// IsDepartureClosed reports whether the leg's outbound projection is frozen.
func IsDepartureClosed(ctx context.Context, q DBTX, legKey string) (bool, uint64, error) {
	var closed int64
	var fence int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT closed FROM bag_leg_departure WHERE leg_key=? LIMIT 1),0),
		COALESCE((SELECT fence_seq FROM bag_leg_departure WHERE leg_key=? LIMIT 1),0)`, legKey, legKey).Scan(&closed, &fence)
	if err != nil {
		return false, 0, err
	}
	// A close snapshot also marks the leg as closed even if no bag rows exist.
	var snap int64
	err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM close_snapshots WHERE leg_key=?`, legKey).Scan(&snap)
	if err != nil {
		return false, 0, err
	}
	return closed == 1 || snap > 0, uint64(fence), nil
}

// GetArrival loads the inbound projection row for a (leg, bag).
func GetArrival(ctx context.Context, q DBTX, legKey, bagTag string) (ArrivalRow, bool, error) {
	var r ArrivalRow
	var latest int64
	err := q.QueryRowContext(ctx, `SELECT leg_key, bag_tag, unload_fact_id, unload_time, transfer_fact_id,
		transfer_time, claim_fact_id, claim_time, latest_seq FROM bag_leg_arrival WHERE leg_key=? AND bag_tag=?`,
		legKey, bagTag).
		Scan(&r.LegKey, &r.BagTag, &r.UnloadFactID, &r.UnloadTime, &r.TransferFactID, &r.TransferTime,
			&r.ClaimFactID, &r.ClaimTime, &latest)
	if err == sql.ErrNoRows {
		return ArrivalRow{}, false, nil
	}
	if err != nil {
		return ArrivalRow{}, false, err
	}
	r.LatestSeq = uint64(latest)
	return r, true, nil
}

// UpsertArrival inserts or replaces an inbound projection row.
func UpsertArrival(ctx context.Context, q DBTX, r ArrivalRow) error {
	_, err := q.ExecContext(ctx, `INSERT INTO bag_leg_arrival(leg_key, bag_tag, unload_fact_id, unload_time,
		transfer_fact_id, transfer_time, claim_fact_id, claim_time, latest_seq)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(leg_key, bag_tag) DO UPDATE SET
			unload_fact_id=excluded.unload_fact_id, unload_time=excluded.unload_time,
			transfer_fact_id=excluded.transfer_fact_id, transfer_time=excluded.transfer_time,
			claim_fact_id=excluded.claim_fact_id, claim_time=excluded.claim_time,
			latest_seq=excluded.latest_seq`,
		r.LegKey, r.BagTag, r.UnloadFactID, r.UnloadTime, r.TransferFactID, r.TransferTime,
		r.ClaimFactID, r.ClaimTime, r.LatestSeq)
	if err != nil {
		return fmt.Errorf("store: upsert arrival: %w", err)
	}
	return nil
}

// ArrivalsForLeg returns all inbound projection rows for a leg.
func ArrivalsForLeg(ctx context.Context, q DBTX, legKey string) ([]ArrivalRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT leg_key, bag_tag, unload_fact_id, unload_time, transfer_fact_id,
		transfer_time, claim_fact_id, claim_time, latest_seq FROM bag_leg_arrival WHERE leg_key=? ORDER BY bag_tag`, legKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArrivalRow
	for rows.Next() {
		var r ArrivalRow
		var latest int64
		if err := rows.Scan(&r.LegKey, &r.BagTag, &r.UnloadFactID, &r.UnloadTime, &r.TransferFactID, &r.TransferTime,
			&r.ClaimFactID, &r.ClaimTime, &latest); err != nil {
			return nil, err
		}
		r.LatestSeq = uint64(latest)
		out = append(out, r)
	}
	return out, rows.Err()
}

// MaxCommitSeq returns the highest commit_seq ever assigned, recovered across
// facts, snapshots and jobs on restart.
func MaxCommitSeq(ctx context.Context, q DBTX) (uint64, error) {
	var facts, snaps, jobs sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(commit_seq),0) FROM scan_facts`).Scan(&facts)
	if err != nil {
		return 0, err
	}
	err = q.QueryRowContext(ctx, `SELECT COALESCE(MAX(fence_seq),0) FROM close_snapshots`).Scan(&snaps)
	if err != nil {
		return 0, err
	}
	err = q.QueryRowContext(ctx, `SELECT COALESCE(MAX(commit_seq),0) FROM logical_messages`).Scan(&jobs)
	if err != nil {
		return 0, err
	}
	m := facts.Int64
	if snaps.Int64 > m {
		m = snaps.Int64
	}
	if jobs.Int64 > m {
		m = jobs.Int64
	}
	return uint64(m), nil
}
