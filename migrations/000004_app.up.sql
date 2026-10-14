-- tenants, request log, corrections
-- Keys, users and scopes belong to the auth service. This schema keeps only what the
-- geocoder itself must remember about a tenant, plus what it logs.
CREATE TYPE app.traffic_class AS ENUM ('real', 'synthetic', 'loadtest');
CREATE TYPE app.correction_status AS ENUM ('pending', 'accepted', 'rejected');

CREATE TABLE app.tenants (
    tenant_id          uuid PRIMARY KEY,                   -- issued by the auth service
    display_name       text NOT NULL,
    traffic_class      app.traffic_class NOT NULL DEFAULT 'real',  -- set here, never by a request header
    retain_raw         boolean NOT NULL DEFAULT false,     -- opt-in to keep raw address text for 30 days
    rate_limit_per_min integer CHECK (rate_limit_per_min > 0),     -- NULL = service default (60)
    google_enabled     boolean NOT NULL DEFAULT false,
    ndaps_enabled      boolean NOT NULL DEFAULT false,
    created_at         timestamptz NOT NULL DEFAULT now()
);

-- Request log: metadata only. Raw address text and answered coordinates are NOT stored here
-- (addresses are personal data), so a leaked log reveals traffic, not people.
CREATE TABLE app.requests (
    id              uuid NOT NULL,
    received_at     timestamptz NOT NULL,
    tenant_id       uuid NOT NULL REFERENCES app.tenants (tenant_id),
    key_id          text NOT NULL,                         -- opaque key identifier, never the key
    endpoint        text NOT NULL,
    traffic_class   app.traffic_class NOT NULL,
    status_code     smallint NOT NULL,
    latency_ms      integer NOT NULL CHECK (latency_ms >= 0),
    input_hash      bytea,                                 -- keyed HMAC-SHA256 of the normalised input
    precision       ref.precision,
    confidence      real CHECK (confidence BETWEEN 0 AND 1),
    state_id        smallint REFERENCES ref.states (id),
    lga_name        text,
    sources_used    text[] NOT NULL DEFAULT '{}',
    warnings        text[] NOT NULL DEFAULT '{}',
    data_version_id integer,
    error_code      text,
    PRIMARY KEY (id, received_at)
) PARTITION BY RANGE (received_at);

-- A default partition means a late partition job can never make logging fail.
CREATE TABLE app.requests_default PARTITION OF app.requests DEFAULT;
CREATE INDEX requests_tenant_time ON app.requests (tenant_id, received_at DESC);
CREATE INDEX requests_class_time  ON app.requests (traffic_class, received_at DESC);

-- Raw text exists only for tenants that opted in, and the database itself enforces the 30-day limit.
CREATE TABLE app.request_raw_text (
    request_id  uuid NOT NULL,
    received_at timestamptz NOT NULL,
    raw_input   text NOT NULL,
    expires_at  timestamptz NOT NULL,
    PRIMARY KEY (request_id, received_at),
    CONSTRAINT raw_text_max_30_days CHECK (expires_at <= received_at + interval '30 days')
);
CREATE INDEX raw_text_expiry ON app.request_raw_text (expires_at);

-- Confirmed pins from tenants. Self-contained on purpose: request rows expire, corrections do not.
CREATE TABLE app.corrections (
    id                   uuid PRIMARY KEY,
    tenant_id            uuid NOT NULL REFERENCES app.tenants (tenant_id),
    request_id           uuid NOT NULL,                    -- no foreign key: request rows are purged
    input_hash           bytea NOT NULL,
    input_text           text,                             -- only for tenants with retain_raw
    input_text_expires_at timestamptz,
    normalized           jsonb NOT NULL,
    original_precision   ref.precision NOT NULL,
    original_confidence  real CHECK (original_confidence BETWEEN 0 AND 1),
    confirmed            geometry(Point, 4326) NOT NULL,
    note                 text,
    status               app.correction_status NOT NULL DEFAULT 'pending',
    created_at           timestamptz NOT NULL DEFAULT now(),
    reviewed_at          timestamptz,
    UNIQUE (tenant_id, request_id),
    -- Catches swapped latitude/longitude and typos: the point must be near Nigeria.
    CONSTRAINT confirmed_in_nigeria CHECK (
        ST_X(confirmed) BETWEEN 2.0 AND 15.0 AND ST_Y(confirmed) BETWEEN 4.0 AND 14.5),
    CONSTRAINT raw_text_has_expiry CHECK (input_text IS NULL OR input_text_expires_at IS NOT NULL)
);
CREATE INDEX corrections_status ON app.corrections (status, created_at);

