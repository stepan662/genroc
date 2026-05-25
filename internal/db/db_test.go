package db

import (
	"os"
	"testing"
	"time"

	"gent/internal/model"
)

func newTestDB(t *testing.T) (*DB, func()) {
	t.Helper()
	f, err := os.CreateTemp("", "gent-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	database, err := Open(f.Name())
	if err != nil {
		os.Remove(f.Name())
		t.Fatal(err)
	}
	return database, func() {
		database.Close()
		os.Remove(f.Name())
	}
}

func insertRunning(t *testing.T, db *DB, id string) {
	t.Helper()
	inst := &model.ProcessInstance{
		ID:             id,
		ProcessName:    "test",
		ProcessVersion: 1,
		StepQueue:      []*model.Step{},
		ContextData:    map[string]any{},
		Status:         model.StatusRunning,
	}
	if err := db.SaveInstance(inst); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}
}

// TestClaimInstances_Basic verifies that an unclaimed instance is returned with
// the claiming worker's ID and a set lease expiry (RETURNING gives post-update state).
func TestClaimInstances_Basic(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	insertRunning(t, db, "inst-1")

	got, err := db.ClaimInstances("worker-A", 10*time.Second, 10)
	if err != nil {
		t.Fatalf("ClaimInstances: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(got))
	}
	if got[0].WorkerID == nil || *got[0].WorkerID != "worker-A" {
		t.Errorf("expected WorkerID=worker-A, got %v", got[0].WorkerID)
	}
	if got[0].LeaseExpiresAt == nil {
		t.Error("expected lease_expires_at to be set")
	}
}

// TestClaimInstances_SkipsLiveLease verifies that a second worker cannot steal
// an instance whose lease has not yet expired.
func TestClaimInstances_SkipsLiveLease(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	insertRunning(t, db, "inst-1")

	if _, err := db.ClaimInstances("worker-A", 10*time.Second, 10); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	got, err := db.ClaimInstances("worker-B", 10*time.Second, 10)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 instances (lease still live), got %d", len(got))
	}
}

// TestClaimInstances_ReclaimsExpiredLease verifies that after a lease expires a new
// worker can reclaim the instance. RETURNING gives post-update state, so the returned
// instance already shows the new owner.
func TestClaimInstances_ReclaimsExpiredLease(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	insertRunning(t, db, "inst-1")

	// Claim with a very short lease.
	if _, err := db.ClaimInstances("worker-A", 10*time.Millisecond, 10); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	time.Sleep(20 * time.Millisecond) // let the lease expire

	got, err := db.ClaimInstances("worker-B", 10*time.Second, 10)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 reclaimed instance, got %d", len(got))
	}
	if got[0].WorkerID == nil || *got[0].WorkerID != "worker-B" {
		t.Errorf("expected WorkerID=worker-B after reclaim, got %v", got[0].WorkerID)
	}
}

// TestRenewLease_Extends verifies that a successful renewal pushes the expiry
// far enough forward that a competing worker cannot reclaim the instance.
func TestRenewLease_Extends(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	insertRunning(t, db, "inst-1")

	// Claim with a lease that would expire in 30 ms.
	if _, err := db.ClaimInstances("worker-A", 30*time.Millisecond, 10); err != nil {
		t.Fatalf("claim: %v", err)
	}

	time.Sleep(20 * time.Millisecond) // still alive but close to expiry

	// Renew for another full second.
	if err := db.RenewWorkerLeases("worker-A", time.Second); err != nil {
		t.Fatalf("RenewWorkerLeases: %v", err)
	}

	time.Sleep(20 * time.Millisecond) // original lease would have expired here

	got, err := db.ClaimInstances("worker-B", 10*time.Second, 10)
	if err != nil {
		t.Fatalf("competitor claim: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 instances after successful renewal, got %d", len(got))
	}
}

// TestRenewLease_WrongWorker verifies that renewal by a non-owner is a no-op,
// so the lease expires on schedule and another worker can reclaim.
func TestRenewLease_WrongWorker(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	insertRunning(t, db, "inst-1")

	if _, err := db.ClaimInstances("worker-A", 30*time.Millisecond, 10); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Wrong worker attempts a renewal — must be a no-op.
	if err := db.RenewWorkerLeases("worker-Z", time.Second); err != nil {
		t.Fatalf("RenewWorkerLeases (wrong worker): %v", err)
	}

	time.Sleep(40 * time.Millisecond) // worker-A's original lease has now expired

	got, err := db.ClaimInstances("worker-B", 10*time.Second, 10)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 instance after bad renewal, got %d", len(got))
	}
}

// TestUpdateInstance_ClearsLease verifies that UpdateInstance always releases the
// lease, regardless of the new status, so the next worker can reclaim freely.
func TestUpdateInstance_ClearsLease(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	insertRunning(t, db, "inst-1")

	claimed, err := db.ClaimInstances("worker-A", 10*time.Second, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: err=%v, count=%d", err, len(claimed))
	}

	inst := claimed[0]
	inst.Status = model.StatusCompleted
	if err := db.UpdateInstance(inst); err != nil {
		t.Fatalf("UpdateInstance: %v", err)
	}

	row, err := db.GetInstance("inst-1")
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if row.WorkerID != nil {
		t.Errorf("expected worker_id=NULL after UpdateInstance, got %q", *row.WorkerID)
	}
	if row.LeaseExpiresAt != nil {
		t.Errorf("expected lease_expires_at=NULL after UpdateInstance, got %v", row.LeaseExpiresAt)
	}
}
