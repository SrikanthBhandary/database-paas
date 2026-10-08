package service

import (
	"context"
	"db-paas/pkg/database"
	"time"
)

type Store interface {
	Create(ctx context.Context, db database.Database) error
	Get(ctx context.Context, id string) (database.Database, error)
	List(ctx context.Context, ownerID string) ([]database.Database, error)
	UpdateStatus(ctx context.Context, id string, status database.Status, reason string) error
	UpdateSpec(ctx context.Context, id string, expectedUpdatedAt time.Time, spec database.Spec) (database.Database, error)
}
