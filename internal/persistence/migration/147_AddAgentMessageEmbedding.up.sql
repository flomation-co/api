-- Phase 2 of agent conversation-history search: semantic retrieval.
-- Mirrors migration 48 (agent_memory embeddings): a vector(1024) column for
-- AWS Bedrock Titan Embeddings v2 + an HNSW cosine index. HNSW handles the
-- many small incremental writes (one per message) without periodic re-training.
CREATE EXTENSION IF NOT EXISTS vector;

ALTER TABLE agent_message ADD COLUMN embedding vector(1024);

CREATE INDEX idx_agent_message_embedding_hnsw
    ON agent_message USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);
