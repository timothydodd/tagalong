// Command tagalong is a self-hosted continuous-deployment service for k3s: it
// receives registry webhooks (and can poll), updates matching workloads, and
// records deploy history. See README.md.
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

	"github.com/timothydodd/tagalong/internal/config"
	"github.com/timothydodd/tagalong/internal/deploy"
	"github.com/timothydodd/tagalong/internal/events"
	"github.com/timothydodd/tagalong/internal/httpapi"
	"github.com/timothydodd/tagalong/internal/model"
	"github.com/timothydodd/tagalong/internal/poller"
	"github.com/timothydodd/tagalong/internal/registry"
	"github.com/timothydodd/tagalong/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	cfg := config.Load()
	log.Info("starting tagalong", "db", cfg.DBPath, "listen", cfg.Listen, "hooks_listen", cfg.HooksListen, "kubeconfig", cfg.Kubeconfig != "")

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// Ensure a portal login exists (default admin/admin on a fresh DB).
	if err := httpapi.SeedAdmin(st, log); err != nil {
		log.Error("seed admin account", "err", err)
		os.Exit(1)
	}

	k8s, err := deploy.NewK8s(cfg.Kubeconfig)
	if err != nil {
		// Degraded mode: serve the UI/API for local development even without a
		// reachable cluster. Deploys will report ErrNoCluster until one is set.
		log.Warn("no kubernetes cluster configured; running in degraded mode (UI/API only)", "err", err)
	}

	bus := events.NewBus()
	// Registry client resolves credentials from stored settings per host.
	reg := registry.NewClient(func(host string) (string, string, bool) {
		if c, ok, _ := st.GetRegistryCred(host); ok {
			return c.Username, c.Password, true
		}
		return "", "", false
	})

	// Cloudflare purger reads the global API token from settings at purge time.
	purger := deploy.NewCloudflarePurger(func() (string, error) {
		return st.GetSetting(model.KeyCloudflareAPIToken)
	})
	engine := deploy.NewEngine(k8s, st, bus, purger, log)

	// Resolve events left in-flight by the previous process, bounded so a slow
	// API server can't stall startup indefinitely. A self-deploy kills the pod
	// mid-rollout, so confirm those against the live cluster (marking them
	// success where the rollout actually landed) instead of always "unknown".
	recCtx, recCancel := context.WithTimeout(context.Background(), time.Minute)
	engine.ReconcileStartup(recCtx)
	recCancel()

	// History retention: prune old deploy events at startup and daily after.
	pruneEvents := func() {
		if n, err := st.PruneEvents(90*24*time.Hour, 2000); err != nil {
			log.Warn("prune deploy events", "err", err)
		} else if n > 0 {
			log.Info("pruned old deploy events", "count", n)
		}
	}
	pruneEvents()
	go func() {
		for range time.Tick(24 * time.Hour) {
			pruneEvents()
		}
	}()

	handler := httpapi.NewServer(st, engine, k8s, bus, reg, log)

	// Background registry poller.
	pollCtx, pollCancel := context.WithCancel(context.Background())
	defer pollCancel()
	go poller.New(st, engine, k8s, reg, log).Run(pollCtx)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go serve(srv, "main", log)

	// Optional second listener serving only the webhook receivers, so the hooks
	// can be exposed publicly while the portal/API stay private on the main port.
	var hooksSrv *http.Server
	if cfg.HooksListen != "" {
		hooksSrv = &http.Server{
			Addr:              cfg.HooksListen,
			Handler:           httpapi.NewHooksHandler(st, engine, k8s, bus, reg, log),
			ReadHeaderTimeout: 10 * time.Second,
		}
		go serve(hooksSrv, "hooks", log)
	}

	// Graceful shutdown.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Info("shutting down")
	pollCancel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	if hooksSrv != nil {
		hooksSrv.Shutdown(ctx)
	}
	// Drain queued/in-flight deploys; past the deadline they're interrupted and
	// left for the next boot's reconciliation.
	engine.Shutdown(ctx)
}

// serve runs an HTTP server, exiting the process on any error other than a
// clean shutdown. name distinguishes listeners in logs.
func serve(srv *http.Server, name string, log *slog.Logger) {
	log.Info("http listening", "listener", name, "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("http server", "listener", name, "err", err)
		os.Exit(1)
	}
}
