package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (s *Service) getAgents(c *gin.Context) {
	if !s.checkPermission(c, rbac.AgentView) {
		return
	}
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
	if !s.checkPermission(c, rbac.AgentCreate) {
		return
	}
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
	if !s.checkPermission(c, rbac.AgentEdit) {
		return
	}
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
	if !s.checkPermission(c, rbac.AgentDelete) {
		return
	}
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
	if !s.checkPermission(c, rbac.AgentStartStop) {
		return
	}
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

	// Register agent with Launch service for webhook/polling activation.
	// Don't send a trigger ID — let executeFlo match channel_type to the correct
	// trigger at dispatch time. This supports multi-channel agents with different
	// trigger types (e.g. Telegram + Slack in the same flow).
	if s.launch != nil {
		if err := s.launch.RegisterAgent(id, agent.OrchestratorFlowID, nil,
			agent.Channels, agent.EnvironmentID, agent.MaxExecutionsPerHour, agent.RequiresApproval); err != nil {
			log.WithFields(log.Fields{"error": err, "id": id}).Warn("unable to register agent with launch service")
			// Non-fatal — agent is started locally even if Launch registration fails
		}
	}

	c.JSON(http.StatusOK, gin.H{"status": "running", "session_id": *sessionID})
}

func (s *Service) stopAgent(c *gin.Context) {
	if !s.checkPermission(c, rbac.AgentStartStop) {
		return
	}
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
	if !s.checkPermission(c, rbac.AgentStartStop) {
		return
	}
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

	// Return session with messages and executions
	limit, offset := parsePagination(c)
	messages, _ := s.persistence.GetAgentSessionMessages(sessionID, limit, offset)
	executions, _ := s.persistence.GetExecutionsBySessionID(sessionID)

	c.JSON(http.StatusOK, gin.H{
		"session":    session,
		"messages":   messages,
		"executions": executions,
	})
}

// --- Session SSE Stream ---

func (s *Service) streamAgentSession(c *gin.Context) {
	sessionID := c.Param("sessionId")

	session, err := s.persistence.GetAgentSessionByID(sessionID)
	if err != nil || session == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	ch := s.agentSessionHub.Subscribe(sessionID)
	defer s.agentSessionHub.Unsubscribe(sessionID, ch)

	// Send initial connected event
	fmt.Fprintf(c.Writer, "event: connected\ndata: {\"session_id\":\"%s\"}\n\n", sessionID)
	c.Writer.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(event.Data)
			fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event.Type, string(data))
			c.Writer.Flush()

		case <-ticker.C:
			fmt.Fprintf(c.Writer, ": keepalive\n\n")
			c.Writer.Flush()

		case <-c.Request.Context().Done():
			return
		}
	}
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

	// Deregister agent from Launch service
	if s.launch != nil {
		if err := s.launch.DeregisterAgent(agent.ID); err != nil {
			log.WithFields(log.Fields{"error": err, "agent_id": agent.ID}).Warn("unable to deregister agent from launch service")
		}
	}
}

// --- Internal endpoints (no JWT, for Launch service-to-service calls) ---

func (s *Service) createAgentMessageInternal(c *gin.Context) {
	id := c.Param("id")

	agent, err := s.persistence.GetAgentByID(id)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
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
		log.WithFields(log.Fields{"error": err}).Error("unable to create agent message (internal)")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	// Publish to SSE subscribers
	if msg.SessionID != nil {
		msg.ID = *msgID
		s.agentSessionHub.PublishJSON(*msg.SessionID, "message", msg)
	}

	c.JSON(http.StatusCreated, gin.H{"id": *msgID})
}

func (s *Service) getAgentStateInternal(c *gin.Context) {
	id := c.Param("id")
	key := c.Param("key")

	state, err := s.persistence.GetAgentStateKey(id, key)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if state == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.JSON(http.StatusOK, state)
}

func (s *Service) setAgentStateInternal(c *gin.Context) {
	id := c.Param("id")
	key := c.Param("key")

	var body struct {
		Value interface{} `json:"value"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.persistence.UpsertAgentState(id, key, body.Value); err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
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
