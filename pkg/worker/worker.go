package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"db-paas/pkg/database"
)

// Store is the subset of the store the worker needs.
type Store interface {
	ClaimPending(ctx context.Context, limit int, reclaimAfter time.Duration) ([]database.Database, error)
	UpdateStatus(ctx context.Context, id string, status database.Status, reason string) error
}

// Provisioner creates the real database. It blocks until the database is
// ready or fails, and it must be idempotent: after a crash the same row can
// be provisioned again.
type Provisioner interface {
	Provision(ctx context.Context, db database.Database) error
}

type Config struct {
	Interval         time.Duration // how often to poll for work
	Concurrency      int           // max provisions in flight
	ProvisionTimeout time.Duration // deadline for one provision
	ReclaimAfter     time.Duration // provisioning rows older than this are retaken
}

func (c Config) withDefaults() Config {
	if c.Interval <= 0 {
		c.Interval = 2 * time.Second
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 4
	}
	if c.ProvisionTimeout <= 0 {
		c.ProvisionTimeout = 10 * time.Minute
	}
	if c.ReclaimAfter <= 0 {
		c.ReclaimAfter = 15 * time.Minute
	}
	return c
}

type Worker struct {
	store Store
	prov  Provisioner
	log   *zap.Logger
	cfg   Config
	sem   chan struct{} // one slot per in-flight provision
	wg    sync.WaitGroup
}

func New(store Store, prov Provisioner, log *zap.Logger, cfg Config) (*Worker, error) {
	cfg = cfg.withDefaults()
	// If a healthy job could outlive the reclaim window, another worker would
	// take it over and provision it twice.
	if cfg.ReclaimAfter <= cfg.ProvisionTimeout {
		return nil, fmt.Errorf("reclaim-after (%s) must be longer than the provision timeout (%s)",
			cfg.ReclaimAfter, cfg.ProvisionTimeout)
	}

	return &Worker{
		store: store,
		prov:  prov,
		log:   log,
		cfg:   cfg,
		sem:   make(chan struct{}, cfg.Concurrency),
	}, nil
}

// Run polls until ctx is cancelled, then waits for in-flight jobs to return.
func (w *Worker) Run(ctx context.Context) {
	w.log.Info("worker started",
		zap.Duration("interval", w.cfg.Interval), zap.Int("concurrency", w.cfg.Concurrency))

	ticker := time.NewTicker(w.cfg.Interval)
	defer ticker.Stop()

	for {
		w.poll(ctx)
		select {
		case <-ctx.Done():
			w.wg.Wait()
			w.log.Info("worker stopped")
			return
		case <-ticker.C:
		}
	}
}

// poll claims only as many rows as there are free slots, so a slow
// provision never blocks others and a claimed row never waits for a slot.
func (w *Worker) poll(ctx context.Context) {
	free := cap(w.sem) - len(w.sem)
	if free == 0 || ctx.Err() != nil {
		return
	}

	claimed, err := w.store.ClaimPending(ctx, free, w.cfg.ReclaimAfter)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Error("claim pending", zap.Error(err))
		}
		return
	}

	for _, db := range claimed {
		w.sem <- struct{}{}
		w.wg.Go(func() {
			defer func() { <-w.sem }()
			w.process(ctx, db)
		})
	}
}

func (w *Worker) process(ctx context.Context, db database.Database) {
	log := w.log.With(zap.String("database_id", db.ID), zap.String("name", db.Name))
	log.Info("provisioning")

	pctx, cancel := context.WithTimeout(ctx, w.cfg.ProvisionTimeout)
	defer cancel()
	err := w.prov.Provision(pctx, db)

	// Status writes must still happen while shutting down, so they use a
	// context that ignores ctx's cancellation.
	wctx, wcancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer wcancel()

	switch {
	case err == nil:
		w.setStatus(wctx, log, db.ID, database.StatusReady, "")
		log.Info("database ready")

	case ctx.Err() != nil:
		// Shutting down, so this failure is not the database's fault. Leave it in
		// provisioning; it is reclaimed after ReclaimAfter.
		log.Warn("provisioning interrupted by shutdown", zap.Error(err))

	default:
		// status_reason is returned to API clients, so it must never carry
		// internals. The real error goes to the log.
		reason := "provisioning failed"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "provisioning timed out"
		}
		log.Error("provisioning failed", zap.Error(err))
		w.setStatus(wctx, log, db.ID, database.StatusFailed, reason)
	}
}

func (w *Worker) setStatus(ctx context.Context, log *zap.Logger, id string, st database.Status, reason string) {
	if err := w.store.UpdateStatus(ctx, id, st, reason); err != nil {
		// The row stays in provisioning and is reclaimed later.
		log.Error("update status", zap.String("status", string(st)), zap.Error(err))
	}
}
