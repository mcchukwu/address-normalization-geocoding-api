-- what the running API additionally needs.
-- Corrections must reference a recent request made by the same tenant, so the API may read
-- those few columns of the request log, and only those: not latency, hashes or warnings.
GRANT SELECT (id, received_at, tenant_id, precision, confidence) ON app.requests TO geocode_api;
