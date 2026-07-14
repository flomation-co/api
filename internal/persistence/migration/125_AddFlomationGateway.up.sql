-- Flomation Gateway: developer-defined HTTP APIs that route to flows ("Flows as
-- an API"). A gateway_api is a named container, scoped to an organisation (or a
-- personal owner), addressed publicly by a SHORT url-safe id (api_id) — never the
-- org/owner UUID. Each gateway_endpoint maps one HTTP method + path pattern (e.g.
-- "/users/:id") to a flow's Web Trigger; the path params feed the trigger's
-- field-source map. Auth is a pluggable policy on the API (open / api_key / basic
-- / oidc / flomation); secrets are stored as salted SHA-256 hashes, never
-- plaintext (mirrors embed_app.secret_key_hash), and Launch verifies at the edge.
CREATE TABLE gateway_api (
    id               UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    api_id           TEXT NOT NULL UNIQUE,   -- short, url-safe; the /gw/<api_id> token
    organisation_id  UUID,                   -- NULL = personal (owner-scoped)
    owner_id         UUID NOT NULL,
    name             TEXT NOT NULL,
    -- Auth policy. auth_type: 'open' | 'api_key' | 'basic' | 'oidc' | 'flomation'.
    -- auth_config holds the NON-secret settings for the chosen type (header name,
    -- basic realm, oidc issuer/jwks_uri/audience/required_claims, flomation
    -- required_permission/required_role). Secret material (api_key value, basic
    -- password) lives ONLY as a salted hash below.
    auth_type        TEXT NOT NULL DEFAULT 'open',
    auth_config      JSONB NOT NULL DEFAULT '{}'::jsonb,
    auth_secret_hash TEXT,                    -- salted SHA-256 hex (api_key/basic)
    auth_secret_salt TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_gateway_api_org ON gateway_api(organisation_id);
CREATE INDEX idx_gateway_api_owner ON gateway_api(owner_id);

-- One endpoint = one HTTP method + path pattern → a flow's Web Trigger. REST
-- style: "GET /users/:id" and "POST /users" are separate endpoints. path_pattern
-- segments starting ':' are params, extracted and passed to the flow. Static
-- segments beat param segments at match time (resolved in Launch).
CREATE TABLE gateway_endpoint (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    gateway_api_id  UUID NOT NULL REFERENCES gateway_api(id) ON DELETE CASCADE,
    method          TEXT NOT NULL,           -- GET|POST|PUT|PATCH|DELETE...
    path_pattern    TEXT NOT NULL,           -- e.g. "/users/:id"
    flow_id         UUID NOT NULL,
    trigger_id      UUID NOT NULL,           -- the flow's Web Trigger record
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (gateway_api_id, method, path_pattern)
);
CREATE INDEX idx_gateway_endpoint_api ON gateway_endpoint(gateway_api_id);
CREATE INDEX idx_gateway_endpoint_flow ON gateway_endpoint(flow_id);
