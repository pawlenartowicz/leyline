package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pawlenartowicz/leyline/internal/buildinfo"
	"github.com/pawlenartowicz/leyline/internal/server/api"
	"github.com/pawlenartowicz/leyline/internal/server/config"
	"github.com/pawlenartowicz/leyline/internal/server/httpx"
	"github.com/pawlenartowicz/leyline/internal/server/hub"
	"github.com/pawlenartowicz/leyline/internal/server/metrics"
	"github.com/pawlenartowicz/leyline/internal/server/registry"
)

// Importing net/http/pprof registers handlers on http.DefaultServeMux as a
// side-effect. We work around this by never mounting DefaultServeMux — every
// handler in this binary goes on an explicit mux. Audit before adding any
// http.Handle / http.HandleFunc call (without a mux receiver). The
// regression test in cmd/server lives at TestPprof_NotOnMainPort.

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.Value)
		return
	}

	setupLogger()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(cfg.VaultsDir, 0755); err != nil {
		slog.Error("create vault root", "error", err)
		os.Exit(1)
	}

	walDir, err := cfg.ResolveWALDir()
	if err != nil {
		slog.Error("resolve wal dir", "error", err)
		os.Exit(1)
	}
	slog.Info("wal dir resolved", "path", walDir)

	reg, err := registry.Load(cfg.Registry)
	if err != nil {
		slog.Error("load registry", "path", cfg.Registry, "error", err)
		os.Exit(1)
	}
	slog.Info("registry loaded", "path", cfg.Registry, "vaults", len(reg.All()))

	h := hub.NewHub(cfg)
	h.SetRegistry(reg)
	go h.Run()

	if hour, min, ok := cfg.GitGCAtParsed(); ok {
		slog.Info("git gc scheduled", "at_utc", cfg.GitGCAt)
		go h.RunGCLoop(hour, min)
	}

	metrics.SetProcessStartTime(time.Now())
	metrics.RegisterRuntimeMetrics()
	metrics.RegisterActiveClients(h.SnapshotVaultClientCounts)
	metrics.RegisterVaultsHydrated(h.VaultCount)
	httpx.SetOnPanic(metrics.PanicsRecovered.Inc)

	metricsServer, _, err := startAuxListener("metrics", os.Getenv("LEYLINE_METRICS_LISTEN"), metricsHandler())
	if err != nil {
		slog.Error("metrics listener", "error", err)
		os.Exit(1)
	}
	pprofServer, _, err := startAuxListener("pprof", os.Getenv("LEYLINE_PPROF_LISTEN"), pprofHandler())
	if err != nil {
		slog.Error("pprof listener", "error", err)
		os.Exit(1)
	}

	// Vaults hydrate lazily on first /_leyline/sync/{vault} or admin request — no
	// eager scan at startup. Exception: server.pinned_vaults are hydrated here,
	// sequentially, and never evicted. A missing vault directory or hydration
	// error logs a warning and is skipped — startup does not fail (a pinned vault
	// may be created later via leyline-admin).
	for _, id := range cfg.Server.PinnedVaults {
		if _, err := h.GetOrHydrate(id); err != nil {
			slog.Warn("pinned vault skipped (not in registry or missing)", "vault", id, "error", err)
		}
	}

	mux := http.NewServeMux()
	// WS is wired directly on the outer mux: TimeoutHandler/AccessLog would
	// either no-op or break the long-lived hijacked connection.
	mux.HandleFunc("GET /_leyline/sync/{vault}", h.ServeWS)

	restMux := http.NewServeMux()
	adminAPI := api.NewAdminAPI(h)
	adminAPI.RegisterRoutes(restMux)
	api.NewOperatorAPI(h, adminAPI).RegisterRoutes(restMux)
	api.NewV1API(h).RegisterRoutes(restMux)
	mux.Handle("/", restMux)

	adminSocketSrv, adminSocketLn, socketErr := api.ServeUnixSocket(cfg.AdminSocket, httpx.Recover(restMux))
	if socketErr != nil {
		slog.Error("start admin socket", "path", cfg.AdminSocket, "error", socketErr)
		os.Exit(1)
	}
	slog.Info("admin socket listening", "path", cfg.AdminSocket)

	addr := net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port))
	server := &http.Server{
		Addr: addr,
		// Order: AccessLog outermost (always logs final status, even after
		// Recover or a per-route Timeout fires); Recover next (catches
		// panics from any handler); per-route Timeout lives inside Mounter.
		Handler:           httpx.AccessLog(httpx.Recover(mux)),
		ReadHeaderTimeout: 5 * time.Second,
		// ReadTimeout > default handler timeout (30s) so legitimate slow
		// ops finish before this kicks in; still bounds slow-trickle body
		// reads that TimeoutHandler can't cancel. WS upgrades hijack the
		// connection before the body phase, so this doesn't affect WS.
		ReadTimeout: 35 * time.Second,
		IdleTimeout: 120 * time.Second,
	}

	// Closed once the shutdown goroutine has fully drained; main() blocks on
	// it after ListenAndServe returns so the process doesn't exit mid-drain
	// and abandon the WAL/idem-cache persist StopAndDrain guarantees.
	shutdownDone := make(chan struct{})
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		<-sigCh

		slog.Info("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		for _, id := range h.ListVaultIDs() {
			h.DisconnectVaultClients(id, "server_shutdown")
		}

		server.Shutdown(ctx)
		if adminSocketSrv != nil {
			adminSocketSrv.Shutdown(ctx)
			_ = adminSocketLn.Close()
			_ = os.Remove(cfg.AdminSocket)
		}
		if metricsServer != nil {
			metricsServer.Shutdown(ctx)
		}
		if pprofServer != nil {
			pprofServer.Shutdown(ctx)
		}

		// Flush any pending staged ops to disk so the WAL stays trim across restart.
		for _, id := range h.ListVaultIDs() {
			vs := h.GetVaultState(id)
			if vs == nil {
				continue
			}
			if err := h.FlushAllStages(vs); err != nil {
				slog.Warn("shutdown flush", "vault", id, "error", err)
			}
		}

		// StopAndDrain waits on every vault's commit runners, persists the
		// idem cache, and closes the WAL before returning. Required for the
		// post-restart idempotency guarantee: a clean shutdown leaves an
		// on-disk snapshot at least as fresh as the last committed batch.
		h.StopAndDrain()
		close(shutdownDone)
	}()

	slog.Info("Leyline server listening", "addr", addr)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
	// Block until the shutdown goroutine finishes draining — it ran
	// server.Shutdown, which is what unblocked ListenAndServe above.
	<-shutdownDone
	slog.Info("server stopped")
}

