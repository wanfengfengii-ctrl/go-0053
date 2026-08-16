-- Baggage leg closeout hub schema (migration 0001).
--
-- All write paths funnel through a single linearised transaction per mutation
-- (see internal/coordinator). Within one transaction the raw frame, idempotency
-- record, scan fact and projection rows are written together so that only
-- fully-committed-before or fully-committed-after states are ever observable.

PRAGMA foreign_keys = ON;

-- Key/value metadata: commit-seq watermark, catalog revision counters.
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- Every byte slice ever received from a connection, including malformed and
-- EOF-partial frames. The original bytes are kept verbatim so that rejected
-- traffic remains auditable.
CREATE TABLE IF NOT EXISTS raw_ingress (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    received_at   TEXT NOT NULL,
    conn_id       TEXT NOT NULL,
    byte_len      INTEGER NOT NULL,
    raw_bytes     BLOB NOT NULL,
    status        TEXT NOT NULL,        -- OK | MALFORMED | EOF_PARTIAL
    reject_reason TEXT NOT NULL DEFAULT ''
);

-- Idempotency and parse result keyed by (device, control_no).
CREATE TABLE IF NOT EXISTS logical_messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    device          TEXT NOT NULL,
    control_no      TEXT NOT NULL,
    raw_ingress_id  INTEGER NOT NULL REFERENCES raw_ingress(id),
    msg_hash        TEXT NOT NULL,        -- sha256 of raw frame bytes
    parse_status    TEXT NOT NULL,        -- OK | BAD_LENGTH | BAD_BOUNDARY | BAD_ASCII | BAD_CRC | BAD_ENUM | EOF_PARTIAL
    parse_error     TEXT NOT NULL DEFAULT '',
    result_status   TEXT NOT NULL,        -- ACCEPTED | DUPLICATE | CONTROL_COLLISION | REJECTED
    result_payload   TEXT NOT NULL DEFAULT '',  -- cached first-result summary for duplicate returns
    commit_seq      INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL,
    UNIQUE (device, control_no)
);

