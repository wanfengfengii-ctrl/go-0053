package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/baghub/baggage-leg-closeout-hub/internal/model"
)

// UpsertFlightLeg inserts or replaces a catalog flight leg.
func UpsertFlightLeg(ctx context.Context, q DBTX, r FlightLegRow) error {
	_, err := q.ExecContext(ctx, `INSERT INTO flight_legs(leg_key, revision, carrier, flight_no, departure_date, origin,
		destination, leg_seq, sched_dep, sched_arr, manifest_revision)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(leg_key) DO UPDATE SET revision=excluded.revision, carrier=excluded.carrier,
			flight_no=excluded.flight_no, departure_date=excluded.departure_date, origin=excluded.origin,
			destination=excluded.destination, leg_seq=excluded.leg_seq, sched_dep=excluded.sched_dep,
			sched_arr=excluded.sched_arr, manifest_revision=excluded.manifest_revision`,
		r.LegKey, r.Revision, r.Carrier, r.FlightNo, r.DepartureDate, r.Origin, r.Destination, r.LegSeq,
		r.SchedDep, r.SchedArr, r.ManifestRevision)
	if err != nil {
		return fmt.Errorf("store: upsert flight_leg: %w", err)
	}
	return nil
}

// GetFlightLeg returns a catalog leg by key.
func GetFlightLeg(ctx context.Context, q DBTX, legKey string) (FlightLegRow, bool, error) {
	var r FlightLegRow
	err := q.QueryRowContext(ctx, `SELECT leg_key, revision, carrier, flight_no, departure_date, origin, destination,
		leg_seq, sched_dep, sched_arr, manifest_revision FROM flight_legs WHERE leg_key=?`, legKey).
		Scan(&r.LegKey, &r.Revision, &r.Carrier, &r.FlightNo, &r.DepartureDate, &r.Origin, &r.Destination,
			&r.LegSeq, &r.SchedDep, &r.SchedArr, &r.ManifestRevision)
	if err == sql.ErrNoRows {
		return FlightLegRow{}, false, nil
	}
	if err != nil {
		return FlightLegRow{}, false, err
	}
	return r, true, nil
}

// ListFlightLegs returns all catalog legs ordered by key.
func ListFlightLegs(ctx context.Context, q DBTX) ([]FlightLegRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT leg_key, revision, carrier, flight_no, departure_date, origin, destination,
		leg_seq, sched_dep, sched_arr, manifest_revision FROM flight_legs ORDER BY leg_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FlightLegRow
	for rows.Next() {
		var r FlightLegRow
		if err := rows.Scan(&r.LegKey, &r.Revision, &r.Carrier, &r.FlightNo, &r.DepartureDate, &r.Origin, &r.Destination,
			&r.LegSeq, &r.SchedDep, &r.SchedArr, &r.ManifestRevision); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpsertZone inserts or replaces a work zone and its allowed scan types.
func UpsertZone(ctx context.Context, q DBTX, r ZoneRow) error {
	allowed := joinScanTypes(r.AllowedScanTypes)
	_, err := q.ExecContext(ctx, `INSERT INTO zones(zone_code, revision, allowed_scan_types)
		VALUES(?,?,?) ON CONFLICT(zone_code) DO UPDATE SET revision=excluded.revision,
			allowed_scan_types=excluded.allowed_scan_types`,
		r.ZoneCode, r.Revision, allowed)
	if err != nil {
		return fmt.Errorf("store: upsert zone: %w", err)
	}
	return nil
}

// GetZone returns a work zone by code.
func GetZone(ctx context.Context, q DBTX, zoneCode string) (ZoneRow, bool, error) {
	var r ZoneRow
	var allowed string
	err := q.QueryRowContext(ctx, `SELECT zone_code, revision, allowed_scan_types FROM zones WHERE zone_code=?`, zoneCode).
		Scan(&r.ZoneCode, &r.Revision, &allowed)
	if err == sql.ErrNoRows {
		return ZoneRow{}, false, nil
	}
	if err != nil {
		return ZoneRow{}, false, err
	}
	r.AllowedScanTypes = parseScanTypes(allowed)
	return r, true, nil
}

// ListZones returns all work zones.
func ListZones(ctx context.Context, q DBTX) ([]ZoneRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT zone_code, revision, allowed_scan_types FROM zones ORDER BY zone_code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ZoneRow
	for rows.Next() {
		var r ZoneRow
		var allowed string
		if err := rows.Scan(&r.ZoneCode, &r.Revision, &allowed); err != nil {
			return nil, err
		}
		r.AllowedScanTypes = parseScanTypes(allowed)
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertManifestEntries bulk-inserts expected bags for a manifest revision.
func InsertManifestEntries(ctx context.Context, q DBTX, revision int64, legKey string, bags []string) error {
	stmt, err := q.PrepareContext(ctx, `INSERT INTO manifest_entries(manifest_revision, leg_key, bag_tag) VALUES(?,?,?)
		ON CONFLICT(manifest_revision, leg_key, bag_tag) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("store: prepare manifest: %w", err)
	}
	defer stmt.Close()
	for _, b := range bags {
		if _, err := stmt.ExecContext(ctx, revision, legKey, b); err != nil {
			return fmt.Errorf("store: insert manifest entry: %w", err)
		}
	}
	return nil
}

// ManifestForLeg returns the expected bag tags for a (revision, leg).
func ManifestForLeg(ctx context.Context, q DBTX, revision int64, legKey string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT bag_tag FROM manifest_entries WHERE manifest_revision=? AND leg_key=? ORDER BY bag_tag`, revision, legKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BagInManifest reports whether a bag is expected on a manifest revision+leg.
func BagInManifest(ctx context.Context, q DBTX, revision int64, legKey, bagTag string) (bool, error) {
	var n int64
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM manifest_entries WHERE manifest_revision=? AND leg_key=? AND bag_tag=?`,
		revision, legKey, bagTag).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func joinScanTypes(ts []model.ScanType) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = string(t)
	}
	return strings.Join(parts, ",")
}

func parseScanTypes(s string) []model.ScanType {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]model.ScanType, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, model.ScanType(p))
	}
	return out
}
