// lifecycle-manager is the hermes-cluster session orchestrator: a stateless
// HTTP service that manages Hermes agent sessions as agent-sandbox
// SandboxClaims, with idle/TTL sweepers and an optional hermes-relay-
// connector integration.
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

	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/config"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/connector"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/httpapi"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/k8s"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/lifecycle"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/reconcile"
	"github.com/nabi-allenby/hermes-cluster/lifecycle-manager/internal/sweeper"
)

var version = "dev" // stamped via -ldflags at release

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}
	log := newLogger(cfg)
	log.Info("lifecycle-manager starting", "version", version,
		"namespace", cfg.Namespace, "warmPool", cfg.WarmPool,
		"connector", cfg.ConnectorEnabled, "sweepInterval", cfg.SweepInterval)

	k8sClient, err := k8s.NewDynamicClient(k8s.Options{
		Namespace: cfg.Namespace,
		Group:     cfg.SandboxAPIGroup,
		ExtGroup:  cfg.SandboxExtAPIGroup,
		Version:   cfg.SandboxAPIVersion,
	})
	if err != nil {
		log.Error("kubernetes client init failed", "error", err)
		os.Exit(1)
	}

	var conn connector.Client = connector.Disabled{}
	if cfg.ConnectorEnabled {
		conn = connector.NewHTTPClient(cfg.ConnectorURL, cfg.ConnectorAdminToken, cfg.ConnectorProvisionToken)
	}

	manager := &lifecycle.Manager{
		K8s:       k8sClient,
		Connector: conn,
		Defaults: lifecycle.Defaults{
			WarmPool:    cfg.WarmPool,
			TTL:         cfg.TTL,
			IdleTimeout: cfg.IdleTimeout,
			Platform:    cfg.ConnectorPlatform,
			BotID:       cfg.ConnectorBotID,
			WakeBaseURL: cfg.WakeBaseURL,
		},
		Log: log,
	}

	store := &reconcile.Store{}
	server := &httpapi.Server{Manager: manager, APIToken: cfg.APIToken, Reconcile: store, Log: log}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner := &sweeper.Runner{Manager: manager, Interval: cfg.SweepInterval, Reconcile: store, Log: log}
	go runner.Run(ctx)

	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	log.Info("listening", "addr", cfg.Listen)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("http server failed", "error", err)
		os.Exit(1)
	}
	log.Info("lifecycle-manager stopped")
}

func newLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.LogFormat == "text" {
		handler = slog.NewTextHandler(os.Stderr, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	}
	log := slog.New(handler)
	slog.SetDefault(log)
	return log
}
