package service

import (
	"context"
	"db-paas/pkg/database"
)

type Store interface {
	Create(ctx context.Context, db database.Database) error
	Get(ctx context.Context, id string) (database.Database, error)
	List(ctx context.Context, ownerID string) ([]database.Database, error)
	UpdateStatus(ctx context.Context, id string, status database.Status, reason string) error
}

type Service struct{ store Store }

func New(store Store) *Service { return &Service{store: store} }
