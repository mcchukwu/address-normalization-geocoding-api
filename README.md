# Nigerian Address Normalization & Geocoding API

Go + PostgreSQL/PostGIS + REST/OpenAPI. Turns messy Nigerian address text into structured parts and a
coordinate that always states its precision, radius and confidence. Never fakes a pin.

    make dev          # database, migrations and the API in Docker  (needs docker, psql)
    source .env && curl -s localhost:8080/v1/geocode -H "X-API-Key: $$AUTH_STATIC_KEYS" \
         -d '{"address":"opp. first bank, allen avenue, ikeja, lagos"}'

Reference docs: `docs/index.html` (generated from `openapi/openapi.yaml`, the source of truth).

## Layout

| Path | What |
| --- | --- |
| `openapi/openapi.yaml` | The contract (OpenAPI 3.1). A test fails if the router drifts from it |
| `migrations/` | 7 numbered migrations (+ down), PostGIS schema from Phase 4 |
| `internal/resolver` | Normalizer (rules), pipeline, sources interface, demo fixture source |
| `internal/api` | HTTP layer: auth gate, tenants, rate limits, handlers, problem+json |
| `internal/auth` | Key introspection with cache, negative cache and stale-if-error |
| `internal/store` | Postgres and in-memory stores |
| `cmd/geocoded` | Server; public API on :8080, `/metrics` on the admin port :9090 |

## Configuration (environment)

| Variable | Meaning |
| --- | --- |
| `GEOCODE_ENV` | `dev` or `prod` (default prod; prod refuses unsafe settings) |
| `DATABASE_URL` | Postgres as the `geocode_api` role. Empty = in-memory (dev only) |
| `AUTH_MODE`, `AUTH_URL` | `http`: introspection endpoint of the auth service (see below) |
| `AUTH_STATIC_KEYS` | dev only: `key|tenant_id|name|scope,scope;...` |
| `HASH_KEY` | secret (16+ chars) for the keyed hash of normalised addresses |
| `SOURCES` | `fixture` for development; the PostGIS source arrives in the resolver phase |
| `DEFAULT_RATE_PER_MIN`, `MAX_BODY_BYTES`, `SOURCE_TIMEOUT_MS` | 60, 131072, 800 |

## What the auth service must provide

`POST $AUTH_URL` with `{"api_key": "..."}` returns `200 {"active": true, "tenant_id": "<uuid>",
"tenant_name": "...", "key_id": "...", "scopes": ["geocode"], "rate_limit_per_min": 120}` for a valid key
(`rate_limit_per_min` optional) and `{"active": false}` (or 401) otherwise. Scopes used: `geocode`,
`normalize`, `corrections:write`.

## Behaviour worth knowing

* Tenants are created on first sight (insert, then cached 60 s) as `real`, raw-text opt-in off, no overrides.
  An operator marks probe or load-test tenants with `UPDATE app.tenants SET traffic_class='synthetic' ...`.
* The request log keeps metadata only. Raw text is kept for 30 days only for opted-in tenants, and only
  from `/v1/geocode` and `/v1/corrections` (not batch) in v1.
* A batch costs 1 request + 1 per 10 addresses. Any schema violation in any item rejects the whole call.
* Corrections must reference a recent request by the same tenant; otherwise 404 (existence is not leaked).
* Dependencies down: auth outage fails closed (503, cached keys keep working 5 more minutes); a store
  outage never fails an answer.

## Tests

* `make test` runs the Go tests with the race detector; `make test-db` adds the Postgres integration tests,
  which connect as a superuser then `SET ROLE geocode_api`, so they also prove the grants are sufficient.
* `make verify` runs 42 schema checks. `make contract` runs schemathesis against a live server.

## Not done yet (by design)

* The PostGIS gazetteer source and calibrated scoring (resolver phase). Until then only the `fixture`
  source exists, with approximate coordinates for a handful of places. Do not use it for real answers.
* NDAPS and Google adapters; NDAPS code recognition (FR9).
* Dockerfile and compose service were written but not built in the authoring sandbox.
