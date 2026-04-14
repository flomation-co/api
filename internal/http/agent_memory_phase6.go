package http

// Phase 6 of the Agent Memory feature — user-facing HTTP handlers for
// memory management, identity management, audit log, and data export.
//
// All endpoints are JWT-authenticated. The authenticated user is mapped
// to their agent_user record via email address lookup against
// agent_identity. If no match is found, the user has never interacted
// with the agent and gets an empty result (not an error).
//
// Route summary (registered in service.go under the agents group):
//
//   GET    /agent/:id/my-memories                 list my memories
//   PATCH  /agent/:id/my-memories/:memoryId       edit a memory
//   DELETE /agent/:id/my-memories/:memoryId        delete a memory
//   POST   /agent/:id/my-memories/forget-all       bulk delete all my memories
//   POST   /agent/:id/my-memories/export           export all my data as JSON
//   GET    /agent/:id/my-identities                list my linked identities
//   DELETE /agent/:id/my-identities/:identityId    unlink an identity
//   GET    /agent/:id/my-audit-log                 my audit events
//   GET    /agent/:id/audit-log                    full agent audit log (admin)
//   GET    /agent/:id/users                        list agent users (admin)
//   PATCH  /agent/:id/retention                    set retention policy (admin)

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	api "flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// resolveAgentUser maps the JWT-authenticated user to their agent_user
// record for a specific agent. Returns nil if the user has never
// interacted with the agent.
func (s *Service) resolveAgentUser(c *gin.Context, agentID string) *api.AgentUser {
	accountID, exists := c.Get("account_id")
	if !exists {
		return nil
	}

	user, err := s.persistence.GetUserByID(accountID.(string))
	if err != nil || user == nil || user.EmailAddress == nil {
		return nil
	}

	agentUser, err := s.persistence.GetAgentUserByEmail(agentID, *user.EmailAddress)
	if err != nil || agentUser == nil {
		return nil
	}
	return agentUser
}

// writeAuditLog is a helper to log an action to the audit trail.
func (s *Service) writeAuditLog(agentID string, agentUserID *string, actorID string, eventType, resourceType string, resourceID *string, detail map[string]interface{}) {
	detailJSON, _ := json.Marshal(detail)
	_, err := s.persistence.CreateAuditLogEntry(api.AgentAuditLog{
		AgentID:      agentID,
		AgentUserID:  agentUserID,
		ActorType:    "user",
		ActorID:      &actorID,
		EventType:    eventType,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Detail:       detailJSON,
	})
	if err != nil {
		log.WithError(err).Warn("failed to write audit log entry")
	}
}

// --- User-facing memory endpoints ---

