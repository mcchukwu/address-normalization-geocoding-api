#!/usr/bin/env bash
# Run a batch of real addresses through the API and print one compact line each, so you can judge the
# answers by eye on real data. Paste the output back when asking for tuning.
#   API_KEY=gk_... scripts/sanity.sh                  # the built-in list
#   API_KEY=gk_... scripts/sanity.sh my-addresses.txt # one address per line
set -u
URL="${API_URL:-http://localhost:8080}"; KEY="${API_KEY:-}"
[ -n "$KEY" ] || { echo "API_KEY is not set"; exit 2; }
command -v python3 >/dev/null || { echo "python3 is needed"; exit 2; }

if [ -n "${1:-}" ]; then ADDRS="$(cat "$1")"; else ADDRS="$(cat <<'LIST'
allen avenue, ikeja
opp. first bank, allen avenue, ikeja, lagos
computer village, ikeja
victoria island, lagos
adeola odeku street, victoria island
lekki phase 1, lagos
ikorodu road, lagos
yaba, lagos
wuse 2, abuja
garki, abuja
trans amadi, port harcourt
gra, port harcourt
zaria road, kano
ogui road, enugu
ring road, benin city
bodija, ibadan
first bank
behind the mango tree
LIST
)"; fi

printf '%-46s %-9s %-5s %-20s %s\n' ADDRESS PRECISION CONF "LAT,LNG" WARNINGS
printf '%-46s %-9s %-5s %-20s %s\n' ------- --------- ---- -------- --------
while IFS= read -r a; do
  [ -z "$a" ] && continue
  body="$(python3 -c 'import json,sys; print(json.dumps({"address": sys.argv[1]}))' "$a")"
  out="$(curl -sS -m 15 -w '\n%{http_code}' -X POST "$URL/v1/geocode" -H "X-API-Key: $KEY" -H 'Content-Type: application/json' -d "$body" 2>&1)"
  code="${out##*$'\n'}"; json="${out%$'\n'*}"
  if [ "$code" != "200" ]; then printf '%-46s HTTP %s %s\n' "${a:0:45}" "$code" "${json:0:60}"; continue; fi
  python3 - "$a" "$json" <<'PY'
import json, sys
a, d = sys.argv[1], json.loads(sys.argv[2])
r = d["result"]
pos = f'{r["lat"]:.4f},{r["lng"]:.4f}' if r else "-"
src = "  <-- DEMO FIXTURE: set SOURCES=gazetteer" if any(c["source"] == "fixture" for c in d["candidates"]) else ""
print(f'{a[:45]:<46} {(r["precision"] if r else "none"):<9} {(r["confidence"] if r else ""):<5} {pos:<20} {",".join(d["warnings"])}{src}')
PY
done <<<"$ADDRS"
