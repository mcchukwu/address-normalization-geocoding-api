#!/usr/bin/env bash
# Contract test: generate requests from openapi/openapi.yaml and check the live server's answers.
# Needs the server running with a key that has all scopes, and: pip install schemathesis
set -euo pipefail
URL="${URL:-http://127.0.0.1:8080}"
KEY="${KEY:-devkey}"
schemathesis run openapi/openapi.yaml --url "$URL" -H "X-API-Key: $KEY" --max-examples "${EXAMPLES:-100}" --workers 2
