// Command todo is a tiny, server-rendered to-do web app used as the running
// example in the Kubernetes basics course. Readability beats cleverness here —
// the code is read aloud and projected on a wall during the training.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	// At this point we only know where the config file lives; everything else
	// comes from that file (written by docker-entrypoint.sh from env vars).
	path := configPath()
	cfg, err := loadConfig(path)
	if err != nil {
		// Use a plain logger here — the real one needs the (not-yet-loaded)
		// log level. Exit non-zero so a bad config fails the container start.
		slog.New(slog.NewTextHandler(os.Stderr, nil)).
			Error("failed to load config", "path", path, "err", err)
		os.Exit(1)
	}

	// Build the real logger now that we know the configured level.
	level, _ := parseLogLevel(cfg.LogLevel)
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	// Log the effective configuration (no secrets) so the lab can see that the
	// config actually took effect. database_url may contain a password, so we
	// log only whether postgres is in use, not the URL itself.
	log.Info("starting todo app",
		"config_file", path,
		"company_name", cfg.CompanyName,
		"listen_addr", cfg.ListenAddr,
		"data_dir", cfg.DataDir,
		"db_driver", cfg.DBDriver,
		"theme", cfg.Theme,
		"log_requests", cfg.LogRequests,
	)

	store, err := openStore(cfg)
	if err != nil {
		log.Error("failed to open store", "err", err)
		os.Exit(1)
	}

	// Resolve the auth credential. The password is owned by the DATABASE: it is
	// set once on first start (from the supplied password or a generated one)
	// and frozen thereafter, so later env changes have no effect.
	authUser, authHash, err := resolveCredential(context.Background(), store, cfg, log)
	if err != nil {
		log.Error("failed to resolve auth credential", "err", err)
		_ = store.Close()
		os.Exit(1)
	}

	srv, err := newServer(cfg, store, log, authUser, authHash)
	if err != nil {
		log.Error("failed to build server", "err", err)
		_ = store.Close()
		os.Exit(1)
	}

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Run the HTTP server in the background; report a clean exit via a channel.
	serverErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.ListenAddr)
		err := httpServer.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil // normal shutdown
		}
		serverErr <- err
	}()

	// Graceful shutdown: the container runtime (and Kubernetes) sends SIGTERM
	// to ask the process to stop. We catch it, drain in-flight requests, then
	// close the store. The visible log line below is the SIGTERM demo in Block 5.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		if err != nil {
			log.Error("server error", "err", err)
			_ = store.Close()
			os.Exit(1)
		}
	case sig := <-stop:
		log.Info("received signal, shutting down", "signal", sig.String())

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := httpServer.Shutdown(ctx); err != nil {
			log.Error("graceful shutdown failed", "err", err)
		}
		if err := store.Close(); err != nil {
			log.Error("closing store failed", "err", err)
		}
		log.Info("shutdown complete")
	}
}
