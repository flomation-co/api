UPDATE credential_provider
SET default_scopes = 'openid email profile'
WHERE slug = 'google';