-- Monthly partitions, created ahead of time by a scheduled job.
-- If a month's rows already landed in the default partition (the job ran late), they are moved into
-- the new partition: a plain CREATE ... PARTITION OF would fail while such rows exist.
-- SECURITY DEFINER lets the low-privilege maintenance role run it without owning the tables.
CREATE FUNCTION app.ensure_request_partitions(months_ahead integer DEFAULT 2)
RETURNS integer LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
DECLARE
    m       timestamp;
    lo      timestamptz;
    hi      timestamptz;
    created integer := 0;
    part    text;
BEGIN
    FOR i IN 0..months_ahead LOOP
        m    := date_trunc('month', now() AT TIME ZONE 'UTC') + make_interval(months => i);
        part := format('requests_%s', to_char(m, 'YYYY_MM'));
        IF to_regclass(format('app.%I', part)) IS NULL THEN
            lo := m AT TIME ZONE 'UTC';
            hi := (m + interval '1 month') AT TIME ZONE 'UTC';
            LOCK TABLE app.requests_default IN SHARE ROW EXCLUSIVE MODE;   -- pause writers to the default partition
            EXECUTE format('CREATE TABLE app.%I (LIKE app.requests INCLUDING ALL)', part);
            EXECUTE format(
                'WITH moved AS (DELETE FROM app.requests_default WHERE received_at >= %L AND received_at < %L RETURNING *) '
                'INSERT INTO app.%I SELECT * FROM moved', lo, hi, part);
            EXECUTE format('ALTER TABLE app.requests ATTACH PARTITION app.%I FOR VALUES FROM (%L) TO (%L)', part, lo, hi);
            created := created + 1;
        END IF;
    END LOOP;
    RETURN created;
END $$;

-- Retention: whole months are dropped once they are entirely older than keep_days.
CREATE FUNCTION app.drop_old_request_partitions(keep_days integer DEFAULT 90)
RETURNS integer LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
DECLARE
    r       record;
    month   timestamp;
    dropped integer := 0;
BEGIN
    FOR r IN
        SELECT c.relname
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'app.requests'::regclass
          AND c.relname ~ '^requests_[0-9]{4}_[0-9]{2}$'
    LOOP
        month := to_timestamp(substring(r.relname FROM 10), 'YYYY_MM') AT TIME ZONE 'UTC';
        IF (month + interval '1 month') AT TIME ZONE 'UTC' < now() - make_interval(days => keep_days) THEN
            EXECUTE format('DROP TABLE app.%I', r.relname);
            dropped := dropped + 1;
        END IF;
    END LOOP;
    RETURN dropped;
END $$;

-- Daily: delete expired raw text and blank expired text inside corrections.
CREATE FUNCTION app.purge_expired_raw_text()
RETURNS TABLE (raw_rows_deleted bigint, correction_texts_cleared bigint)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
DECLARE a bigint; b bigint;
BEGIN
    DELETE FROM app.request_raw_text WHERE expires_at < now();
    GET DIAGNOSTICS a = ROW_COUNT;
    UPDATE app.corrections
       SET input_text = NULL, input_text_expires_at = NULL
     WHERE input_text IS NOT NULL AND input_text_expires_at < now();
    GET DIAGNOSTICS b = ROW_COUNT;
    RETURN QUERY SELECT a, b;
END $$;

-- Functions are executable by everyone by default; these three are privileged, so say who may run them.
REVOKE ALL ON FUNCTION app.ensure_request_partitions(integer),
                       app.drop_old_request_partitions(integer),
                       app.purge_expired_raw_text() FROM PUBLIC;