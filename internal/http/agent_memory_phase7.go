package http

// Phase 7 of the Agent Memory feature — memory hygiene HTTP handlers.
//
// Route summary (registered in service.go under the internal group):
//
//   POST   /internal/agent/:id/memory/check-hygiene    find contradictions + duplicates
//   POST   /internal/agent/:id/memory/supersede         mark old memory as superseded
//   POST   /internal/agent/:id/memory/merge             mark duplicate as merged
//   GET    /internal/agent/:id/memory/pinned-count      count pinned memories for a user
//   POST   /internal/agent/:id/memory/enforce-pin-limit enforce max pinned limit

import (
	"encoding/json"
	"net/http"

	api "flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	pgvector "github.com/pgvector/pgvector-go"
	log "github.com/sirupsen/logrus"
)

// --- Hygiene Check ---

type checkHygieneRequest struct {
	AgentUserID string    `json:"agent_user_id" binding:"required"`
	MemoryType  string    `json:"memory_type" binding:"required"`
	MemoryID    string    `json:"memory_id"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	Embedding   []float32 `json:"embedding"`
}

type checkHygieneResponse struct {
	Contradictions []*api.AgentMemory `json:"contradictions"`
	Duplicates     []*api.AgentMemory `json:"duplicates"`
}

// checkHygieneInternal finds potential contradictions and duplicates for
// a newly written memory. Returns candidates — the caller (extraction
// pipeline) decides whether to supersede/merge.
func (s *Service) checkHygieneInternal(c *gin.Context) {
	var body checkHygieneRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Find contradiction candidates via two methods:
	// 1. Embedding similarity > 0.75 (semantic contradictions)
	// 2. Same title + same type but different body (direct contradictions
	//    like "Location: Lives in London" vs "Location: Lives in Chester"
	//    which may have low embedding similarity)
	var contradictions []*api.AgentMemory
	var err error
	if len(body.Embedding) > 0 {
		embedding := pgvector.NewVector(body.Embedding)
		contradictions, err = s.persistence.FindContradictionCandidates(
			body.AgentUserID, body.MemoryType, embedding, 0.75, 3)
		if err != nil {
			log.WithError(err).Error("failed to find contradiction candidates")
			contradictions = []*api.AgentMemory{}
		}
	}

	// Title-based contradiction: same title, same type, different body.
	if body.Title != "" {
		existing, _ := s.persistence.GetAgentMemoriesForUser(body.AgentUserID, false, 100)
		for _, e := range existing {
			if e.Status == "active" && e.MemoryType == body.MemoryType &&
				e.Title == body.Title && e.Body != body.Body {
				// Check it's not already in the embedding-based list.
				found := false
				for _, c := range contradictions {
					if c.ID == e.ID {
						found = true
						break
					}
				}
				if !found {
					contradictions = append(contradictions, e)
				}
			}
		}
	}

	// Find near-duplicates: same type, same user, cosine > 0.95
	var duplicates []*api.AgentMemory
	if len(body.Embedding) > 0 {
		excludeID := body.MemoryID
		if excludeID == "" {
			excludeID = "00000000-0000-0000-0000-000000000000"
		}
		embedding := pgvector.NewVector(body.Embedding)
		duplicates, err = s.persistence.FindNearDuplicates(
			body.AgentUserID, body.MemoryType, embedding, 0.95, excludeID, 3)
		if err != nil {
			log.WithError(err).Error("failed to find near-duplicates")
			duplicates = []*api.AgentMemory{}
		}
	}

	// Filter out the new memory itself from contradictions
	if body.MemoryID != "" {
		filtered := make([]*api.AgentMemory, 0, len(contradictions))
		for _, m := range contradictions {
			if m.ID != body.MemoryID {
				filtered = append(filtered, m)
			}
		}
		contradictions = filtered
	}

	c.JSON(http.StatusOK, checkHygieneResponse{
		Contradictions: contradictions,
		Duplicates:     duplicates,
	})
}

// --- Supersession ---

type supersedeRequest struct {
	OldID string `json:"old_id" binding:"required"`
	NewID string `json:"new_id" binding:"required"`
}

func (s *Service) supersedeMemoryInternal(c *gin.Context) {
	agentID := c.Param("id")

	var body supersedeRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.persistence.SupersedeMemory(body.OldID, body.NewID); err != nil {
		log.WithFields(log.Fields{
			"old_id": body.OldID,
			"new_id": body.NewID,
			"error":  err,
		}).Error("failed to supersede memory")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	// Audit log
	detail, _ := json.Marshal(map[string]string{
		"old_id": body.OldID,
		"new_id": body.NewID,
	})
	_, _ = s.persistence.CreateAuditLogEntry(api.AgentAuditLog{
		AgentID:      agentID,
		ActorType:    "system",
		EventType:    "memory_superseded",
		ResourceType: "memory",
		ResourceID:   &body.OldID,
		Detail:       detail,
	})

	c.Status(http.StatusNoContent)
}

// --- Merge ---

type mergeRequest struct {
	DuplicateID string `json:"duplicate_id" binding:"required"`
	CanonicalID string `json:"canonical_id" binding:"required"`
}

func (s *Service) mergeMemoryInternal(c *gin.Context) {
	agentID := c.Param("id")

	var body mergeRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.persistence.MergeMemory(body.DuplicateID, body.CanonicalID); err != nil {
		log.WithError(err).Error("failed to merge memory")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	detail, _ := json.Marshal(map[string]string{
		"duplicate_id": body.DuplicateID,
		"canonical_id": body.CanonicalID,
	})
	_, _ = s.persistence.CreateAuditLogEntry(api.AgentAuditLog{
		AgentID:      agentID,
		ActorType:    "system",
		EventType:    "memory_deduplicated",
		ResourceType: "memory",
		ResourceID:   &body.DuplicateID,
		Detail:       detail,
	})

	c.Status(http.StatusNoContent)
}

// --- Pin Governance ---

func (s *Service) pinnedCountInternal(c *gin.Context) {
	agentUserID := c.Query("agent_user_id")
	if agentUserID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "agent_user_id required"})
		return
	}

	count, err := s.persistence.CountPinnedMemories(agentUserID)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusOK, gin.H{"count": count})
}

func (s *Service) enforcePinLimitInternal(c *gin.Context) {
	agentID := c.Param("id")

	var body struct {
		AgentUserID string `json:"agent_user_id" binding:"required"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	maxPinned, err := s.persistence.GetMaxPinnedMemories(agentID)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	count, err := s.persistence.CountPinnedMemories(body.AgentUserID)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	if count <= maxPinned {
		c.JSON(http.StatusOK, gin.H{"unpinned": 0, "count": count, "limit": maxPinned})
		return
	}

	excess := count - maxPinned
	unpinnedIDs, err := s.persistence.UnpinOldestMemories(body.AgentUserID, excess)
	if err != nil {
		log.WithError(err).Error("failed to enforce pin limit")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	// Audit
	for _, uid := range unpinnedIDs {
		detail, _ := json.Marshal(map[string]string{"reason": "pin_limit_exceeded"})
		id := uid
		_, _ = s.persistence.CreateAuditLogEntry(api.AgentAuditLog{
			AgentID:      agentID,
			AgentUserID:  &body.AgentUserID,
			ActorType:    "system",
			EventType:    "memory_unpinned",
			ResourceType: "memory",
			ResourceID:   &id,
			Detail:       detail,
		})
	}

	c.JSON(http.StatusOK, gin.H{"unpinned": len(unpinnedIDs), "count": count - excess, "limit": maxPinned})
}

// --- Admin: max pinned config ---

func (s *Service) updateMaxPinnedMemories(c *gin.Context) {
	agentID := c.Param("id")

	var body struct {
		MaxPinnedMemories *int `json:"max_pinned_memories"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.persistence.UpdateMaxPinnedMemories(agentID, body.MaxPinnedMemories); err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.Status(http.StatusNoContent)
}
