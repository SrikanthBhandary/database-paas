package main

import (
	"context"
	"db-paas/pkg/provisioner/fake"
	"db-paas/pkg/repository/postgres"
	"db-paas/pkg/worker"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

func main() {
	var (
		dbURL          string
		healthAddr     string
		concurrency    int
		interval       time.Duration
		reclaimAfter   time.Duration
		provisionDelay time.Duration
	)

	flag.StringVar(&dbURL, "database-url", os.Getenv("DATABASE_URL"), "Postgres connection string (default: $DATABASE_URL)")
	flag.StringVar(&healthAddr, "health-addr", ":8081", "address for the /healthz probe endpoint (empty = disabled)")
	flag.IntVar(&concurrency, "concurrency", 4, "max provisions in flight")
	flag.DurationVar(&interval, "interval", 2*time.Second, "how often to poll for work")
	flag.DurationVar(&reclaimAfter, "reclaim-after", 15*time.Minute, "retake rows stuck in provisioning for this long")
	flag.DurationVar(&provisionDelay, "provision-delay", 5*time.Second, "how long the fake provisioner takes")
	flag.Parse()

	log, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	defer log.Sync()

	// A separate process cannot share an in-memory store with the API,
	// so the worker requires a real database.
	if dbURL == "" {
		log.Fatal("a database URL is required: set -database-url or $DATABASE_URL")
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		log.Fatal("connect to database", zap.Error(err))
	}
	defer pool.Close()
	if err := pool.Ping(context.Background()); err != nil {
		log.Fatal("ping database", zap.Error(err))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	w, err := worker.New(postgres.New(pool), fake.Provisioner{Delay: provisionDelay}, log, worker.Config{
		Interval:         interval,
		Concurrency:      concurrency,
		ProvisionTimeout: reclaimAfter * 2 / 3, // must stay shorter than reclaim-after
		ReclaimAfter:     reclaimAfter,
	})
	if err != nil {
		log.Fatal("create worker", zap.Error(err))
	}
	// Probe endpoint so Kubernetes can tell whether the process is alive
	// and the database is reachable.
	if healthAddr != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, r *http.Request) {
			pctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := pool.Ping(pctx); err != nil {
				rw.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			rw.WriteHeader(http.StatusOK)
		})
		hs := &http.Server{Addr: healthAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("health server", zap.Error(err))
			}
		}()
		defer hs.Close()
	}

	// Run returns after ctx is cancelled (SIGINT/SIGTERM) and in-flight
	// jobs have returned. Interrupted jobs stay in provisioning and are
	// reclaimed by any worker after reclaim-after.
	w.Run(ctx)
	log.Info("worker exited")

}