func (s *Service) getMyAgentMemories(c *gin.Context) {
	agentID := c.Param("id")
	agentUser := s.resolveAgentUser(c, agentID)
	if agentUser == nil {
		c.JSON(http.StatusOK, []*api.AgentMemory{})
		return
	}

	pinnedOnly := c.Query("pinned") == "true"
	limit := 500
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 && l < 500 {
		limit = l
	}

	memories, err := s.persistence.GetAgentMemoriesForUser(agentUser.ID, pinnedOnly, limit)
	if err != nil {
		log.WithError(err).Error("failed to list user memories")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if memories == nil {
		memories = []*api.AgentMemory{}
	}
	c.JSON(http.StatusOK, memories)
}

func (s *Service) updateMyAgentMemory(c *gin.Context) {
	agentID := c.Param("id")
	memoryID := c.Param("memoryId")

	agentUser := s.resolveAgentUser(c, agentID)
	if agentUser == nil {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	// Verify the memory belongs to this user.
	mem, err := s.persistence.GetAgentMemoryByID(memoryID)
	if err != nil || mem == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if mem.AgentUserID == nil || *mem.AgentUserID != agentUser.ID {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	var body struct {
		Title  *string `json:"title"`
		Body   *string `json:"body"`
		Pinned *bool   `json:"pinned"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	title := mem.Title
	bodyText := mem.Body
	pinned := mem.Pinned
	if body.Title != nil {
		title = *body.Title
	}
	if body.Body != nil {
		bodyText = *body.Body
	}
	if body.Pinned != nil {
		pinned = *body.Pinned
	}

	if err := s.persistence.UpdateAgentMemory(memoryID, title, bodyText, pinned); err != nil {
		log.WithError(err).Error("failed to update memory")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	accountID, _ := c.Get("account_id")
	s.writeAuditLog(agentID, &agentUser.ID, accountID.(string),
		"memory_updated", "memory", &memoryID,
		map[string]interface{}{"title": title, "pinned": pinned})

	c.Status(http.StatusNoContent)
}

func (s *Service) deleteMyAgentMemory(c *gin.Context) {
	agentID := c.Param("id")
	memoryID := c.Param("memoryId")

	agentUser := s.resolveAgentUser(c, agentID)
	if agentUser == nil {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	mem, err := s.persistence.GetAgentMemoryByID(memoryID)
	if err != nil || mem == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if mem.AgentUserID == nil || *mem.AgentUserID != agentUser.ID {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	if err := s.persistence.DeleteAgentMemory(memoryID); err != nil {
		log.WithError(err).Error("failed to delete memory")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	accountID, _ := c.Get("account_id")
	s.writeAuditLog(agentID, &agentUser.ID, accountID.(string),
		"memory_deleted", "memory", &memoryID,
		map[string]interface{}{"title": mem.Title})

	c.Status(http.StatusNoContent)
}

func (s *Service) forgetAllMyAgentMemories(c *gin.Context) {
	agentID := c.Param("id")

	agentUser := s.resolveAgentUser(c, agentID)
	if agentUser == nil {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	count, err := s.persistence.DeleteAllMemoriesForUser(agentUser.ID)
	if err != nil {
		log.WithError(err).Error("failed to bulk delete memories")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	accountID, _ := c.Get("account_id")
	s.writeAuditLog(agentID, &agentUser.ID, accountID.(string),
		"bulk_forget", "memory", nil,
		map[string]interface{}{"count": count})

	c.JSON(http.StatusOK, gin.H{"deleted": count})
}

func (s *Service) exportMyAgentData(c *gin.Context) {
	agentID := c.Param("id")

	agentUser := s.resolveAgentUser(c, agentID)
	if agentUser == nil {
		c.JSON(http.StatusOK, api.AgentDataExport{ExportedAt: time.Now()})
		return
	}

	export, err := s.persistence.GetAllDataForUser(agentUser.ID)
	if err != nil {
		log.WithError(err).Error("failed to export user data")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	accountID, _ := c.Get("account_id")
	s.writeAuditLog(agentID, &agentUser.ID, accountID.(string),
		"data_export", "user", &agentUser.ID, nil)

	c.JSON(http.StatusOK, export)
}

// --- User-facing identity endpoints ---

func (s *Service) getMyAgentIdentities(c *gin.Context) {
	agentID := c.Param("id")
	agentUser := s.resolveAgentUser(c, agentID)
	if agentUser == nil {
		c.JSON(http.StatusOK, []*api.AgentIdentity{})
		return
	}

	identities, err := s.persistence.GetAgentIdentitiesByUserID(agentUser.ID)
	if err != nil {
		log.WithError(err).Error("failed to list identities")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if identities == nil {
		identities = []*api.AgentIdentity{}
	}
	c.JSON(http.StatusOK, identities)
}

func (s *Service) unlinkMyAgentIdentity(c *gin.Context) {
	agentID := c.Param("id")
	identityID := c.Param("identityId")

	agentUser := s.resolveAgentUser(c, agentID)
	if agentUser == nil {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	// Verify the identity belongs to this user.
	identities, err := s.persistence.GetAgentIdentitiesByUserID(agentUser.ID)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	found := false
	for _, id := range identities {
		if id.ID == identityID {
			found = true
			break
		}
	}
	if !found {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	if err := s.persistence.UnlinkAgentIdentity(identityID); err != nil {
		log.WithError(err).Error("failed to unlink identity")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	accountID, _ := c.Get("account_id")
	s.writeAuditLog(agentID, &agentUser.ID, accountID.(string),
		"identity_unlinked", "identity", &identityID, nil)

	c.Status(http.StatusNoContent)
}

// --- Audit log endpoints ---

func (s *Service) getMyAgentAuditLog(c *gin.Context) {
	agentID := c.Param("id")
	agentUser := s.resolveAgentUser(c, agentID)
	if agentUser == nil {
		c.JSON(http.StatusOK, []*api.AgentAuditLog{})
		return
	}

	limit, offset := parsePagination(c)
	entries, err := s.persistence.GetAuditLogForUser(agentUser.ID, limit, offset)
	if err != nil {
		log.WithError(err).Error("failed to list user audit log")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []*api.AgentAuditLog{}
	}
	c.JSON(http.StatusOK, entries)
}

func (s *Service) getAgentAuditLog(c *gin.Context) {
	agentID := c.Param("id")
	limit, offset := parsePagination(c)

	entries, err := s.persistence.GetAuditLogForAgent(agentID, limit, offset)
	if err != nil {
		log.WithError(err).Error("failed to list agent audit log")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []*api.AgentAuditLog{}
	}
	c.JSON(http.StatusOK, entries)
}

// --- Admin endpoints ---

func (s *Service) getAgentUsers(c *gin.Context) {
	agentID := c.Param("id")
	limit, offset := parsePagination(c)

	users, err := s.persistence.GetAgentUsersByAgentID(agentID, limit, offset)
	if err != nil {
		log.WithError(err).Error("failed to list agent users")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if users == nil {
		users = []*api.AgentUser{}
	}
	c.JSON(http.StatusOK, users)
}

func (s *Service) updateAgentRetention(c *gin.Context) {
	agentID := c.Param("id")

	var body struct {
		MemoryRetentionDays *int `json:"memory_retention_days"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.persistence.UpdateAgentRetentionDays(agentID, body.MemoryRetentionDays); err != nil {
		log.WithError(err).Error("failed to update retention policy")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	accountID, _ := c.Get("account_id")
	actorID := accountID.(string)
	s.writeAuditLog(agentID, nil, actorID,
		"retention_updated", "agent", &agentID,
		map[string]interface{}{"memory_retention_days": body.MemoryRetentionDays})

	c.Status(http.StatusNoContent)
}

// --- Internal endpoints for retention poller ---

func (s *Service) getExpiredMemoriesInternal(c *gin.Context) {
	limit := 100
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 {
		limit = l
	}

	memories, err := s.persistence.GetExpiredMemories(limit)
	if err != nil {
		log.WithError(err).Error("failed to get expired memories")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if memories == nil {
		memories = []*api.AgentMemory{}
	}
	c.JSON(http.StatusOK, memories)
}

func (s *Service) getAgentRetentionPoliciesInternal(c *gin.Context) {
	policies, err := s.persistence.GetAgentsWithRetentionPolicy()
	if err != nil {
		log.WithError(err).Error("failed to get retention policies")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusOK, policies)
}

func (s *Service) bulkDeleteExpiredMemoriesInternal(c *gin.Context) {
	var body struct {
		AgentID       string `json:"agent_id"`
		OlderThan     string `json:"older_than"`
		ExcludePinned bool   `json:"exclude_pinned"`
		Limit         int    `json:"limit"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if body.AgentID != "" && body.OlderThan != "" {
		olderThan, err := time.Parse(time.RFC3339, body.OlderThan)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid older_than format"})
			return
		}
		count, err := s.persistence.DeleteMemoriesOlderThan(body.AgentID, olderThan, body.ExcludePinned)
		if err != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": count})
		return
	}

	// Delete individually expired memories.
	limit := body.Limit
	if limit <= 0 {
		limit = 100
	}
	count, err := s.persistence.DeleteExpiredMemories(limit)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": count})
}

func (s *Service) createAuditLogEntryInternal(c *gin.Context) {
	var entry api.AgentAuditLog
	if err := c.BindJSON(&entry); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	id, err := s.persistence.CreateAuditLogEntry(entry)
	if err != nil {
		log.WithError(err).Error("failed to create audit log entry")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

// parsePagination is defined in agent.go — reused here.
