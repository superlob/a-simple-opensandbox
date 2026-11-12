// Command execd is the in-sandbox execution service.
//
// Usage:
//
//	execd               run the server (configured through EXECD_CONFIG and EXECD_* overrides)
//	execd healthcheck   probe the local /health endpoint; exit 0 if healthy (for Docker HEALTHCHECK)
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

	"sandbox/execd/internal/api"
	"sandbox/execd/internal/config"
	"sandbox/execd/internal/fileops"
	"sandbox/execd/internal/lifecycle"
	"sandbox/execd/internal/store"
	"sandbox/execd/internal/task"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "execd:", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := cfg.CheckDirs(); err != nil {
		return fmt.Errorf("check directories: %w", err)
	}
	st, err := store.New(cfg.DataDir)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	defer func() {}()

	if os.Getpid() == 1 {
		// Running as the container's init process: reap orphaned descendants.
		lifecycle.StartReaper(ctx, false)
		logger.Info("pid 1 mode: reaping orphaned child processes")
	}

	mgr, err := task.New(cfg, st, logger)
	if err != nil {
		return fmt.Errorf("initialise tasks: %w", err)
	}
	files := &fileops.Service{Workspace: cfg.WorkspaceDir, MaxUploadBytes: cfg.MaxUploadBytes}

	handler := api.NewRouter(api.Deps{
		Config:    cfg,
		Files:     files,
		Tasks:     mgr,
		Logger:    logger,
		StartedAt: time.Now(),
	})
	srv := &http.Server{
		Addr:              net.JoinHostPort(cfg.ListenAddr, strconv.Itoa(cfg.Port)),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", srv.Addr, "workspace", cfg.WorkspaceDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	logger.Info("shutdown requested", "grace", cfg.ShutdownGrace.Std().String())
	shutCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace.Std())
	defer cancel()
	// Cancel running tasks first so in-flight foreground requests return, then close HTTP.
	if err := mgr.Shutdown(shutCtx); err != nil {
		logger.Warn("task shutdown incomplete", "err", err)
	}
	if err := srv.Shutdown(shutCtx); err != nil {
		logger.Warn("http shutdown incomplete", "err", err)
	}
	logger.Info("stopped")
	return nil
}

// healthcheck returns 0 when the local server reports healthy, 1 otherwise.
func healthcheck() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	host := cfg.ListenAddr
	if host == "0.0.0.0" || host == "" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, strconv.Itoa(cfg.Port)) + "/health")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.StatusCode)
		return 1
	}
	return 0
}
