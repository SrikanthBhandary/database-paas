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

func TestDeleteLifecycle(t *testing.T) {
	s := memory.New()
	in := newDB("1", "alice", "orders")
	in.Status = database.StatusReady
	_ = s.Create(t.Context(), in)

	got, err := s.RequestDelete(t.Context(), "1")
	if err != nil || got.Status != database.StatusDeletePending {
		t.Fatalf("request: %+v, %v", got, err)
	}

	claimed, _ := s.ClaimPending(t.Context(), 1, time.Hour)
	if len(claimed) != 1 || claimed[0].Status != database.StatusDeleting {
		t.Fatalf("claimed = %+v, want one deleting row", claimed)
	}

	// failed teardown: delete_failed, and a retry is allowed
	if err := s.MarkFailed(t.Context(), "1", "delete failed"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if cur, _ := s.Get(t.Context(), "1"); cur.Status != database.StatusDeleteFailed {
		t.Fatalf("status = %q, want delete_failed", cur.Status)
	}
	if _, err := s.RequestDelete(t.Context(), "1"); err != nil {
		t.Fatalf("retry: %v", err)
	}

	// success removes the row
	_, _ = s.ClaimPending(t.Context(), 1, time.Hour)
	if err := s.Delete(t.Context(), "1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(t.Context(), "1"); !errors.Is(err, database.ErrNotFound) {
		t.Errorf("get after delete: err = %v, want ErrNotFound", err)
	}
	// and the name is free again
	if err := s.Create(t.Context(), newDB("2", "alice", "orders")); err != nil {
		t.Errorf("name should be reusable: %v", err)
	}
}

func TestRequestDeleteGuards(t *testing.T) {
	s := memory.New()
	for i, st := range []database.Status{database.StatusProvisioning, database.StatusUpdating, database.StatusDeleting} {
		d := newDB(strconv.Itoa(i), "alice", "db-"+strconv.Itoa(i))
		d.Status = st
		_ = s.Create(t.Context(), d)

		if _, err := s.RequestDelete(t.Context(), d.ID); !errors.Is(err, database.ErrInvalidState) {
			t.Errorf("%s: RequestDelete err = %v, want ErrInvalidState", st, err)
		}
	}
	if _, err := s.RequestDelete(t.Context(), "nope"); !errors.Is(err, database.ErrInvalidState) {
		t.Errorf("unknown id: err = %v", err)
	}

	// Delete only removes a row a worker has claimed for deletion
	ready := newDB("9", "alice", "ready-one")
	ready.Status = database.StatusReady
	_ = s.Create(t.Context(), ready)
	if err := s.Delete(t.Context(), "9"); !errors.Is(err, database.ErrInvalidState) {
		t.Errorf("Delete on a ready row: err = %v, want ErrInvalidState", err)
	}
	if _, err := s.Get(t.Context(), "9"); err != nil {
		t.Errorf("row must still exist: %v", err)
	}
}
