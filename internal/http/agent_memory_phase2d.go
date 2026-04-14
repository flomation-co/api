package http

// Phase 2d-α of the Agent Memory feature — the extract-dispatch endpoint.
//
// This is the single entry point both Launch (after storing an inbound
// user turn) and the executor's assistant-reply hook (after storing an
// outbound assistant turn) will call to trigger memory extraction. See
// plans/agent_memory.md §"The extraction pipeline" for the full design.
//
// The endpoint is deliberately a no-op (204 No Content) when the agent's
// extraction_flow_id is NULL. That lets callers invoke it unconditionally
// without checking whether extraction is configured for the agent — which
// is important for the Phase 2d rollout, because Launch and the executor
// will start calling this endpoint in Phase 2d-γ before Phase 2d-δ (or
// whenever the seed migration actually lands) has populated the column
// for existing agents. A 204 response means "extraction not configured,
// nothing to do here" and the caller just moves on.
//
// When extraction IS configured, the endpoint resolves the flow's
// triggers, picks the manual trigger, and calls the same TriggerExecution
// path that the public /execute endpoint uses. Extraction flows run on
// the normal runner fleet via the normal execution pipeline — there is
// no extraction-specific fork anywhere in the platform. This is a core
// tenet of the plan doc: "debuggable as a flow because it IS a flow".

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// extractAgentInternalRequest is the payload Launch and the executor's
// assistant-reply hook send to the extract endpoint. Every field is
// optional — the minimum viable request is `{"role": "user", "content": "..."}`
// but callers that have more context should pass it through so the
// extraction flow's AI action gets richer grounding.
type extractAgentInternalRequest struct {
	// Role identifies which side of the conversation the extraction is
	// running against: "user" (inbound turn) or "assistant" (outbound
	// reply). The extraction flow branches on this to emit appropriate
	// made_by values on commitments.
	Role string `json:"role" binding:"required"`

	// Content is the verbatim text being extracted from. For user turns
	// this is the incoming message body; for assistant turns this is
	// the AI reply text.
	Content string `json:"content" binding:"required"`

	// MessageID is the agent_message row the content came from. The
	// extraction flow writes this into the source_message column of
	// any memories / pending actions / commitments it creates, so an
	// admin can trace a memory back to the exact turn that produced it.
	MessageID *string `json:"message_id,omitempty"`

	// AgentUserID is the canonical user the memory should be scoped to.
	// May be nil for unresolved webhook senders — the extraction flow
	// can still process the content and the writes will be agent-global
	// (scope='global').
	AgentUserID *string `json:"agent_user_id,omitempty"`

	// ConversationID scopes the extraction to a specific conversation
	// thread. Used by the flow to attribute source_conversation on the
	// resulting records.
	ConversationID *string `json:"conversation_id,omitempty"`

	// ConversationHistory is the recent conversation context (last few
	// turns) so the extraction AI can determine if short replies like
	// "yes"/"no" are confirmations of pending actions.
	ConversationHistory interface{} `json:"conversation_history,omitempty"`
}

// extractAgentInternal handles POST /api/v1/internal/agent/:id/extract.
//
// Workflow:
//  1. Resolve the agent. 404 if unknown.
//  2. If the agent has no extraction_flow_id configured → 204 (no-op).
//  3. Look up the extraction flow's triggers; pick the manual one.
//     404 + error log if the flow has no usable trigger.
//  4. Build trigger data with the caller's fields plus agent_id, and
//     dispatch via TriggerExecution — the same code path the public
//     /execute endpoint uses.
//  5. Return 202 Accepted with the execution ID so the caller can
//     correlate logs if it wants to.
//
// This function is deliberately *non-blocking* in the semantic sense:
// TriggerExecution only records the execution in the queue — the actual
// flow runs on a runner picked up asynchronously. The caller's reply
// path is never held up by extraction latency.
func (s *Service) extractAgentInternal(c *gin.Context) {
	agentID := c.Param("id")

	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	// No extraction flow configured → no-op. This is the expected path
	// for agents that exist today (pre-seed) and for agents where an
	// admin has explicitly disabled extraction by NULL-ing the column.
	if agent.ExtractionFlowID == nil || *agent.ExtractionFlowID == "" {
		c.Status(http.StatusNoContent)
		return
	}

	var body extractAgentInternalRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Resolve the flow's triggers. Extraction flows ship with a single
	// manual trigger (the seed migration in Phase 2d-γ enforces this).
	triggers, err := s.persistence.GetTriggersByFloID(*agent.ExtractionFlowID)
	if err != nil {
		log.WithFields(log.Fields{
			"error":              err,
			"agent_id":           agentID,
			"extraction_flow_id": *agent.ExtractionFlowID,
		}).Error("unable to resolve extraction flow triggers")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	var triggerID string
	for _, t := range triggers {
		if t.TypeName == "manual" {
			triggerID = t.ID
			break
		}
	}
	if triggerID == "" && len(triggers) > 0 {
		triggerID = triggers[0].ID
	}
	if triggerID == "" {
		// A flow with no triggers at all is a configuration error that
		// the seed migration should have prevented. Log loudly and fail
		// the request so the caller surfaces it rather than silently
		// dropping extractions.
		log.WithFields(log.Fields{
			"agent_id":           agentID,
			"extraction_flow_id": *agent.ExtractionFlowID,
		}).Error("extraction flow has no usable trigger")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	// Build the trigger data map the extraction flow will see as its
	// ${trigger.*} variables. Include agent_id so the agent/remember,
	// agent/pending_action, and agent/commitment writes inside the
	// extraction flow can scope themselves without an extra lookup.
	triggerData := map[string]interface{}{
		"agent_id": agentID,
		"role":     body.Role,
		"content":  body.Content,
	}
	if body.MessageID != nil {
		triggerData["message_id"] = *body.MessageID
	}
	if body.AgentUserID != nil {
		triggerData["agent_user_id"] = *body.AgentUserID
	}
	if body.ConversationID != nil {
		triggerData["conversation_id"] = *body.ConversationID
	}
	// Pass the agent's AI API key so the extraction flow's Anthropic
	// node can authenticate without depending on a user environment.
	if agent.AIAPIKey != nil && *agent.AIAPIKey != "" {
		triggerData["api_key"] = *agent.AIAPIKey
	}

	// Marshal once so the shape is fixed; TriggerExecution accepts
	// interface{} and stores the raw JSON on the execution row.
	raw, err := json.Marshal(triggerData)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to marshal extract trigger data")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	executionID, err := s.persistence.TriggerExecution(*agent.ExtractionFlowID, triggerID, json.RawMessage(raw))
	if err != nil {
		log.WithFields(log.Fields{
			"error":              err,
			"agent_id":           agentID,
			"extraction_flow_id": *agent.ExtractionFlowID,
		}).Error("unable to trigger extraction execution")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	// Tag the execution with the agent_id so the admin Executions page
	// can filter extraction runs to a specific agent. Mirrors what
	// /internal/flo/:FloID/execute does for orchestrator dispatches.
	if executionID != nil {
		if err := s.persistence.SetExecutionAgentID(*executionID, agentID); err != nil {
			log.WithFields(log.Fields{
				"error":        err,
				"execution_id": *executionID,
			}).Warn("unable to tag extraction execution with agent_id")
		}
	}

	// Wake any long-polling runners so the extraction job gets picked
	// up promptly. The orchestrator dispatch path does the same; without
	// this the extraction would wait up to one poll interval.
	s.executionNotifier.Notify()

	c.JSON(http.StatusAccepted, gin.H{
		"execution_id": executionID,
	})
}
