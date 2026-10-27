#!/usr/bin/env bash
# Give the API its restricted database login (geocode_app) a real password.
# Run once after the migrations, on the server:
#   PGHOST=127.0.0.1 PGUSER=geocode_owner PGPASSWORD=... PGDATABASE=geocode APP_DB_PASSWORD=... deploy/create-app-role.sh
set -euo pipefail
: "${APP_DB_PASSWORD:?set APP_DB_PASSWORD}"
psql -v ON_ERROR_STOP=1 -v pw="$APP_DB_PASSWORD" <<'SQL'
SELECT 'CREATE ROLE geocode_app LOGIN IN ROLE geocode_api'
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'geocode_app')
\gexec
ALTER ROLE geocode_app PASSWORD :'pw';
SQL
echo "geocode_app can now log in with APP_DB_PASSWORD; it inherits only the geocode_api privileges."
