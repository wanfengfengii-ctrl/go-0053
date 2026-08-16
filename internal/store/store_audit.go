package store

import (
	"context"
	"fmt"
)

// InsertAudit appends an audit record with the given ISO-8601 UTC timestamp.
func InsertAudit(ctx context.Context, q DBTX, ts, category, ref, payload string) error {
	_, err := q.ExecContext(ctx, `INSERT INTO audit_records(ts, category, ref, payload) VALUES(?,?,?,?)`,
		ts, category, ref, payload)
	if err != nil {
		return fmt.Errorf("store: insert audit: %w", err)
	}
	return nil
}

// ListAudit returns up to the limit most recent audit records, ordered by id
// from oldest to newest so that the newest record is the last element of the
// returned slice. When limit is less than the total number of records only the
// most recent `limit` records are returned, still in oldest-to-newest order.
func ListAudit(ctx context.Context, q DBTX, limit int) ([]AuditRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, ts, category, ref, payload FROM (
			SELECT id, ts, category, ref, payload FROM audit_records ORDER BY id DESC LIMIT ?
		) ORDER BY id ASC`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var a AuditRow
		if err := rows.Scan(&a.ID, &a.Ts, &a.Category, &a.Ref, &a.Payload); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
