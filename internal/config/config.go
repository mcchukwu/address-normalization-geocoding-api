// Package config reads the service's settings from environment variables.
package config

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds every setting. Secrets never have defaults in production.
type Config struct {
	Env               string // "dev" or "prod"
	Addr              string
	AdminAddr         string // /metrics is served here, never on the public listener
	DatabaseURL       string // empty = in-memory store (dev only)
	AuthMode          string // "static" or "http"
	AuthStatic        string
	AuthURL           string
	HashKey           []byte
	DefaultRatePerMin int
	MaxBodyBytes      int64
	SourceTimeout     time.Duration
	Sources           []string
	DataVersion       string
}

// FromEnv reads and checks the environment.
func FromEnv() (Config, error) {
	c := Config{
		Env:               get("GEOCODE_ENV", "prod"),
		Addr:              get("ADDR", ":8080"),
		AdminAddr:         get("ADMIN_ADDR", "127.0.0.1:9090"),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		AuthMode:          get("AUTH_MODE", "http"),
		AuthStatic:        os.Getenv("AUTH_STATIC_KEYS"),
		AuthURL:           os.Getenv("AUTH_URL"),
		DefaultRatePerMin: atoi("DEFAULT_RATE_PER_MIN", 60),
		MaxBodyBytes:      int64(atoi("MAX_BODY_BYTES", 128<<10)),
		SourceTimeout:     time.Duration(atoi("SOURCE_TIMEOUT_MS", 800)) * time.Millisecond,
		DataVersion:       get("DATA_VERSION", "dev"),
	}
	for _, s := range strings.Split(os.Getenv("SOURCES"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			c.Sources = append(c.Sources, s)
		}
	}

	if c.Env != "dev" && c.Env != "prod" {
		return c, fmt.Errorf("GEOCODE_ENV must be dev or prod, got %q", c.Env)
	}
	switch c.AuthMode {
	case "static":
		if c.Env == "prod" {
			return c, errors.New("AUTH_MODE=static is for development only")
		}
		if c.AuthStatic == "" {
			return c, errors.New("AUTH_STATIC_KEYS is required when AUTH_MODE=static")
		}
	case "http":
		if c.AuthURL == "" {
			return c, errors.New("AUTH_URL is required when AUTH_MODE=http")
		}
	default:
		return c, fmt.Errorf("AUTH_MODE must be static or http, got %q", c.AuthMode)
	}
	if c.DatabaseURL == "" && c.Env == "prod" {
		return c, errors.New("DATABASE_URL is required in prod")
	}
	if len(c.Sources) == 0 {
		return c, errors.New("no sources configured: set SOURCES (development: SOURCES=fixture)")
	}
	for _, s := range c.Sources {
		if s == "fixture" && c.Env == "prod" {
			return c, errors.New("the fixture source is for development only")
		}
		if s != "fixture" {
			return c, fmt.Errorf("unknown source %q (the PostGIS gazetteer source arrives in the resolver phase)", s)
		}
	}

	key := os.Getenv("HASH_KEY")
	switch {
	case len(key) >= 16:
		c.HashKey = []byte(key)
	case c.Env == "dev":
		c.HashKey = make([]byte, 32) // random per start: hashes are not comparable across restarts
		if _, err := rand.Read(c.HashKey); err != nil {
			return c, err
		}
	default:
		return c, errors.New("HASH_KEY (at least 16 characters) is required in prod")
	}
	return c, nil
}

func get(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func atoi(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
