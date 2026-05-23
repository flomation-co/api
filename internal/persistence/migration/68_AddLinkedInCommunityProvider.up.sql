INSERT INTO credential_provider (slug, name, icon, auth_url, token_url, default_scopes) VALUES
('linkedin_community', 'LinkedIn Community', 'linkedin', 'https://www.linkedin.com/oauth/v2/authorization', 'https://www.linkedin.com/oauth/v2/accessToken', 'r_member_social w_member_social r_organization_social w_organization_social rw_organization_admin')
ON CONFLICT (slug) DO NOTHING;
