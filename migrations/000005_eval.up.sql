-- evaluation sets, runs and the metrics the requirements are judged by
CREATE TYPE eval.tier AS ENUM ('A', 'B', 'C');
CREATE TYPE eval.address_style AS ENUM ('structured', 'landmark', 'mixed', 'poi_name', 'postcode');

CREATE TABLE eval.sets (
    id          integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name        text NOT NULL UNIQUE,
    tier        eval.tier NOT NULL,        -- A field-labelled, B web-sourced (no truth), C proxy datasets
    description text,
    source      text NOT NULL,
    licence     text,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE eval.cases (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    set_id             integer NOT NULL REFERENCES eval.sets (id) ON DELETE CASCADE,
    raw_text           text NOT NULL,
    city               text,
    state_id           smallint REFERENCES ref.states (id),
    style              eval.address_style,
    expected           geometry(Point, 4326),          -- NULL for tier B: web pages give text, not truth
    expected_precision ref.precision,                  -- what the truth really supports
    truth_method       text,                           -- door_step_gps, facility_registry, ndaps_building, ...
    collected_at       date,
    source_url         text,
    notes              text,
    -- truth and its precision come together or not at all
    CONSTRAINT truth_pair CHECK ((expected IS NULL) = (expected_precision IS NULL)),
    -- never accept ground truth copied from Google
    CONSTRAINT truth_not_google CHECK (truth_method IS NULL OR truth_method NOT ILIKE '%google%'),
    CONSTRAINT expected_in_nigeria CHECK (
        expected IS NULL OR (ST_X(expected) BETWEEN 2.0 AND 15.0 AND ST_Y(expected) BETWEEN 4.0 AND 14.5))
);
CREATE INDEX cases_set ON eval.cases (set_id);

CREATE TABLE eval.runs (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    set_id          integer NOT NULL REFERENCES eval.sets (id),
    git_sha         text NOT NULL,
    data_version_id integer REFERENCES gaz.data_versions (id),
    config          jsonb NOT NULL DEFAULT '{}',        -- enabled sources, thresholds, weights
    started_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz
);

CREATE TABLE eval.results (
    run_id     bigint NOT NULL REFERENCES eval.runs (id) ON DELETE CASCADE,
    case_id    bigint NOT NULL REFERENCES eval.cases (id) ON DELETE CASCADE,
    precision  ref.precision NOT NULL,
    confidence real CHECK (confidence BETWEEN 0 AND 1),
    result     geometry(Point, 4326),
    sources    text[] NOT NULL DEFAULT '{}',
    warnings   text[] NOT NULL DEFAULT '{}',
    latency_ms integer CHECK (latency_ms >= 0),
    PRIMARY KEY (run_id, case_id),
    CONSTRAINT none_has_no_point CHECK ((precision = 'none') = (result IS NULL))
);

-- Error is always recomputed from the stored points, so a stored number can never go stale.
CREATE VIEW eval.scored_results AS
SELECT r.run_id, r.case_id, r.precision, r.confidence, r.latency_ms,
       c.set_id, c.style, c.expected_precision,
       ST_Distance(r.result::geography, c.expected::geography) AS error_m,
       CASE WHEN p.radius_m IS NOT NULL AND r.result IS NOT NULL AND c.expected IS NOT NULL
            THEN ST_Distance(r.result::geography, c.expected::geography) <= p.radius_m
       END AS within_radius
FROM eval.results r
JOIN eval.cases c ON c.id = r.case_id
JOIN ref.precision_classes p ON p.precision = r.precision;

-- The numbers the requirements are judged by. "Confident" means confidence >= 0.8.
-- false_confidence_rate is the share of confident answers with a measured error over 500 m.
CREATE VIEW eval.run_metrics AS
SELECT run_id,
       count(*)                                                        AS n,
       count(*) FILTER (WHERE precision <> 'none')                     AS resolved,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY error_m)
           FILTER (WHERE error_m IS NOT NULL)                          AS median_error_m,
       avg((error_m <= 50)::int)   FILTER (WHERE error_m IS NOT NULL)  AS within_50m,
       avg((error_m <= 100)::int)  FILTER (WHERE error_m IS NOT NULL)  AS within_100m,
       avg((error_m <= 500)::int)  FILTER (WHERE error_m IS NOT NULL)  AS within_500m,
       avg((error_m <= 1000)::int) FILTER (WHERE error_m IS NOT NULL)  AS within_1km,
       count(*) FILTER (WHERE confidence >= 0.8 AND error_m IS NOT NULL) AS confident_n,
       avg(within_radius::int)
           FILTER (WHERE confidence >= 0.8 AND within_radius IS NOT NULL) AS calibration_at_0_8,
       avg((error_m > 500)::int)
           FILTER (WHERE confidence >= 0.8 AND error_m IS NOT NULL)    AS false_confidence_rate
FROM eval.scored_results
GROUP BY run_id;
