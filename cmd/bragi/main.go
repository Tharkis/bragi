// Command bragi runs the Bragi media manager.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Tharkis/bragi/internal/api"
	"github.com/Tharkis/bragi/internal/config"
	"github.com/Tharkis/bragi/internal/db"
	"github.com/Tharkis/bragi/internal/store"
	"github.com/Tharkis/bragi/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bragi:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	if err := os.MkdirAll(cfg.ConfigDir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	dbPath := filepath.Join(cfg.ConfigDir, "bragi.db")
	d, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer d.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := db.Migrate(ctx, d); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	var ui fs.FS
	if sub, err := fs.Sub(web.Dist, "dist"); err == nil {
		if _, err := fs.Stat(sub, "index.html"); err == nil {
			ui = sub
		}
	}
	if ui == nil {
		log.Warn("web UI not embedded; serving the API only")
	}

	go backupLoop(ctx, log, d, filepath.Join(cfg.ConfigDir, "backups"))

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           api.New(api.Options{Store: store.New(d), Logger: log, Version: version, UI: ui}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("bragi started", "version", version, "port", cfg.Port, "config", cfg.ConfigDir)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
	return nil
}

// backupLoop snapshots the database once a day and keeps seven copies.
func backupLoop(ctx context.Context, log *slog.Logger, d *sql.DB, dir string) {
	const keep = 7
	backup := func() {
		path, err := db.Backup(ctx, d, dir, keep)
		if err != nil {
			log.Error("database backup failed", "err", err)
			return
		}
		log.Info("database backed up", "path", path)
	}

	// First snapshot shortly after start, then every 24 hours.
	timer := time.NewTimer(5 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			backup()
			timer.Reset(24 * time.Hour)
		}
	}
}
