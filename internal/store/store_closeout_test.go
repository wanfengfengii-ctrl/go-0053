package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestClaimedCloseJobCannotBeImmediatelyStolen(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "close-jobs.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

	const (
		legKey = "LEG-20260816-PVG-001"
		nowISO = "2026-08-16T01:42:15.000000Z"
	)
	inserted, err := InsertCloseJob(ctx, s.DB(), CloseJobRow{
		JobKey:           "close-job-1",
		LegKey:           legKey,
		ManifestRevision: 1,
		State:            "PENDING",
		InitiatedAt:      nowISO,
	})
	if err != nil || !inserted {
		t.Fatalf("InsertCloseJob() = %v, %v; want true, nil", inserted, err)
	}

	claimed, err := ClaimCloseJobLease(ctx, s.DB(), legKey, "worker-1", nowISO)
	if err != nil || !claimed {
		t.Fatalf("first ClaimCloseJobLease() = %v, %v; want true, nil", claimed, err)
	}

	job, found, err := GetCloseJob(ctx, s.DB(), legKey)
	if err != nil || !found {
		t.Fatalf("GetCloseJob() after first claim = %#v, %v, %v; want job, true, nil", job, found, err)
	}
	if job.State != "LEASED" || job.LeaseToken != "worker-1" {
		t.Fatalf("first persisted lease = state %q, token %q; want LEASED, worker-1", job.State, job.LeaseToken)
	}
	leaseExpiry, err := time.Parse(time.RFC3339Nano, job.LeaseExpiresAt)
	if err != nil {
		t.Fatalf("persisted lease_expires_at %q is not RFC3339: %v", job.LeaseExpiresAt, err)
	}
	now, err := time.Parse(time.RFC3339Nano, nowISO)
	if err != nil {
		t.Fatalf("test now %q is not RFC3339: %v", nowISO, err)
	}
	if !leaseExpiry.After(now) {
		t.Fatalf("first lease expiry = %s; want after claim time %s", job.LeaseExpiresAt, nowISO)
	}
	firstExpiryISO := job.LeaseExpiresAt

	expired, err := ExpiredLeasedJobs(ctx, s.DB(), nowISO)
	if err != nil {
		t.Fatalf("ExpiredLeasedJobs() at claim time error = %v", err)
	}
	if len(expired) != 0 {
		t.Fatalf("ExpiredLeasedJobs() at claim time returned %d jobs; want 0", len(expired))
	}

	claimed, err = ClaimCloseJobLease(ctx, s.DB(), legKey, "worker-2", nowISO)
	if err != nil || claimed {
		t.Fatalf("competing ClaimCloseJobLease() = %v, %v; want false, nil", claimed, err)
	}
	job, found, err = GetCloseJob(ctx, s.DB(), legKey)
	if err != nil || !found {
		t.Fatalf("GetCloseJob() after rejected claim = %#v, %v, %v; want job, true, nil", job, found, err)
	}
	if job.LeaseToken != "worker-1" || job.LeaseExpiresAt != firstExpiryISO {
		t.Fatalf("rejected claim persisted token %q, expiry %q; want worker-1, %q", job.LeaseToken, job.LeaseExpiresAt, firstExpiryISO)
	}

	expired, err = ExpiredLeasedJobs(ctx, s.DB(), firstExpiryISO)
	if err != nil {
		t.Fatalf("ExpiredLeasedJobs() at expiry error = %v", err)
	}
	if len(expired) != 1 || expired[0].LeaseToken != "worker-1" {
		t.Fatalf("ExpiredLeasedJobs() at expiry = %#v; want worker-1 lease", expired)
	}

	claimed, err = ClaimCloseJobLease(ctx, s.DB(), legKey, "worker-2", firstExpiryISO)
	if err != nil || !claimed {
		t.Fatalf("post-expiry ClaimCloseJobLease() = %v, %v; want true, nil", claimed, err)
	}
	job, found, err = GetCloseJob(ctx, s.DB(), legKey)
	if err != nil || !found {
		t.Fatalf("GetCloseJob() after takeover = %#v, %v, %v; want job, true, nil", job, found, err)
	}
	if job.State != "LEASED" || job.LeaseToken != "worker-2" {
		t.Fatalf("takeover persisted state %q, token %q; want LEASED, worker-2", job.State, job.LeaseToken)
	}
	newExpiry, err := time.Parse(time.RFC3339Nano, job.LeaseExpiresAt)
	if err != nil {
		t.Fatalf("takeover lease_expires_at %q is not RFC3339: %v", job.LeaseExpiresAt, err)
	}
	if !newExpiry.After(leaseExpiry) {
		t.Fatalf("takeover lease expiry = %s; want after takeover time %s", job.LeaseExpiresAt, firstExpiryISO)
	}
}
