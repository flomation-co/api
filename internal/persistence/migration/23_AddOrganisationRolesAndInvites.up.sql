ALTER TABLE organisation_user ADD COLUMN IF NOT EXISTS role VARCHAR NOT NULL DEFAULT 'member';

CREATE TABLE IF NOT EXISTS organisation_invite (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    organisation_id UUID NOT NULL REFERENCES organisation(id) ON DELETE CASCADE,
    email VARCHAR,
    invite_code VARCHAR NOT NULL DEFAULT substring(md5(random()::text || clock_timestamp()::text), 0, 20),
    role VARCHAR NOT NULL DEFAULT 'member',
    created_by UUID NOT NULL REFERENCES users(id),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    accepted_at TIMESTAMP,
    accepted_by UUID REFERENCES users(id),
    expires_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP + INTERVAL '7 days'
);

CREATE INDEX IF NOT EXISTS idx_organisation_invite_code ON organisation_invite(invite_code);
CREATE INDEX IF NOT EXISTS idx_organisation_invite_org ON organisation_invite(organisation_id);

-- Set existing org members as admin (they were the creators)
UPDATE organisation_user SET role = 'admin' WHERE role = 'member';
