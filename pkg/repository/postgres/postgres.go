package postgres

import (
	"context"
	"db-paas/pkg/database"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

const columns = `id::text, name, engine, version, plan, status, status_reason, owner_id,
	cpu_millicores, memory_mb, storage_gb,
	autoscale_enabled,
	autoscale_min_cpu_millicores, autoscale_min_memory_mb, autoscale_min_storage_gb,
	autoscale_max_cpu_millicores, autoscale_max_memory_mb, autoscale_max_storage_gb,
	backup_enabled, backup_schedule, backup_retention_days,
	created_at, updated_at`

func (s *Store) Create(ctx context.Context, db database.Database) error {
	return nil
}

func (s *Store) Get(ctx context.Context, id string) (database.Database, error) {
	return database.Database{}, nil
}

func (s *Store) List(ctx context.Context, ownerID string) ([]database.Database, error) {
	return nil, nil
}

func (s *Store) UpdateStatus(ctx context.Context, id string, status database.Status, reason string) error {
	return nil
}
