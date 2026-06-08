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

	// Hydrate identity context for the standalone trigger, mirroring the
	// agent inbound pipeline. Resolves the platform user_id (declared or
	// anonymous stub) plus the user's declared-identity snapshot scoped
	// to the flow's organisation. Skipped when no channel external_id is
	// derivable from the payload (e.g. schedule, webhook with raw JSON,
	// git-poll).
	flo, floErr := s.persistence.GetFloByID(flowID)
	if floErr == nil && flo != nil {
		var orgID *string
		if flo.OrganisationID != nil && *flo.OrganisationID != "" {
			orgID = flo.OrganisationID
		}
		externalID := extractChannelExternalID(body)
		if externalID != "" {
			channelType := normaliseDispatchChannelType(body.ChannelType)
			tu, err := agent.ResolveTriggeringUser(s.persistence, orgID, channelType, externalID, body.Sender)
			if err != nil {
				log.WithFields(log.Fields{
					"trigger_id":   triggerID,
					"channel_type": channelType,
					"error":        err,
				}).Warn("dispatchTrigger: identity resolution failed; continuing without hydration")
			} else if tu != nil {
				triggerData["user_id"] = tu.UserID
				triggerData["__triggering_user_id"] = tu.UserID
				if orgID != nil {
					triggerData["organisation_id"] = *orgID
				}
				if len(tu.Identities) > 0 {
					out := make([]map[string]interface{}, 0, len(tu.Identities))
					for _, i := range tu.Identities {
						if i == nil {
							continue
						}
						row := map[string]interface{}{
							"channel_type": i.ChannelType,
							"external_id":  i.ExternalID,
						}
						if i.DisplayName != nil && *i.DisplayName != "" {
							row["display_name"] = *i.DisplayName
						}
						out = append(out, row)
					}
					triggerData["identities"] = out
				}
			}
		}
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

// extractChannelExternalID pulls the external sender identifier from a
// dispatch body's metadata. Returns "" when no usable identifier is
// found — caller skips identity hydration in that case.
//
// All Launch channel handlers populate metadata["user_id"] as the
// canonical sender identifier (Telegram chat user ID, Slack user ID,
// Teams AAD object ID, Twilio phone number, Facebook PSID). We prefer
// that; fall back to the human-readable Sender if absent (e.g. a future
// handler that omits the canonical key).
func extractChannelExternalID(body channelDispatchBody) string {
	if body.Metadata != nil {
		if v, ok := body.Metadata["user_id"].(string); ok && v != "" {
			return v
		}
	}
	return body.Sender
}

// normaliseDispatchChannelType collapses channel sub-types to their
// base type for identity resolution (telegram_voice → telegram,
// twilio_voice → twilio). Mirrors the agent inbound pipeline's
// normaliseChannelType but lives here so the http package doesn't
// need to import the agent package's internals.
func normaliseDispatchChannelType(channelType string) string {
	switch channelType {
	case "telegram_voice":
		return "telegram"
	case "twilio_voice":
		return "twilio"
	case "teams":
		// Identity declarations collapse Microsoft transports onto
		// a single "microsoft" channel — same rule as the API's
		// agent.normaliseChannelType (migration 85).
		return "microsoft"
	default:
		return channelType
	}
}