// startAuxListener spins up an opt-in HTTP listener bound to a loopback
// address. Returns (nil, "", nil) when addr is empty. Returns an error if
// addr resolves to a non-loopback host — auxiliary listeners (metrics, pprof)
// expose vault names and live profile data, so they must not be publicly
// reachable. Put a reverse proxy in front (with its own auth) or use an SSH
// tunnel if you need remote access.
//
// The handler is wrapped with httpx.Recover so a panic in (e.g.) Prom text
// emission doesn't kill the aux goroutine. httpx.AccessLog is skipped — aux
// listeners are loopback-only and low volume; access logs would just clutter
// stderr.
//
// Listens before returning so callers (and tests using ":0") can capture the
// actual bound address. The returned address is "" only when addr is "".
func startAuxListener(name, addr string, h http.Handler) (*http.Server, string, error) {
	if addr == "" {
		return nil, "", nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, "", fmt.Errorf("%s listen addr %q: %w", name, addr, err)
	}
	if !isLoopbackHost(host) {
		return nil, "", fmt.Errorf(
			"%s listener: refuses non-loopback bind %q. "+
				"Auxiliary listeners expose sensitive data. "+
				"Bind to 127.0.0.1 or ::1 and put a reverse proxy in front if you need remote access.",
			name, host)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("%s listen: %w", name, err)
	}
	srv := &http.Server{Handler: httpx.Recover(h), ReadHeaderTimeout: 5 * time.Second}
	bound := ln.Addr().String()
	go func() {
		slog.Info(name+" listener", "addr", bound)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error(name+" listener", "error", err)
		}
	}()
	return srv, bound, nil
}

func isLoopbackHost(host string) bool {
	if host == "" {
		return false // empty = all interfaces; refuse
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// metricsHandler exposes the Default registry at GET /metrics. Anything else
// 404s — there is no index page, no /favicon.ico, no /_leyline/healthz here.
func metricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = metrics.Default.WriteTo(w)
	})
}

// pprofHandler explicitly wires the standard pprof routes onto a dedicated
// mux. We deliberately do NOT use http.DefaultServeMux (which net/http/pprof's
// init() also registers on) — see the import comment in this file.
func pprofHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// setupLogger configures the default slog logger from env vars.
// LEYLINE_LOG_FORMAT: "json" (default) or "text"
// LEYLINE_LOG_LEVEL:  "debug", "info" (default), "warn", "error"
func setupLogger() {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("LEYLINE_LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if strings.EqualFold(os.Getenv("LEYLINE_LOG_FORMAT"), "text") {
		handler = slog.NewTextHandler(os.Stderr, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(handler))
}
