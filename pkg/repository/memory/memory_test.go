package memory_test

import (
	"db-paas/pkg/database"
	"db-paas/pkg/repository/memory"
	"db-paas/pkg/service"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

var _ service.Store = (*memory.Store)(nil)

func newDB(id, owner, name string) database.Database {
	now := time.Now().UTC()
	return database.Database{
		ID:        id,
		Name:      name,
		Engine:    database.EnginePostgres,
		OwnerID:   owner,
		Status:    database.StatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestCreateAndGet(t *testing.T) {

	s := memory.New()

	if err := s.Create(t.Context(), newDB("1", "alice", "orders")); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.Get(t.Context(), "1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "orders" {
		t.Errorf("name = %q, want orders", got.Name)
	}
}

func TestGetNotFound(t *testing.T) {
	_, err := memory.New().Get(t.Context(), "nope")
	if !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDuplicateNamePerOwner(t *testing.T) {

	s := memory.New()

	_ = s.Create(t.Context(), newDB("1", "alice", "orders"))

	// same owner, same name: rejected
	err := s.Create(t.Context(), newDB("2", "alice", "orders"))
	if !errors.Is(err, database.ErrAlreadyExists) {
		t.Fatalf("err = %v, want ErrAlreadyExists", err)
	}

	// different owner, same name: allowed
	if err := s.Create(t.Context(), newDB("3", "bob", "orders")); err != nil {
		t.Fatalf("different owner should be allowed: %v", err)
	}
}

func TestListFiltersByOwner(t *testing.T) {

	s := memory.New()

	_ = s.Create(t.Context(), newDB("1", "alice", "a"))
	_ = s.Create(t.Context(), newDB("2", "alice", "b"))
	_ = s.Create(t.Context(), newDB("3", "bob", "c"))

	got, _ := s.List(t.Context(), "alice")
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
}

func TestUpdateStatus(t *testing.T) {

	s := memory.New()
	_ = s.Create(t.Context(), newDB("1", "alice", "a"))

	if err := s.UpdateStatus(t.Context(), "1", database.StatusFailed, "out of capacity"); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := s.Get(t.Context(), "1")
	if got.Status != database.StatusFailed || got.StatusReason != "out of capacity" {
		t.Errorf("got %+v", got)
	}

	if err := s.UpdateStatus(t.Context(), "missing", database.StatusReady, ""); !errors.Is(err, database.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestConcurrentCreate(t *testing.T) {
	s := memory.New()

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Go(func() {
			id := strconv.Itoa(i)
			if err := s.Create(t.Context(), newDB(id, "alice", "db-"+id)); err != nil {
				t.Errorf("create %s: %v", id, err)
			}
		})
	}
	wg.Wait()

	got, _ := s.List(t.Context(), "alice")
	if len(got) != 100 {
		t.Fatalf("len = %d, want 100", len(got))
	}
}

func TestMarkReady(t *testing.T) {
	s := memory.New()
	_ = s.Create(t.Context(), newDB("1", "alice", "orders"))
	if got, _ := s.ClaimPending(t.Context(), 1, time.Hour); len(got) != 1 {
		t.Fatalf("claim = %+v", got)
	}

	conn := database.Connection{Host: "orders-rw.tenant-x.svc", Port: 5432, Database: "app", Username: "app",
		SecretNamespace: "tenant-x", SecretName: "orders-app"}
	if err := s.MarkReady(t.Context(), "1", conn); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	got, _ := s.Get(t.Context(), "1")
	if got.Status != database.StatusReady || got.Connection != conn {
		t.Errorf("got %+v", got)
	}
}

func TestMarkReadyRequiresProvisioning(t *testing.T) {
	s := memory.New()
	_ = s.Create(t.Context(), newDB("1", "alice", "orders")) // still pending

	for name, id := range map[string]string{"pending row": "1", "unknown id": "nope"} {
		if err := s.MarkReady(t.Context(), id, database.Connection{}); !errors.Is(err, database.ErrInvalidState) {
			t.Errorf("%s: err = %v, want ErrInvalidState", name, err)
		}
	}
}

func TestUpdateLifecycle(t *testing.T) {
	s := memory.New()
	in := newDB("1", "alice", "orders")
	in.Status = database.StatusReady
	_ = s.Create(t.Context(), in)

	spec := database.Spec{
		Plan:      "medium",
		Resources: database.Resources{CPUMillicores: 2000, MemoryMB: 4096, StorageGB: 50},
		Replicas:  3,
	}
	got, err := s.UpdateSpec(t.Context(), "1", in.UpdatedAt, spec)
	if err != nil || got.Status != database.StatusUpdatePending || got.Spec() != spec {
		t.Fatalf("update: %+v, %v", got, err)
	}

	// a worker claims it: update_pending -> updating
	claimed, _ := s.ClaimPending(t.Context(), 1, time.Hour)
	if len(claimed) != 1 || claimed[0].Status != database.StatusUpdating {
		t.Fatalf("claimed = %+v, want one updating row", claimed)
	}

	// it fails: updating -> update_failed (not failed)
	if err := s.MarkFailed(t.Context(), "1", "update failed"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	cur, _ := s.Get(t.Context(), "1")
	if cur.Status != database.StatusUpdateFailed || cur.StatusReason != "update failed" {
		t.Fatalf("got %q / %q", cur.Status, cur.StatusReason)
	}

	// a retry is allowed from update_failed, and this time it succeeds
	if _, err := s.UpdateSpec(t.Context(), "1", cur.UpdatedAt, spec); err != nil {
		t.Fatalf("retry: %v", err)
	}
	_, _ = s.ClaimPending(t.Context(), 1, time.Hour)
	if err := s.MarkReady(t.Context(), "1", database.Connection{Host: "h"}); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	if cur, _ = s.Get(t.Context(), "1"); cur.Status != database.StatusReady {
		t.Errorf("status = %q, want ready", cur.Status)
	}
}

func TestUpdateSpecGuards(t *testing.T) {
	s := memory.New()
	ready := newDB("1", "alice", "orders")
	ready.Status = database.StatusReady
	_ = s.Create(t.Context(), ready)
	_ = s.Create(t.Context(), newDB("2", "alice", "pending-one")) // pending

	spec := database.Spec{Plan: "small", Resources: database.Resources{CPUMillicores: 600, MemoryMB: 1024, StorageGB: 10}, Replicas: 1}

	cases := map[string]struct {
		id string
		at time.Time
	}{
		"stale version": {"1", ready.UpdatedAt.Add(-time.Second)},
		"wrong status":  {"2", ready.UpdatedAt},
		"unknown id":    {"nope", ready.UpdatedAt},
	}
	for name, c := range cases {
		if _, err := s.UpdateSpec(t.Context(), c.id, c.at, spec); !errors.Is(err, database.ErrInvalidState) {
			t.Errorf("%s: err = %v, want ErrInvalidState", name, err)
		}
	}
	if err := s.MarkFailed(t.Context(), "2", "x"); !errors.Is(err, database.ErrInvalidState) {
		t.Errorf("MarkFailed on a pending row: err = %v, want ErrInvalidState", err)
	}
}
