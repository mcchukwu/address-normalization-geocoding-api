-- least-privilege roles. Passwords and LOGIN are granted outside migrations.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'geocode_api')   THEN CREATE ROLE geocode_api   NOLOGIN; END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'geocode_etl')   THEN CREATE ROLE geocode_etl   NOLOGIN; END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'geocode_maint') THEN CREATE ROLE geocode_maint NOLOGIN; END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'geocode_eval')  THEN CREATE ROLE geocode_eval  NOLOGIN; END IF;
END $$;

-- The running API: reads reference data and the gazetteer, writes its own log. Never deletes.
GRANT USAGE ON SCHEMA ref, gaz, app TO geocode_api;
GRANT SELECT ON ALL TABLES IN SCHEMA ref, gaz TO geocode_api;
GRANT EXECUTE ON FUNCTION gaz.search_names(text, gaz.feature_kind, bigint, real, integer) TO geocode_api;
GRANT SELECT, INSERT, UPDATE ON app.tenants TO geocode_api;
GRANT INSERT ON app.requests, app.request_raw_text TO geocode_api;
GRANT SELECT, INSERT ON app.corrections TO geocode_api;

-- The gazetteer builder: the only role that writes the gazetteer.
GRANT USAGE ON SCHEMA ref, gaz TO geocode_etl;
GRANT SELECT ON ALL TABLES IN SCHEMA ref TO geocode_etl;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA gaz TO geocode_etl;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA gaz TO geocode_etl;

-- Scheduled retention jobs: the only role that deletes from the request log.
GRANT USAGE, CREATE ON SCHEMA app TO geocode_maint;
GRANT SELECT, DELETE ON app.request_raw_text TO geocode_maint;
GRANT SELECT, UPDATE ON app.corrections TO geocode_maint;
GRANT EXECUTE ON FUNCTION app.ensure_request_partitions(integer),
                          app.drop_old_request_partitions(integer),
                          app.purge_expired_raw_text() TO geocode_maint;

-- The evaluation harness: full access to eval, read-only elsewhere.
GRANT USAGE ON SCHEMA ref, gaz, app, eval TO geocode_eval;
GRANT SELECT ON ALL TABLES IN SCHEMA ref, gaz TO geocode_eval;
GRANT SELECT ON app.corrections TO geocode_eval;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA eval TO geocode_eval;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA eval TO geocode_eval;
