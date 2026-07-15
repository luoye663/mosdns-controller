package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/managed-dns/controller/internal/app"
	"github.com/managed-dns/controller/internal/auth"
	"github.com/managed-dns/controller/internal/config"
	"github.com/managed-dns/controller/internal/storage"
	"github.com/managed-dns/controller/internal/version"
)

func main() {
	command := "serve"
	if len(os.Args) > 1 && os.Args[1][0] != '-' {
		command = os.Args[1]
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}
	if command == "version" {
		fmt.Println(version.String())
		return
	}
	flags := flag.NewFlagSet(command, flag.ExitOnError)
	configPath := flags.String("config", "", "controller YAML config path")
	username := flags.String("username", "", "administrator username")
	password := flags.String("password", "", "administrator password")
	_ = flags.Parse(os.Args[1:])
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal(err)
	}
	ctx := context.Background()
	store, err := storage.Open(ctx, cfg.Storage.Path)
	if err != nil {
		fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		fatal(fmt.Errorf("migrate database: %w", err))
	}
	switch command {
	case "migrate":
		return
	case "create-admin":
		if err := auth.New(store, cfg.Web.SessionTTL).CreateAdmin(ctx, *username, *password); err != nil {
			fatal(err)
		}
		return
	case "healthcheck":
		if err := store.DB().PingContext(ctx); err != nil {
			fatal(err)
		}
		return
	case "serve":
		serve(cfg, store)
	default:
		fatal(fmt.Errorf("unknown command %q", command))
	}
}

func serve(cfg config.Config, store *storage.Store) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	application := app.New(logger, cfg, store)
	publicServer := &http.Server{Addr: cfg.Server.PublicListen, Handler: application.PublicHandler(), ReadHeaderTimeout: 5 * time.Second}
	internalServer := &http.Server{Addr: cfg.Server.InternalListen, Handler: application.InternalHandler(), ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 2)
	go serveHTTP(logger, "public", publicServer, errCh)
	go serveHTTP(logger, "internal", internalServer, errCh)
	select {
	case err := <-errCh:
		logger.Error("controller stopped unexpectedly", "error", err)
		os.Exit(1)
	case <-signalContext().Done():
		shutdown(publicServer, internalServer, logger)
	}
}
func serveHTTP(logger *slog.Logger, name string, server *http.Server, errCh chan<- error) {
	logger.Info("HTTP server started", "listener", name, "address", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- err
	}
}
func signalContext() context.Context {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() { <-ctx.Done(); stop() }()
	return ctx
}
func shutdown(publicServer, internalServer *http.Server, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, server := range []*http.Server{publicServer, internalServer} {
		if err := server.Shutdown(ctx); err != nil {
			logger.Error("HTTP server shutdown failed", "address", server.Addr, "error", err)
		}
	}
}
func fatal(err error) { fmt.Fprintln(os.Stderr, "controller:", err); os.Exit(1) }
