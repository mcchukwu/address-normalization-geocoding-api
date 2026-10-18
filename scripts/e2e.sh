#!/usr/bin/env bash
# End-to-end test of the whole product on a throwaway database. It never touches your real data.
#
#   PG_ADMIN_URL='postgres://postgres:secret@localhost:5432/postgres?sslmode=disable' scripts/e2e.sh
#
# PG_ADMIN_URL must be a superuser (it creates and drops a scratch database, and needs the postgis
# extension). Needs: psql, osm2pgsql, go, curl. Takes about 30 seconds.
#
# What it does: migrates a new database, loads testdata/mini.osm with osm2pgsql, builds the gazetteer,
# issues API keys with the keys command, starts the real server on those keys and calls it over HTTP,
# then checks the request log, metrics, rate limiting, key revocation and the geoeval report.
set -uo pipefail
: "${PG_ADMIN_URL:?set PG_ADMIN_URL to a superuser connection string}"
for tool in psql osm2pgsql go curl; do command -v "$tool" >/dev/null || { echo "missing tool: $tool"; exit 2; }; done
cd "$(dirname "$0")/.."

DB="geocode_e2e_$$"
PORT="${E2E_PORT:-18080}"; ADMIN_PORT="${E2E_ADMIN_PORT:-19090}"; PROBE_PORT="${E2E_PROBE_PORT:-19100}"
with_db()    { echo "$PG_ADMIN_URL" | sed -E "s#/[^/?]+(\?|\$)#/$1\1#"; }
ADMIN_DB_URL="$(with_db "$DB")"
# The API connects as a throwaway login that inherits only geocode_api, so this script never touches
# the geocode_app login (or its password) that your own development setup may use.
APP_ROLE="geocode_e2e_app_$$"
APP_PW="$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')"
APP_URL="$(echo "$ADMIN_DB_URL" | sed -E "s#//[^@]+@#//$APP_ROLE:$APP_PW@#")"
TMP="$(mktemp -d)"; PID=""; PROBE_PID=""; PROD_PID=""
PASS=0; FAIL=0

cleanup() {
  for p in "$PID" "$PROBE_PID" "$PROD_PID"; do [ -n "$p" ] && kill "$p" 2>/dev/null && wait "$p" 2>/dev/null; done
  psql "$PG_ADMIN_URL" -q -c "DROP DATABASE IF EXISTS \"$DB\"" >/dev/null 2>&1
  psql "$PG_ADMIN_URL" -q -c "DROP ROLE IF EXISTS \"$APP_ROLE\"" >/dev/null 2>&1
  rm -rf "$TMP"
}
trap cleanup EXIT

ok()   { PASS=$((PASS+1)); printf '  \033[32mPASS\033[0m  %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  \033[31mFAIL\033[0m  %s\n' "$1"; [ -n "${2:-}" ] && printf '        %s\n' "$2"; }
check() { # check "description" "expected substring" "actual"
  if [[ "$3" == *"$2"* ]]; then ok "$1"; else bad "$1" "wanted [$2], got [${3:0:200}]"; fi
}
code() { curl -s -o "$TMP/body" -w '%{http_code}' "$@"; }
call() { # call METHOD path key body -> prints status; body in $TMP/body
  code -X "$1" "http://127.0.0.1:$PORT$2" ${3:+-H "X-API-Key: $3"} ${4:+-d "$4"}
}
body() { cat "$TMP/body"; }
step() { printf '\n== %s\n' "$1"; }

step "build"
for c in geocoded keys gazetteer geoeval probe; do go build -o "$TMP/$c" "./cmd/$c" || { echo "build of $c failed"; exit 2; }; done
ok "five binaries built"

