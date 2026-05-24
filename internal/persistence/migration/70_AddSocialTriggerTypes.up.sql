-- Social media trigger types for Facebook and LinkedIn.

INSERT INTO trigger_type (name) VALUES
    ('facebook-messenger'),
    ('facebook-feed'),
    ('linkedin-poll')
ON CONFLICT DO NOTHING;

-- Add pages_messaging to Facebook provider default scopes for Messenger triggers.
UPDATE credential_provider
SET default_scopes = 'pages_manage_posts pages_read_engagement pages_messaging'
WHERE slug = 'facebook';
