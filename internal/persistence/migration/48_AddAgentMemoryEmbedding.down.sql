DROP INDEX IF EXISTS idx_agent_memory_embedding_hnsw;
ALTER TABLE agent_memory DROP COLUMN IF EXISTS embedding;