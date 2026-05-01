-- Cached entitlements pushed from the billing service for quota enforcement.
-- The billing service is the source of truth; this table is a local cache
-- updated via POST /api/v1/internal/entitlements/sync.

CREATE TABLE IF NOT EXISTS subscription_entitlement (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id            VARCHAR(100) NOT NULL,
    organisation_id     VARCHAR(100),
    plan_slug           VARCHAR(50) NOT NULL,
    entitlement_key     VARCHAR(100) NOT NULL,
    value_int           BIGINT,
    value_bool          BOOLEAN,
    value_json          JSONB,
    subscription_status VARCHAR(30) NOT NULL DEFAULT 'active',
    period_end          TIMESTAMPTZ,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(owner_id, organisation_id, entitlement_key)
);

CREATE INDEX IF NOT EXISTS idx_sub_ent_owner ON subscription_entitlement(owner_id);
CREATE INDEX IF NOT EXISTS idx_sub_ent_org ON subscription_entitlement(organisation_id);
