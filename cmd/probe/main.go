// Command probe checks the live API like a customer and exposes the result for Prometheus.
//
//	API_URL=https://api.example.com API_KEY=gk_... go run ./cmd/probe
//
// Environment: API_URL (required), API_KEY (required), INTERVAL (default 60s), LISTEN (default :9100).
// Give it a key whose tenant is marked synthetic (keys create -traffic-class synthetic) so its
// requests never count as customer traffic.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mcchukwu/address-normalization-geocoding-api/internal/probe"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	url, key := os.Getenv("API_URL"), os.Getenv("API_KEY")
	if url == "" || key == "" {
		log.Error("API_URL and API_KEY are required")
		os.Exit(1)
	}
	interval := 60 * time.Second
	if v := os.Getenv("INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second {
			log.Error("INTERVAL must be a duration of at least 1s", "value", v)
			os.Exit(1)
		}
		interval = d
	}
	listen := os.Getenv("LISTEN")
	if listen == "" {
		listen = ":9100"
	}
	p := &probe.Prober{URL: url, Key: key}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		p.WritePrometheus(w)
	})
	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("metrics server", "err", err.Error())
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("probing", "url", url, "interval", interval.String())
	p.Run(ctx, interval, func(err error) { log.Warn("probe failed", "err", err.Error()) })
	_ = srv.Shutdown(context.Background())
}
