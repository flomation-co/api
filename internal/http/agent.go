package http

import (
	"net/http"
	"strconv"
	"time"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (s *Service) getAgents(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var agents []*api.Agent
	var err error

	if len(user.Organisations) > 0 {
		agents, err = s.persistence.GetAgentsByOrgID(user.Organisations[0].ID)
	} else {
		agents, err = s.persistence.GetAgents(user.ID)
	}

	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get agents")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	if len(agents) == 0 {
		c.JSON(http.StatusOK, []interface{}{})
		return
	}

	c.JSON(http.StatusOK, agents)
}

func (s *Service) getAgentByID(c *gin.Context) {
	id := c.Param("id")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	agent, err := s.persistence.GetAgentByID(id)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "id": id}).Error("unable to get agent")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	// Check ownership
	if !s.canAccessAgent(user, agent) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	// Attach active session ID if running
	if agent.Status == api.AgentStatusRunning || agent.Status == api.AgentStatusPaused {
		session, _ := s.persistence.GetActiveAgentSession(agent.ID)
		if session != nil {
			agent.ActiveSessionID = &session.ID
		}
	}

	c.JSON(http.StatusOK, agent)
}

func (s *Service) createAgent(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var agent api.Agent
	if err := c.BindJSON(&agent); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to bind agent")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if agent.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	agent.OwnerID = user.ID
	if len(user.Organisations) > 0 {
		orgID := user.Organisations[0].ID
		agent.OrganisationID = &orgID
	}

	// Defaults
	if agent.MaxConcurrentExecutions <= 0 {
		agent.MaxConcurrentExecutions = 3
	}
	if agent.IdleTimeoutSeconds <= 0 {
		agent.IdleTimeoutSeconds = 3600
	}
	if agent.MaxExecutionsPerHour <= 0 {
		agent.MaxExecutionsPerHour = 100
	}

	id, err := s.persistence.CreateAgent(agent)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to create agent")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": *id})
}

func (s *Service) updateAgent(c *gin.Context) {
	id := c.Param("id")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	existing, err := s.persistence.GetAgentByID(id)
	if err != nil || existing == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.canAccessAgent(user, existing) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	var agent api.Agent
	if err := c.BindJSON(&agent); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	agent.ID = id
	if err := s.persistence.UpdateAgent(agent); err != nil {
		log.WithFields(log.Fields{"error": err, "id": id}).Error("unable to update agent")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (s *Service) archiveAgent(c *gin.Context) {
	id := c.Param("id")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	existing, err := s.persistence.GetAgentByID(id)
	if err != nil || existing == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.canAccessAgent(user, existing) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	// Stop agent if running before archiving
	if existing.Status == api.AgentStatusRunning || existing.Status == api.AgentStatusPaused {
		s.stopAgentRuntime(existing)
	}

	if err := s.persistence.ArchiveAgent(id); err != nil {
		log.WithFields(log.Fields{"error": err, "id": id}).Error("unable to archive agent")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (s *Service) startAgent(c *gin.Context) {
	id := c.Param("id")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	agent, err := s.persistence.GetAgentByID(id)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.canAccessAgent(user, agent) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	if agent.Status == api.AgentStatusRunning {
		c.JSON(http.StatusConflict, gin.H{"error": "agent is already running"})
		return
	}

	// Create session
	sessionID, err := s.persistence.CreateAgentSession(id)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "id": id}).Error("unable to create agent session")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	// Update status
	now := time.Now()
	if err := s.persistence.UpdateAgentStatus(id, api.AgentStatusRunning, &now, nil); err != nil {
		log.WithFields(log.Fields{"error": err, "id": id}).Error("unable to update agent status")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	// TODO: Register agent with Launch service for webhook/polling activation
	// s.launch.RegisterAgent(id, agent)

	c.JSON(http.StatusOK, gin.H{"status": "running", "session_id": *sessionID})
}

func (s *Service) stopAgent(c *gin.Context) {
	id := c.Param("id")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	agent, err := s.persistence.GetAgentByID(id)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.canAccessAgent(user, agent) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	if agent.Status == api.AgentStatusStopped {
		c.JSON(http.StatusConflict, gin.H{"error": "agent is already stopped"})
		return
	}

	s.stopAgentRuntime(agent)

	c.JSON(http.StatusOK, gin.H{"status": "stopped"})
}

func (s *Service) pauseAgent(c *gin.Context) {
	id := c.Param("id")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	agent, err := s.persistence.GetAgentByID(id)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.canAccessAgent(user, agent) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	if agent.Status != api.AgentStatusRunning {
		c.JSON(http.StatusConflict, gin.H{"error": "agent is not running"})
		return
	}

	if err := s.persistence.UpdateAgentStatus(id, api.AgentStatusPaused, agent.StartedAt, nil); err != nil {
		log.WithFields(log.Fields{"error": err, "id": id}).Error("unable to pause agent")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "paused"})
}

// --- Sessions ---

func (s *Service) getAgentSessions(c *gin.Context) {
	id := c.Param("id")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	agent, err := s.persistence.GetAgentByID(id)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.canAccessAgent(user, agent) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	limit, offset := parsePagination(c)
	sessions, err := s.persistence.GetAgentSessions(id, limit, offset)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "agent_id": id}).Error("unable to get sessions")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, sessions)
}