-- Immutable scan facts. commit_seq is globally unique and monotonic.
CREATE TABLE IF NOT EXISTS scan_facts (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    commit_seq        INTEGER NOT NULL UNIQUE,
    leg_key           TEXT NOT NULL,
    bag_tag           TEXT NOT NULL,
    scan_type         TEXT NOT NULL,
    device            TEXT NOT NULL,
    control_no        TEXT NOT NULL,
    scan_time         TEXT NOT NULL,        -- frame UTC, YYYYMMDDHHMMSS
    work_zone         TEXT NOT NULL,
    operator          TEXT NOT NULL,
    status_reason     TEXT NOT NULL DEFAULT '',
    screen_outcome    TEXT NOT NULL DEFAULT '',  -- CLEAR | HOLD | ''
    is_late_evidence  INTEGER NOT NULL DEFAULT 0,
    is_wrong_zone     INTEGER NOT NULL DEFAULT 0,
    is_unknown_bag    INTEGER NOT NULL DEFAULT 0,
    raw_ingress_id    INTEGER NOT NULL REFERENCES raw_ingress(id),
    created_at        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_scan_facts_leg     ON scan_facts(leg_key, bag_tag, scan_type);
CREATE INDEX IF NOT EXISTS idx_scan_facts_seq     ON scan_facts(commit_seq);

-- Outbound projection per (leg, bag). Frozen at close.
CREATE TABLE IF NOT EXISTS bag_leg_departure (
    leg_key            TEXT NOT NULL,
    bag_tag            TEXT NOT NULL,
    checkin_fact_id    INTEGER NOT NULL DEFAULT 0,
    checkin_time       TEXT NOT NULL DEFAULT '',
    sort_fact_id        INTEGER NOT NULL DEFAULT 0,
    sort_time          TEXT NOT NULL DEFAULT '',
    screen_fact_id      INTEGER NOT NULL DEFAULT 0,
    screen_time        TEXT NOT NULL DEFAULT '',
    screen_outcome     TEXT NOT NULL DEFAULT '',  -- CLEAR | HOLD | ''
    load_fact_id        INTEGER NOT NULL DEFAULT 0,
    load_time          TEXT NOT NULL DEFAULT '',
    offload_fact_id     INTEGER NOT NULL DEFAULT 0,
    offload_time       TEXT NOT NULL DEFAULT '',
    latest_seq          INTEGER NOT NULL DEFAULT 0,
    closed              INTEGER NOT NULL DEFAULT 0,
    fence_seq          INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (leg_key, bag_tag)
);
CREATE INDEX IF NOT EXISTS idx_departure_leg ON bag_leg_departure(leg_key);

-- Inbound projection per (leg, bag). Independent of departure close.
CREATE TABLE IF NOT EXISTS bag_leg_arrival (
    leg_key            TEXT NOT NULL,
    bag_tag            TEXT NOT NULL,
    unload_fact_id     INTEGER NOT NULL DEFAULT 0,
    unload_time        TEXT NOT NULL DEFAULT '',
    transfer_fact_id  INTEGER NOT NULL DEFAULT 0,
    transfer_time      TEXT NOT NULL DEFAULT '',
    claim_fact_id      INTEGER NOT NULL DEFAULT 0,
    claim_time         TEXT NOT NULL DEFAULT '',
    latest_seq         INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (leg_key, bag_tag)
);
CREATE INDEX IF NOT EXISTS idx_arrival_leg ON bag_leg_arrival(leg_key);

-- Immutable flight-leg catalog, versioned by revision.
CREATE TABLE IF NOT EXISTS flight_legs (
    leg_key            TEXT PRIMARY KEY,
    revision           INTEGER NOT NULL,
    carrier            TEXT NOT NULL,
    flight_no          TEXT NOT NULL,
    departure_date     TEXT NOT NULL,
    origin             TEXT NOT NULL,
    destination        TEXT NOT NULL,
    leg_seq            TEXT NOT NULL,
    sched_dep          TEXT NOT NULL DEFAULT '',
    sched_arr          TEXT NOT NULL DEFAULT '',
    manifest_revision  INTEGER NOT NULL DEFAULT 0
);

-- Work zones and the scan types each permits.
CREATE TABLE IF NOT EXISTS zones (
    zone_code          TEXT PRIMARY KEY,
    revision           INTEGER NOT NULL,
    allowed_scan_types TEXT NOT NULL DEFAULT ''  -- comma-separated model.ScanType
);

-- Expected-bag manifests, immutable per revision.
CREATE TABLE IF NOT EXISTS manifest_entries (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    manifest_revision INTEGER NOT NULL,
    leg_key           TEXT NOT NULL,
    bag_tag           TEXT NOT NULL,
    UNIQUE (manifest_revision, leg_key, bag_tag)
);
CREATE INDEX IF NOT EXISTS idx_manifest_rev_leg ON manifest_entries(manifest_revision, leg_key);

-- Persistent close jobs with leases.
CREATE TABLE IF NOT EXISTS close_jobs (
    job_key            TEXT PRIMARY KEY,
    leg_key            TEXT NOT NULL UNIQUE,
    manifest_revision  INTEGER NOT NULL,
    lease_token        TEXT NOT NULL DEFAULT '',
    lease_expires_at   TEXT NOT NULL DEFAULT '',
    state              TEXT NOT NULL,        -- PENDING | LEASED | COMPLETED | FAILED
    initiated_at       TEXT NOT NULL,
    completed_at       TEXT NOT NULL DEFAULT '',
    snapshot_id        INTEGER NOT NULL DEFAULT 0,
    last_error         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_close_jobs_state ON close_jobs(state);

-- Frozen departure snapshot + canonical baseline report. One per leg.
CREATE TABLE IF NOT EXISTS close_snapshots (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    leg_key           TEXT NOT NULL UNIQUE,
    job_key           TEXT NOT NULL,
    manifest_revision INTEGER NOT NULL,
    fence_seq         INTEGER NOT NULL,
    frozen_at         TEXT NOT NULL,
    report_json       TEXT NOT NULL,
    report_sha256     TEXT NOT NULL,
    counts_json       TEXT NOT NULL DEFAULT ''
);

-- Reconciled discrepancy rows belonging to a snapshot, in stable order.
CREATE TABLE IF NOT EXISTS discrepancies (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    snapshot_id INTEGER NOT NULL REFERENCES close_snapshots(id),
    ord         INTEGER NOT NULL,
    bag_tag     TEXT NOT NULL,
    code        TEXT NOT NULL,
    fact_ids    TEXT NOT NULL DEFAULT '',   -- comma-separated scan_facts.id
    detail      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_discrepancies_snap ON discrepancies(snapshot_id, ord);

-- Append-only audit trail.
CREATE TABLE IF NOT EXISTS audit_records (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    ts       TEXT NOT NULL,
    category TEXT NOT NULL,
    ref      TEXT NOT NULL DEFAULT '',
    payload  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_records(ts);
