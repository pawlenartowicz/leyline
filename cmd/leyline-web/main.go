// Package main is the leyline-web binary entrypoint.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/pawlenartowicz/leyline/internal/buildinfo"
	"github.com/pawlenartowicz/leyline/internal/web/config"
	"github.com/pawlenartowicz/leyline/internal/web/server"
)

func main() {
	if handled, code := dispatchSubcommand(os.Args[1:]); handled {
		os.Exit(code)
	}

	configPath := flag.String("config", "config/config.yaml", "path to config.yaml")
	themesFlag := flag.String("themes", "", "themes directory (default: <config-dir>/themes)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.Value)
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(2)
	}

	themesRoot := *themesFlag
	if themesRoot == "" {
		themesRoot = filepath.Join(filepath.Dir(*configPath), "themes")
	}
	srv, err := server.New(cfg, themesRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "server: %v\n", err)
		os.Exit(2)
	}
	srv.SetConfigPath(*configPath)
	defer srv.Close()

	if typstPath, err := exec.LookPath("typst"); err == nil {
		logger.Info("typst available", "path", typstPath)
	} else {
		logger.Info("typst not found in PATH — .typ rendering will return an error", "err", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// SIGHUP triggers an in-place config reload. Parse failure keeps the
	// previous config serving traffic.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				if err := srv.ReloadConfig(); err != nil {
					logger.Error("SIGHUP reload failed — keeping previous config", "err", err)
				} else {
					logger.Info("SIGHUP reload applied")
				}
			}
		}
	}()

	logger.Info("leyline-web listening", "addr", cfg.Listen, "dev_mode", cfg.DevMode)
	if err := srv.ListenAndServe(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}
}
