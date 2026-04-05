-- Reverse of 42_AddAgentMemoryPhase2.up.sql.
--
-- Drops in reverse dependency order. All three tables have FKs into the
-- Phase 1 tables (agent_user, agent_conversation, agent_message, agent)
-- but no FKs amongst themselves, so any order among the three is safe.
-- Indexes are dropped implicitly with their tables.

DROP TABLE IF EXISTS agent_commitment;
DROP TABLE IF EXISTS agent_pending_action;
DROP TABLE IF EXISTS agent_memory;