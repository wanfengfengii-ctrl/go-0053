package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// InsertCloseJob creates a new close job. Returns false if a job already exists
// for the leg.
func InsertCloseJob(ctx context.Context, q DBTX, r CloseJobRow) (bool, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO close_jobs(job_key, leg_key, manifest_revision, lease_token,
		lease_expires_at, state, initiated_at, completed_at, snapshot_id, last_error)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		r.JobKey, r.LegKey, r.ManifestRevision, r.LeaseToken, r.LeaseExpiresAt, r.State, r.InitiatedAt,
		r.CompletedAt, r.SnapshotID, r.LastError)
	if err != nil {
		// UNIQUE(job_key) or UNIQUE(leg_key) conflict => already exists.
		if isUniqueConstraint(err) {
			return false, nil
		}
		return false, fmt.Errorf("store: insert close_job: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetCloseJob returns a close job by leg key.
func GetCloseJob(ctx context.Context, q DBTX, legKey string) (CloseJobRow, bool, error) {
	var r CloseJobRow
	err := q.QueryRowContext(ctx, `SELECT job_key, leg_key, manifest_revision, lease_token, lease_expires_at, state,
		initiated_at, completed_at, snapshot_id, last_error FROM close_jobs WHERE leg_key=?`, legKey).
		Scan(&r.JobKey, &r.LegKey, &r.ManifestRevision, &r.LeaseToken, &r.LeaseExpiresAt, &r.State, &r.InitiatedAt,
			&r.CompletedAt, &r.SnapshotID, &r.LastError)
	if err == sql.ErrNoRows {
		return CloseJobRow{}, false, nil
	}
	if err != nil {
		return CloseJobRow{}, false, err
	}
	return r, true, nil
}

// UpdateCloseJob writes back a close job's mutable fields.
func UpdateCloseJob(ctx context.Context, q DBTX, r CloseJobRow) error {
	_, err := q.ExecContext(ctx, `UPDATE close_jobs SET manifest_revision=?, lease_token=?, lease_expires_at=?, state=?,
		completed_at=?, snapshot_id=?, last_error=? WHERE leg_key=?`,
		r.ManifestRevision, r.LeaseToken, r.LeaseExpiresAt, r.State, r.CompletedAt, r.SnapshotID, r.LastError, r.LegKey)
	if err != nil {
		return fmt.Errorf("store: update close_job: %w", err)
	}
	return nil
}

// ClaimCloseJobLease atomically acquires or refreshes a lease for an existing
// job. It only succeeds when the job is PENDING or its current lease has
// expired (by virtual clock). Returns true when the lease was acquired.
func ClaimCloseJobLease(ctx context.Context, q DBTX, legKey, token, nowISO string) (bool, error) {
	res, err := q.ExecContext(ctx, `UPDATE close_jobs SET lease_token=?, lease_expires_at=?, state='LEASED'
		WHERE leg_key=? AND (state='PENDING' OR lease_expires_at='' OR lease_expires_at<=?)`,
		token, nowISO, legKey, nowISO)
	if err != nil {
		return false, fmt.Errorf("store: claim lease: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ListCloseJobsByState returns jobs in a given state.
func ListCloseJobsByState(ctx context.Context, q DBTX, state string) ([]CloseJobRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT job_key, leg_key, manifest_revision, lease_token, lease_expires_at, state,
		initiated_at, completed_at, snapshot_id, last_error FROM close_jobs WHERE state=? ORDER BY leg_key`, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CloseJobRow
	for rows.Next() {
		var r CloseJobRow
		if err := rows.Scan(&r.JobKey, &r.LegKey, &r.ManifestRevision, &r.LeaseToken, &r.LeaseExpiresAt, &r.State,
			&r.InitiatedAt, &r.CompletedAt, &r.SnapshotID, &r.LastError); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ExpiredLeasedJobs returns LEASED jobs whose lease has expired by nowISO.
func ExpiredLeasedJobs(ctx context.Context, q DBTX, nowISO string) ([]CloseJobRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT job_key, leg_key, manifest_revision, lease_token, lease_expires_at, state,
		initiated_at, completed_at, snapshot_id, last_error FROM close_jobs WHERE state='LEASED' AND lease_expires_at<>'' AND lease_expires_at<=? ORDER BY leg_key`, nowISO)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CloseJobRow
	for rows.Next() {
		var r CloseJobRow
		if err := rows.Scan(&r.JobKey, &r.LegKey, &r.ManifestRevision, &r.LeaseToken, &r.LeaseExpiresAt, &r.State,
			&r.InitiatedAt, &r.CompletedAt, &r.SnapshotID, &r.LastError); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertCloseSnapshot freezes a departure snapshot and baseline report. The
// UNIQUE(leg_key) constraint guarantees only one baseline snapshot per leg.
func InsertCloseSnapshot(ctx context.Context, q DBTX, r CloseSnapshotRow) (int64, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO close_snapshots(leg_key, job_key, manifest_revision, fence_seq,
		frozen_at, report_json, report_sha256, counts_json) VALUES(?,?,?,?,?,?,?,?)`,
		r.LegKey, r.JobKey, r.ManifestRevision, r.FenceSeq, r.FrozenAt, r.ReportJSON, r.ReportSHA256, r.CountsJSON)
	if err != nil {
		if isUniqueConstraint(err) {
			return 0, ErrAlreadyClosed
		}
		return 0, fmt.Errorf("store: insert snapshot: %w", err)
	}
	return res.LastInsertId()
}

// ErrAlreadyClosed signals a leg already has a frozen baseline snapshot.
var ErrAlreadyClosed = fmt.Errorf("close snapshot already exists for leg")

// GetCloseSnapshot returns the frozen snapshot for a leg.
func GetCloseSnapshot(ctx context.Context, q DBTX, legKey string) (CloseSnapshotRow, bool, error) {
	var r CloseSnapshotRow
	err := q.QueryRowContext(ctx, `SELECT id, leg_key, job_key, manifest_revision, fence_seq, frozen_at, report_json,
		report_sha256, counts_json FROM close_snapshots WHERE leg_key=?`, legKey).
		Scan(&r.ID, &r.LegKey, &r.JobKey, &r.ManifestRevision, &r.FenceSeq, &r.FrozenAt, &r.ReportJSON,
			&r.ReportSHA256, &r.CountsJSON)
	if err == sql.ErrNoRows {
		return CloseSnapshotRow{}, false, nil
	}
	if err != nil {
		return CloseSnapshotRow{}, false, err
	}
	return r, true, nil
}

// InsertDiscrepancies bulk-inserts discrepancy rows for a snapshot.
func InsertDiscrepancies(ctx context.Context, q DBTX, snapshotID int64, rows []DiscrepancyRow) error {
	for i, d := range rows {
		_, err := q.ExecContext(ctx, `INSERT INTO discrepancies(snapshot_id, ord, bag_tag, code, fact_ids, detail)
			VALUES(?,?,?,?,?,?)`, snapshotID, i, d.BagTag, string(d.Code), d.FactIDs, d.Detail)
		if err != nil {
			return fmt.Errorf("store: insert discrepancy: %w", err)
		}
	}
	return nil
}

// DiscrepanciesForSnapshot returns discrepancy rows in stable order.
func DiscrepanciesForSnapshot(ctx context.Context, q DBTX, snapshotID int64) ([]DiscrepancyRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, snapshot_id, ord, bag_tag, code, fact_ids, detail FROM discrepancies
		WHERE snapshot_id=? ORDER BY ord`, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiscrepancyRow
	for rows.Next() {
		var d DiscrepancyRow
		if err := rows.Scan(&d.ID, &d.SnapshotID, &d.Ord, &d.BagTag, &d.Code, &d.FactIDs, &d.Detail); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// isUniqueConstraint reports whether err is a SQLite UNIQUE constraint failure.
func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE") || strings.Contains(msg, "constraint failed: UNIQUE")
}
