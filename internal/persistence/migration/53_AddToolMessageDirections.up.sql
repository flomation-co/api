-- Allow tool_use and tool_result as agent_message direction values.
-- These record intermediate tool exchanges within a single AI turn so
-- the conversation history replayed on the next turn includes what
-- tools the agent called and what results came back.

ALTER TABLE agent_message
    DROP CONSTRAINT IF EXISTS agent_message_direction_check;

ALTER TABLE agent_message
    ADD CONSTRAINT agent_message_direction_check
    CHECK (direction IN ('inbound', 'outbound', 'system', 'tool_use', 'tool_result'));