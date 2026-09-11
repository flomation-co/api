DROP INDEX IF EXISTS idx_agent_message_embedding_hnsw;
ALTER TABLE agent_message DROP COLUMN IF EXISTS embedding;
