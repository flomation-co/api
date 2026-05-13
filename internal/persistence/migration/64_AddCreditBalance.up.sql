-- Credit balance cache: pushed from billing service, checked locally for quota.
-- The billing service is the source of truth; this table only stores the
-- balance for real-time quota enforcement without cross-service calls.

CREATE TABLE IF NOT EXISTS credit_balance (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id            VARCHAR(100) NOT NULL,
    organisation_id     VARCHAR(100),
    balance_pence       BIGINT NOT NULL DEFAULT 0,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(owner_id, organisation_id)
);

CREATE INDEX IF NOT EXISTS idx_credit_balance_owner ON credit_balance(owner_id);

-- Credit deductions: overage durations recorded locally, synced to billing
-- for cost calculation using the dynamic rate schedule.
CREATE TABLE IF NOT EXISTS credit_deduction (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id            VARCHAR(100) NOT NULL,
    organisation_id     VARCHAR(100),
    execution_id        UUID NOT NULL,
    duration_ms         BIGINT NOT NULL,
    amount_pence        BIGINT,
    synced              BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_credit_deduction_owner ON credit_deduction(owner_id);
CREATE INDEX IF NOT EXISTS idx_credit_deduction_synced ON credit_deduction(synced) WHERE NOT synced;
