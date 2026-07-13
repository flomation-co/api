-- Web threads: generic, opt-in conversation history for the Web Trigger invoke.
-- The SDK's client-held chat is layered over this server-side thread — the flow
-- reads the trusted running history as ${history}. A thread is scoped to a flow
-- and, when the invoker is a logged-in user, bound to that user_id so the history
-- is durable and reconcilable with their other identities. Anonymous invokers get
-- an unguessable thread keyed only by its (UUID) id. This is deliberately generic
-- and decoupled from the agent conversation tables (reconciliation is later).
CREATE TABLE web_thread (
    id         UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    flow_id    UUID NOT NULL,
    user_id    UUID,               -- NULL = anonymous
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_web_thread_flow ON web_thread(flow_id);
CREATE INDEX idx_web_thread_user ON web_thread(user_id);

CREATE TABLE web_thread_turn (
    id         UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    thread_id  UUID NOT NULL REFERENCES web_thread(id) ON DELETE CASCADE,
    role       TEXT NOT NULL,      -- "user" | "assistant"
    content    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_web_thread_turn_thread ON web_thread_turn(thread_id, created_at);
