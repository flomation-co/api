-- Remove aws_role credentials first (FK to credential_provider), then the
-- provider row itself.
DELETE FROM environment_credential WHERE provider_slug = 'aws_role';
DELETE FROM credential_provider WHERE slug = 'aws_role';
