package service

import (
	"context"
	"db-paas/pkg/database"
	"time"

	"github.com/google/uuid"
)

type Service struct {
	store Store
	now   func() time.Time
}

type CreateDatabaseInput struct {
	OwnerID   string
	Name      string
	Replicas  int
	Engine    database.Engine
	Version   string
	Plan      string // "small", "medium", "large": validated against the plans table
	StorageGB int
	AutoScale database.Autoscale
	Backup    *database.Backup
}

func New(store Store) *Service {
	return &Service{
		store: store,
		now:   func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) CreateDatabase(ctx context.Context, in CreateDatabaseInput) (database.Database, error) {
	plan, errs := validate(in) // returns the resolved plan and a map of field errors
	if len(errs) > 0 {
		return database.Database{}, &database.ValidationError{Fields: errs}
	}

	res := plan.Resources
	if in.StorageGB > 0 {
		res.StorageGB = in.StorageGB
	}
	backup := defaultBackup
	if in.Backup != nil {
		backup = *in.Backup
	}

	now := s.now()
	db := database.Database{
		ID:        uuid.NewString(),
		Name:      in.Name,
		Engine:    in.Engine,
		Version:   in.Version,
		Plan:      plan.Name,
		Status:    database.StatusPending,
		OwnerID:   in.OwnerID,
		Resources: res,
		Autoscale: in.AutoScale,
		Backup:    backup,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.Create(ctx, db); err != nil {
		return database.Database{}, err // ErrAlreadyExists passes through
	}
	return db, nil
}

func (s *Service) GetDatabase(ctx context.Context, ownerID, id string) (database.Database, error) {
	db, err := s.store.Get(ctx, id)
	if err != nil {
		return database.Database{}, err
	}
	if db.OwnerID != ownerID {
		return database.Database{}, database.ErrNotFound // 404, not 403: don't reveal it exists
	}
	return db, nil
}
