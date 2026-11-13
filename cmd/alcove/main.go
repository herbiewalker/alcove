// Command alcove is the Alcove server.
//
//	alcove                   run the server (needs a key file, see `alcove keygen`)
//	alcove keygen [-out P]   create a master key (the server never makes one itself)
//	alcove keycheck -file P  open a key file and print its fingerprint (prove a second copy)
//	alcove -healthcheck      probe /healthz on localhost, for the container HEALTHCHECK
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

	"github.com/herbiewalker/alcove/internal/app"
	"github.com/herbiewalker/alcove/internal/config"
	"github.com/herbiewalker/alcove/internal/keycli"
	"github.com/herbiewalker/alcove/internal/server"
	"github.com/herbiewalker/alcove/internal/store"
)

// version is stamped at build time: -ldflags "-X main.version=..."
var version = "dev"

// backend adapts the application to what the web server needs.
type backend struct{ a *app.App }

func (b backend) State() string         { return string(b.a.State()) }
func (b backend) Store() *store.Store   { return b.a.Store() }
func (b backend) Unlock(p string) error { return b.a.Unlock(p) }

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "keygen":
			os.Exit(keycli.Keygen(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
		case "keycheck":
			os.Exit(keycli.Keycheck(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
		}
	}

	healthcheck := flag.Bool("healthcheck", false, "probe /healthz on localhost and exit 0 or 1 (for the container HEALTHCHECK)")
	flag.Parse()

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "alcove: configuration error:", err)
		os.Exit(2)
	}
	if *healthcheck {
		os.Exit(probe(cfg.Addr))
	}
	os.Exit(run(cfg))
}

func run(cfg config.Config) int {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// Start refuses (and starts nothing) when a hard constraint would be violated: the key file
	// inside the data directory, a missing key, a wrong key, a TmpDir that is not tmpfs.
	a, err := app.Start(cfg, app.Options{Logger: logger})
	if err != nil {
		logger.Error("refusing to start", "reason", err.Error())
		fmt.Fprintln(os.Stderr, "alcove: refusing to start:", err)
		return 1
	}

	web, err := server.New(backend{a}, server.Options{Version: version, Env: cfg.Net, Logger: logger})
	if err != nil {
		logger.Error("refusing to start", "reason", err.Error())
		fmt.Fprintln(os.Stderr, "alcove: refusing to start:", err)
		_ = a.Close()
		return 1
	}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           web,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("alcove listening", "addr", cfg.Addr, "version", version, "state", string(a.State()))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err.Error())
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	code := 0
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "err", err.Error())
		code = 1
	}
	// Seal the database one last time, remove the live copy from tmpfs and wipe the key.
	if err := a.Close(); err != nil {
		logger.Error("final seal failed", "err", err.Error())
		code = 1
	}
	logger.Info("shutdown complete")
	return code
}

// probe checks the local /healthz. It exists because a distroless image has no shell or curl to
// run a HEALTHCHECK with.
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
