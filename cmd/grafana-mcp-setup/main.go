// Command grafana-mcp-setup serves the self-service page that hands people a
// personal, read-only Grafana token for their MCP client.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/shurshun/grafana-mcp-setup/internal/server"
)

// Version is stamped in at build time by goreleaser.
var Version = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := server.FromEnv()
	if err != nil {
		slog.Error("configuration", "err", err)
		os.Exit(1)
	}

	s, err := server.New(context.Background(), cfg)
	if err != nil {
		slog.Error("starting up", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("listening",
		"version", Version,
		"addr", cfg.Addr,
		"path", cfg.BasePath,
		"grafana", cfg.GrafanaURL,
		"groups", cfg.RequiredGroups,
		"ttl", cfg.TokenTTL,
	)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serving", "err", err)
		os.Exit(1)
	}
}
