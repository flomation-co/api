-- Phase 2d-α of the Agent Memory feature.
--
-- Adds agent.extraction_flow_id: the ID of the flow that Launch (and
-- the executor's assistant-reply recording hook) will dispatch after
-- every turn to extract memories, pending actions, and commitments
-- from the conversation.
--
-- The column is nullable. Existing agents default to NULL, meaning the
-- extract endpoint (POST /internal/agent/:id/extract) will return a
-- 204 no-op when called — this lets Launch start calling the endpoint
-- unconditionally the moment Phase 2d-α ships, without waiting for
-- Phase 2d-γ (which seeds the canonical extraction flow and backfills
-- the column for every existing agent).
--
-- See plans/agent_memory.md §"The extraction pipeline" and the Phase 2
-- roadmap entry for the full design.

ALTER TABLE agent
    ADD COLUMN IF NOT EXISTS extraction_flow_id UUID REFERENCES flo(id) ON DELETE SET NULL;

-- Index supports reverse lookups ("which agents use this extraction
-- flow?") that the Phase 6 admin UI and the Phase 2d-γ seed migration
-- will both want. Partial index since the vast majority of rows are
-- NULL during the Phase 2d-α/β window before the seed lands.
CREATE INDEX IF NOT EXISTS idx_agent_extraction_flow
    ON agent(extraction_flow_id)
    WHERE extraction_flow_id IS NOT NULL;