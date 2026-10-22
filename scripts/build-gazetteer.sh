#!/usr/bin/env bash
# Build and activate a new gazetteer from an OpenStreetMap extract plus geoBoundaries files.
#
#   1. osm2pgsql reads the .osm.pbf into a staging schema (osm_stage), using scripts/osm2pgsql-gazetteer.lua
#   2. cmd/gazetteer turns the staging rows plus the boundaries into gaz.features and gaz.names,
#      normalising every name with the same Go code the API uses, and makes the build active
#
# Needs: osm2pgsql (sudo apt install osm2pgsql), psql, go. ETL_DATABASE_URL must be a role that can
# create schemas and write gaz (the database owner in development), not the API's restricted role.
set -euo pipefail
: "${ETL_DATABASE_URL:?set ETL_DATABASE_URL (database owner), e.g. postgres://postgres:...@localhost:5432/geocode}"
PBF="${PBF:-data/osm/nigeria-latest.osm.pbf}"
ADM1="${ADM1:-data/boundaries/geoBoundaries-NGA-ADM1.geojson}"
ADM2="${ADM2:-data/boundaries/geoBoundaries-NGA-ADM2.geojson}"
LABEL="${LABEL:-$(date -u +%Y-%m-%d)-gaz}"

for f in "$PBF" "$ADM1" "$ADM2"; do
  [ -f "$f" ] || { echo "missing $f (see README: where to put the data files)"; exit 1; }
done

psql "$ETL_DATABASE_URL" -v ON_ERROR_STOP=1 -c 'CREATE SCHEMA IF NOT EXISTS osm_stage'
osm2pgsql -d "$ETL_DATABASE_URL" -O flex -S scripts/osm2pgsql-gazetteer.lua --slim --drop "$PBF"
DATABASE_URL="$ETL_DATABASE_URL" go run ./cmd/gazetteer -adm1 "$ADM1" -adm2 "$ADM2" -label "$LABEL" -activate
echo "Done. The API watcher will pick up the active data version within 30 seconds."
