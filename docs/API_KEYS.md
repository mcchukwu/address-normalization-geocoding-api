# API keys

Keys are owned by this service (table `app.api_keys`), not by the auth service: that service
authenticates people (sessions, roles in organisations), while a product API key is a long random
secret with scopes, an expiry and usage metering. The check is behind `auth.Introspector`, so another
source of keys (including an external auth service, `AUTH_MODE=http`) can replace it without touching the API.

## Format

    gk_<8 hex>_<64 hex>        e.g. gk_3f9a1c07_ …64 hex characters…

The first part is public: it identifies the key in `keys list` and in logs. The 64 hex characters are
256 random bits and are shown once, at creation. Only their SHA-256 is stored. A random 256-bit secret
cannot be guessed, so a fast hash is correct here (a slow password hash protects human-chosen secrets).

## Operating keys (run with the database owner's connection string)

    DATABASE_URL=... go run ./cmd/keys create -tenant-name "Acme Logistics" -name production -scopes geocode,normalize
    DATABASE_URL=... go run ./cmd/keys create -tenant-id <uuid> -name staging -scopes geocode -expires 30d
    DATABASE_URL=... go run ./cmd/keys list
    DATABASE_URL=... go run ./cmd/keys revoke <key_id>
    DATABASE_URL=... go run ./cmd/keys usage -days 7

Scopes: `geocode`, `normalize`, `corrections:write`. The database refuses any other value.

## Behaviour to know

* Every failure (malformed, unknown id, wrong secret, revoked, expired) returns the same 401, so a caller
  learns nothing about which part was wrong. Malformed keys are rejected without a database query.
* The API caches a valid answer for 60 seconds, so a revoked key can work for up to a minute. An
  invalid key is cached for 10 seconds so a guessing flood costs one query.
* The API's database role can read only the columns needed to check a key, stamp `last_used_at`, and
  nothing else: it cannot create keys or widen scopes. Creating keys needs the owner's connection.
* Usage comes from the request log through the view `app.usage_daily` (the log keeps 90 days).
* Tenants are created by `keys create -tenant-name`; the geocoder's tenant settings (traffic class,
  raw-text opt-in, rate-limit override) are changed with SQL on `app.tenants`.

## Not built yet

Monthly quotas and plans, key rotation with an overlap window, and a self-service dashboard. Rotation
today is: create a new key, switch the client, revoke the old one.
