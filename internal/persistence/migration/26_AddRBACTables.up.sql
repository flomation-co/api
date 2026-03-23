CREATE TABLE IF NOT EXISTS organisation_group (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    organisation_id UUID NOT NULL REFERENCES organisation(id) ON DELETE CASCADE,
    name VARCHAR NOT NULL,
    description VARCHAR,
    is_default BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_org_group_org ON organisation_group(organisation_id);

CREATE TABLE IF NOT EXISTS organisation_group_member (
    group_id UUID NOT NULL REFERENCES organisation_group(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    added_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (group_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_org_group_member_user ON organisation_group_member(user_id);

CREATE TABLE IF NOT EXISTS organisation_group_permission (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    group_id UUID NOT NULL REFERENCES organisation_group(id) ON DELETE CASCADE,
    permission VARCHAR NOT NULL,
    UNIQUE(group_id, permission)
);

CREATE INDEX IF NOT EXISTS idx_org_group_perm_group ON organisation_group_permission(group_id);
