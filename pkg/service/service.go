package service

import (
	"context"
	"db-paas/pkg/database"
	"fmt"
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
		Replicas:  in.Replicas, // was missing
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

func (s *Service) ListDatabases(ctx context.Context, ownerID string) ([]database.Database, error) {
	return s.store.List(ctx, ownerID)
}

// UpdateDatabaseInput is a merge patch: nil means "leave unchanged".
type UpdateDatabaseInput struct {
	Plan          *string
	Replicas      *int
	CPUMillicores *int
	MemoryMB      *int
	StorageGB     *int
}

// UpdateDatabase validates the change against the database's current state
// and queues it for a worker. changed is false when the patch alters nothing
// (no work is queued).
func (s *Service) UpdateDatabase(ctx context.Context, ownerID, id string, in UpdateDatabaseInput) (db database.Database, changed bool, err error) {
	cur, err := s.GetDatabase(ctx, ownerID, id) // also enforces ownership
	if err != nil {
		return database.Database{}, false, err
	}

	// One change at a time: wait for the previous one to finish.
	if cur.Status != database.StatusReady && cur.Status != database.StatusUpdateFailed {
		return database.Database{}, false,
			fmt.Errorf("status %q: %w", cur.Status, database.ErrInvalidState)
	}

	spec, errs := resolveUpdate(cur, in)
	if len(errs) > 0 {
		return database.Database{}, false, &database.ValidationError{Fields: errs}
	}
	if spec == cur.Spec() && cur.Status == database.StatusReady {
		return cur, false, nil
	}

	// updated_at acts as a version: if anyone changed the row since we read
	// it, the store refuses and the client retries.
	updated, err := s.store.UpdateSpec(ctx, cur.ID, cur.UpdatedAt, spec)
	if err != nil {
		return database.Database{}, false, err
	}
	return updated, true, nil
}

// resolveUpdate merges the patch into the current spec and validates the
// result against the target plan.
func resolveUpdate(cur database.Database, in UpdateDatabaseInput) (database.Spec, map[string]string) {
	errs := make(map[string]string)
	spec := cur.Spec()

	plan, ok := plans[cur.Plan]
	if in.Plan != nil {
		plan, ok = plans[*in.Plan]
		if !ok {
			errs["plan"] = "must be one of: small, medium, large"
			return spec, errs // plan-dependent rules are meaningless now
		}
		if plan.Name != cur.Plan {
			spec.Plan = plan.Name
			// a new tier means that tier's CPU and memory; storage and
			// replicas are kept (storage can only grow)
			spec.Resources.CPUMillicores = plan.Resources.CPUMillicores
			spec.Resources.MemoryMB = plan.Resources.MemoryMB
		}
	}
	if !ok {
		errs["plan"] = "current plan is unknown" // plan table changed under an existing row
		return spec, errs
	}

	if in.CPUMillicores != nil {
		spec.Resources.CPUMillicores = *in.CPUMillicores
	}
	if in.MemoryMB != nil {
		spec.Resources.MemoryMB = *in.MemoryMB
	}
	if in.StorageGB != nil {
		spec.Resources.StorageGB = *in.StorageGB
	}
	if in.Replicas != nil {
		spec.Replicas = *in.Replicas
	}

	// A value the client did not send but that no longer fits (a downgrade)
	// is reported under "plan", since that is what they changed.
	fail := func(field string, provided bool, msg string) {
		if provided {
			errs[field] = msg
		} else {
			errs["plan"] = msg
		}
	}

	r := spec.Resources
	if r.CPUMillicores < minCPUMillicores || r.CPUMillicores > plan.MaxCPUMillicores {
		fail("cpu_millicores", in.CPUMillicores != nil,
			fmt.Sprintf("must be between %d and %d for the %s plan", minCPUMillicores, plan.MaxCPUMillicores, plan.Name))
	}
	if r.MemoryMB < minMemoryMB || r.MemoryMB > plan.MaxMemoryMB {
		fail("memory_mb", in.MemoryMB != nil,
			fmt.Sprintf("must be between %d and %d for the %s plan", minMemoryMB, plan.MaxMemoryMB, plan.Name))
	}
	switch {
	case r.StorageGB < cur.Resources.StorageGB:
		errs["storage_gb"] = fmt.Sprintf("cannot shrink below the current %d GB", cur.Resources.StorageGB)
	case r.StorageGB > plan.MaxStorageGB:
		fail("storage_gb", in.StorageGB != nil,
			fmt.Sprintf("%d GB exceeds the %s plan maximum of %d GB (storage cannot shrink)", r.StorageGB, plan.Name, plan.MaxStorageGB))
	}
	if spec.Replicas < 1 || spec.Replicas > plan.MaxReplicas {
		fail("replicas", in.Replicas != nil,
			fmt.Sprintf("must be between 1 and %d for the %s plan", plan.MaxReplicas, plan.Name))
	}

	// Growing past the autoscale ceiling would leave the config contradictory.
	if cur.Autoscale.Enabled && r.StorageGB >= cur.Autoscale.MaxStorageGB {
		if _, already := errs["storage_gb"]; !already {
			errs["storage_gb"] = fmt.Sprintf("must stay below the autoscale ceiling (%d GB)", cur.Autoscale.MaxStorageGB)
		}
	}
	return spec, errs
}
