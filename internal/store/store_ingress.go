package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"

	"github.com/baghub/baggage-leg-closeout-hub/internal/model"
)

// InsertRawIngress persists a raw byte slice received from a connection,
// returning its assigned id. status is OK, MALFORMED or EOF_PARTIAL.
func InsertRawIngress(ctx context.Context, q DBTX, receivedAt, connID string, raw []byte, status, reason string) (int64, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO raw_ingress(received_at, conn_id, byte_len, raw_bytes, status, reject_reason)
		VALUES(?,?,?,?,?,?)`, receivedAt, connID, len(raw), raw, status, reason)
	if err != nil {
		return 0, fmt.Errorf("store: insert raw_ingress: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

// GetRawIngress loads a raw ingress row by id.
func GetRawIngress(ctx context.Context, q DBTX, id int64) (RawIngressRow, error) {
	var r RawIngressRow
	err := q.QueryRowContext(ctx, `SELECT id, received_at, conn_id, byte_len, raw_bytes, status, reject_reason
		FROM raw_ingress WHERE id=?`, id).
		Scan(&r.ID, &r.ReceivedAt, &r.ConnID, &r.ByteLen, &r.RawBytes, &r.Status, &r.RejectReason)
	if err != nil {
		return RawIngressRow{}, err
	}
	return r, nil
}

// FindLogicalMessage returns the existing idempotency record for a device and
// control number, if any.
func FindLogicalMessage(ctx context.Context, q DBTX, device, controlNo string) (LogicalMessageRow, bool, error) {
	var r LogicalMessageRow
	err := q.QueryRowContext(ctx, `SELECT id, device, control_no, raw_ingress_id, msg_hash, parse_status, parse_error,
		result_status, result_payload, commit_seq, created_at
		FROM logical_messages WHERE device=? AND control_no=?`, device, controlNo).
		Scan(&r.ID, &r.Device, &r.ControlNo, &r.RawIngressID, &r.MsgHash, &r.ParseStatus, &r.ParseError,
			&r.ResultStatus, &r.ResultPayload, &r.CommitSeq, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return LogicalMessageRow{}, false, nil
	}
	if err != nil {
		return LogicalMessageRow{}, false, err
	}
	return r, true, nil
}

// InsertLogicalMessage creates a new idempotency record.
func InsertLogicalMessage(ctx context.Context, q DBTX, r LogicalMessageRow) (int64, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO logical_messages(device, control_no, raw_ingress_id, msg_hash,
		parse_status, parse_error, result_status, result_payload, commit_seq, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		r.Device, r.ControlNo, r.RawIngressID, r.MsgHash, r.ParseStatus, r.ParseError,
		r.ResultStatus, r.ResultPayload, r.CommitSeq, r.CreatedAt)
	if err != nil {
		return 0, fmt.Errorf("store: insert logical_messages: %w", err)
	}
	return res.LastInsertId()
}

// HashFrame returns the lowercase hex sha256 of raw frame bytes, used for
// duplicate detection and control-collision detection.
func HashFrame(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// InsertScanFact persists an immutable scan fact and returns its id. The
// commit_seq unique constraint guarantees no two facts share a ticket.
func InsertScanFact(ctx context.Context, q DBTX, r ScanFactRow) (int64, error) {
	var late, wrong, unknown int64
	if r.IsLateEvidence {
		late = 1
	}
	if r.IsWrongZone {
		wrong = 1
	}
	if r.IsUnknownBag {
		unknown = 1
	}
	res, err := q.ExecContext(ctx, `INSERT INTO scan_facts(commit_seq, leg_key, bag_tag, scan_type, device, control_no,
		scan_time, work_zone, operator, status_reason, screen_outcome, is_late_evidence, is_wrong_zone, is_unknown_bag,
		raw_ingress_id, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.CommitSeq, r.LegKey, r.BagTag, string(r.ScanType), r.Device, r.ControlNo, r.ScanTime, r.WorkZone,
		r.Operator, r.StatusReason, string(r.ScreenOutcome), late, wrong, unknown, r.RawIngressID, r.CreatedAt)
	if err != nil {
		return 0, fmt.Errorf("store: insert scan_facts: %w", err)
	}
	return res.LastInsertId()
}

