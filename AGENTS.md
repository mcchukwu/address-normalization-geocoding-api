# AGENTS.md

## Commands

- `make test`: go vet and unit tests.
- `make test-db`: unit tests plus Postgres tests. Needs `TEST_DATABASE_URL`.
- `make verify`: schema checks. Needs the dev database.
- `scripts/contract.sh`: OpenAPI contract tests against a running server.
- `PG_ADMIN_URL=... scripts/e2e.sh`: full end to end on a scratch database.
- `API_KEY=... scripts/sanity.sh`: 18 real addresses against the running API.
- `make lint`: OpenAPI lint.

## Definition of done

- The change has a test that fails without it.
- `make test` passes.
- Schema changes also pass `make verify`.
- API changes update `openapi/openapi.yaml` first and pass `make lint`.
- No scoring change without a stored geoeval run.

## Style

- Comments explain why, not what. One line when needed.
- No section banners, no commented out code, no em dashes.
- Standard library first. No new dependency without asking.
- No abstraction until a second caller exists.
- Early returns, no unnecessary loops.

## Commits

- `area: lowercase imperative summary`, one intent per commit.
- Tests travel with the behavior they protect.
- Contract and docs changes may be separate commits.
- Never commit a failing test.

## Safety

- Do not run `docker compose down -v`.
- Do not delete data or untracked files.
- Do not push, force push, deploy or rotate keys.
- Do not change thresholds or weights without a baseline eval.
