package config

import "testing"

func setenv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{"GEOCODE_ENV", "AUTH_MODE", "AUTH_STATIC_KEYS", "AUTH_URL", "DATABASE_URL", "HASH_KEY", "SOURCES"} {
		t.Setenv(k, "")
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestProductionRefusesUnsafeSettings(t *testing.T) {
	cases := map[string]map[string]string{
		"static auth in prod":   {"GEOCODE_ENV": "prod", "AUTH_MODE": "static", "AUTH_STATIC_KEYS": "k|t|n|geocode", "DATABASE_URL": "x", "HASH_KEY": "0123456789abcdef", "SOURCES": "gazetteer"},
		"fixture in prod":       {"GEOCODE_ENV": "prod", "AUTH_MODE": "http", "AUTH_URL": "http://auth", "DATABASE_URL": "x", "HASH_KEY": "0123456789abcdef", "SOURCES": "fixture"},
		"no hash key in prod":   {"GEOCODE_ENV": "prod", "AUTH_MODE": "http", "AUTH_URL": "http://auth", "DATABASE_URL": "x", "SOURCES": "gazetteer"},
		"no database in prod":   {"GEOCODE_ENV": "prod", "AUTH_MODE": "http", "AUTH_URL": "http://auth", "HASH_KEY": "0123456789abcdef", "SOURCES": "gazetteer"},
		"no sources":            {"GEOCODE_ENV": "dev", "AUTH_MODE": "static", "AUTH_STATIC_KEYS": "k|t|n|geocode"},
		"http auth without url": {"GEOCODE_ENV": "dev", "AUTH_MODE": "http", "SOURCES": "fixture"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			setenv(t, env)
			if _, err := FromEnv(); err == nil {
				t.Errorf("expected an error")
			}
		})
	}
}

func TestDevelopmentDefaults(t *testing.T) {
	setenv(t, map[string]string{"GEOCODE_ENV": "dev", "AUTH_MODE": "static", "AUTH_STATIC_KEYS": "k|t|n|geocode", "SOURCES": "fixture"})
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.HashKey) != 32 || c.DefaultRatePerMin != 60 || c.AdminAddr != "127.0.0.1:9090" {
		t.Errorf("unexpected defaults: %+v", c)
	}
}

func TestProductionAcceptsTheGazetteerSource(t *testing.T) {
	setenv(t, map[string]string{"GEOCODE_ENV": "prod", "AUTH_MODE": "http", "AUTH_URL": "http://auth", "DATABASE_URL": "postgres://x",
		"HASH_KEY": "0123456789abcdef", "SOURCES": "gazetteer"})
	if _, err := FromEnv(); err != nil {
		t.Fatalf("a correct production setup should load: %v", err)
	}
	setenv(t, map[string]string{"GEOCODE_ENV": "dev", "AUTH_MODE": "static", "AUTH_STATIC_KEYS": "k|t|n|geocode", "SOURCES": "gazetteer"})
	if _, err := FromEnv(); err == nil {
		t.Errorf("the gazetteer source without a database must be refused")
	}
}
