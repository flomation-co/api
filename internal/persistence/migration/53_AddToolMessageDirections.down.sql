ALTER TABLE agent_message
    DROP CONSTRAINT IF EXISTS agent_message_direction_check;

ALTER TABLE agent_message
    ADD CONSTRAINT agent_message_direction_check
    CHECK (direction IN ('inbound', 'outbound', 'system'));