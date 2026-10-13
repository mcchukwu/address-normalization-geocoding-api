# make dev      database + migrations + API in Docker, ready to call
# make test     unit tests (add TEST_DATABASE_URL to include the Postgres integration tests)
# make contract run the OpenAPI contract tests against a running server
DB_URL ?= postgres://miracle:holly@localhost:5432/anga?sslmode=disable
export GOFLAGS ?=

.PHONY: dev up migrate verify down-all psql run test test-db contract docs lint
dev: up migrate
	psql "$(DB_URL)" -q -v ON_ERROR_STOP=1 -f scripts/dev-roles.sql
	docker compose up -d --build api
	@echo "API on http://localhost:8080  (key: devkey)   metrics on http://localhost:9090/metrics"

up:
	docker compose up -d --wait db

migrate:
	@for f in $$(ls migrations/*.up.sql | sort); do \
	  echo "applying $$f"; psql "$(DB_URL)" -q -v ON_ERROR_STOP=1 -f $$f || exit 1; \
	done

verify:
	psql "$(DB_URL)" -q -v ON_ERROR_STOP=1 -f tests/verify.sql

down-all:
	@for f in $$(ls migrations/*.down.sql | sort -r); do \
	  echo "reverting $$f"; psql "$(DB_URL)" -q -v ON_ERROR_STOP=1 -f $$f || exit 1; \
	done

psql:
	psql "$(DB_URL)"

run:           ## run locally against the dev database, with the demo fixture source
	GEOCODE_ENV=dev AUTH_MODE=static SOURCES=fixture \
	AUTH_STATIC_KEYS='devkey|11111111-1111-4111-8111-111111111111|Dev Tenant|geocode,normalize,corrections:write' \
	DATABASE_URL='postgres://miracle:holly@localhost:5432/anga?sslmode=disable' \
	go run ./cmd/geocoded

test:
	go vet ./... && go test -race -count=1 ./...

test-db:       ## includes the Postgres integration tests (connects as a superuser, then SET ROLE geocode_api)
	TEST_DATABASE_URL='$(DB_URL)' go test -count=1 ./internal/store

contract:
	./scripts/contract.sh

docs:          ## static HTML reference from the contract
	npx -y @redocly/cli@latest build-docs openapi/openapi.yaml -o docs/index.html

lint:
	npx -y @redocly/cli@latest lint openapi/openapi.yaml
