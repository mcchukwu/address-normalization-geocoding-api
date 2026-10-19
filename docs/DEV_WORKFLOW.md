# Day-to-day workflow (Docker)

## Stop using `docker compose down -v` as your reset button

`down -v` deletes the database volume. Everything in the database goes with it: the schema, the gazetteer,
**your API keys and tenants**, the request log, **evaluation sets, runs and corrections**. That is why a curl
that worked yesterday answers 401 today. Plain `docker compose down` keeps the volume.

## What changed? Run only this

(The commands assume the compose services are called `db` and `api`; adjust if yours differ.)

| You changed | Run | The database |
| --- | --- | --- |
| Go code: handlers, scoring weights, thresholds | `docker compose up -d --build api` | untouched |
| `.env` or the compose file | `docker compose up -d` | untouched |
| A new migration file | `scripts/migrate.sh` (applies only what is new) | updated in place |
| The normalizer or dictionary (`NameKey`, `terms.go`) | `scripts/build-gazetteer.sh` (about 30 s). No API restart needed | new version, old one kept |
| New OSM or boundary files | `scripts/build-gazetteer.sh` | new version |
| Only docs or scripts | nothing | untouched |

With Docker for the database and no `psql` on your machine, point the migrator at the container:

    PSQL_CMD='docker compose exec -T db psql -U postgres -d geocode' scripts/migrate.sh

If you already migrated by hand, run `scripts/migrate.sh baseline` once so it does not try to redo them.

## You do not need a wipe to test a fresh install

`scripts/e2e.sh` builds a scratch database from nothing (migrations, test map, gazetteer, keys, the real server) and
drops it afterwards. It is the "does a clean setup work?" test, and it never touches your data.

If you do want to start over completely: `docker compose down -v`, `up -d`, `scripts/migrate.sh`,
`scripts/build-gazetteer.sh`, and create new keys.

## Keep what matters outside the database

* Your E1-lite addresses live in a CSV in git (`data/eval/`). `geoeval import` can recreate the set at any time.
* Before a risky change: `docker compose exec -T db pg_dump -U postgres -Fc geocode > backup.dump`.
* Keys are shown once. Store the ones you use in `.env` (which is not committed).

## First-run checklist

    docker compose up -d db
    scripts/migrate.sh
    scripts/build-gazetteer.sh          # needs the data files and ETL_DATABASE_URL
    docker compose up -d --build api
    keys create ...                     # docs/API_KEYS.md
    API_KEY=gk_... scripts/try.sh "opp. first bank, allen avenue, ikeja"
    API_KEY=gk_... scripts/sanity.sh    # 18 real addresses, one line each

## When something answers nothing

`scripts/try.sh` explains the common cases (cannot reach the API, 401, 403, 503). If it cannot reach the API,
read the last log lines: `docker compose logs api --tail 30`. A 503 `not_ready` means no gazetteer is active yet:
run the builder; the API picks it up within 30 seconds, no restart.
