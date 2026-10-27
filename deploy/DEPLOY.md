# Deploying to one server

Written without being able to run Docker where it was authored, so treat the first deploy as a rehearsal:
the monitoring rules, dashboard queries and database scripts were checked (promtool, unit tests, a scratch
database), the compose file and Caddyfile were only read for mistakes.

## What you need

* A server: 2 vCPU and 4 GB RAM is a sensible start (the requirements assume at most about $30 a month).
  The Nigeria gazetteer load needs RAM and disk while it runs; check the disk size before choosing.
* A domain, with `api.<domain>` and `status.<domain>` A records pointing at the server's IP.
* Docker with the compose plugin. A firewall that allows only 22, 80 and 443.

## First start

    git clone <your repo> /srv/geocode && cd /srv/geocode
    cp deploy/.env.prod.example deploy/.env.prod      # fill every secret: openssl rand -hex 32
    alias dc='docker compose -f deploy/compose.prod.yaml --env-file deploy/.env.prod'

    dc up -d db
    dc run --rm migrate                                 # applies migrations/*.up.sql
    PGHOST=127.0.0.1 PGUSER=<POSTGRES_USER> PGPASSWORD=<POSTGRES_PASSWORD> PGDATABASE=<POSTGRES_DB> \
      APP_DB_PASSWORD=<APP_DB_PASSWORD> deploy/create-app-role.sh

## Load the data

Build on the server (the load is heavy and the database port is only open to the server itself):

    sudo apt install osm2pgsql psql golang          # or the packages your distribution names
    # copy nigeria-latest.osm.pbf and the two GeoJSON files into data/osm and data/boundaries
    ETL_DATABASE_URL='postgres://<POSTGRES_USER>:<POSTGRES_PASSWORD>@127.0.0.1:5432/<POSTGRES_DB>' scripts/build-gazetteer.sh

## Start everything and create keys

    dc up -d api prometheus grafana caddy
    dc run --rm keys create -tenant-name "My first customer" -name production -scopes geocode,normalize
    dc run --rm keys create -tenant-name probe -name probe -scopes geocode -traffic-class synthetic
    # put the probe key in deploy/.env.prod as PROBE_API_KEY, then:
    dc --profile probe up -d probe

Check: `curl https://api.<domain>/healthz`, then open `https://status.<domain>`: the dashboard is public and
read-only on purpose. The admin password is only for you.

## Keep it healthy (cron on the server)

Partitions and retention are not automatic. Run this daily; it creates next months' log partitions, drops
logs older than 90 days and deletes expired raw text:

    15 2 * * * cd /srv/geocode && docker compose -f deploy/compose.prod.yaml --env-file deploy/.env.prod exec -T db \
      psql -U <POSTGRES_USER> -d <POSTGRES_DB> -c "SELECT app.ensure_request_partitions(2); SELECT app.drop_old_request_partitions(90); SELECT app.purge_expired_raw_text();"

Back up nightly and copy the file off the server, and test a restore once:

    30 2 * * * docker exec geocode-db-1 pg_dump -U <POSTGRES_USER> -Fc <POSTGRES_DB> > /var/backups/geocode-$(date +\%F).dump

(The container name may differ: `docker ps` shows it.) After each new gazetteer build, restart the API so it reports the new data version:
`dc restart api`.

## The uptime window

Write down the date the probe first shows green. The dashboard's 30-day availability panel needs 30 days of probe data
(Prometheus keeps 45), so quote the figure only once it has a full window, and only what the dashboard shows.
Customer traffic and probe traffic are counted separately; do not present probe requests as customer requests.

## Before you call it done

* `scripts/e2e.sh` and `scripts/contract.sh` pass.
* The probe has been green for a while and the alerts in `deploy/monitoring/rules.yml` have a destination:
  Prometheus shows them, but nothing emails you until you add Alertmanager or watch the dashboard.
* A restore from backup has worked once.
* Pin the Prometheus and Grafana image tags in `.env.prod` after the first working deploy.
