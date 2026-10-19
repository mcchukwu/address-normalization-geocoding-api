# Testing the product, from fastest to most realistic

Run these in order; each one catches a different kind of failure.

| # | Command | Needs | Time | Catches |
| --- | --- | --- | --- | --- |
| 1 | `go vet ./... && go test -race ./...` | Go | seconds | logic bugs, data races, router drifting from openapi.yaml |
| 2 | `TEST_DATABASE_URL=... go test -race ./...` | a migrated database; for the gazetteer and evaluation tests also the test map staged (see below) | seconds | SQL mistakes, permission gaps (connects as a superuser, then `SET ROLE geocode_api`) |
| 3 | `psql "$DB_URL" -f tests/verify.sql` | a migrated database | seconds | broken constraints, grants, partition and retention functions. Rolled back; safe on a database with data |
| 4 | `PG_ADMIN_URL=... scripts/e2e.sh` | psql, osm2pgsql, go, curl on the host; a superuser URL | about 30 s | everything wired together: migrations, OSM staging, gazetteer build, keys, the real server over HTTP, rate limits, revocation, metrics, the probe, production-mode boot, the accuracy report |
| 5 | `scripts/contract.sh` against a running server | schemathesis (`pip install schemathesis`) | about a minute | the server disagreeing with the OpenAPI contract on thousands of generated requests |
| 6 | the real data load, then curl | the Nigeria files (see "Real data") | the load takes a while | real-world names, spelling, scale |
| 7 | `geoeval run -set e1-lite` | your 60 door-step addresses | seconds | accuracy, calibration, false confidence: the CV numbers |

## 4 in detail: the one-command end-to-end test

    PG_ADMIN_URL='postgres://postgres:YOURPASSWORD@localhost:5432/postgres?sslmode=disable' scripts/e2e.sh

It creates a scratch database, runs everything against the small test map in `testdata/`, and drops the
database and its throwaway login at the end. It never touches your real database, your `geocode_app`
login, or the active gazetteer. It ends with a count of passed and failed checks and exits non-zero on any failure.
If the server cannot start it prints the server's log.

Host tools: `psql` (`sudo apt install postgresql-client`), `osm2pgsql`, `go`, `curl`. The database can be the
Docker one as long as port 5432 is published to the host and the URL is a superuser.

## 2 in detail: the database-backed Go tests

For the key, store, evaluation and gazetteer tests, stage the test map into the database you point at:

    psql "$DB_URL" -c 'CREATE SCHEMA IF NOT EXISTS osm_stage'
    osm2pgsql -d "$DB_URL" -O flex -S scripts/osm2pgsql-gazetteer.lua --slim --drop testdata/mini.osm
    TEST_DATABASE_URL="$DB_URL" go test -race -count=1 ./...

(Use a test database, not the one holding your real gazetteer: the gazetteer test builds a version, activates
it, and restores the previous active version afterwards.)

## Real data (6)

    # put nigeria-latest.osm.pbf in data/osm/ and the two geoBoundaries GeoJSON files in data/boundaries/
    ETL_DATABASE_URL=... scripts/build-gazetteer.sh
    # restart the API, then try addresses you know:
    curl -s localhost:8080/v1/geocode -H "X-API-Key: $KEY" -d '{"address":"opp. first bank, allen avenue, ikeja"}'

Things to look for: an answer on the right road; `precision` honest about what it knows (a long road should
come back as `locality`, not `street`); `warnings` explaining a missing pin (`low_confidence`,
`ambiguous_place`, `landmark_not_matched`).

## Looking at real answers

    API_KEY=gk_... scripts/try.sh "opp. first bank, allen avenue, ikeja"   # one address, explained
    API_KEY=gk_... scripts/sanity.sh                                       # 18 addresses, one line each
    API_KEY=gk_... scripts/sanity.sh my-addresses.txt                      # your own list

## Not covered yet

A load test of the 300 ms p95 target on the full gazetteer: run it after the real data load, because
query time depends on the data size.
