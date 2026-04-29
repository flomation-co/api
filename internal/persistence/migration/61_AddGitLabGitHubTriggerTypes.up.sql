INSERT INTO trigger_type (name) VALUES ('gitlab-webhook') ON CONFLICT DO NOTHING;
INSERT INTO trigger_type (name) VALUES ('github-webhook') ON CONFLICT DO NOTHING;
