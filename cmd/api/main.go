package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"db-paas/pkg/repository/memory"
	"db-paas/pkg/repository/postgres"
	"db-paas/pkg/server"
	"db-paas/pkg/service"
)

func main() {
	var (
		port    string
		dbURL   string
		timeout int
	)
	flag.StringVar(&port, "port", "8000", "port in which server should run")
	flag.StringVar(&dbURL, "database-url", os.Getenv("DATABASE_URL"),
		"Postgres connection string (default: $DATABASE_URL; empty = in-memory store)")
	flag.IntVar(&timeout, "timeout", 10, "graceful shutdown timeout in seconds")
	flag.Parse()

	if timeout <= 0 {
		os.Stderr.WriteString("timeout must be greater than 0\n")
		os.Exit(2)
	}

	log, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	defer log.Sync()

	// ---- store ----
	var store service.Store
	if dbURL == "" {
		log.Warn("no database URL set; using the in-memory store (data is lost on exit)")
		store = memory.New()
	} else {
		pool, err := pgxpool.New(context.Background(), dbURL)
		if err != nil {
			log.Fatal("connect to database", zap.Error(err))
		}
		defer pool.Close() // runs after Shutdown below, so in-flight queries finish first

		if err := pool.Ping(context.Background()); err != nil {
			log.Fatal("ping database", zap.Error(err))
		}
		store = postgres.New(pool)
	}

	// ---- http ----
	api := server.NewAPIServer(service.New(store), log)
	api.RegisterAPI()

	handler := server.LoggingMiddleware(log)(api.Router)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       70 * time.Second,
	}

	// ctx is cancelled on SIGINT (Ctrl+C) or SIGTERM (Docker/Kubernetes stop)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		log.Info("starting server", zap.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		log.Error("server failed", zap.Error(err))
		_ = log.Sync()
		os.Exit(1)
	case <-ctx.Done():
		stop() // restore default behavior: a second Ctrl+C force-kills
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", zap.Error(err))
		_ = srv.Close() // force close remaining connections
		return
	}
	log.Info("server stopped")
}
