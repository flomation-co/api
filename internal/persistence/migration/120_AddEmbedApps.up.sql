-- Embed apps: the control plane for the developer SDK. An embed app is a
-- publishable-key credential, scoped to an organisation (or a personal owner),
-- that lets a developer render Flomation forms / invoke flows / chat with agents
-- natively inside their own website or app.
--
-- The publishable key (pk_...) is safe to ship in client-side JavaScript — like
-- Stripe's pk_. Security does NOT rely on keeping it secret; it comes from three
-- layers: (1) the allowed-origins list below, (2) per-resource opt-in below, and
-- (3) the server re-validating every write (the existing form sanitisation
-- pipeline is the trust boundary). The optional secret key (sk_...) is for
-- server-side SDK use and only its hash is stored (added in a later phase).
CREATE TABLE embed_app (
    id               UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    organisation_id  UUID,                 -- NULL = personal (owner-scoped)
    owner_id         UUID NOT NULL,
    name             TEXT NOT NULL,
    publishable_key  TEXT NOT NULL UNIQUE,
    secret_key_hash  TEXT,                 -- optional; sha256 hex, later phase
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_embed_app_org ON embed_app(organisation_id);
CREATE INDEX idx_embed_app_owner ON embed_app(owner_id);

-- Allowed browser origins for an embed app. A cross-origin write (submit,
-- autosave, compute, payment) is accepted only when the request Origin matches
-- one of these exactly (scheme + host + optional port). This is what makes a
-- publishable key safe to expose: a stolen key is useless from an origin that
-- isn't listed here.
CREATE TABLE embed_allowed_origin (
    embed_app_id  UUID NOT NULL REFERENCES embed_app(id) ON DELETE CASCADE,
    origin        TEXT NOT NULL,
    PRIMARY KEY (embed_app_id, origin)
);

-- Per-resource opt-in. A form / flow / agent is embeddable only when it has been
-- explicitly published for an embed app — nothing is embeddable by default. This
-- is the safety gate that prevents a publishable key from reaching resources the
-- developer never meant to expose. resource_type is 'form' | 'flow' | 'agent';
-- resource_id is the trigger id (form), flow id, or agent id.
CREATE TABLE embed_resource (
    embed_app_id   UUID NOT NULL REFERENCES embed_app(id) ON DELETE CASCADE,
    resource_type  TEXT NOT NULL,
    resource_id    UUID NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (embed_app_id, resource_type, resource_id)
);
CREATE INDEX idx_embed_resource_lookup ON embed_resource(resource_type, resource_id);
