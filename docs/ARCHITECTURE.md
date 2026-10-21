# Architecture

## The shape

    cmd/                 one small main per program; each only wires packages together
      geocoded           the API server
      gazetteer          builds a gazetteer version from OSM and boundaries
      keys               issues and revokes API keys
      geoeval            measures accuracy on your own addresses
      probe              black-box uptime check
    internal/            everything else; Go forbids other modules from importing it
      resolver           the domain: normalizer, pipeline, Source interface. Imports nothing of ours
      gazetteer          builds the place index, and is a resolver.Source backed by PostGIS
      api                HTTP: routing, auth gate, rate limits, handlers, error format
      auth, keys         key checking (interface + cache) and the key store behind it
      store              tenants, request log, corrections (Postgres, and in-memory for tests)
      evaluate           accuracy measurement against ground-truth addresses
      ratelimit, metrics, probe, config   small leaf packages with no internal imports

## Dependencies run one way

    cmd/geocoded -> api -> resolver
                       -> auth <- keys
                       -> store, ratelimit, metrics
    cmd/gazetteer, cmd/geoeval -> gazetteer, evaluate -> resolver

`resolver` knows nothing about HTTP or databases. `gazetteer` plugs into it through the `Source` interface, so
the pipeline never imports it: a source can be added, swapped or switched off from `cmd/geocoded` alone. `auth`
defines what "check a key" means and `keys` implements it, so the API depends on an interface it owns, not
on where keys are stored. There are no import cycles.

## Where new things go

| You are adding | Put it in |
| --- | --- |
| A new data source (NDAPS, Google) | a new package implementing `resolver.Source`; register it in `cmd/geocoded` |
| A scoring rule | `internal/gazetteer/source.go` (a method on `resolution`) |
| A parsing rule or abbreviation | `internal/resolver/normalize.go`, and the dictionary in `terms.go` plus the migration seed (a test keeps them equal) |
| An endpoint | `openapi/openapi.yaml` first, then a handler in `internal/api`; a test fails if they disagree |
| A table or column | a new numbered migration, then `tests/verify.sql` checks |
| A background job | a new `cmd/` program, not a goroutine inside the API |

## Conventions

* Errors are returned and wrapped with context (`fmt.Errorf("stage %s: %w", ...)`); the HTTP layer maps them to
  problem documents with stable codes. The pipeline never returns an error: a failing source becomes a warning.
* Bookkeeping fails soft (a store outage never fails an answer); security fails closed (an auth outage is a 503).
* Rules that must hold live in the database as constraints, not only in code.
* Tests that need a database run against real PostgreSQL as the restricted role, not mocks.

## Known compromises

* `Pipeline.Geocode`, `Normalizer.Normalize`, `handleCorrection` and `handleBatch` are still 60 to 80 lines. Readable, but the next to split.
* One geocode runs several SQL queries one after another. Fine until the load test says otherwise; the first fixes
  would be caching the 37 states in memory and running independent lookups concurrently.
* Scoring weights are hand-set and provisional until `geoeval` measures them.

Further reading: [Organizing a Go module](https://go.dev/doc/modules/layout), and the `internal` directory rule in the
[Go command docs](https://pkg.go.dev/cmd/go#hdr-Internal_Directories).
