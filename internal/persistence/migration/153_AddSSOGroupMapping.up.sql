-- Maps an IdP group (Entra group object-id or name, Okta group, etc.) to a
-- Flomation Team (organisation_group). At SSO login the user's IdP groups are
-- reconciled against these mappings: they're added to mapped Teams they qualify
-- for and removed from mapped Teams they no longer do. Teams that appear in no
-- mapping are "manually managed" and left untouched. Many-to-many: one IdP group
-- may map to several Teams and vice-versa.
CREATE TABLE IF NOT EXISTS sso_group_mapping (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    organisation_id UUID NOT NULL REFERENCES organisation(id) ON DELETE CASCADE,
    idp_group VARCHAR NOT NULL,
    organisation_group_id UUID NOT NULL REFERENCES organisation_group(id) ON DELETE CASCADE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(organisation_id, idp_group, organisation_group_id)
);

CREATE INDEX IF NOT EXISTS idx_sso_group_mapping_org ON sso_group_mapping(organisation_id);
