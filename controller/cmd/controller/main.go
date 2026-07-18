package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/managed-dns/controller/internal/app"
	"github.com/managed-dns/controller/internal/auth"
	"github.com/managed-dns/controller/internal/config"
	"github.com/managed-dns/controller/internal/mosdnsclient"
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
	passwordStdin := flags.Bool("password-stdin", false, "read administrator password from standard input")
	backupOutput := flags.String("output", "", "backup output path")
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
	case "reset-password":
		if *password != "" || !*passwordStdin {
			fatal(errors.New("reset-password requires -password-stdin; do not pass passwords as command arguments"))
		}
		resetPassword, err := passwordFromStdin()
		if err != nil {
			fatal(err)
		}
		if err := auth.New(store, cfg.Web.SessionTTL).ResetPassword(ctx, *username, resetPassword); err != nil {
			fatal(err)
		}
		return
	case "healthcheck":
		if err := store.DB().PingContext(ctx); err != nil {
			fatal(err)
		}
		return
	case "backup":
		if err := store.Backup(ctx, *backupOutput); err != nil {
			fatal(err)
		}
		return
	case "serve":
		token, err := mosdnsclient.ReadToken(cfg.Mosdns.TokenFile)
		if err != nil {
			fatal(err)
		}
		serve(cfg, store, mosdnsclient.New(cfg.Mosdns.BaseURL, token, 5*time.Second), token)
	default:
		fatal(fmt.Errorf("unknown command %q", command))
	}
}

func passwordFromStdin() (string, error) {
	value, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	if len(value) > 4096 {
		return "", errors.New("password input exceeds 4096 bytes")
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(value), "\n"), "\r"), nil
}

func serve(cfg config.Config, store *storage.Store, client mosdnsclient.Client, ingestToken string) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	application := app.New(logger, cfg, store, client, ingestToken)
	defer application.Close()
	// 对账失败不阻止服务启动：DNS 数据面会继续使用自己的最近快照。
	if state, err := application.Reconcile(context.Background()); err != nil {
		logger.Warn("startup reconcile failed", "error", err)
	} else {
		logger.Info("startup reconcile completed", "state", state)
	}
	if err := application.SyncSettings(context.Background()); err != nil {
		logger.Warn("startup settings sync failed", "error", err)
	}
	publicServer := &http.Server{Addr: cfg.Server.PublicListen, Handler: application.PublicHandler(), ReadHeaderTimeout: 5 * time.Second}
	internalServer := &http.Server{Addr: cfg.Server.InternalListen, Handler: application.InternalHandler(), ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 2)
	go serveHTTP(logger, "public", publicServer, errCh)
	go serveHTTP(logger, "internal", internalServer, errCh)
	shutdownSignal := signalContext()
	go reconcilePeriodically(shutdownSignal, application, logger)
	go refreshSubscriptionsPeriodically(shutdownSignal, application)
	select {
	case err := <-errCh:
		logger.Error("controller stopped unexpectedly", "error", err)
		os.Exit(1)
	case <-shutdownSignal.Done():
		shutdown(publicServer, internalServer, logger)
	}
}
func reconcilePeriodically(ctx context.Context, application *app.App, logger *slog.Logger) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if state, err := application.Reconcile(ctx); err != nil {
				logger.Warn("periodic reconcile failed", "error", err)
			} else {
				logger.Info("periodic reconcile completed", "state", state)
			}
		}
	}
}
func refreshSubscriptionsPeriodically(ctx context.Context, application *app.App) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			application.RefreshSubscriptions(ctx)
		}
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
