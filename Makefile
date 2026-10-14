# make dev      database + migrations + API in Docker, ready to call
# make test     unit tests (add TEST_DATABASE_URL to include the Postgres integration tests)
# make contract run the OpenAPI contract tests against a running server
export GOFLAGS ?=

.ONESHELL:

.PHONY: dev up migrate verify down-all psql run test test-db contract docs lint
# Load .env for make targets
include .env
export

dev: up migrate
	docker compose exec -T db psql -U "$(POSTGRES_USER)" -d "$(POSTGRES_DB)" -q -v ON_ERROR_STOP=1 -f - < scripts/dev-roles.sql
	docker compose up -d --build api
	@echo "API on http://localhost:8080  (key: $(AUTH_STATIC_KEYS))   metrics on http://localhost:9090/metrics"

up:
	docker compose up -d --wait db

migrate:
	@for f in $$(ls migrations/*.up.sql | sort); do \
	  echo "applying $$f"; docker compose exec -T db psql -U "$(POSTGRES_USER)" -d "$(POSTGRES_DB)" -q -v ON_ERROR_STOP=1 -f - < $$f || exit 1; \
	done

verify:
	docker compose exec -T db psql -U "$(POSTGRES_USER)" -d "$(POSTGRES_DB)" -q -v ON_ERROR_STOP=1 -f - < tests/verify.sql

down-all:
	@for f in $$(ls migrations/*.down.sql | sort -r); do \
	  echo "reverting $$f"; docker compose exec -T db psql -U "$(POSTGRES_USER)" -d "$(POSTGRES_DB)" -q -v ON_ERROR_STOP=1 -f - < $$f || exit 1; \
	done

psql:
	docker compose exec -T db psql -U "$(POSTGRES_USER)" -d "$(POSTGRES_DB)"

run:           ## run locally against the dev database, with the demo fixture source
	GEOCODE_ENV=dev AUTH_MODE=static SOURCES=fixture \
	AUTH_STATIC_KEYS="$$AUTH_STATIC_KEYS" \
	DATABASE_URL="$$DATABASE_URL" \
	go run ./cmd/geocoded

test:
	go vet ./... && go test -race -count=1 ./...

test-db:       ## includes the Postgres integration tests (connects as a superuser, then SET ROLE geocode_api)
	TEST_DATABASE_URL="$$DATABASE_URL" go test -count=1 ./internal/store

contract:
	./scripts/contract.sh

docs:          ## static HTML reference from the contract
	npx -y @redocly/cli@latest build-docs openapi/openapi.yaml -o docs/index.html

lint:
	npx -y @redocly/cli@latest lint openapi/openapi.yaml
