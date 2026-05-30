INSERT INTO trigger_type (name) VALUES ('twilio-sms') ON CONFLICT DO NOTHING;
INSERT INTO trigger_type (name) VALUES ('twilio-voice') ON CONFLICT DO NOTHING;