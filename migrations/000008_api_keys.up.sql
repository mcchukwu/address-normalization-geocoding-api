-- 000008: API keys owned by this service.
-- A key looks like gk_<8 hex>_<64 hex>. The first part is public (it identifies the key in logs and
-- in `keys list`); the secret is shown once at creation and only its SHA-256 is stored. A random
-- 256-bit secret needs no slow password hash: there is nothing to guess.
CREATE TABLE app.api_keys (
    id           uuid PRIMARY KEY,
    tenant_id    uuid NOT NULL REFERENCES app.tenants (tenant_id),
    key_id       text NOT NULL UNIQUE CHECK (key_id ~ '^[0-9a-f]{8}$'),
    secret_hash  bytea NOT NULL CHECK (octet_length(secret_hash) = 32),
    name         text NOT NULL CHECK (btrim(name) <> ''),
    scopes       text[] NOT NULL
                 CHECK (cardinality(scopes) > 0 AND scopes <@ ARRAY['geocode', 'normalize', 'corrections:write']),
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz,
    revoked_at   timestamptz,
    last_used_at timestamptz
);
CREATE INDEX api_keys_tenant ON app.api_keys (tenant_id);

-- Usage comes from the request log, so metering adds no write path to the request.
-- (The log keeps 90 days; export these rows before they age out if billing needs longer.)
CREATE VIEW app.usage_daily AS
SELECT tenant_id,
       (received_at AT TIME ZONE 'UTC')::date AS day,
       endpoint,
       traffic_class,
       count(*)                                  AS requests,
       count(*) FILTER (WHERE status_code >= 500) AS server_errors,
       count(*) FILTER (WHERE status_code = 429)  AS rate_limited
FROM app.requests
GROUP BY 1, 2, 3, 4;

-- The running API may read what it needs to check a key and stamp last use; nothing else, and it can never create keys.
GRANT SELECT (id, tenant_id, key_id, secret_hash, scopes, expires_at, revoked_at) ON app.api_keys TO geocode_api;
GRANT UPDATE (last_used_at) ON app.api_keys TO geocode_api;
-- It also needs the tenant's name for the principal it returns (tenants is already readable).
GRANT SELECT ON app.usage_daily TO geocode_maint;
