// Command tally-gateway distributes SwitchFrame tally state to tally devices
// over WebSocket.
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

	"github.com/marquies/dmf-app-tally-gateway/internal/config"
	"github.com/marquies/dmf-app-tally-gateway/internal/gateway"
	"github.com/marquies/dmf-app-tally-gateway/internal/registry"
	"github.com/marquies/dmf-app-tally-gateway/internal/upstream"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("exiting", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	reg, err := registry.Open(cfg.DataDir)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	g := gateway.New(cfg, reg, log)
	poller := &upstream.Poller{
		URL:      cfg.SwitchFrameURL,
		Token:    cfg.SwitchFrameToken,
		Interval: cfg.PollInterval,
		Client:   &http.Client{Timeout: cfg.UpstreamStaleTimeout},
		Observe:  g.ObservePoll,
	}
	go g.Run(ctx, poller)

	srv := &http.Server{Addr: cfg.ListenAddr, Handler: g.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.ListenAddr, "switchframe", cfg.SwitchFrameURL, "tls", cfg.TLSCertFile != "")
		if cfg.TLSCertFile != "" {
			errc <- srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			errc <- srv.ListenAndServe()
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Shutdown does not wait for hijacked WebSocket connections; devices see
	// the socket close and fall back to fail-safe.
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
