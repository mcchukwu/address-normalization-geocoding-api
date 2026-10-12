REVOKE ALL ON ALL TABLES IN SCHEMA ref, gaz, app, eval FROM geocode_api, geocode_etl, geocode_maint, geocode_eval;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA gaz, eval FROM geocode_etl, geocode_eval;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA gaz, app FROM geocode_api, geocode_maint;
REVOKE ALL ON SCHEMA ref, gaz, app, eval FROM geocode_api, geocode_etl, geocode_maint, geocode_eval;
-- Roles are cluster-wide and may be shared, so they are left in place.
