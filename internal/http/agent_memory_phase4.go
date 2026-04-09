package http

// Phase 4 of the Agent Memory feature — HTTP handlers for semantic
// retrieval with pgvector embeddings.
//
// Route summary (registered in service.go):
//
//   POST   /internal/agent/:id/memory/search     semantic search by embedding
//   GET    /internal/memory/unembedded            list memories without embeddings
//   PATCH  /internal/memory/:id/embedding         update embedding on a memory

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	pgvector "github.com/pgvector/pgvector-go"
	log "github.com/sirupsen/logrus"
)

// --- Semantic Search ---

type searchMemoriesRequest struct {
	AgentUserID   string    `json:"agent_user_id" binding:"required"`
	Embedding     []float32 `json:"embedding" binding:"required"`
	TopK          int       `json:"top_k,omitempty"`
	ExcludePinned bool      `json:"exclude_pinned,omitempty"`
}

// searchAgentMemoriesInternal handles POST /api/v1/internal/agent/:id/memory/search.
func (s *Service) searchAgentMemoriesInternal(c *gin.Context) {
	agentID := c.Param("id")

	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var body searchMemoriesRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if len(body.Embedding) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "embedding must not be empty"})
		return
	}

	topK := body.TopK
	if topK <= 0 {
		topK = 10
	}

	vec := pgvector.NewVector(body.Embedding)
	results, err := s.persistence.SearchMemoriesByEmbedding(
		agentID, body.AgentUserID, vec, topK, body.ExcludePinned,
	)
	if err != nil {
		log.WithError(err).Error("failed to search memories by embedding")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	if results == nil {
		results = []*api.AgentMemory{}
	}

	c.JSON(http.StatusOK, results)
}

// --- Unembedded Memories ---

// getUnembeddedMemoriesInternal handles GET /api/v1/internal/memory/unembedded.
func (s *Service) getUnembeddedMemoriesInternal(c *gin.Context) {
	limit := 20
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	results, err := s.persistence.GetMemoriesWithoutEmbedding(limit)
	if err != nil {
		log.WithError(err).Error("failed to fetch unembedded memories")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	if results == nil {
		results = []*api.AgentMemory{}
	}

	c.JSON(http.StatusOK, results)
}

// --- Update Embedding ---

type updateEmbeddingRequest struct {
	Embedding []float32 `json:"embedding" binding:"required"`
}

// updateMemoryEmbeddingInternal handles PATCH /api/v1/internal/memory/:id/embedding.
func (s *Service) updateMemoryEmbeddingInternal(c *gin.Context) {
	memoryID := c.Param("id")

	var body updateEmbeddingRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if len(body.Embedding) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "embedding must not be empty"})
		return
	}

	vec := pgvector.NewVector(body.Embedding)
	if err := s.persistence.UpdateMemoryEmbedding(memoryID, vec); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		log.WithError(err).Error("failed to update memory embedding")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.Status(http.StatusNoContent)
}
