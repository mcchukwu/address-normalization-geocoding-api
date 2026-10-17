-- Schema verification. Run after all migrations; everything happens in one transaction
-- that is rolled back, so it leaves nothing behind.
--   psql -v ON_ERROR_STOP=1 -d <db> -f tests/verify.sql
\set ON_ERROR_STOP on
BEGIN;

CREATE FUNCTION pg_temp.must_fail(label text, stmt text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    BEGIN
        EXECUTE stmt;
    EXCEPTION WHEN others THEN
        RAISE NOTICE 'ok   rejected: %  (%)', label, left(SQLERRM, 70);
        RETURN;
    END;
    RAISE EXCEPTION 'NOT REJECTED: %', label;
END $$;

CREATE FUNCTION pg_temp.check_that(label text, cond boolean) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    IF cond IS NOT TRUE THEN RAISE EXCEPTION 'FAILED: %', label; END IF;
    RAISE NOTICE 'ok   %', label;
END $$;

-- ---------------------------------------------------------------- reference data
SELECT pg_temp.check_that('37 states loaded', (SELECT count(*) FROM ref.states) = 37);
SELECT pg_temp.check_that('building radius is 50 m',
    (SELECT radius_m FROM ref.precision_classes WHERE precision = 'building') = 50);
SELECT pg_temp.check_that('terms expand opp -> opposite',
    (SELECT expansion FROM ref.address_terms WHERE term = 'opp' AND kind = 'abbreviation') = 'opposite');

-- ---------------------------------------------------------------- gazetteer
DO $$
DECLARE v integer; lagos bigint; ikeja bigint; road bigint; n integer; top text;
BEGIN
    INSERT INTO gaz.data_versions (label, osm_extract_date) VALUES ('verify-' || gen_random_uuid(), '2026-10-01') RETURNING id INTO v;
    INSERT INTO gaz.active_version (singleton, data_version_id) VALUES (true, v)
        ON CONFLICT (singleton) DO UPDATE SET data_version_id = EXCLUDED.data_version_id;   -- safe on a database that already has a build: all rolled back

    INSERT INTO gaz.features (data_version_id, kind, geom, source, source_id, licence, fetched_at)
    VALUES (v, 'state', ST_GeomFromText('POLYGON((2.7 6.3,4.4 6.3,4.4 6.8,2.7 6.8,2.7 6.3))', 4326),
            'geoboundaries', 'lagos', 'CC BY', now()) RETURNING id INTO lagos;
    INSERT INTO gaz.features (data_version_id, kind, geom, state_id, source, source_id, licence, fetched_at)
    VALUES (v, 'lga', ST_GeomFromText('POLYGON((3.30 6.57,3.40 6.57,3.40 6.65,3.30 6.65,3.30 6.57))', 4326),
            lagos, 'geoboundaries', 'ikeja', 'CC BY', now()) RETURNING id INTO ikeja;
    INSERT INTO gaz.features (data_version_id, kind, category, geom, state_id, lga_id, source, source_id, licence, fetched_at)
    VALUES (v, 'road', 'primary', ST_GeomFromText('LINESTRING(3.3426 6.5964,3.3515 6.6018)', 4326),
            lagos, ikeja, 'osm', 'way/1', 'ODbL', now()) RETURNING id INTO road;
    INSERT INTO gaz.names (feature_id, name, name_norm, is_primary) VALUES
        (road, 'Allen Avenue', 'allen avenue', true), (road, 'Allen Ave', 'allen avenue', false);
    INSERT INTO gaz.names (feature_id, name, name_norm, is_primary) VALUES
        (lagos, 'Lagos', 'lagos', true);

    PERFORM pg_temp.check_that('rep_point is generated and lies on a polygon',
        (SELECT ST_Intersects(geom, rep_point) FROM gaz.features WHERE id = ikeja));
    SELECT count(*), max(name) INTO n, top FROM gaz.search_names('alen avenue', 'road', lagos);
    PERFORM pg_temp.check_that('fuzzy search finds a misspelled road', n >= 1 AND top = 'Allen Avenue');
    SELECT count(*) INTO n FROM gaz.search_names('opebi road', 'road', lagos);
    PERFORM pg_temp.check_that('fuzzy search does not invent matches', n = 0);

    PERFORM pg_temp.must_fail('state with a point geometry',
        format($f$INSERT INTO gaz.features (data_version_id, kind, geom, source, source_id, licence, fetched_at)
                  VALUES (%s, 'state', ST_Point(3.3, 6.5, 4326), 'osm', 'x1', 'ODbL', now())$f$, v));
    PERFORM pg_temp.must_fail('address without a house number',
        format($f$INSERT INTO gaz.features (data_version_id, kind, geom, source, source_id, licence, fetched_at)
                  VALUES (%s, 'address', ST_Point(3.3, 6.5, 4326), 'osm', 'x2', 'ODbL', now())$f$, v));
    PERFORM pg_temp.must_fail('google as a gazetteer source',
        format($f$INSERT INTO gaz.features (data_version_id, kind, geom, source, source_id, licence, fetched_at)
                  VALUES (%s, 'place', ST_Point(3.3, 6.5, 4326), 'google', 'x3', 'n/a', now())$f$, v));
    PERFORM pg_temp.must_fail('duplicate source row in one version',
        format($f$INSERT INTO gaz.features (data_version_id, kind, geom, state_id, lga_id, source, source_id, licence, fetched_at)
                  VALUES (%s, 'road', ST_GeomFromText('LINESTRING(3.3 6.5,3.4 6.6)', 4326), %s, %s, 'osm', 'way/1', 'ODbL', now())$f$, v, lagos, ikeja));
    PERFORM pg_temp.must_fail('two primary names on one feature',
        format($f$INSERT INTO gaz.names (feature_id, name, name_norm, is_primary) VALUES (%s, 'Allen', 'allen', true)$f$, road));

    -- switching the active version hides the old build from the API's views
    INSERT INTO gaz.data_versions (label) VALUES ('verify-other-' || gen_random_uuid());
    UPDATE gaz.active_version SET data_version_id = (SELECT max(id) FROM gaz.data_versions);
    PERFORM pg_temp.check_that('inactive version is invisible to the API views',
        (SELECT count(*) FROM gaz.current_features) = 0);
    UPDATE gaz.active_version SET data_version_id = v;
END $$;

-- ---------------------------------------------------------------- app schema
DO $$
DECLARE t uuid := gen_random_uuid(); r uuid := gen_random_uuid(); made integer; dropped integer; p text; cnt bigint; cleared bigint;
BEGIN
    INSERT INTO app.tenants (tenant_id, display_name) VALUES (t, 'Test tenant');
    PERFORM app.ensure_request_partitions(2);
    PERFORM pg_temp.check_that('partition job is idempotent', app.ensure_request_partitions(2) = 0);
    PERFORM pg_temp.check_that('three monthly partitions exist',
        (SELECT count(*) FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
          WHERE i.inhparent = 'app.requests'::regclass AND c.relname ~ '^requests_[0-9]{4}_[0-9]{2}$') >= 3);

    -- the job ran late: this month's rows are already in the default partition
    EXECUTE format('DROP TABLE app.requests_%s', to_char(now() AT TIME ZONE 'UTC', 'YYYY_MM'));
    INSERT INTO app.requests_default (id, received_at, tenant_id, key_id, endpoint, traffic_class, status_code, latency_ms)
    VALUES (gen_random_uuid(), now(), t, 'late', '/v1/geocode', 'real', 200, 5);
    made := app.ensure_request_partitions(0);
    PERFORM pg_temp.check_that('late rows are moved out of the default partition',
        made = 1 AND (SELECT count(*) FROM app.requests_default WHERE key_id = 'late') = 0
        AND (SELECT count(*) FROM app.requests WHERE key_id = 'late') = 1);

    INSERT INTO app.requests (id, received_at, tenant_id, key_id, endpoint, traffic_class, status_code, latency_ms, precision, confidence)
    VALUES (r, now(), t, 'key_1', '/v1/geocode', 'real', 200, 42, 'street', 0.71);
    SELECT tableoid::regclass::text INTO p FROM app.requests WHERE id = r;
    PERFORM pg_temp.check_that('a request lands in this month''s partition',
        p = 'app.requests_' || to_char(now() AT TIME ZONE 'UTC', 'YYYY_MM'));
    INSERT INTO app.requests (id, received_at, tenant_id, key_id, endpoint, traffic_class, status_code, latency_ms)
    VALUES (gen_random_uuid(), now() - interval '5 years', t, 'key_1', '/v1/geocode', 'real', 200, 10);
    SELECT tableoid::regclass::text INTO p FROM app.requests WHERE received_at < now() - interval '4 years';
    PERFORM pg_temp.check_that('a row with no partition falls into the default partition, not an error', p = 'app.requests_default');

    CREATE TABLE app.requests_2020_01 PARTITION OF app.requests
        FOR VALUES FROM ('2020-01-01 00:00:00+00') TO ('2020-02-01 00:00:00+00');
    dropped := app.drop_old_request_partitions(90);
    PERFORM pg_temp.check_that('retention drops only old months', dropped = 1 AND to_regclass('app.requests_2020_01') IS NULL
        AND to_regclass('app.requests_' || to_char(now() AT TIME ZONE 'UTC', 'YYYY_MM')) IS NOT NULL);

    PERFORM pg_temp.must_fail('negative latency',
        format($f$INSERT INTO app.requests (id, received_at, tenant_id, key_id, endpoint, traffic_class, status_code, latency_ms)
                  VALUES (gen_random_uuid(), now(), %L, 'k', '/v1/geocode', 'real', 200, -1)$f$, t));
    PERFORM pg_temp.must_fail('raw text kept longer than 30 days',
        format($f$INSERT INTO app.request_raw_text (request_id, received_at, raw_input, expires_at)
                  VALUES (gen_random_uuid(), now(), 'opp. first bank', now() + interval '31 days')$f$));
    INSERT INTO app.request_raw_text (request_id, received_at, raw_input, expires_at)
    VALUES (gen_random_uuid(), now() - interval '40 days', 'old text', now() - interval '10 days'),
           (gen_random_uuid(), now(), 'fresh text', now() + interval '30 days');
    SELECT raw_rows_deleted, correction_texts_cleared INTO cnt, cleared FROM app.purge_expired_raw_text();
    PERFORM pg_temp.check_that('purge deletes expired raw text only',
        cnt >= 1
        AND NOT EXISTS (SELECT 1 FROM app.request_raw_text WHERE raw_input = 'old text')
        AND EXISTS (SELECT 1 FROM app.request_raw_text WHERE raw_input = 'fresh text'));

    PERFORM pg_temp.must_fail('correction with swapped Lagos coordinates',
        format($f$INSERT INTO app.corrections (id, tenant_id, request_id, input_hash, normalized, original_precision, confirmed)
                  VALUES (gen_random_uuid(), %L, gen_random_uuid(), '\x00', '{}', 'street', ST_Point(6.52, 3.38, 4326))$f$, t));
    INSERT INTO app.corrections (id, tenant_id, request_id, input_hash, normalized, original_precision, confirmed)
    VALUES (gen_random_uuid(), t, r, '\x00', '{"area":"ikeja"}', 'street', ST_Point(3.3515, 6.6018, 4326));
    PERFORM pg_temp.must_fail('second correction for the same request',
        format($f$INSERT INTO app.corrections (id, tenant_id, request_id, input_hash, normalized, original_precision, confirmed)
                  VALUES (gen_random_uuid(), %L, %L, '\x00', '{}', 'street', ST_Point(3.35, 6.60, 4326))$f$, t, r));
    PERFORM pg_temp.must_fail('correction text without an expiry',
        format($f$INSERT INTO app.corrections (id, tenant_id, request_id, input_hash, input_text, normalized, original_precision, confirmed)
                  VALUES (gen_random_uuid(), %L, gen_random_uuid(), '\x00', 'opp. first bank', '{}', 'street', ST_Point(3.35, 6.60, 4326))$f$, t));
END $$;

-- ---------------------------------------------------------------- evaluation
DO $$
DECLARE s integer; run bigint; c bigint[] := '{}'; i integer; base_lat double precision := 6.5244; base_lon double precision := 3.3792;
        m record; cid bigint;
        offs double precision[] := ARRAY[0, 0.0008, 0.0100, NULL, 0.0135];
        prec ref.precision[] := ARRAY['street','street','street','none','locality']::ref.precision[];
        conf real[] := ARRAY[0.9, 0.9, 0.9, NULL, 0.5];
BEGIN
    INSERT INTO eval.sets (name, tier, source) VALUES ('test set', 'A', 'unit test') RETURNING id INTO s;
    FOR i IN 1..5 LOOP
        INSERT INTO eval.cases (set_id, raw_text, expected, expected_precision, truth_method, style)
        VALUES (s, 'case ' || i, ST_Point(base_lon, base_lat, 4326), 'building', 'door_step_gps', 'landmark')
        RETURNING id INTO cid;
        c := c || cid;
    END LOOP;
    INSERT INTO eval.sets (name, tier, source) VALUES ('web set', 'B', 'public pages');
    INSERT INTO eval.cases (set_id, raw_text) SELECT id, 'opp. first bank, allen avenue, ikeja' FROM eval.sets WHERE name = 'web set';
    PERFORM pg_temp.check_that('tier B cases need no truth', true);

    PERFORM pg_temp.must_fail('truth with only a coordinate',
        format($f$INSERT INTO eval.cases (set_id, raw_text, expected) VALUES (%s, 'x', ST_Point(3.3, 6.5, 4326))$f$, s));
    PERFORM pg_temp.must_fail('truth copied from Google',
        format($f$INSERT INTO eval.cases (set_id, raw_text, expected, expected_precision, truth_method)
                  VALUES (%s, 'x', ST_Point(3.3, 6.5, 4326), 'building', 'google_maps_pin')$f$, s));
    PERFORM pg_temp.must_fail('truth outside Nigeria',
        format($f$INSERT INTO eval.cases (set_id, raw_text, expected, expected_precision, truth_method)
                  VALUES (%s, 'x', ST_Point(-0.2, 51.5, 4326), 'building', 'door_step_gps')$f$, s));

    INSERT INTO eval.runs (set_id, git_sha) VALUES (s, 'abc123') RETURNING id INTO run;
    FOR i IN 1..5 LOOP
        INSERT INTO eval.results (run_id, case_id, precision, confidence, result)
        VALUES (run, c[i], prec[i], conf[i],
                CASE WHEN prec[i] = 'none' THEN NULL ELSE ST_Point(base_lon, base_lat + offs[i], 4326) END);
    END LOOP;
    PERFORM pg_temp.must_fail('a none answer that still carries a point',
        format($f$INSERT INTO eval.results (run_id, case_id, precision, result)
                  VALUES (%s, %s, 'none', ST_Point(3.3, 6.5, 4326))$f$, run, c[4]));

    SELECT * INTO m FROM eval.run_metrics WHERE run_id = run;
    PERFORM pg_temp.check_that('metrics: n and resolved', m.n = 5 AND m.resolved = 4);
    PERFORM pg_temp.check_that('metrics: median error about 600 m', m.median_error_m BETWEEN 580 AND 610);
    PERFORM pg_temp.check_that('metrics: within 50 m / 100 m / 500 m / 1 km = 25% / 50% / 50% / 50%',
        round(m.within_50m::numeric, 2) = 0.25 AND round(m.within_100m::numeric, 2) = 0.50
        AND round(m.within_500m::numeric, 2) = 0.50 AND round(m.within_1km::numeric, 2) = 0.50);
    PERFORM pg_temp.check_that('metrics: calibration at 0.8 is 2 of 3 inside the claimed radius',
        m.confident_n = 3 AND round(m.calibration_at_0_8::numeric, 2) = 0.67);
    PERFORM pg_temp.check_that('metrics: false-confidence rate is 1 of 3 confident answers',
        round(m.false_confidence_rate::numeric, 2) = 0.33);
END $$;

-- ---------------------------------------------------------------- api keys
DO $$
DECLARE t uuid := gen_random_uuid();
BEGIN
    INSERT INTO app.tenants (tenant_id, display_name) VALUES (t, 'Keys tenant');
    INSERT INTO app.api_keys (id, tenant_id, key_id, secret_hash, name, scopes)
    VALUES (gen_random_uuid(), t, 'abcdef01', sha256('secret'), 'prod', '{geocode,normalize}');
    PERFORM pg_temp.check_that('an API key row is accepted', (SELECT count(*) FROM app.api_keys WHERE tenant_id = t) = 1);
    PERFORM pg_temp.must_fail('key with an unknown scope',
        format($f$INSERT INTO app.api_keys (id, tenant_id, key_id, secret_hash, name, scopes)
                  VALUES (gen_random_uuid(), %L, 'abcdef02', sha256('x'), 'k', '{admin}')$f$, t));
    PERFORM pg_temp.must_fail('key with no scopes',
        format($f$INSERT INTO app.api_keys (id, tenant_id, key_id, secret_hash, name, scopes)
                  VALUES (gen_random_uuid(), %L, 'abcdef03', sha256('x'), 'k', '{}')$f$, t));
    PERFORM pg_temp.must_fail('key stored with a hash of the wrong length',
        format($f$INSERT INTO app.api_keys (id, tenant_id, key_id, secret_hash, name, scopes)
                  VALUES (gen_random_uuid(), %L, 'abcdef04', '\x00', 'k', '{geocode}')$f$, t));
    PERFORM pg_temp.must_fail('key id that is not 8 hex characters',
        format($f$INSERT INTO app.api_keys (id, tenant_id, key_id, secret_hash, name, scopes)
                  VALUES (gen_random_uuid(), %L, 'NOPE', sha256('x'), 'k', '{geocode}')$f$, t));
    PERFORM pg_temp.must_fail('two keys with the same public id',
        format($f$INSERT INTO app.api_keys (id, tenant_id, key_id, secret_hash, name, scopes)
                  VALUES (gen_random_uuid(), %L, 'abcdef01', sha256('x'), 'k', '{geocode}')$f$, t));
    INSERT INTO app.requests (id, received_at, tenant_id, key_id, endpoint, traffic_class, status_code, latency_ms)
    VALUES (gen_random_uuid(), now(), t, 'abcdef01', 'geocode', 'real', 200, 5),
           (gen_random_uuid(), now(), t, 'abcdef01', 'geocode', 'real', 429, 1);
    PERFORM pg_temp.check_that('daily usage counts requests and rate limiting',
        (SELECT requests = 2 AND rate_limited = 1 FROM app.usage_daily WHERE tenant_id = t AND endpoint = 'geocode'));
END $$;

-- ---------------------------------------------------------------- roles
DO $$
BEGIN
    SET LOCAL ROLE geocode_api;
    PERFORM count(*) FROM gaz.current_features;
    PERFORM pg_temp.must_fail('API role deleting from the request log', 'DELETE FROM app.requests');
    PERFORM count(id) FROM app.requests WHERE tenant_id IS NOT NULL;   -- the columns corrections need
    PERFORM pg_temp.must_fail('API role reading request latency', 'SELECT latency_ms FROM app.requests');
    PERFORM pg_temp.must_fail('API role reading request hashes', 'SELECT input_hash FROM app.requests');
    PERFORM count(key_id) FROM app.api_keys;                            -- the columns needed to check a key
    PERFORM pg_temp.must_fail('API role reading key names', 'SELECT name FROM app.api_keys');
    PERFORM pg_temp.must_fail('API role issuing keys', $f$INSERT INTO app.api_keys (id, tenant_id, key_id, secret_hash, name, scopes) VALUES (gen_random_uuid(), gen_random_uuid(), 'abcdef09', sha256('x'), 'k', '{geocode}')$f$);
    PERFORM pg_temp.must_fail('API role widening scopes', $f$UPDATE app.api_keys SET scopes = '{geocode,normalize}'$f$);
    PERFORM pg_temp.must_fail('API role writing the gazetteer',
        $f$INSERT INTO gaz.data_versions (label) VALUES ('hijack')$f$);
    PERFORM pg_temp.must_fail('API role reading evaluation data', 'SELECT count(*) FROM eval.sets');
    PERFORM pg_temp.must_fail('API role running the retention job', 'SELECT app.drop_old_request_partitions(0)');
    RESET ROLE;
    SET LOCAL ROLE geocode_maint;
    PERFORM app.ensure_request_partitions(0);
    PERFORM app.purge_expired_raw_text();
    PERFORM pg_temp.must_fail('maintenance role reading the gazetteer', 'SELECT count(*) FROM gaz.features');
    PERFORM count(*) FROM app.usage_daily;                              -- operators can read usage
    RESET ROLE;
    SET LOCAL ROLE geocode_etl;
    PERFORM pg_temp.must_fail('ETL role reading the request log', 'SELECT count(*) FROM app.requests');
    RESET ROLE;
END $$;

ROLLBACK;
\echo 'ALL CHECKS PASSED'
