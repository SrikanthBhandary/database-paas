package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"db-paas/pkg/database"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// The order here must match the Scan order in scan() and the INSERT below.
const columns = `id::text, name, engine, version, plan, status, status_reason, owner_id,
	cpu_millicores, memory_mb, storage_gb,
	replicas,
	autoscale_enabled, autoscale_max_storage_gb,
	backup_enabled, backup_schedule, backup_retention_days,
	created_at, updated_at`

func (s *Store) Create(ctx context.Context, db database.Database) error {
	_, err := s.pool.Exec(
		ctx,
		`INSERT INTO databases (
			id, name, engine, version, plan, status, status_reason, owner_id,
			cpu_millicores, memory_mb, storage_gb,
			replicas,
			autoscale_enabled, autoscale_max_storage_gb,
			backup_enabled, backup_schedule, backup_retention_days,
			created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		db.ID, db.Name, string(db.Engine), db.Version, db.Plan, string(db.Status), db.StatusReason, db.OwnerID,
		db.Resources.CPUMillicores, db.Resources.MemoryMB, db.Resources.StorageGB,
		db.Replicas,
		db.Autoscale.Enabled, db.Autoscale.MaxStorageGB,
		db.Backup.Enabled, db.Backup.Schedule, db.Backup.RetentionDays,
		db.CreatedAt, db.UpdatedAt,
	)
	return mapErr(err)
}

func (s *Store) Get(ctx context.Context, id string) (database.Database, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+columns+` FROM databases WHERE id = $1`, id)
	db, err := scan(row)
	if err != nil {
		return database.Database{}, mapErr(err)
	}
	return db, nil
}

func (s *Store) List(ctx context.Context, ownerID string) ([]database.Database, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+columns+` FROM databases WHERE owner_id = $1 ORDER BY created_at, id`, ownerID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	out := make([]database.Database, 0)
	for rows.Next() {
		db, err := scan(rows)
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, db)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

func (s *Store) UpdateStatus(ctx context.Context, id string, status database.Status, reason string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE databases SET status = $2, status_reason = $3, updated_at = now() WHERE id = $1`,
		id, string(status), reason)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return database.ErrNotFound
	}
	return nil
}

// scan works for both pgx.Row and pgx.Rows.
func scan(row pgx.Row) (database.Database, error) {
	var (
		db             database.Database
		engine, status string
	)
	err := row.Scan(
		&db.ID, &db.Name, &engine, &db.Version, &db.Plan, &status, &db.StatusReason, &db.OwnerID,
		&db.Resources.CPUMillicores, &db.Resources.MemoryMB, &db.Resources.StorageGB,
		&db.Replicas,
		&db.Autoscale.Enabled, &db.Autoscale.MaxStorageGB,
		&db.Backup.Enabled, &db.Backup.Schedule, &db.Backup.RetentionDays,
		&db.CreatedAt, &db.UpdatedAt,
	)
	if err != nil {
		return database.Database{}, err
	}
	db.Engine = database.Engine(engine)
	db.Status = database.Status(status)
	return db, nil
}

// mapErr translates pgx/Postgres errors into domain errors so nothing above
// this package ever sees a pgx type.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return database.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation (PK or owner+name index)
			return fmt.Errorf("%s: %w", pgErr.ConstraintName, database.ErrAlreadyExists)
		case "23514": // check_violation (bad engine/status/resources)
			return fmt.Errorf("%s: %w", pgErr.ConstraintName, database.ErrInvalidInput)
		case "22P02": // invalid_text_representation, e.g. a malformed uuid
			return database.ErrNotFound
		}
	}
	return err
}
