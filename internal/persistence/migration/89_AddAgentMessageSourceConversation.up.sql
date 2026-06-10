-- Records the conversation that *initiated* a relay when the message
-- was sent via an AI tool call to a different recipient than the
-- conversation that triggered the orchestrator.
--
-- Use case: Andy on Telegram tells the agent "tell Bob about the
-- meeting on Slack". The agent's send-slack-message tool call lands in
-- Bob's Slack conversation as an outbound message; this column points
-- back at Andy's Telegram conversation, so a future ops query can
-- answer "why did the agent message Bob?" with one join.
--
-- Nullable: most outbound messages originate from the same conversation
-- they're delivered to (a regular reply). The column only populates
-- when the AI tool loop records a cross-conversation relay via
-- POST /api/v1/internal/agent/:id/record-outbound.
--
-- No backfill: existing rows are pre-relay-feature, all sit at
-- source_conversation_id = NULL.

ALTER TABLE agent_message
    ADD COLUMN source_conversation_id UUID
        REFERENCES agent_conversation(id) ON DELETE SET NULL;

-- Audit index: small partial index so "find every relay originating
-- from conversation X" is a single index scan rather than a sequential
-- table scan. Restricted to non-NULL rows so the index stays tiny.
CREATE INDEX IF NOT EXISTS idx_agent_message_source_conversation
    ON agent_message(source_conversation_id)
    WHERE source_conversation_id IS NOT NULL;
