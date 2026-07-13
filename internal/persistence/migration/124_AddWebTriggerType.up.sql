-- Seed the trigger_type row for the Web Trigger. Without it, createFloRevision
-- resolves the type via (SELECT id FROM trigger_type WHERE name = :type_name) ->
-- NULL, which violates the NOT NULL constraint on trigger.type and the trigger
-- fails to register ("null value in column type ... violates not-null
-- constraint", type=web). Each new trigger integration seeds its own row.
--
-- Unlike webhook/poll triggers, the Web Trigger is invoked directly via the embed
-- edge (POST /v1/embed/flow/:id/invoke), so it needs no Launch activation — but
-- the trigger row is still written so the editor can surface its invoke URL.
INSERT INTO trigger_type (name) VALUES ('web') ON CONFLICT DO NOTHING;
