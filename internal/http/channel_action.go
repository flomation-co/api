package http

// Channel action proxy — forwards channel-specific SDK actions
// (typing indicators, etc.) to the Launch service.
//
// Route: POST /api/v1/internal/agent/:id/channel-action

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type channelActionInternalRequest struct {
	ChannelType string `json:"channel_type" binding:"required"`
	Action      string `json:"action" binding:"required"`
	ChatID      string `json:"chat_id"`
}

func (s *Service) channelActionInternal(c *gin.Context) {
	agentID := c.Param("id")

	var body channelActionInternalRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.launch.ChannelAction(agentID, body.ChannelType, body.Action, body.ChatID); err != nil {
		log.WithFields(log.Fields{
			"agent_id":     agentID,
			"channel_type": body.ChannelType,
			"action":       body.Action,
			"error":        err,
		}).Warn("channel action failed")
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
