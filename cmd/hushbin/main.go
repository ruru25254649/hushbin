// Command hushbin runs the Hushbin server: a self-hosted, end-to-end
// encrypted clipboard. Configuration comes from HB_* environment variables
// (see server/README.md).
//
// Usage:
//
//	hushbin              run the server
//	hushbin healthcheck  GET /healthz on the local server; exit 0 if healthy
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"hushbin/server/api"
	"hushbin/server/config"
	"hushbin/server/store"
	"hushbin/web"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			os.Exit(healthcheck())
		case "version", "-version", "--version":
			fmt.Println("hushbin", config.Version)
			return
		default:
			fmt.Fprintf(os.Stderr, "usage: %s [healthcheck|version]\n", os.Args[0])
			os.Exit(2)
		}
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func newLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.LogLevel))
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.EqualFold(cfg.LogFormat, "json") {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	slog.SetDefault(log)

	st, err := store.Open(cfg.DataDir, store.Options{MaxImageRefs: cfg.MaxImageRefs})
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// No request is in flight yet: anything left in tmp/ or without a paste
	// row is debris from an earlier crash.
	if err := st.CleanTemp(0); err != nil {
		log.Warn("temp cleanup failed", "err", err)
	}
	if n, err := st.RemoveOrphanBlobs(ctx); err != nil {
		log.Warn("orphan blob cleanup failed", "err", err)
	} else if n > 0 {
		log.Info("removed orphaned attachment directories", "count", n)
	}

	spa, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		return err
	}
	if _, err := fs.Stat(spa, "index.html"); err != nil {
		log.Warn("embedded frontend has no index.html; only the API will work")
	}

	app := api.New(api.Options{Config: cfg, Store: st, Logger: log}, spa)
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	srv.RegisterOnShutdown(app.Shutdown)

	bg, cancelBg := context.WithCancel(ctx)
	defer cancelBg()
	go app.Run(bg)

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	log.Info("hushbin listening", "addr", ln.Addr().String(), "data", st.DataDir(), "version", config.Version,
		"trusted_proxies", len(cfg.TrustedProxies))

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := srv.Shutdown(shCtx); err != nil {
			log.Warn("graceful shutdown incomplete", "err", err)
			_ = srv.Close()
		}
	}
	cancelBg()
	return nil
}

// healthcheck probes the local server (for container HEALTHCHECK, as the
// distroless image has no shell or curl).
func healthcheck() int {
	addr := os.Getenv("HB_ADDR")
	if addr == "" {
		addr = config.Default().Addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck: bad HB_ADDR:", err)
		return 1
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.Status)
		return 1
	}
	return 0
}
