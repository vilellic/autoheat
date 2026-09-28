// Command autoheat serves heating decisions to Home Assistant over HTTP.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // TZ works in a minimal container image

	"github.com/vilellic/autoheat/internal/api"
	"github.com/vilellic/autoheat/internal/config"
)

func main() {
	configPath := flag.String("config", envOr("AUTOHEAT_CONFIG", "config.yaml"), "config file")
	addr := flag.String("addr", envOr("AUTOHEAT_ADDR", ":8080"), "listen address")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(*configPath, *addr, log); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}

func run(configPath, addr string, log *slog.Logger) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           api.New(cfg, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()

	log.Info("listening", "addr", addr, "config", configPath, "devices", cfg.DeviceNames())
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("shut down")
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
