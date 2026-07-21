-- Seed the aws_role credential provider. Unlike the OAuth providers there is no
-- token exchange: an aws_role credential stores a customer-supplied IAM Role ARN
-- plus a Flomation-generated External ID in its metadata JSONB, and the executor
-- uses them to STS AssumeRole at run time (base identity = Flomation's own,
-- resolved from the runner host's ambient AWS credentials). auth_url/token_url
-- are NOT NULL on the provider table, so we store empty strings; they are never
-- used for this provider.
--
-- NUMBERING: 132. origin/main tops out at 131 (database-row trigger) when this is
-- written. golang-migrate silently skips an out-of-order version and a duplicate
-- makes the api fail to boot, so re-check this number after rebasing.
INSERT INTO credential_provider (slug, name, icon, auth_url, token_url, revoke_url, default_scopes)
VALUES ('aws_role', 'AWS Role (STS)', 'cloud', '', '', NULL, NULL)
ON CONFLICT (slug) DO NOTHING;
