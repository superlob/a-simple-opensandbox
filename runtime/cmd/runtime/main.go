// Command runtime is the Sandbox Runtime service. It manages Docker-based Sandbox
// containers and exposes them to the Manager over HTTP.
//
// Configuration comes from environment variables (see internal/config). Load
// SANDBOX.env before starting, for example: set -a; . ../SANDBOX.env; set +a
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"sandbox/runtime/internal/api"
	"sandbox/runtime/internal/config"
	"sandbox/runtime/internal/docker"
	"sandbox/runtime/internal/endpoint"
	"sandbox/runtime/internal/sandbox"
)

// shutdownGrace bounds how long in-flight HTTP requests may take to finish.
const shutdownGrace = 15 * time.Second

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("runtime exited", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	dc, err := docker.New(docker.Options{Host: cfg.DockerHost})
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer dc.Close()

	// Start even if the daemon is down: the API reports docker_unavailable per request,
	// and the process keeps running so it can recover without a restart.
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := dc.Ping(pingCtx); err != nil {
		logger.Warn("docker daemon not reachable at startup", "error", err)
	} else {
		logger.Info("docker daemon reachable")
	}
	cancel()

	svc := sandbox.NewService(cfg, dc, endpoint.NewRegistry(), sandbox.Options{})

	// Recovery runs before the API serves requests. If the daemon is down, it fails
	// and the Runtime keeps going: endpoints are also derived from Docker on demand.
	recoverCtx, cancelRecover := context.WithTimeout(context.Background(), 60*time.Second)
	rep, err := svc.Recover(recoverCtx)
	cancelRecover()
	if err != nil {
		logger.Warn("startup recovery incomplete", "error", err)
	} else {
		logger.Info("startup recovery done",
			"adopted", len(rep.Adopted), "removed", rep.Removed, "unready", rep.Unready, "errors", len(rep.Errors))
		for _, e := range rep.Errors {
			logger.Warn("recovery error", "error", e)
		}
	}
	srv := &http.Server{
		Addr: net.JoinHostPort(cfg.BindHost, strconv.Itoa(cfg.Port)),
		Handler: api.NewRouter(api.Deps{
			Service: svc,
			Logger:  logger,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if cfg.SandboxTTL.Std() > 0 {
		interval := time.Minute
		if ttl := cfg.SandboxTTL.Std(); ttl/10 < interval {
			interval = ttl / 10
		}
		go svc.RunReaper(ctx, interval, func(removed []string, errs []error) {
			logger.Info("reaper pass", "removed", removed, "errors", len(errs))
			for _, e := range errs {
				logger.Warn("reaper error", "error", e)
			}
		})
		logger.Info("ttl reaper enabled", "ttl", cfg.SandboxTTL.Std().String(), "interval", interval.String())
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("runtime listening", "addr", srv.Addr, "public_host", cfg.PublicHost)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}
	logger.Info("shutdown requested")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	logger.Info("runtime stopped")
	return nil
}
