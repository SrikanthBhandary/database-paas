package main

import (
	"context"
	"db-paas/pkg/server"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
)

func main() {
	var port string
	var timeout int
	flag.StringVar(&port, "port", "8000", "port in which server should run")
	flag.IntVar(&timeout, "timeout", 10, "graceful shutdown timeout in seconds")

	flag.Parse()

	log, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}

	defer log.Sync()

	api := server.NewAPIServer()
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
