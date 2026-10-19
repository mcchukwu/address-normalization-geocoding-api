#!/usr/bin/env bash
# Applies migrations/*.up.sql that have not been applied yet, each in its own transaction, and records
# them in public.applied_migrations. Safe to run again and again, so you never need `docker compose down -v`
# just to "reset the schema".
#
#   scripts/migrate.sh            # apply what is new          (same as: up)
#   scripts/migrate.sh status     # list applied and pending
#   scripts/migrate.sh baseline   # mark every file as applied WITHOUT running it
#                                 # (once, for a database you migrated by hand before this script existed)
#
# Connection: DATABASE_URL (the database owner) with psql on this machine, or set PSQL_CMD to any command that
# runs psql against the database, for example:
#   PSQL_CMD='docker compose exec -T db psql -U postgres -d geocode' scripts/migrate.sh
set -euo pipefail
cd "$(dirname "$0")/.."
if [ -z "${PSQL_CMD:-}" ]; then
  : "${DATABASE_URL:?set DATABASE_URL (database owner) or PSQL_CMD}"
  PSQL_CMD="psql \"$DATABASE_URL\""
fi
run() { eval "$PSQL_CMD -X -q -v ON_ERROR_STOP=1 $*"; }

run -c "'CREATE TABLE IF NOT EXISTS public.applied_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())'"
applied() { run -At -c "'SELECT name FROM public.applied_migrations ORDER BY name'"; }

cmd="${1:-up}"
files=$(ls migrations/*.up.sql | sort)
done_list="$(applied)"

case "$cmd" in
  status)
    for f in $files; do
      n=$(basename "$f")
      if grep -qx "$n" <<<"$done_list"; then echo "applied  $n"; else echo "PENDING  $n"; fi
    done ;;
  baseline)
    for f in $files; do
      n=$(basename "$f")
      run -c "\"INSERT INTO public.applied_migrations (name) VALUES ('$n') ON CONFLICT DO NOTHING\""
    done
    echo "baselined: every existing file is now recorded as applied (nothing was run)" ;;
  up)
    count=0
    for f in $files; do
      n=$(basename "$f")
      grep -qx "$n" <<<"$done_list" && continue
      echo "applying $n"
      { echo "BEGIN;"; cat "$f"; echo; echo "INSERT INTO public.applied_migrations (name) VALUES ('$n');"; echo "COMMIT;"; } | run -f - >/dev/null
      count=$((count+1))
    done
    echo "$count migration(s) applied" ;;
  *) echo "usage: $0 [up|status|baseline]"; exit 2 ;;
esac
