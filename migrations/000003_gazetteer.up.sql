-- the gazetteer
-- Rebuilt from open data by one reproducible command; every row keeps where it came from.
-- 'google' is deliberately NOT a source: Google coordinates may be cached for 30 days only,
-- so they can never become gazetteer rows. NDAPS is absent until its terms are read.
CREATE TYPE gaz.source AS ENUM ('osm', 'overture', 'geoboundaries', 'manual');
CREATE TYPE gaz.feature_kind AS ENUM ('state', 'lga', 'locality', 'road', 'place', 'address');

-- One row per gazetteer build. Every API response carries the label it was answered from.
CREATE TABLE gaz.data_versions (
    id                integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    label             text NOT NULL UNIQUE,
    built_at          timestamptz NOT NULL DEFAULT now(),
    osm_extract_date  date,
    overture_release  text,
    boundaries_source text,
    notes             text
);

-- Which build the API reads. A single row; switching versions is one UPDATE.
CREATE TABLE gaz.active_version (
    singleton       boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    data_version_id integer NOT NULL REFERENCES gaz.data_versions (id)
);

-- Every named thing we can resolve to: states, LGAs, localities, roads, places, addresses.
-- One table (instead of six) so candidate generation is one fuzzy search with one provenance model.
CREATE TABLE gaz.features (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    data_version_id integer NOT NULL REFERENCES gaz.data_versions (id) ON DELETE CASCADE,
    kind            gaz.feature_kind NOT NULL,
    category        text,                                  -- bank, school, residential, ...
    geom            geometry(Geometry, 4326) NOT NULL,
    -- A point guaranteed to lie on the feature; used when we answer at locality/LGA/state level.
    rep_point       geometry(Point, 4326) GENERATED ALWAYS AS (ST_PointOnSurface(geom)) STORED,
    parent_id       bigint REFERENCES gaz.features (id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
    state_id        bigint REFERENCES gaz.features (id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
    lga_id          bigint REFERENCES gaz.features (id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
    house_number    text,                                  -- addresses only
    street_norm     text,                                  -- addresses only
    -- provenance: where it came from, under what licence, and when it stops being usable
    source          gaz.source NOT NULL,
    source_id       text NOT NULL,                         -- e.g. 'way/123456' for OSM
    licence         text NOT NULL,
    fetched_at      timestamptz NOT NULL,
    expires_at      timestamptz,
    CONSTRAINT geom_matches_kind CHECK (
        CASE kind
            WHEN 'state'   THEN GeometryType(geom) IN ('POLYGON', 'MULTIPOLYGON')
            WHEN 'lga'     THEN GeometryType(geom) IN ('POLYGON', 'MULTIPOLYGON')
            WHEN 'road'    THEN GeometryType(geom) IN ('LINESTRING', 'MULTILINESTRING')
            WHEN 'address' THEN GeometryType(geom) = 'POINT'
            ELSE true
        END),
    CONSTRAINT address_has_number CHECK (kind <> 'address' OR house_number IS NOT NULL)
);
CREATE UNIQUE INDEX features_source_uq ON gaz.features (data_version_id, source, source_id, kind);
CREATE INDEX features_geom_gix  ON gaz.features USING gist (geom);
CREATE INDEX features_kind_state ON gaz.features (data_version_id, kind, state_id);
CREATE INDEX features_lga        ON gaz.features (lga_id);

-- Every name and alias a feature goes by. name_norm is produced by the Go normalizer
-- (the single implementation, so ingestion and request paths cannot drift apart).
CREATE TABLE gaz.names (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    feature_id bigint NOT NULL REFERENCES gaz.features (id) ON DELETE CASCADE,
    name       text NOT NULL,
    name_norm  text NOT NULL,
    is_primary boolean NOT NULL DEFAULT false,
    CHECK (name_norm <> '' AND name_norm = lower(btrim(name_norm)))
);
CREATE INDEX names_trgm_gin ON gaz.names USING gin (name_norm gin_trgm_ops);
CREATE INDEX names_feature  ON gaz.names (feature_id);
CREATE UNIQUE INDEX names_one_primary ON gaz.names (feature_id) WHERE is_primary;

-- What the API reads: only the active build.
CREATE VIEW gaz.current_features AS
SELECT f.*
FROM gaz.features f
JOIN gaz.active_version a ON a.data_version_id = f.data_version_id;

CREATE VIEW gaz.current_names AS
SELECT n.id, n.feature_id, n.name, n.name_norm, n.is_primary,
       f.kind, f.state_id, f.lga_id
FROM gaz.names n
JOIN gaz.current_features f ON f.id = n.feature_id;

-- Fuzzy name search used by candidate generation. Misspellings are the normal case, so
-- matching is by trigram similarity; the threshold is set per call.
CREATE FUNCTION gaz.search_names(
    q        text,
    want     gaz.feature_kind,
    in_state bigint DEFAULT NULL,
    min_sim  real DEFAULT 0.4,
    lim      integer DEFAULT 20
) RETURNS TABLE (feature_id bigint, name text, similarity real)
LANGUAGE plpgsql STABLE AS $$
BEGIN
    PERFORM set_config('pg_trgm.similarity_threshold', min_sim::text, true);
    RETURN QUERY
    SELECT n.feature_id, n.name, similarity(n.name_norm, q)
    FROM gaz.current_names n
    WHERE n.kind = want
      AND n.name_norm % q
      AND (in_state IS NULL OR n.state_id = in_state)
    ORDER BY similarity(n.name_norm, q) DESC, n.feature_id
    LIMIT lim;
END $$;
