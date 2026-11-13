// Command alcove is the Alcove server. At the scaffold stage it only serves
// /healthz and shuts down cleanly on SIGINT or SIGTERM.
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

	"github.com/herbiewalker/alcove/internal/server"
)

// version is stamped at build time: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe /healthz on localhost and exit 0 or 1 (for the container HEALTHCHECK)")
	flag.Parse()

	addr := os.Getenv("ALCOVE_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	if *healthcheck {
		os.Exit(probe(addr))
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	srv := &http.Server{
		Addr:              addr,
		Handler:           server.Handler(version),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("alcove listening", "addr", addr, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err.Error())
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "err", err.Error())
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}

// probe checks the local /healthz. It exists because a distroless image has no
// shell or curl to run a HEALTHCHECK with.
func probe(addr string) int {
	host := addr
	if len(host) > 0 && host[0] == ':' {
		host = "127.0.0.1" + host
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + host + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
