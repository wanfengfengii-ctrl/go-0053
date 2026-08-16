// Package catalog manages the immutable, versioned reference data that gives
// every scan a place to land: flight legs, work zones (and the scan types each
// permits) and expected-bag manifests.
//
// Each import allocates a fresh, monotonically increasing revision stored in
// the meta table and writes the new rows without disturbing older revisions,
// so a close that freezes manifest_revision N keeps seeing exactly the bags
// that were expected at revision N even after a later manifest import.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/baghub/baggage-leg-closeout-hub/internal/model"
	"github.com/baghub/baggage-leg-closeout-hub/internal/store"
)

// Catalog wraps a store with versioned reference-data operations.
type Catalog struct {
	store *store.Store
}

// New returns a Catalog backed by s.
func New(s *store.Store) *Catalog { return &Catalog{store: s} }

// FlightLegInput is one leg in a flight-table import file.
type FlightLegInput struct {
	Carrier         string `json:"carrier"`
	FlightNo        string `json:"flight_no"`
	DepartureDate   string `json:"departure_date"`
	Origin          string `json:"origin"`
	Destination     string `json:"destination"`
	LegSeq          string `json:"leg_seq"`
	SchedDep        string `json:"sched_dep"`
	SchedArr        string `json:"sched_arr"`
	ManifestRevision int64  `json:"manifest_revision"`
}

// FlightTableFile is the JSON shape of a flight-table import.
type FlightTableFile struct {
	Legs []FlightLegInput `json:"legs"`
}

// ZoneInput is one zone in a zone-table import file.
type ZoneInput struct {
	Code             string   `json:"code"`
	AllowedScanTypes []string `json:"allowed_scan_types"`
}

// ZoneFile is the JSON shape of a zone-table import.
type ZoneFile struct {
	Zones []ZoneInput `json:"zones"`
}

// ManifestEntryInput is one expected bag in a manifest import file.
type ManifestEntryInput struct {
	Carrier       string `json:"carrier"`
	FlightNo      string `json:"flight_no"`
	DepartureDate string `json:"departure_date"`
	Origin        string `json:"origin"`
	Destination   string `json:"destination"`
	LegSeq        string `json:"leg_seq"`
	BagTag        string `json:"bag_tag"`
}

// ManifestFile is the JSON shape of an expected-bag manifest import.
type ManifestFile struct {
	Entries []ManifestEntryInput `json:"entries"`
}

