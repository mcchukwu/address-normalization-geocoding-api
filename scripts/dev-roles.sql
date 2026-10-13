-- Development only: a login that inherits the least-privilege API role. In production, create the
-- login with a real secret outside the migrations.
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'geocode_app') THEN
    CREATE ROLE geocode_app LOGIN PASSWORD 'geocode_app' IN ROLE geocode_api;
  END IF;
END $$;