func (s *Service) getAgentSessionByID(c *gin.Context) {
	agentID := c.Param("id")
	sessionID := c.Param("sessionId")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.canAccessAgent(user, agent) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	session, err := s.persistence.GetAgentSessionByID(sessionID)
	if err != nil || session == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	// Return session with recent messages
	limit, offset := parsePagination(c)
	messages, _ := s.persistence.GetAgentSessionMessages(sessionID, limit, offset)

	c.JSON(http.StatusOK, gin.H{
		"session":  session,
		"messages": messages,
	})
}

// --- State ---

func (s *Service) getAgentState(c *gin.Context) {
	id := c.Param("id")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	agent, err := s.persistence.GetAgentByID(id)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.canAccessAgent(user, agent) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	state, err := s.persistence.GetAgentState(id)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "agent_id": id}).Error("unable to get agent state")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, state)
}

func (s *Service) getAgentStateKey(c *gin.Context) {
	id := c.Param("id")
	key := c.Param("key")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	agent, err := s.persistence.GetAgentByID(id)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.canAccessAgent(user, agent) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	state, err := s.persistence.GetAgentStateKey(id, key)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get agent state key")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if state == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.JSON(http.StatusOK, state)
}

func (s *Service) setAgentStateKey(c *gin.Context) {
	id := c.Param("id")
	key := c.Param("key")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	agent, err := s.persistence.GetAgentByID(id)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.canAccessAgent(user, agent) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	var body struct {
		Value interface{} `json:"value"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.persistence.UpsertAgentState(id, key, body.Value); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to set agent state")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (s *Service) deleteAgentStateKey(c *gin.Context) {
	id := c.Param("id")
	key := c.Param("key")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	agent, err := s.persistence.GetAgentByID(id)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.canAccessAgent(user, agent) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	if err := s.persistence.DeleteAgentStateKey(id, key); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to delete agent state key")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// --- Messages ---

func (s *Service) getAgentMessages(c *gin.Context) {
	id := c.Param("id")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	agent, err := s.persistence.GetAgentByID(id)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.canAccessAgent(user, agent) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	limit, offset := parsePagination(c)
	messages, err := s.persistence.GetAgentMessages(id, limit, offset)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get agent messages")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, messages)
}

func (s *Service) createAgentMessage(c *gin.Context) {
	id := c.Param("id")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	agent, err := s.persistence.GetAgentByID(id)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.canAccessAgent(user, agent) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	var msg api.AgentMessage
	if err := c.BindJSON(&msg); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	msg.AgentID = id

	// Attach active session if present
	if session, _ := s.persistence.GetActiveAgentSession(id); session != nil {
		msg.SessionID = &session.ID
	}

	msgID, err := s.persistence.CreateAgentMessage(msg)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to create agent message")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": *msgID})
}

// --- Executions ---

func (s *Service) getAgentExecutions(c *gin.Context) {
	id := c.Param("id")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	agent, err := s.persistence.GetAgentByID(id)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.canAccessAgent(user, agent) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	limit, offset := parsePagination(c)
	executions, err := s.persistence.GetAgentExecutions(id, limit, offset)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get agent executions")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, executions)
}

// --- Helpers ---

// canAccessAgent checks if the user owns the agent or is in the same organisation.
func (s *Service) canAccessAgent(user *api.User, agent *api.Agent) bool {
	if user.ID == agent.OwnerID {
		return true
	}
	if agent.OrganisationID != nil {
		for _, org := range user.Organisations {
			if org.ID == *agent.OrganisationID {
				return true
			}
		}
	}
	return false
}

// stopAgentRuntime ends the active session and updates agent status.
func (s *Service) stopAgentRuntime(agent *api.Agent) {
	// End active session
	if session, _ := s.persistence.GetActiveAgentSession(agent.ID); session != nil {
		_ = s.persistence.EndAgentSession(session.ID, api.AgentSessionEnded, nil)
	}

	// Update status
	now := time.Now()
	_ = s.persistence.UpdateAgentStatus(agent.ID, api.AgentStatusStopped, agent.StartedAt, &now)

	// TODO: Deregister agent from Launch service
	// s.launch.DeregisterAgent(agent.ID)
}

// parsePagination extracts limit/offset from query parameters with sensible defaults.
func parsePagination(c *gin.Context) (int, int) {
	limitStr := c.DefaultQuery("limit", "50")
	offsetStr := c.DefaultQuery("offset", "0")

	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 || limit > 200 {
		limit = 50
	}

	offset, err := strconv.Atoi(offsetStr)
	if err != nil || offset < 0 {
		offset = 0
	}

	return limit, offset
}
