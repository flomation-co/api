-- Add Google Forms scopes to the Google credential provider so a Google
-- connection can create/edit forms (forms.body) and read responses
-- (forms.responses.readonly). default_scopes drives the OAuth authorize URL
-- (see credential.go), so new connections request these; existing Google
-- credentials must be reconnected to gain the new scopes.
UPDATE credential_provider
SET default_scopes = 'openid email profile https://www.googleapis.com/auth/forms.body https://www.googleapis.com/auth/forms.responses.readonly'
WHERE slug = 'google';