step "database: create, migrate, stage the test map, build the gazetteer"
psql "$PG_ADMIN_URL" -q -v ON_ERROR_STOP=1 -c "CREATE DATABASE \"$DB\"" || exit 2
for f in $(ls migrations/*.up.sql | sort); do
  psql "$ADMIN_DB_URL" -q -v ON_ERROR_STOP=1 -f "$f" >/dev/null 2>"$TMP/mig.err" || { cat "$TMP/mig.err"; echo "migration $f failed"; exit 2; }
done
ok "all migrations applied"
psql "$ADMIN_DB_URL" -q -v ON_ERROR_STOP=1 -c "CREATE ROLE \"$APP_ROLE\" LOGIN PASSWORD '$APP_PW' IN ROLE geocode_api" || exit 2
psql "$ADMIN_DB_URL" -q -v ON_ERROR_STOP=1 -c 'CREATE SCHEMA IF NOT EXISTS osm_stage' || exit 2
osm2pgsql -d "$ADMIN_DB_URL" -O flex -S scripts/osm2pgsql-gazetteer.lua --slim --drop testdata/mini.osm >"$TMP/osm.log" 2>&1 \
  && ok "osm2pgsql staged the test map" || { tail -5 "$TMP/osm.log"; bad "osm2pgsql failed"; exit 1; }
out=$(DATABASE_URL="$ADMIN_DB_URL" "$TMP/gazetteer" -adm1 testdata/adm1.geojson -adm2 testdata/adm2.geojson -label e2e -activate 2>&1)
check "gazetteer built and activated" "active: true" "$out"
check "gazetteer has the expected roads" "road      2" "$out"

step "schema checks"
psql "$ADMIN_DB_URL" -q -v ON_ERROR_STOP=1 -f tests/verify.sql >"$TMP/verify.out" 2>&1
check "tests/verify.sql passes" "ALL CHECKS PASSED" "$(cat "$TMP/verify.out")"

step "keys"
mk() { DATABASE_URL="$ADMIN_DB_URL" "$TMP/keys" create "$@" 2>&1 | grep -o 'gk_[0-9a-f]\{8\}_[0-9a-f]\{64\}'; }
KEY_A="$(mk -tenant-name 'E2E Tenant A' -name main -scopes geocode,normalize)"
KEY_B="$(mk -tenant-name 'E2E Tenant B' -name limited -scopes geocode)"
KEY_C="$(mk -tenant-name 'E2E Tenant C' -name soon-revoked -scopes geocode)"
[ -n "$KEY_A" ] && [ -n "$KEY_B" ] && [ -n "$KEY_C" ] && ok "three keys issued (shown once, only hashes stored)" || { bad "key creation failed"; exit 1; }
LEAK=$(psql "$ADMIN_DB_URL" -Atc "SELECT count(*) FROM app.api_keys WHERE secret_hash = decode('$(echo -n "$KEY_A" | sha256sum | cut -d' ' -f1)', 'hex')")
check "the database holds the hash of a key, not the key" "1" "$LEAK"
# tenant B gets a tiny rate limit before its first request
psql "$ADMIN_DB_URL" -q -c "UPDATE app.tenants SET rate_limit_per_min = 3 WHERE display_name = 'E2E Tenant B'"

step "start the real server (local keys, gazetteer source)"
GEOCODE_ENV=dev AUTH_MODE=local SOURCES=gazetteer ADDR="127.0.0.1:$PORT" ADMIN_ADDR="127.0.0.1:$ADMIN_PORT" \
  DATABASE_URL="$APP_URL" "$TMP/geocoded" >"$TMP/server.log" 2>&1 &
PID=$!
for _ in $(seq 1 40); do curl -s "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break; sleep 0.25; done
check "healthz" "200" "$(call GET /healthz)"
[ "$(code "http://127.0.0.1:$PORT/healthz")" = "200" ] || { echo "---- server log:"; tail -15 "$TMP/server.log"; }
check "readyz (database reachable as the restricted role)" "200" "$(call GET /readyz)"
check "server reports the active data version" '"version":"e2e"' "$(grep -o '"version":"[^"]*"' "$TMP/server.log" | head -1)"

step "authentication"
check "no key is refused" "401" "$(call POST /v1/geocode '' '{"address":"allen avenue"}')"
check "not-a-key is refused" "401" "$(call POST /v1/geocode devkey '{"address":"allen avenue"}')"
check "a key lacking the scope gets 403" "403" "$(call POST /v1/corrections "$KEY_B" '{}')"

step "geocoding on real (test-map) data"
check "house number gives a building-level answer" "200" "$(call POST /v1/geocode "$KEY_A" '{"address":"12 allen avenue, ikeja, lagos"}')"
check "  precision is building" '"precision":"building"' "$(body)"
check "landmark address gives a street-level answer" '"precision":"street"' "$(call POST /v1/geocode "$KEY_A" '{"address":"opp. first bank, allen avenue, ikeja, lagos"}' >/dev/null; body)"
call POST /v1/geocode "$KEY_A" '{"address":"first bank"}' >/dev/null
check "an ambiguous name returns no pin, not a guess" '"result":null' "$(body)"
check "  and explains why" 'low_confidence' "$(body)"
call POST /v1/geocode "$KEY_A" '{"address":"behind the mango tree"}' >/dev/null
check "nothing resolvable is a 200 with a null result" '"result":null' "$(body)"
call POST /v1/geocode "$KEY_A" '{"address":"V.I., lagos"}' >/dev/null
check "an alias (V.I.) resolves to Victoria Island" '"precision":"locality"' "$(body)"
call POST /v1/normalize "$KEY_A" '{"address":"opp. first bank, allen avenue, ikeja"}' >/dev/null
check "normalize splits out the landmark relation" '"relation":"opposite"' "$(body)"
check "batch endpoint works" "200" "$(call POST /v1/geocode:batch "$KEY_A" '{"items":[{"address":"garki, abuja"},{"address":"allen avenue, ikeja"}]}')"
check "  and returns one result per item" '"index":1' "$(body)"

step "validation and errors"
check "too-short address is 422" "422" "$(call POST /v1/geocode "$KEY_A" '{"address":"ab"}')"
check "a quoted number for max_candidates is 422" "422" "$(call POST /v1/geocode "$KEY_A" '{"address":"allen avenue","max_candidates":"2"}')"
check "2.0 is accepted as an integer" "200" "$(call POST /v1/geocode "$KEY_A" '{"address":"allen avenue, ikeja","max_candidates":2.0}')"
check "broken JSON is 400" "400" "$(call POST /v1/geocode "$KEY_A" '{"address":')"
check "wrong method gets 405 as a problem document" "405" "$(call GET /v1/geocode "$KEY_A")"
check "errors carry a request id" '"request_id"' "$(call POST /v1/geocode "$KEY_A" '{}' >/dev/null; body)"

step "corrections"
RID=$(call POST /v1/geocode "$KEY_A" '{"address":"12 allen avenue, ikeja, lagos"}' >/dev/null; body | grep -o '"request_id":"[^"]*"' | cut -d'"' -f4)
sleep 1.2   # the request log is written off the request path
KEY_A2="$(mk -tenant-id "$(psql "$ADMIN_DB_URL" -Atc "SELECT tenant_id FROM app.tenants WHERE display_name='E2E Tenant A'")" -name corr -scopes corrections:write)"
CORR="{\"request_id\":\"$RID\",\"address\":\"12 allen avenue, ikeja, lagos\",\"confirmed\":{\"lat\":6.5990,\"lng\":3.3480}}"
check "a correction for your own request is recorded" "201" "$(call POST /v1/corrections "$KEY_A2" "$CORR")"
check "a second correction for the same request is 409" "409" "$(call POST /v1/corrections "$KEY_A2" "$CORR")"
check "another tenant cannot correct it (404, no leak)" "404" "$(call POST /v1/corrections "$(mk -tenant-name 'E2E Tenant D' -name x -scopes corrections:write)" "$CORR")"

step "rate limiting (tenant B is limited to 3 a minute)"
codes=""; for i in 1 2 3 4 5; do codes="$codes $(call POST /v1/geocode "$KEY_B" '{"address":"allen avenue, ikeja"}')"; done
check "the fourth request in a minute is refused" "200 200 200 429 429" "${codes# }"
code -X POST "http://127.0.0.1:$PORT/v1/geocode" -H "X-API-Key: $KEY_B" -d '{"address":"allen avenue"}' >/dev/null
check "  with a problem document naming the code" "rate_limited" "$(body)"

step "what was recorded"
sleep 1.2
check "requests are logged per tenant" "E2E Tenant A" "$(DATABASE_URL="$ADMIN_DB_URL" "$TMP/keys" usage -days 1)"
check "no raw address text is stored by default" "0" "$(psql "$ADMIN_DB_URL" -Atc 'SELECT count(*) FROM app.request_raw_text')"
check "the correction is stored with its original precision" "building" "$(psql "$ADMIN_DB_URL" -Atc 'SELECT original_precision FROM app.corrections LIMIT 1')"
metrics="$(curl -s "http://127.0.0.1:$ADMIN_PORT/metrics")"
check "metrics on the admin port: request counter" "geocode_http_requests_total" "$metrics"
check "metrics: latency histogram" "geocode_http_request_duration_seconds_bucket" "$metrics"
check "metrics: results by precision" 'precision="building"' "$metrics"
check "the public port does not serve metrics" "404" "$(call GET /metrics)"

step "probe: synthetic traffic through the same path a customer uses"
KEY_P="$(mk -tenant-name 'E2E Probe' -name probe -scopes geocode -traffic-class synthetic)"
API_URL="http://127.0.0.1:$PORT" API_KEY="$KEY_P" INTERVAL=1s LISTEN="127.0.0.1:$PROBE_PORT" "$TMP/probe" >"$TMP/probe.log" 2>&1 &
PROBE_PID=$!
sleep 3.5
pm="$(curl -s "http://127.0.0.1:$PROBE_PORT/metrics")"
check "the probe reports itself up" "probe_up 1" "$(echo "$pm" | grep '^probe_up ')"
check "the probe counted successful requests" 'probe_requests_total{outcome="success"}' "$pm"
kill "$PROBE_PID" 2>/dev/null; wait "$PROBE_PID" 2>/dev/null; PROBE_PID=""
check "the API labels the probe's traffic synthetic, not real" 'traffic_class="synthetic"' "$(curl -s "http://127.0.0.1:$ADMIN_PORT/metrics")"

step "production-mode configuration boots"
GEOCODE_ENV=prod AUTH_MODE=local SOURCES=gazetteer HASH_KEY=0123456789abcdef0123456789abcdef ADDR="127.0.0.1:$((PORT+1))" ADMIN_ADDR="127.0.0.1:$((ADMIN_PORT+1))" \
  DATABASE_URL="$APP_URL" "$TMP/geocoded" >"$TMP/prod.log" 2>&1 &
PROD_PID=$!
for _ in $(seq 1 40); do curl -s "http://127.0.0.1:$((PORT+1))/healthz" >/dev/null 2>&1 && break; sleep 0.25; done
check "prod mode serves /healthz" "200" "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$((PORT+1))/healthz")"
kill "$PROD_PID" 2>/dev/null; wait "$PROD_PID" 2>/dev/null; PROD_PID=""
GEOCODE_ENV=prod AUTH_MODE=static SOURCES=fixture DATABASE_URL="$APP_URL" "$TMP/geocoded" >"$TMP/unsafe.log" 2>&1
check "prod mode refuses static keys and the fixture source" "static" "$(cat "$TMP/unsafe.log")"

step "revoking a key"
check "key C works" "200" "$(call POST /v1/geocode "$KEY_C" '{"address":"allen avenue, ikeja"}')"
DATABASE_URL="$ADMIN_DB_URL" "$TMP/keys" revoke "$(echo "$KEY_C" | cut -d_ -f2)" >/dev/null
kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null
GEOCODE_ENV=dev AUTH_MODE=local SOURCES=gazetteer ADDR="127.0.0.1:$PORT" ADMIN_ADDR="127.0.0.1:$ADMIN_PORT" \
  DATABASE_URL="$APP_URL" "$TMP/geocoded" >"$TMP/server2.log" 2>&1 &
PID=$!
for _ in $(seq 1 40); do curl -s "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break; sleep 0.25; done
check "after revocation (fresh server, empty cache) key C is refused" "401" "$(call POST /v1/geocode "$KEY_C" '{"address":"allen avenue, ikeja"}')"
check "  while key A still works" "200" "$(call POST /v1/geocode "$KEY_A" '{"address":"allen avenue, ikeja"}')"

step "accuracy report (geoeval)"
DATABASE_URL="$ADMIN_DB_URL" "$TMP/geoeval" import -set e2e-sample -tier A -source "mini-map fixture" -file testdata/eval-sample.csv >/dev/null 2>&1
rep=$(DATABASE_URL="$ADMIN_DB_URL" "$TMP/geoeval" run -set e2e-sample 2>&1)
check "geoeval ran and resolved 4 of 6" "4 of 6" "$rep"
check "  calibration meets its target" "meets target" "$rep"

printf '\n%s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