// ImportFlightTable parses a flight-table JSON file, allocates the next flight
// revision and upserts every leg. Legs whose required fields are empty are
// rejected. Returns the allocated revision.
func (c *Catalog) ImportFlightTable(ctx context.Context, data []byte) (int64, error) {
	var f FlightTableFile
	if err := json.Unmarshal(data, &f); err != nil {
		return 0, fmt.Errorf("catalog: parse flight table: %w", err)
	}
	for i, l := range f.Legs {
		if strings.TrimSpace(l.Carrier) == "" || strings.TrimSpace(l.FlightNo) == "" ||
			strings.TrimSpace(l.DepartureDate) == "" || strings.TrimSpace(l.Origin) == "" ||
			strings.TrimSpace(l.Destination) == "" || strings.TrimSpace(l.LegSeq) == "" {
			return 0, fmt.Errorf("catalog: flight leg %d missing required field", i)
		}
	}
	tx, err := c.store.BeginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rev, err := nextRevision(ctx, tx, "catalog.flight_revision")
	if err != nil {
		return 0, err
	}
	for _, l := range f.Legs {
		legKey := model.LegKey(l.Carrier, l.FlightNo, l.DepartureDate, l.Origin, l.Destination, l.LegSeq)
		row := store.FlightLegRow{
			LegKey:           legKey,
			Revision:         rev,
			Carrier:          strings.ToUpper(strings.TrimSpace(l.Carrier)),
			FlightNo:         strings.ToUpper(strings.TrimSpace(l.FlightNo)),
			DepartureDate:    strings.TrimSpace(l.DepartureDate),
			Origin:           strings.ToUpper(strings.TrimSpace(l.Origin)),
			Destination:      strings.ToUpper(strings.TrimSpace(l.Destination)),
			LegSeq:           strings.TrimSpace(l.LegSeq),
			SchedDep:         l.SchedDep,
			SchedArr:         l.SchedArr,
			ManifestRevision: l.ManifestRevision,
		}
		if err := store.UpsertFlightLeg(ctx, tx, row); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return rev, nil
}

// ImportZones parses a zone-table JSON file, allocates the next zone revision
// and upserts every zone. Unknown scan-type names are rejected.
func (c *Catalog) ImportZones(ctx context.Context, data []byte) (int64, error) {
	var f ZoneFile
	if err := json.Unmarshal(data, &f); err != nil {
		return 0, fmt.Errorf("catalog: parse zones: %w", err)
	}
	valid := map[model.ScanType]bool{}
	for _, st := range model.AllScanTypes() {
		valid[st] = true
	}
	for i, z := range f.Zones {
		if strings.TrimSpace(z.Code) == "" {
			return 0, fmt.Errorf("catalog: zone %d missing code", i)
		}
		for _, st := range z.AllowedScanTypes {
			if !valid[model.ScanType(strings.TrimSpace(st))] {
				return 0, fmt.Errorf("catalog: zone %s unknown scan type %q", z.Code, st)
			}
		}
	}
	tx, err := c.store.BeginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rev, err := nextRevision(ctx, tx, "catalog.zone_revision")
	if err != nil {
		return 0, err
	}
	for _, z := range f.Zones {
		types := make([]model.ScanType, 0, len(z.AllowedScanTypes))
		for _, st := range z.AllowedScanTypes {
			types = append(types, model.ScanType(strings.TrimSpace(st)))
		}
		if err := store.UpsertZone(ctx, tx, store.ZoneRow{
			ZoneCode:         strings.TrimSpace(z.Code),
			Revision:         rev,
			AllowedScanTypes: types,
		}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return rev, nil
}

// ImportManifest parses an expected-bag manifest JSON file, allocates the next
// manifest revision and inserts every entry. Returns the allocated revision.
func (c *Catalog) ImportManifest(ctx context.Context, data []byte) (int64, error) {
	var f ManifestFile
	if err := json.Unmarshal(data, &f); err != nil {
		return 0, fmt.Errorf("catalog: parse manifest: %w", err)
	}
	tx, err := c.store.BeginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rev, err := nextRevision(ctx, tx, "catalog.manifest_revision")
	if err != nil {
		return 0, err
	}
	byLeg := map[string][]string{}
	for _, e := range f.Entries {
		legKey := model.LegKey(e.Carrier, e.FlightNo, e.DepartureDate, e.Origin, e.Destination, e.LegSeq)
		byLeg[legKey] = append(byLeg[legKey], strings.TrimSpace(e.BagTag))
	}
	for legKey, bags := range byLeg {
		if err := store.InsertManifestEntries(ctx, tx, rev, legKey, bags); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return rev, nil
}

// LegByKey returns the catalog leg for a leg key.
func (c *Catalog) LegByKey(ctx context.Context, legKey string) (store.FlightLegRow, bool, error) {
	return store.GetFlightLeg(ctx, c.store.DB(), legKey)
}

// ZoneByCode returns a work zone.
func (c *Catalog) ZoneByCode(ctx context.Context, code string) (store.ZoneRow, bool, error) {
	return store.GetZone(ctx, c.store.DB(), code)
}

// ZoneAllows reports whether a zone permits a scan type. An unknown zone
// permits nothing.
func (c *Catalog) ZoneAllows(ctx context.Context, zoneCode string, st model.ScanType) (bool, error) {
	z, ok, err := c.ZoneByCode(ctx, zoneCode)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	for _, a := range z.AllowedScanTypes {
		if a == st {
			return true, nil
		}
	}
	return false, nil
}

// ManifestBags returns the expected bag tags for a (revision, leg).
func (c *Catalog) ManifestBags(ctx context.Context, revision int64, legKey string) ([]string, error) {
	return store.ManifestForLeg(ctx, c.store.DB(), revision, legKey)
}

// BagExpected reports whether a bag is on a manifest revision+leg.
func (c *Catalog) BagExpected(ctx context.Context, revision int64, legKey, bagTag string) (bool, error) {
	return store.BagInManifest(ctx, c.store.DB(), revision, legKey, bagTag)
}

// ListLegs returns all catalog legs.
func (c *Catalog) ListLegs(ctx context.Context) ([]store.FlightLegRow, error) {
	return store.ListFlightLegs(ctx, c.store.DB())
}

// ListZones returns all work zones.
func (c *Catalog) ListZones(ctx context.Context) ([]store.ZoneRow, error) {
	return store.ListZones(ctx, c.store.DB())
}

// nextRevision reads and increments a meta revision counter inside a tx.
func nextRevision(ctx context.Context, q store.DBTX, key string) (int64, error) {
	v, err := store.MetaGet(ctx, q, key)
	if err != nil {
		return 0, err
	}
	var rev int64
	if v != "" {
		if _, err := fmt.Sscanf(v, "%d", &rev); err != nil {
			return 0, fmt.Errorf("catalog: bad revision meta %q: %w", key, err)
		}
	}
	rev++
	if err := store.MetaSetTx(ctx, q, key, fmt.Sprintf("%d", rev)); err != nil {
		return 0, err
	}
	return rev, nil
}
