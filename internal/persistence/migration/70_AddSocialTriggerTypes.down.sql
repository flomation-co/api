DELETE FROM trigger_type WHERE name IN ('facebook-messenger', 'facebook-feed', 'linkedin-poll');

UPDATE credential_provider
SET default_scopes = 'pages_manage_posts pages_read_engagement'
WHERE slug = 'facebook';
