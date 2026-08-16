package store

import "github.com/baghub/baggage-leg-closeout-hub/internal/model"

// RawIngressRow is a persisted raw byte slice received from a connection.
type RawIngressRow struct {
	ID           int64
	ReceivedAt   string
	ConnID       string
	ByteLen      int
	RawBytes     []byte
	Status       string // OK | MALFORMED | EOF_PARTIAL
	RejectReason string
}

// LogicalMessageRow is the idempotency and parse-result record for a frame.
type LogicalMessageRow struct {
	ID            int64
	Device        string
	ControlNo     string
	RawIngressID  int64
	MsgHash       string
	ParseStatus   string
	ParseError    string
	ResultStatus  string // ACCEPTED | DUPLICATE | CONTROL_COLLISION | REJECTED
	ResultPayload string
	CommitSeq     uint64
	CreatedAt     string
}

// ScanFactRow is an immutable scan fact.
type ScanFactRow struct {
	ID              int64
	CommitSeq       uint64
	LegKey          string
	BagTag          string
	ScanType        model.ScanType
	Device          string
	ControlNo       string
	ScanTime        string
	WorkZone        string
	Operator        string
	StatusReason    string
	ScreenOutcome   model.ScreenOutcome
	IsLateEvidence  bool
	IsWrongZone     bool
	IsUnknownBag    bool
	RawIngressID    int64
	CreatedAt       string
}

// DepartureRow is the outbound projection for one (leg, bag).
type DepartureRow struct {
	LegKey         string
	BagTag         string
	CheckinFactID  int64
	CheckinTime    string
	SortFactID     int64
	SortTime       string
	ScreenFactID   int64
	ScreenTime     string
	ScreenOutcome  model.ScreenOutcome
	LoadFactID     int64
	LoadTime       string
	OffloadFactID  int64
	OffloadTime    string
	LatestSeq      uint64
	Closed         bool
	FenceSeq       uint64
}

// ArrivalRow is the inbound projection for one (leg, bag).
type ArrivalRow struct {
	LegKey         string
	BagTag         string
	UnloadFactID   int64
	UnloadTime     string
	TransferFactID int64
	TransferTime   string
	ClaimFactID    int64
	ClaimTime      string
	LatestSeq      uint64
}

// FlightLegRow is a catalog flight leg.
type FlightLegRow struct {
	LegKey          string
	Revision        int64
	Carrier         string
	FlightNo        string
	DepartureDate   string
	Origin          string
	Destination     string
	LegSeq          string
	SchedDep        string
	SchedArr        string
	ManifestRevision int64
}

// ZoneRow is a catalog work zone.
type ZoneRow struct {
	ZoneCode         string
	Revision         int64
	AllowedScanTypes []model.ScanType
}

// ManifestEntryRow is an expected bag on a leg manifest.
type ManifestEntryRow struct {
	ID               int64
	ManifestRevision int64
	LegKey           string
	BagTag           string
}

// CloseJobRow is a persistent close task.
type CloseJobRow struct {
	JobKey          string
	LegKey          string
	ManifestRevision int64
	LeaseToken      string
	LeaseExpiresAt  string
	State           string // PENDING | LEASED | COMPLETED | FAILED
	InitiatedAt     string
	CompletedAt     string
	SnapshotID      int64
	LastError       string
}

// CloseSnapshotRow is a frozen departure snapshot and baseline report.
type CloseSnapshotRow struct {
	ID               int64
	LegKey           string
	JobKey           string
	ManifestRevision int64
	FenceSeq         uint64
	FrozenAt         string
	ReportJSON       string
	ReportSHA256     string
	CountsJSON       string
}

// DiscrepancyRow is a reconciled mismatch within a snapshot.
type DiscrepancyRow struct {
	ID         int64
	SnapshotID int64
	Ord        int
	BagTag     string
	Code       model.DiscrepancyCode
	FactIDs    string
	Detail     string
}

// AuditRow is an append-only audit entry.
type AuditRow struct {
	ID       int64
	Ts       string
	Category string
	Ref      string
	Payload  string
}
