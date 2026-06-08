package http

import (
	"net/http"

	"flomation.app/automate/api/internal/agent"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// channelDispatchBody is the payload Launch sends to dispatch a
// channel-triggered message into either the agent inbound pipeline or
// a standalone flow execution.
type channelDispatchBody struct {
	ChannelType string                 `json:"channel_type"`
	Sender      string                 `json:"sender"`
	Content     string                 `json:"content"`
	Metadata    map[string]interface{} `json:"metadata"`
}

// dispatchTrigger handles POST /api/v1/internal/trigger/:trigger_id/dispatch.
//
// One endpoint, two paths — chosen by whether the trigger's flow is an
// agent's orchestrator flow:
//
//   - If yes: full agent inbound pipeline (identity resolution,
//     conversation tracking, message persistence, history fetch, then
//     orchestrator flow execution via InboundHandler.HandleInboundMessage).
//   - If no: the channel message becomes raw trigger data and the flow
//     fires as a standalone trigger execution. The trigger node's
//     outputs (sender, chat_id, content, etc.) are populated from the
//     flattened payload.
//
// This is the unified dispatch for every channel webhook in Launch
// (Telegram, Slack, Teams, Twilio SMS/Voice, Facebook). Launch keeps
// its existing direct-to-InboundHandler path for legacy agent_id-keyed
// URLs as a fallback.
func (s *Service) dispatchTrigger(c *gin.Context) {
	triggerID := c.Param("id")
	if triggerID == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	var body channelDispatchBody
	if err := c.ShouldBindJSON(&body); err != nil {
		log.WithFields(log.Fields{
			"trigger_id": triggerID,
			"error":      err,
		}).Warn("dispatchTrigger: invalid body")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	trigger, err := s.persistence.GetTriggerByID(triggerID)
	if err != nil {
		log.WithFields(log.Fields{
			"trigger_id": triggerID,
			"error":      err,
		}).Warn("dispatchTrigger: trigger lookup failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if trigger == nil || trigger.FloID == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	flowID := *trigger.FloID

	// Decide: is this trigger's flow an agent orchestrator?
	agentRecord, err := s.persistence.GetAgentByOrchestratorFloID(flowID)
	if err != nil {
		log.WithFields(log.Fields{
			"trigger_id": triggerID,
			"flow_id":    flowID,
			"error":      err,
		}).Warn("dispatchTrigger: agent lookup failed; treating as standalone")
		// Fall through to standalone path.
	}

	if agentRecord != nil {
		msg := agent.InboundMessage{
			ChannelType: body.ChannelType,
			Sender:      body.Sender,
			Content:     body.Content,
			Metadata:    body.Metadata,
		}
		result, err := s.inboundHandler.HandleInboundMessage(agentRecord.ID, msg)
		if err != nil {
			log.WithFields(log.Fields{
				"trigger_id": triggerID,
				"agent_id":   agentRecord.ID,
				"error":      err,
			}).Error("dispatchTrigger: agent inbound failed")
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.JSON(http.StatusAccepted, result)
		return
	}

	// Standalone trigger path — flatten the channel payload into the
	// trigger data map so the trigger node's outputs (channel_type,
	// sender, content, chat_id, sender_id, etc.) populate correctly.
	triggerData := map[string]interface{}{
		"channel_type": body.ChannelType,
		"sender":       body.Sender,
		"content":      body.Content,
	}
	for k, v := range body.Metadata {
		// Don't let metadata overwrite the canonical channel_type /
		// sender / content fields.
		if _, exists := triggerData[k]; exists {
			continue
		}
		triggerData[k] = v
	}

	executionID, err := s.persistence.TriggerExecution(flowID, triggerID, triggerData)
	if err != nil {
		log.WithFields(log.Fields{
			"trigger_id": triggerID,
			"flow_id":    flowID,
			"error":      err,
		}).Error("dispatchTrigger: trigger execution failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	s.executionNotifier.Notify()

	c.JSON(http.StatusCreated, gin.H{
		"execution_id": executionID,
	})
}
