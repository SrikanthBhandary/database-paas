//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"db-paas/pkg/database"
	"db-paas/pkg/repository/postgres"
	"db-paas/pkg/service"
)

var _ service.Store = (*postgres.Store)(nil)

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		fmt.Fprintln(os.Stderr, "TEST_DATABASE_URL must be set for integration tests")
		os.Exit(0)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "ping:", err)
		os.Exit(1)
	}
	testPool = pool

	code := m.Run()
	pool.Close()
	os.Exit(code)
}

// newStore returns a Store backed by an empty table.
func newStore(t *testing.T) *postgres.Store {
	t.Helper()
	if _, err := testPool.Exec(t.Context(), `TRUNCATE databases`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return postgres.New(testPool)
}

func newDB(owner, name string) database.Database {
	now := time.Now().UTC().Truncate(time.Microsecond) // Postgres keeps microseconds
	return database.Database{
		ID:        uuid.NewString(),
		Name:      name,
		Engine:    database.EnginePostgres,
		Version:   "17",
		Plan:      "small",
		Status:    database.StatusPending,
		OwnerID:   owner,
		Resources: database.Resources{CPUMillicores: 500, MemoryMB: 1024, StorageGB: 10},
		Autoscale: database.Autoscale{
			Enabled:      true,
			MaxResources: database.Resources{CPUMillicores: 2000, MemoryMB: 4096, StorageGB: 50},
		},
		Backup:    database.Backup{Enabled: true, Schedule: "0 2 * * *", RetentionDays: 7},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestCreate(t *testing.T) {
	s := newStore(t)

	if err := s.Create(t.Context(), newDB("alice", "orders")); err != nil {
		t.Fatalf("create: %v", err)
	}
}

func TestCreateDuplicateNamePerOwner(t *testing.T) {
	s := newStore(t)
	_ = s.Create(t.Context(), newDB("alice", "orders"))

	// same owner, same name: rejected
	err := s.Create(t.Context(), newDB("alice", "orders"))
	if !errors.Is(err, database.ErrAlreadyExists) {
		t.Fatalf("err = %v, want ErrAlreadyExists", err)
	}

	// different owner, same name: allowed
	if err := s.Create(t.Context(), newDB("bob", "orders")); err != nil {
		t.Fatalf("different owner should be allowed: %v", err)
	}
}

func TestCreateDuplicateID(t *testing.T) {
	s := newStore(t)
	first := newDB("alice", "a")
	_ = s.Create(t.Context(), first)

	second := newDB("bob", "b")
	second.ID = first.ID

	if err := s.Create(t.Context(), second); !errors.Is(err, database.ErrAlreadyExists) {
		t.Fatalf("err = %v, want ErrAlreadyExists", err)
	}
}

func TestCreateInvalidEngine(t *testing.T) {
	s := newStore(t)
	in := newDB("alice", "a")
	in.Engine = "oracle" // violates the CHECK constraint

	if err := s.Create(t.Context(), in); !errors.Is(err, database.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestConcurrentCreate(t *testing.T) {
	s := newStore(t)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			in := newDB("alice", fmt.Sprintf("db-%d", i))
			if err := s.Create(t.Context(), in); err != nil {
				t.Errorf("create %d: %v", i, err)
			}
		})
	}
	wg.Wait()

	got, _ := s.List(t.Context(), "alice")
	if len(got) != 50 {
		t.Fatalf("len = %d, want 50", len(got))
	}
}

// ---- Get ----

func TestGet(t *testing.T) {
	s := newStore(t)
	in := newDB("alice", "orders")
	if err := s.Create(t.Context(), in); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.Get(t.Context(), in.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != in.ID || got.Name != "orders" || got.Engine != database.EnginePostgres {
		t.Errorf("got %+v", got)
	}
	// nested structs survive the column flattening round trip
	if got.Resources != in.Resources {
		t.Errorf("resources = %+v, want %+v", got.Resources, in.Resources)
	}
	if got.Autoscale != in.Autoscale {
		t.Errorf("autoscale = %+v, want %+v", got.Autoscale, in.Autoscale)
	}
	if got.Backup != in.Backup {
		t.Errorf("backup = %+v, want %+v", got.Backup, in.Backup)
	}
}

func TestGetNotFound(t *testing.T) {
	s := newStore(t)

	_, err := s.Get(t.Context(), uuid.NewString())
	if !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGetMalformedID(t *testing.T) {
	s := newStore(t)

	_, err := s.Get(t.Context(), "not-a-uuid")
	if !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// ---- List ----

func TestListFiltersByOwner(t *testing.T) {
	s := newStore(t)
	_ = s.Create(t.Context(), newDB("alice", "a"))
	_ = s.Create(t.Context(), newDB("alice", "b"))
	_ = s.Create(t.Context(), newDB("bob", "c"))

	got, err := s.List(t.Context(), "alice")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
}

func TestListOrderedByCreatedAt(t *testing.T) {
	s := newStore(t)
	base := time.Now().UTC().Truncate(time.Microsecond)

	third := newDB("alice", "c")
	third.CreatedAt = base.Add(2 * time.Second)
	first := newDB("alice", "a")
	first.CreatedAt = base
	second := newDB("alice", "b")
	second.CreatedAt = base.Add(time.Second)

	// inserted out of order on purpose
	for _, d := range []database.Database{third, first, second} {
		if err := s.Create(t.Context(), d); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	got, err := s.List(t.Context(), "alice")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 || got[0].Name != "a" || got[1].Name != "b" || got[2].Name != "c" {
		t.Fatalf("got %+v", got)
	}
}

func TestListUnknownOwnerIsEmpty(t *testing.T) {
	s := newStore(t)

	got, err := s.List(t.Context(), "nobody")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("got %v, want empty non-nil slice", got)
	}
}

// ---- UpdateStatus ----

func TestUpdateStatus(t *testing.T) {
	s := newStore(t)
	in := newDB("alice", "a")
	_ = s.Create(t.Context(), in)

	if err := s.UpdateStatus(t.Context(), in.ID, database.StatusFailed, "out of capacity"); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := s.Get(t.Context(), in.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != database.StatusFailed || got.StatusReason != "out of capacity" {
		t.Errorf("got status=%q reason=%q", got.Status, got.StatusReason)
	}
	if !got.UpdatedAt.After(in.UpdatedAt) {
		t.Errorf("updated_at not advanced: %v vs %v", got.UpdatedAt, in.UpdatedAt)
	}
}

func TestUpdateStatusNotFound(t *testing.T) {
	s := newStore(t)

	err := s.UpdateStatus(t.Context(), uuid.NewString(), database.StatusReady, "")
	if !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestUpdateStatusInvalid(t *testing.T) {
	s := newStore(t)
	in := newDB("alice", "a")
	_ = s.Create(t.Context(), in)

	err := s.UpdateStatus(t.Context(), in.ID, database.Status("exploded"), "")
	if !errors.Is(err, database.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}
