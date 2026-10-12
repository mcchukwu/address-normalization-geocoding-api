-- extensions and schemas

-- One database, four schemas, so each kind of data gets its own owner and permissions:
--   ref  small reference dictionaries (precision classes, states, address terms)
--   gaz  the gazetteer: rebuilt from open data, versioned, read-only at runtime
--   app  tenants, request log, corrections: written by the API at runtime
--   eval evaluation sets and runs: used only by the geoeval harness
CREATE EXTENSION IF NOT EXISTS postgis;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE SCHEMA ref;
CREATE SCHEMA gaz;
CREATE SCHEMA app;
CREATE SCHEMA eval;
