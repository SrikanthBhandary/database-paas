package memory

import (
	"context"
	"db-paas/pkg/database"
	"fmt"
	"sort"
	"sync"
	"time"
)

type Store struct {
	mu    sync.RWMutex
	items map[string]database.Database
}

func New() *Store {
	return &Store{items: make(map[string]database.Database)}
}

func (s *Store) Create(_ context.Context, db database.Database) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.items[db.ID]; ok {
		return fmt.Errorf("id %s: %w", db.ID, database.ErrAlreadyExists)
	}
	// name must be unique per owner (a DB unique constraint in Postgres)
	for _, existing := range s.items {
		if existing.OwnerID == db.OwnerID && existing.Name == db.Name {
			return fmt.Errorf("name %q: %w", db.Name, database.ErrAlreadyExists)
		}
	}

	s.items[db.ID] = db
	return nil
}

func (s *Store) Get(_ context.Context, id string) (database.Database, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	db, ok := s.items[id]
	if !ok {
		return database.Database{}, database.ErrNotFound
	}
	return db, nil
}

func (s *Store) List(_ context.Context, ownerID string) ([]database.Database, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]database.Database, 0)
	for _, db := range s.items {
		if db.OwnerID == ownerID {
			out = append(out, db)
		}
	}
	// map iteration order is random, so sort for stable results
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (s *Store) UpdateStatus(_ context.Context, id string, status database.Status, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	db, ok := s.items[id]
	if !ok {
		return database.ErrNotFound
	}
	db.Status = status
	db.StatusReason = reason
	db.UpdatedAt = time.Now().UTC()
	s.items[id] = db
	return nil
}

// ClaimPending moves up to limit pending rows (oldest first) to provisioning
// and returns them. Rows stuck in provisioning for reclaimAfter or longer are
// taken again, which recovers from a crashed worker. reclaimAfter <= 0
// disables reclaiming.
func (s *Store) ClaimPending(_ context.Context, limit int, reclaimAfter time.Duration) ([]database.Database, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	var eligible []database.Database
	for _, db := range s.items {
		queued := db.Status == database.StatusPending || db.Status == database.StatusUpdatePending
		stale := reclaimAfter > 0 &&
			(db.Status == database.StatusProvisioning || db.Status == database.StatusUpdating) &&
			now.Sub(db.UpdatedAt) >= reclaimAfter
		if queued || stale {
			eligible = append(eligible, db)
		}
	}

	sort.Slice(eligible, func(i, j int) bool {
		return eligible[i].CreatedAt.Before(eligible[j].CreatedAt)
	})
	if limit < len(eligible) {
		eligible = eligible[:limit]
	}

	for i := range eligible {
		switch eligible[i].Status { // stale rows keep their in-progress status
		case database.StatusPending:
			eligible[i].Status = database.StatusProvisioning
		case database.StatusUpdatePending:
			eligible[i].Status = database.StatusUpdating
		}
		eligible[i].StatusReason = ""
		eligible[i].UpdatedAt = now
		s.items[eligible[i].ID] = eligible[i]
	}
	return eligible, nil
}

func (s *Store) MarkReady(_ context.Context, id string, c database.Connection) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	db, ok := s.items[id]
	if !ok || (db.Status != database.StatusProvisioning && db.Status != database.StatusUpdating) {
		return database.ErrInvalidState
	}
	db.Status = database.StatusReady
	db.StatusReason = ""
	db.Connection = c
	db.UpdatedAt = time.Now().UTC()
	s.items[id] = db
	return nil
}

// MarkFailed ends an in-progress operation: provisioning -> failed,
// updating -> update_failed. Anything else is a stale result and is refused.
func (s *Store) MarkFailed(_ context.Context, id, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	db, ok := s.items[id]
	if !ok {
		return database.ErrInvalidState
	}
	switch db.Status {
	case database.StatusProvisioning:
		db.Status = database.StatusFailed
	case database.StatusUpdating:
		db.Status = database.StatusUpdateFailed
	default:
		return database.ErrInvalidState
	}
	db.StatusReason = reason
	db.UpdatedAt = time.Now().UTC()
	s.items[id] = db
	return nil
}

// UpdateSpec queues a change. It applies only if the row is still in the
// state the caller validated against: ready or update_failed, with the same
// updated_at. Otherwise the caller's checks are stale.
func (s *Store) UpdateSpec(_ context.Context, id string, expectedUpdatedAt time.Time, spec database.Spec) (database.Database, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	db, ok := s.items[id]
	if !ok || !db.UpdatedAt.Equal(expectedUpdatedAt) ||
		(db.Status != database.StatusReady && db.Status != database.StatusUpdateFailed) {
		return database.Database{}, database.ErrInvalidState
	}
	db.Plan = spec.Plan
	db.Resources = spec.Resources
	db.Replicas = spec.Replicas
	db.Status = database.StatusUpdatePending
	db.StatusReason = ""
	db.UpdatedAt = time.Now().UTC()
	s.items[id] = db
	return db, nil
}
