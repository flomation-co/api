package http

// Agent inbound message handler — Phase 3 of the Launch → API migration.
// Replaces Launch's 7-step pipeline (7+ HTTP round-trips) with a single
// API endpoint that uses direct DB access.
//
// Route: POST /api/v1/internal/agent/:id/inbound-message

import (
	"net/http"

	"flomation.app/automate/api/internal/agent"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// handleInboundMessageInternal handles POST /api/v1/internal/agent/:id/inbound-message.
// This is the single-call replacement for Launch's multi-step pipeline.
func (s *Service) handleInboundMessageInternal(c *gin.Context) {
	agentID := c.Param("id")

	var msg agent.InboundMessage
	if err := c.BindJSON(&msg); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if s.inboundHandler == nil {
		log.WithField("agent_id", agentID).Error("inbound handler not initialised")
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}

	result, err := s.inboundHandler.HandleInboundMessage(agentID, msg)
	if err != nil {
		log.WithFields(log.Fields{
			"agent_id": agentID,
			"error":    err,
		}).Error("inbound message processing failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusAccepted, result)
}
