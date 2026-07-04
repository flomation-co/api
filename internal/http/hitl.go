package http

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// Human-in-the-Loop internal endpoints. Registered under /api/v1/internal/ and
// intentionally NOT behind JWT auth — they are service-to-service calls from
// the executor (request registration) and Launch (response capture).

type createHITLRequestBody struct {
	ExecutionID string           `json:"execution_id"`
	FloID       string           `json:"flo_id"`
	NodeID      string           `json:"node_id"`
	Message     string           `json:"message"`
	Options     []api.HITLOption `json:"options"`
	ExpiresAt   string           `json:"expires_at"`
	WebBaseURL  string           `json:"web_base_url"`
}

type createHITLRequestResponse struct {
	RequestID  string           `json:"request_id"`
	WebBaseURL string           `json:"web_base_url"`
	Options    []api.HITLOption `json:"options"`
}

// createHITLRequestInternal handles POST /api/v1/internal/hitl/request.
// Called by the Await action on its first (suspending) pass. Idempotent on
// (execution_id, node_id) so a runner retry reuses the same request + tokens.
func (s *Service) createHITLRequestInternal(c *gin.Context) {
	var body createHITLRequestBody
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if body.ExecutionID == "" || body.NodeID == "" || len(body.Options) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "execution_id, node_id and options are required"})
		return
	}

	// Idempotency: if a request already exists for this node, return it with
	// its stored (already-tokenised) options.
	if existing, err := s.persistence.GetHITLRequestByExecutionNode(body.ExecutionID, body.NodeID); err == nil && existing != nil {
		var opts []api.HITLOption
		_ = json.Unmarshal(existing.Options, &opts)
		c.JSON(http.StatusOK, createHITLRequestResponse{
			RequestID:  existing.ID,
			WebBaseURL: body.WebBaseURL,
			Options:    opts,
		})
		return
	}

	// Mint a per-option token for the web click-link / Telegram callback.
	tokens := make([]api.HITLToken, 0, len(body.Options))
	for i := range body.Options {
		tok := newHITLToken()
		body.Options[i].Token = tok
		tokens = append(tokens, api.HITLToken{Token: tok, OptionValue: body.Options[i].Value})
	}

	optionsJSON, _ := json.Marshal(body.Options)
	req := &api.HITLRequest{
		ExecutionID: body.ExecutionID,
		FloID:       body.FloID,
		NodeID:      body.NodeID,
		Message:     body.Message,
		Options:     optionsJSON,
	}
	if body.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, body.ExpiresAt); err == nil {
			req.ExpiresAt = &t
		}
	}

	if err := s.persistence.InsertHITLRequest(req, tokens); err != nil {
		// A concurrent insert (unique violation) means another attempt won the
		// race — return that row instead of failing.
		if existing, gerr := s.persistence.GetHITLRequestByExecutionNode(body.ExecutionID, body.NodeID); gerr == nil && existing != nil {
			var opts []api.HITLOption
			_ = json.Unmarshal(existing.Options, &opts)
			c.JSON(http.StatusOK, createHITLRequestResponse{RequestID: existing.ID, WebBaseURL: body.WebBaseURL, Options: opts})
			return
		}
		log.WithError(err).Error("unable to create HITL request")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusCreated, createHITLRequestResponse{
		RequestID:  req.ID,
		WebBaseURL: body.WebBaseURL,
		Options:    body.Options,
	})
}

type respondHITLBody struct {
	Token       string `json:"token"`
	RequestID   string `json:"request_id"`
	OptionValue string `json:"option_value"`
	AnsweredBy  string `json:"answered_by"`
	Channel     string `json:"channel"`
}

type respondHITLResponse struct {
	Status      string            `json:"status"` // answered | already_answered | not_found
	ExecutionID string            `json:"execution_id,omitempty"`
	Channels    []api.HITLChannel `json:"channels,omitempty"`
}

// respondHITLInternal handles POST /api/v1/internal/hitl/respond. Called by
// Launch when a human clicks a button or a web link. Enforces first-response-
// wins and, on the winning response, resumes the suspended execution down the
// chosen option's branch.
func (s *Service) respondHITLInternal(c *gin.Context) {
	var body respondHITLBody
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Resolve the request, preferring the opaque token (which also carries the
	// option) over an explicit request_id + option_value.
	var (
		req    *api.HITLRequest
		option = body.OptionValue
		err    error
	)
	if body.Token != "" {
		req, option, err = s.persistence.GetHITLRequestByToken(body.Token)
	} else if body.RequestID != "" {
		req, err = s.persistence.GetHITLRequestByID(body.RequestID)
	}
	if err != nil {
		log.WithError(err).Error("unable to resolve HITL request")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if req == nil {
		c.JSON(http.StatusNotFound, respondHITLResponse{Status: "not_found"})
		return
	}

	won, claimed, err := s.persistence.ClaimHITLResponse(req.ID, option, body.AnsweredBy, body.Channel)
	if err != nil {
		log.WithError(err).Error("unable to claim HITL response")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if !won {
		c.JSON(http.StatusOK, respondHITLResponse{Status: "already_answered", Channels: decodeChannels(req.Channels)})
		return
	}

	// Winning response — resume the execution with the chosen option.
	resumeData := map[string]interface{}{
		"await": map[string]interface{}{
			"request_id":   req.ID,
			"outcome":      "option",
			"option_value": option,
			"answered_by":  body.AnsweredBy,
		},
	}
	if err := s.resumeExecutionWithData(claimed.ExecutionID, resumeData); err != nil {
		log.WithError(err).Error("unable to resume execution after HITL response")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, respondHITLResponse{
		Status:      "answered",
		ExecutionID: claimed.ExecutionID,
		Channels:    decodeChannels(claimed.Channels),
	})
}

func decodeChannels(raw json.RawMessage) []api.HITLChannel {
	var ch []api.HITLChannel
	_ = json.Unmarshal(raw, &ch)
	return ch
}

// newHITLToken returns a short, URL-safe opaque token (~22 chars) suitable for
// a web link and for Telegram callback_data (which is capped at 64 bytes).
func newHITLToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// rand.Read failing is catastrophic; fall back to a timestamp-based
		// value so we never mint an empty token.
		return base64.RawURLEncoding.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
