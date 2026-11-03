// Command kvnode runs one in-memory key-value node.
//
//	kvnode -addr :8081
//
// Logs are JSON lines on stderr, with UTC timestamps and a request ID that
// matches the X-Request-Id header the client sees.
//
// SIGINT or SIGTERM shuts the server down gracefully: in-flight requests get
// a few seconds to finish, then the process exits 0.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nickstrad/systems-learnings/jumphash/internal/kv"
	"github.com/nickstrad/systems-learnings/jumphash/internal/kvhttp"
	"github.com/nickstrad/systems-learnings/shared/logx"
)

func main() {
	addr := flag.String("addr", ":8081", "listen address")
	quiet := flag.Bool("quiet", false, "log only warnings and errors")
	flag.Parse()

	level := slog.LevelInfo
	if *quiet {
		level = slog.LevelWarn
	}
	logger := logx.New(os.Stderr, level).With("component", "kvnode", "addr", *addr)

	h := kvhttp.NewHandler(kv.NewStore(), logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: *addr, Handler: h}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()

	logger.Info("listening")
	select {
	case err := <-serveErr:
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("stopped")
}
