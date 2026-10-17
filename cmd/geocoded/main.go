// Command geocoded serves the Nigerian address normalization and geocoding API.
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

	"github.com/mcchukwu/address-normalization-geocoding-api/internal/api"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/auth"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/config"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/gazetteer"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/keys"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/metrics"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/ratelimit"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/resolver"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// storage
	var st store.Store = store.NewMemory()
	if cfg.DatabaseURL != "" {
		pg, err := store.OpenPostgres(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		st = pg
	} else {
		log.Warn("no DATABASE_URL: using the in-memory store, nothing is persisted")
	}
	defer st.Close()

	// authentication: ask the auth service (or fixed dev keys), with a short cache
	var inner auth.Introspector
	switch cfg.AuthMode {
	case "local":
		pg, ok := st.(*store.Postgres)
		if !ok {
			return errors.New("AUTH_MODE=local needs DATABASE_URL")
		}
		inner = keys.Local{DB: pg.DB}
	case "static":
		s, err := auth.ParseStatic(cfg.AuthStatic)
		if err != nil {
			return err
		}
		inner = s
		log.Warn("using static development API keys")
	default:
		inner = auth.HTTPIntrospector{URL: cfg.AuthURL}
	}
	authn := &auth.Cached{Inner: inner, TTL: time.Minute, NegativeTTL: 10 * time.Second, Grace: 5 * time.Minute}

	// resolver
	norm := resolver.NewNormalizer(resolver.DefaultTerms())
	var sources []resolver.Source
	for _, s := range cfg.Sources {
		switch s {
		case "fixture":
			log.Warn("fixture source enabled: approximate demo data, never for production")
			sources = append(sources, resolver.FixtureSource{})
		case "gazetteer":
			pg, ok := st.(*store.Postgres)
			if !ok {
				return errors.New("the gazetteer source needs DATABASE_URL")
			}
			_, label, err := gazetteer.ActiveVersion(ctx, pg.DB)
			if err != nil {
				return err
			}
			cfg.DataVersion = label // answers name the build they came from; restart to pick up a new build
			log.Info("gazetteer active", "version", label)
			sources = append(sources, gazetteer.NewSource(pg.DB, norm))
		}
	}
	pipe := resolver.NewPipeline(norm, sources...)
	pipe.SourceTimeout = cfg.SourceTimeout

	m := metrics.New()
	limiter := ratelimit.New()
	reqlog := api.NewRequestLogger(st, 1024, m, log)
	srv := api.New(api.Options{Auth: authn, Store: st, Pipeline: pipe, Limiter: limiter, Metrics: m, Logger: log,
		ReqLog: reqlog, HashKey: cfg.HashKey, DefaultRatePerMin: cfg.DefaultRatePerMin,
		MaxBodyBytes: cfg.MaxBodyBytes, DataVersion: cfg.DataVersion})

	public := &http.Server{Addr: cfg.Addr, Handler: srv, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}

	adminMux := http.NewServeMux()
	adminMux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		m.WritePrometheus(w)
	})
	admin := &http.Server{Addr: cfg.AdminAddr, Handler: adminMux, ReadHeaderTimeout: 5 * time.Second}

	// forget idle rate-limit buckets
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				limiter.Sweep(time.Hour)
			}
		}
	}()

	errc := make(chan error, 2)
	go func() { errc <- public.ListenAndServe() }()
	go func() { errc <- admin.ListenAndServe() }()
	log.Info("listening", "addr", cfg.Addr, "admin", cfg.AdminAddr, "env", cfg.Env, "sources", cfg.Sources)

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = public.Shutdown(shutdown)
	_ = admin.Shutdown(shutdown)
	reqlog.Close() // write whatever is still queued
	return nil
}