// GetScanFact loads a scan fact by id.
func GetScanFact(ctx context.Context, q DBTX, id int64) (ScanFactRow, error) {
	var r ScanFactRow
	var late, wrong, unknown int64
	var screen string
	err := q.QueryRowContext(ctx, `SELECT id, commit_seq, leg_key, bag_tag, scan_type, device, control_no, scan_time,
		work_zone, operator, status_reason, screen_outcome, is_late_evidence, is_wrong_zone, is_unknown_bag,
		raw_ingress_id, created_at FROM scan_facts WHERE id=?`, id).
		Scan(&r.ID, &r.CommitSeq, &r.LegKey, &r.BagTag, &r.ScanType, &r.Device, &r.ControlNo, &r.ScanTime,
			&r.WorkZone, &r.Operator, &r.StatusReason, &screen, &late, &wrong, &unknown, &r.RawIngressID, &r.CreatedAt)
	if err != nil {
		return ScanFactRow{}, err
	}
	r.ScreenOutcome = model.ScreenOutcome(screen)
	r.IsLateEvidence = late == 1
	r.IsWrongZone = wrong == 1
	r.IsUnknownBag = unknown == 1
	return r, nil
}

// FactsForLeg returns all scan facts for a leg, ordered by commit_seq.
func FactsForLeg(ctx context.Context, q DBTX, legKey string) ([]ScanFactRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, commit_seq, leg_key, bag_tag, scan_type, device, control_no, scan_time,
		work_zone, operator, status_reason, screen_outcome, is_late_evidence, is_wrong_zone, is_unknown_bag,
		raw_ingress_id, created_at FROM scan_facts WHERE leg_key=? ORDER BY commit_seq`, legKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFactsFromRows(rows)
}

// FactsForLegBag returns all scan facts for a (leg, bag).
func FactsForLegBag(ctx context.Context, q DBTX, legKey, bagTag string) ([]ScanFactRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, commit_seq, leg_key, bag_tag, scan_type, device, control_no, scan_time,
		work_zone, operator, status_reason, screen_outcome, is_late_evidence, is_wrong_zone, is_unknown_bag,
		raw_ingress_id, created_at FROM scan_facts WHERE leg_key=? AND bag_tag=? ORDER BY commit_seq`, legKey, bagTag)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFactsFromRows(rows)
}

// EvidenceFactsForLeg returns wrong-zone and unknown-bag evidence facts for a leg.
func EvidenceFactsForLeg(ctx context.Context, q DBTX, legKey string) ([]ScanFactRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, commit_seq, leg_key, bag_tag, scan_type, device, control_no, scan_time,
		work_zone, operator, status_reason, screen_outcome, is_late_evidence, is_wrong_zone, is_unknown_bag,
		raw_ingress_id, created_at FROM scan_facts WHERE leg_key=? AND (is_wrong_zone=1 OR is_unknown_bag=1)
		ORDER BY bag_tag, scan_type, commit_seq`, legKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFactsFromRows(rows)
}

func scanFactsFromRows(rows *sql.Rows) ([]ScanFactRow, error) {
	var out []ScanFactRow
	for rows.Next() {
		var r ScanFactRow
		var late, wrong, unknown int64
		var screen string
		if err := rows.Scan(&r.ID, &r.CommitSeq, &r.LegKey, &r.BagTag, &r.ScanType, &r.Device, &r.ControlNo, &r.ScanTime,
			&r.WorkZone, &r.Operator, &r.StatusReason, &screen, &late, &wrong, &unknown, &r.RawIngressID, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.ScreenOutcome = model.ScreenOutcome(screen)
		r.IsLateEvidence = late == 1
		r.IsWrongZone = wrong == 1
		r.IsUnknownBag = unknown == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// LateEvidenceForLeg returns outbound facts that arrived after the leg's close
// fence, ordered by scan_time, device, control_no, id.
func LateEvidenceForLeg(ctx context.Context, q DBTX, legKey string, fenceSeq uint64) ([]ScanFactRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, commit_seq, leg_key, bag_tag, scan_type, device, control_no, scan_time,
		work_zone, operator, status_reason, screen_outcome, is_late_evidence, is_wrong_zone, is_unknown_bag,
		raw_ingress_id, created_at FROM scan_facts WHERE leg_key=? AND is_late_evidence=1
		ORDER BY scan_time, device, control_no, id`, legKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFactsFromRows(rows)
}
