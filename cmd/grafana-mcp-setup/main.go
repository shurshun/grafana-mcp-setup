// Command grafana-mcp-setup serves the self-service token page.
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

	"github.com/shurshun/grafana-mcp-setup/internal/server"
)

var Version = "dev"

type emailFlags []string

func (v *emailFlags) String() string { return fmt.Sprint([]string(*v)) }
func (v *emailFlags) Set(value string) error {
	*v = append(*v, value)
	return nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(os.Args[1:]); err != nil {
		slog.Error("grafana-mcp-setup stopped", "err", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := server.FromEnv()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if len(args) > 0 && (args[0] == "reconcile" || args[0] == "offboard") {
		return runDryRun(args[0], args[1:], cfg)
	}
	if len(args) > 0 && args[0] != "serve" {
		return fmt.Errorf("unknown command %q", args[0])
	}
	return serve(cfg)
}

func runDryRun(command string, args []string, cfg server.Config) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	var emails emailFlags
	var apply bool
	var deleteExpired bool
	var keepToken string
	flags.Var(&emails, "email", "explicit email target; repeat for more targets")
	flags.BoolVar(&apply, "apply", false, "apply the reported changes")
	if command == "reconcile" {
		flags.BoolVar(&deleteExpired, "delete-expired", false, "remove expired and revoked tokens")
		flags.StringVar(&keepToken, "keep-token", "", "keep this exact live token name and remove the others")
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("targets must use repeated --email flags")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if command == "offboard" {
		return server.Offboard(ctx, cfg, emails, apply, os.Stdout)
	}
	return server.Reconcile(ctx, cfg, emails, server.CleanupOptions{Apply: apply, DeleteExpired: deleteExpired, KeepToken: keepToken}, os.Stdout)
}

func serve(cfg server.Config) error {
	startupCtx, startupCancel := context.WithTimeout(context.Background(), cfg.StartupTimeout)
	s, err := server.New(startupCtx, cfg)
	startupCancel()
	if err != nil {
		return fmt.Errorf("starting up: %w", err)
	}
	s.SetVersion(Version)
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	slog.Info("listening", "version", Version, "addr", cfg.Addr, "path", cfg.BasePath, "grafana", cfg.GrafanaURL, "lock_mode", cfg.RotationLockMode)
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		return nil
	}
}
