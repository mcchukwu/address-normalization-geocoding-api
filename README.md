# Nigerian Address Normalization & Geocoding API

Turns messy Nigerian address text into structured parts and a coordinate that always says how sure it is.
Go, PostgreSQL/PostGIS, REST/OpenAPI. An answer carries a **precision class** (building, street, locality, LGA,
state), a **radius** and a **confidence**; when the evidence is not good enough it returns no pin and says why,
instead of guessing.

    POST /v1/geocode   {"address": "opp. first bank, allen avenue, ikeja, lagos"}
    -> { "result": {"lat": 6.6018, "lng": 3.3515, "precision": "street", "radius_m": 300, "confidence": 0.8},
         "normalized": {...}, "candidates": [...], "warnings": [...], "data_version": "2026-10-18-gaz" }

Reference docs: `docs/index.html` (generated from `openapi/openapi.yaml`, the source of truth).

## Run it

    cp .env.example .env                       # then fill in
    docker compose up -d db
    scripts/migrate.sh                         # applies only what is new
    scripts/build-gazetteer.sh                 # real data: see "Data" below
    docker compose up -d --build api
    keys create -tenant-name "Me" -name dev -scopes geocode,normalize     # docs/API_KEYS.md
    API_KEY=gk_... scripts/try.sh "opp. first bank, allen avenue, ikeja"

Do not use `docker compose down -v` as a reset: it deletes the database, including keys and evaluation data
(`docs/DEV_WORKFLOW.md` says what to run for each kind of change).

## Data

| File | Where to get it | Put it in |
| --- | --- | --- |
| `nigeria-latest.osm.pbf` | https://download.geofabrik.de/africa/nigeria-latest.osm.pbf | `data/osm/` |
| geoBoundaries NGA ADM1 (states) and ADM2 (LGAs), as GeoJSON | https://www.geoboundaries.org/ | `data/boundaries/geoBoundaries-NGA-ADM1.geojson`, `...ADM2.geojson` |

`scripts/build-gazetteer.sh` stages the OSM file with osm2pgsql, builds a new gazetteer version, and activates it.
The API follows the active version within 30 seconds; no restart. Until one exists, geocoding answers 503 `not_ready`.

## How it works

normalize the text (rules and a Nigerian dictionary) -> ask each source for candidates (the PostGIS gazetteer) ->
score them (name similarity, context, ambiguity, road length) -> pick the finest precision whose confidence clears
its bar, or answer with no pin. Sources sit behind an interface, so each can be added, swapped or switched off.

## Tests

| Command | What it covers |
| --- | --- |
| `make test` / `go test -race ./...` | unit tests; add `TEST_DATABASE_URL` for the database-backed ones |
| `psql ... -f tests/verify.sql` | 52 schema checks (rolled back; safe on real data) |
| `PG_ADMIN_URL=... scripts/e2e.sh` | the whole product on a scratch database in about 30 s |
| `scripts/contract.sh` | schemathesis against the live API and the OpenAPI contract |
| `geoeval run -set e1-lite` | accuracy on your own ground-truth addresses (`docs/EVALUATION.md`) |

More in `docs/TESTING.md`.

## Layout

| Path | What |
| --- | --- |
| `openapi/openapi.yaml` | the contract; a test fails if the router drifts from it |
| `migrations/` | schema (PostGIS), numbered up and down files |
| `internal/resolver` | normalizer, pipeline, sources interface, demo fixture |
| `internal/gazetteer` | builder from OSM and boundaries, the PostGIS source, the active-version watcher |
| `internal/api` | HTTP layer: auth gate, tenants, rate limits, handlers, problem+json |
| `internal/keys`, `internal/auth` | API keys owned by this service; key checking with cache |
| `internal/evaluate`, `internal/probe`, `internal/metrics` | accuracy measurement, black-box probe, Prometheus metrics |
| `cmd/` | `geocoded` (API), `gazetteer`, `keys`, `geoeval`, `probe` |
| `scripts/` | migrate, build-gazetteer, e2e, try, sanity, contract |
| `deploy/` | one-server production stack, monitoring rules and dashboard, `DEPLOY.md` |

## Configuration

`GEOCODE_ENV` (dev or prod; prod refuses unsafe settings), `DATABASE_URL` (the API's restricted login),
`AUTH_MODE` (`local` keys in the database, `http` external service, `static` dev only), `SOURCES` (`gazetteer`;
`fixture` is demo data for development only), `HASH_KEY`, `DEFAULT_RATE_PER_MIN`, `MAX_BODY_BYTES`,
`SOURCE_TIMEOUT_MS`, `ADDR`, `ADMIN_ADDR` (metrics). Operator tools use `DATABASE_URL`/`ETL_DATABASE_URL` as the
database owner.

## Behaviour worth knowing

* Unresolved is `200` with `result: null`, never a made-up pin; `warnings` says why.
* Keys are shown once and stored as hashes; a revoked key can work for up to a minute (cache).
* The request log holds metadata only; raw address text is kept for 30 days only for tenants that opt in.
* Scoring weights and thresholds are provisional until measured with `geoeval` on ground-truth addresses.

## Status

Built and tested: contract, schema, gazetteer build, scoring, keys, evaluation tool, probe, monitoring rules.
Not done: measured accuracy numbers, a load test of the 300 ms target, the first production deploy
(`deploy/DEPLOY.md`; the compose stack has not been run), NDAPS and Google adapters.
