-- Phase 4: Semantic retrieval with pgvector embeddings.
-- Adds a vector(1024) column to agent_memory for AWS Bedrock Titan
-- Embeddings v2 (1024-dimensional cosine similarity search).

CREATE EXTENSION IF NOT EXISTS vector;

ALTER TABLE agent_memory ADD COLUMN embedding vector(1024);

-- HNSW index for cosine similarity. Preferred over IVFFlat because it
-- handles incremental inserts without periodic re-training — the
-- agent_memory table sees many small writes per conversation turn.
CREATE INDEX idx_agent_memory_embedding_hnsw
    ON agent_memory USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);
