DROP FUNCTION IF EXISTS gaz.search_names(text, gaz.feature_kind, bigint, real, integer);
DROP VIEW  IF EXISTS gaz.current_names;
DROP VIEW  IF EXISTS gaz.current_features;
DROP TABLE IF EXISTS gaz.names;
DROP TABLE IF EXISTS gaz.features;
DROP TABLE IF EXISTS gaz.active_version;
DROP TABLE IF EXISTS gaz.data_versions;
DROP TYPE  IF EXISTS gaz.feature_kind;
DROP TYPE  IF EXISTS gaz.source;
