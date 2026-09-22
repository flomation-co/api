-- Optional human-readable label for the mapped IdP group. idp_group stores the
-- value the token's groups claim carries (an Entra object-id GUID, an Okta/Google
-- group name/email), which isn't friendly to show — the label captures the
-- display name chosen from the group picker. Purely cosmetic; reconciliation
-- still matches on idp_group.
ALTER TABLE sso_group_mapping ADD COLUMN IF NOT EXISTS idp_group_label VARCHAR;
