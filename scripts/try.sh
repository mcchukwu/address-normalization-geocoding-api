#!/usr/bin/env bash
# Try one address against the running API and read the answer, or get told what is wrong.
#   API_KEY=gk_... scripts/try.sh "opp. first bank, allen avenue, ikeja"
# API_URL defaults to http://localhost:8080.
set -u
URL="${API_URL:-http://localhost:8080}"; KEY="${API_KEY:-}"; ADDR="${1:-}"
[ -n "$ADDR" ] || { echo "usage: API_KEY=gk_... $0 \"address\""; exit 2; }
[ -n "$KEY" ] || { echo "API_KEY is not set. Create a key with the keys command (docs/API_KEYS.md) and export it."; exit 2; }
command -v python3 >/dev/null || { echo "python3 is needed to format the answer"; exit 2; }

tmp="$(mktemp)"; trap 'rm -f "$tmp" "$tmp.err"' EXIT
body="$(python3 -c 'import json,sys; print(json.dumps({"address": sys.argv[1], "max_candidates": 3}))' "$ADDR")"
code="$(curl -sS -m 15 -o "$tmp" -w '%{http_code}' -X POST "$URL/v1/geocode" \
        -H "X-API-Key: $KEY" -H 'Content-Type: application/json' -d "$body" 2>"$tmp.err")" || true

case "$code" in
  000)
    echo "Could not reach $URL: $(cat "$tmp.err")"
    echo "Is the API running? Try:  docker compose ps   and   docker compose logs api --tail 30"
    echo "A common cause: the API exited at start-up because of its configuration (read the last log lines)."
    exit 1 ;;
  200)
    python3 - "$tmp" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
print("input      ", d["input"])
n = {k: v for k, v in d["normalized"].items()}
print("understood ", json.dumps(n, ensure_ascii=False))
r = d["result"]
if r is None:
    print("answer      none: no pin (an honest 'not sure enough')")
else:
    rad = f'{r["radius_m"]} m' if r["radius_m"] is not None else "the area's extent"
    print(f'answer      {r["precision"]} level, confidence {r["confidence"]}, within {rad}')
    print(f'            {r["lat"]:.5f}, {r["lng"]:.5f}   https://www.openstreetmap.org/?mlat={r["lat"]}&mlon={r["lng"]}#map=17/{r["lat"]}/{r["lng"]}')
for c in d["candidates"]:
    print(f'candidate   {c["precision"]:<9} conf {c["confidence"]:<5} {c["lat"]:.5f}, {c["lng"]:.5f}  ({c["source"]})')
print("warnings   ", ", ".join(d["warnings"]) or "none")
print("data version", d["data_version"])
if any(c["source"] == "fixture" for c in d["candidates"]):
    print("\nNOTE: these candidates come from the demo FIXTURE, not your gazetteer. Set SOURCES=gazetteer and recreate the API.")
PY
    ;;
  401) echo "401: the API does not accept this key."
       echo "Check: the key was copied whole (gk_<8 hex>_<64 hex>); it was not revoked or expired; the server's AUTH_MODE matches"
       echo "how you made it (keys made with the keys command need AUTH_MODE=local); and 'docker compose down -v' did not wipe it:"
       echo "that command deletes the database volume, and the keys live in the database. Make a new key." ;;
  403) echo "403: the key is valid but lacks the 'geocode' scope. Make a key with -scopes geocode." ;;
  503) echo "503: $(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d.get("code"),"-",d.get("detail"))' "$tmp")" ;;
  *)   echo "HTTP $code"; cat "$tmp"; echo ;;
esac
