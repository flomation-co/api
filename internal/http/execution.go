package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	htmltemplate "html/template"
	"io"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"flomation.app/automate/api"
	appmetrics "flomation.app/automate/api/internal/metrics"
	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (s *Service) updateExecutionState(c *gin.Context) {
	id := c.Param("id")

	execution, err := s.persistence.GetExecutionByID(id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get execution")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if execution == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	type state struct {
		State string `json:"state"`
	}

	var executionStatus state
	if err := c.BindJSON(&executionStatus); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to bind json")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	log.WithFields(log.Fields{
		"id":    id,
		"state": executionStatus.State,
	}).Info("updating execution state")

	if err := s.persistence.UpdateExecutionStatus(id, executionStatus.State); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to update execution status")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Notify SSE subscribers of the status change
	s.logHub.Publish(id, []string{"__STATUS__:" + executionStatus.State})

	c.Status(http.StatusOK)
}

func (s *Service) cancelExecution(c *gin.Context) {
	id := c.Param("id")
	user := s.getUserFromContext(c)

	execution, err := s.persistence.GetExecutionByID(id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get execution")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if execution == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if !s.verifyOrgAccess(user, execution.OrganisationID) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	// Only cancel executions that are still in progress
	if execution.CompletionStatus != "pending" {
		c.JSON(http.StatusConflict, gin.H{"error": "execution is not in progress"})
		return
	}

	log.WithFields(log.Fields{
		"id":   id,
		"user": user.ID,
	}).Info("cancelling execution")

	if err := s.persistence.UpdateCompletionStatus(id, "cancel"); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to update completion status")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	if err := s.persistence.UpdateExecutionStatus(id, "executed"); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to update execution status")
	}

	// Notify SSE subscribers
	s.logHub.Publish(id, []string{"__STATUS__:cancelled"})
	s.logHub.Complete(id)
	go func() {
		time.Sleep(5 * time.Second)
		s.logHub.Cleanup(id)
	}()

	c.Status(http.StatusOK)
}

func (s *Service) getExecutionStatus(c *gin.Context) {
	id := c.Param("id")

	execution, err := s.persistence.GetExecutionByID(id)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if execution == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"execution_status":  execution.ExecutionStatus,
		"completion_status": execution.CompletionStatus,
	})
}

func (s *Service) updateExecution(c *gin.Context) {
	id := c.Param("id")

	execution, err := s.persistence.GetExecutionByID(id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get execution")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if execution == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var result api.ExecutionResult
	if err := c.BindJSON(&result); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to bind json")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Handle suspended executions — store checkpoint and update status
	if result.Suspended {
		s.handleSuspendedExecution(c, id, execution, result)
		return
	}

	if err := s.persistence.UpdateExecutionStatus(id, "executed"); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to update execution status")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	completion := "success"
	if result.Cancelled {
		completion = "cancel"
	} else if result.HasErrored {
		completion = "fail"
	}

	appmetrics.ExecutionsTotal.WithLabelValues(completion).Inc()

	if err := s.persistence.UpdateCompletionStatus(id, completion); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to update execution status")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	j, err := json.Marshal(result.State)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to marshal data")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	result.State = j

	if err := s.persistence.UpdateExecutionResult(id, result.State); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to update execution result")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Notify SSE subscribers that execution is complete
	s.logHub.Complete(id)
	go func() {
		time.Sleep(5 * time.Second)
		s.logHub.Cleanup(id)
	}()

	// Notify agent session SSE subscribers if this was an agent execution
	if execution.AgentSessionID != nil && *execution.AgentSessionID != "" {
		// Re-fetch execution with full result for the SSE event
		if updated, _ := s.persistence.GetExecutionByID(id); updated != nil {
			s.agentSessionHub.PublishJSON(*execution.AgentSessionID, "execution", updated)
		}
	}

	// Send notification emails if configured
	go s.sendExecutionNotification(execution.FloID, completion, execution)

	// Deduct credit for overage if applicable.
	go s.processPostExecutionCredit(id)

	c.Status(http.StatusOK)
}

func (s *Service) getExecutionByID(c *gin.Context) {
	if !s.checkPermission(c, rbac.FlowExecute) {
		return
	}

	id := c.Param("id")
	user := s.getUserFromContext(c)

	exec, err := s.persistence.GetExecutionByID(id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get execution by ID")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if exec == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if !s.verifyOrgAccess(user, exec.OrganisationID) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	// Data and Result are json.RawMessage — they serialise as raw JSON directly
	c.JSON(http.StatusOK, exec)
}

func (s *Service) getExecutions(c *gin.Context) {
	if !s.checkPermission(c, rbac.FlowExecute) {
		return
	}

	search := c.DefaultQuery("search", "")

	offset := c.DefaultQuery("offset", "0")
	limit := c.DefaultQuery("limit", "10")

	offsetStr, err := strconv.ParseInt(offset, 10, 64)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get offset string")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	limitStr, err := strconv.ParseInt(limit, 10, 64)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get limit string")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if limitStr > 100 {
		limitStr = 100
	}

	user := s.getUserFromContext(c)

	var orgID *string
	if len(user.Organisations) > 0 {
		orgID = &user.Organisations[0].ID
	}

	rootOnly := c.DefaultQuery("root_only", "false") == "true"

	executions, count, err := s.persistence.GetExecutions(offsetStr, limitStr, search, user.ID, orgID, rootOnly)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get executions")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if len(executions) == 0 {
		c.Status(http.StatusNoContent)
		return
	}

	// Enrich with credit costs where applicable.
	execIDs := make([]string, len(executions))
	for i, e := range executions {
		execIDs[i] = e.ID
	}
	if costs, err := s.persistence.GetCreditCostsForExecutions(execIDs); err == nil && len(costs) > 0 {
		for _, e := range executions {
			if cost, ok := costs[e.ID]; ok {
				e.CreditCostPence = &cost
			}
		}
	}

	c.Writer.Header().Set("x-total-items", fmt.Sprintf("%v", count))

	c.JSON(http.StatusOK, executions)
}

func (s *Service) appendExecutionLogs(c *gin.Context) {
	id := c.Param("id")

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	var payload struct {
		Lines []string `json:"lines"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if len(payload.Lines) > 0 {
		s.logHub.Publish(id, payload.Lines)

		// Forward __NODE__ events to agent session SSE if applicable
		// and intercept __LINK_OFFER__ events for identity linking.
		var sessionID *string
		for _, line := range payload.Lines {
			if strings.HasPrefix(line, "__LINK_OFFER__:") {
				tag := strings.TrimPrefix(line, "__LINK_OFFER__:")
				go s.handleLinkOfferEvent(id, tag)
				continue
			}
			if strings.HasPrefix(line, "__NODE__:") {
				// Lazy lookup — only hit DB once per batch
				if sessionID == nil {
					if exec, _ := s.persistence.GetExecutionByID(id); exec != nil && exec.AgentSessionID != nil {
						sessionID = exec.AgentSessionID
					} else {
						empty := ""
						sessionID = &empty
					}
				}
				if *sessionID != "" {
					nodeData := strings.TrimPrefix(line, "__NODE__:")
					s.agentSessionHub.PublishJSON(*sessionID, "node", json.RawMessage(nodeData))
				}
			}
		}
	}

	c.Status(http.StatusOK)
}

// handleLinkOfferEvent processes a __LINK_OFFER__ event from the executor.
// The tag format is "channel_type:external_id". Creates an identity_link
// pending action for the agent user associated with this execution.
func (s *Service) handleLinkOfferEvent(executionID, tag string) {
	parts := strings.SplitN(tag, ":", 2)
	if len(parts) != 2 {
		return
	}
	channelType, externalID := parts[0], parts[1]

	exec, err := s.persistence.GetExecutionByID(executionID)
	if err != nil || exec == nil || exec.AgentID == nil {
		return
	}

	// Get the agent_user_id from the execution's data (trigger data contains it)
	var triggerData map[string]interface{}
	if exec.Data != nil {
		_ = json.Unmarshal(exec.Data, &triggerData)
	}

	agentUserID, _ := triggerData["agent_user_id"].(string)
	if agentUserID == "" {
		return
	}

	// Expire any existing identity_link PAs so the latest one takes precedence.
	existingPAs, _ := s.persistence.GetOpenPendingActionsForUser(agentUserID)
	for _, existing := range existingPAs {
		if existing.Type == "identity_link" {
			_ = s.persistence.UpdatePendingActionStatus(existing.ID, "expired")
		}
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"channel_type": channelType,
		"external_id":  externalID,
	})

	expires := time.Now().Add(24 * time.Hour)
	paID, err := s.persistence.CreateAgentPendingAction(api.AgentPendingAction{
		AgentID:     *exec.AgentID,
		AgentUserID: agentUserID,
		Type:        "identity_link",
		Payload:     payload,
		Evidence:    fmt.Sprintf("AI offered to link %s identity: %s", channelType, externalID),
		Status:      "awaiting_confirmation",
		ExpiresAt:   &expires,
	})
	if err != nil {
		log.WithFields(log.Fields{
			"agent_id": *exec.AgentID,
			"error":    err,
		}).Error("failed to create identity_link PA from LINK_OFFER event")
		return
	}

	// Mark as notified immediately — the AI already asked the user in its
	// response, so the poller shouldn't re-notify and checkPendingActionConfirmation
	// should accept the user's "yes" without waiting for NotifiedAt.
	if paID != nil {
		_ = s.persistence.MarkPendingActionNotified(*paID)
	}

	log.WithFields(log.Fields{
		"agent_id":     *exec.AgentID,
		"channel_type": channelType,
		"external_id":  externalID,
	}).Info("identity_link PA created from LINK_OFFER event")
}

func (s *Service) streamExecutionLogs(c *gin.Context) {
	id := c.Param("id")

	// Verify the execution exists
	exec, err := s.persistence.GetExecutionByID(id)
	if err != nil || exec == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	ch, buffered := s.logHub.Subscribe(id)
	defer s.logHub.Unsubscribe(id, ch)

	// Send buffered lines first
	for _, line := range buffered {
		if strings.HasPrefix(line, "__NODE__:") {
			nodeData := strings.TrimPrefix(line, "__NODE__:")
			_, _ = fmt.Fprintf(c.Writer, "event: node\ndata: %s\n\n", nodeData)
		} else if strings.HasPrefix(line, "__STATUS__:") {
			status := strings.TrimPrefix(line, "__STATUS__:")
			_, _ = fmt.Fprintf(c.Writer, "event: status\ndata: %s\n\n", status)
		} else {
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", line)
		}
	}
	c.Writer.Flush()

	// If execution is already complete, send completion and close
	if exec.CompletionStatus != "pending" {
		_, _ = fmt.Fprintf(c.Writer, "event: complete\ndata: %s\n\n", exec.CompletionStatus)
		c.Writer.Flush()
		return
	}

	// Stream new lines as they arrive
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return
			}
			if line == "__COMPLETE__" {
				// Re-fetch to get final status
				final, _ := s.persistence.GetExecutionByID(id)
				status := "unknown"
				if final != nil {
					status = final.CompletionStatus
				}
				_, _ = fmt.Fprintf(c.Writer, "event: complete\ndata: %s\n\n", status)
				c.Writer.Flush()
				return
			}
			if strings.HasPrefix(line, "__NODE__:") {
				nodeData := strings.TrimPrefix(line, "__NODE__:")
				_, _ = fmt.Fprintf(c.Writer, "event: node\ndata: %s\n\n", nodeData)
				c.Writer.Flush()
				continue
			}
			if strings.HasPrefix(line, "__STATUS__:") {
				status := strings.TrimPrefix(line, "__STATUS__:")
				_, _ = fmt.Fprintf(c.Writer, "event: status\ndata: %s\n\n", status)
				c.Writer.Flush()
				continue
			}
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", line)
			c.Writer.Flush()

		case <-ticker.C:
			// Keep-alive comment
			_, _ = fmt.Fprintf(c.Writer, ": keepalive\n\n")
			c.Writer.Flush()

		case <-c.Request.Context().Done():
			return
		}
	}
}

// issueStreamToken creates a short-lived opaque token for SSE authentication,
// avoiding exposure of long-lived JWTs in query parameters.
func (s *Service) issueStreamToken(c *gin.Context) {
	userIDFromContext, exists := c.Get("account_id")
	if !exists {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	token := s.streamTokens.Issue(userIDFromContext.(string))
	c.JSON(http.StatusOK, gin.H{"token": token})
}

// sendExecutionNotification checks if the flow has notification settings
// and sends an email for the given completion status.
func (s *Service) sendExecutionNotification(floID string, completion string, execution *api.Execution) {
	flo, err := s.persistence.GetFloByID(floID)
	if err != nil || flo == nil {
		return
	}

	shouldNotify := (completion == "success" && flo.NotifyOnSuccess) ||
		(completion == "fail" && flo.NotifyOnFailure)

	if !shouldNotify {
		return
	}

	// Determine recipients: use configured emails, or fall back to flow author.
	emailList := ""
	if flo.NotificationEmails != nil && *flo.NotificationEmails != "" {
		emailList = *flo.NotificationEmails
	} else if flo.AuthorID != nil && *flo.AuthorID != "" {
		author, err := s.persistence.GetUserByID(*flo.AuthorID)
		if err == nil && author != nil && author.EmailAddress != nil && *author.EmailAddress != "" {
			emailList = *author.EmailAddress
		}
	}
	if emailList == "" {
		return
	}

	if s.config.SMTP.Host == "" {
		log.Warn("notification email configured but SMTP not set up")
		return
	}

	recipients := strings.Split(emailList, ",")
	for i := range recipients {
		recipients[i] = strings.TrimSpace(recipients[i])
	}

	status := "succeeded"
	statusEmoji := "✅"
	if completion == "fail" {
		status = "failed"
		statusEmoji = "❌"
	}

	subject := fmt.Sprintf("Flow %q %s", flo.Name, status)

	titleStatus := strings.ToUpper(status[:1]) + status[1:]
	header := fmt.Sprintf("%s Flow %s", statusEmoji, titleStatus)
	message := fmt.Sprintf(
		"Your flow <strong>%s</strong> has %s.<br><br>"+
			"<strong>Execution ID:</strong> %s<br>"+
			"<strong>Status:</strong> %s<br>"+
			"<strong>Timestamp:</strong> %s",
		html.EscapeString(flo.Name),
		html.EscapeString(status),
		html.EscapeString(execution.ID),
		strings.ToUpper(html.EscapeString(completion)),
		time.Now().UTC().Format(time.RFC1123),
	)

	htmlBody := renderNotificationEmail(header, message, execution.ID)

	msg := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=\"UTF-8\"\r\n\r\n%s",
		s.config.SMTP.From,
		strings.Join(recipients, ", "),
		subject,
		htmlBody,
	)

	addr := fmt.Sprintf("%s:%d", s.config.SMTP.Host, s.config.SMTP.Port)

	var auth smtp.Auth
	if s.config.SMTP.Username != "" {
		auth = smtp.PlainAuth("", s.config.SMTP.Username, s.config.SMTP.Password, s.config.SMTP.Host)
	}

	if err := smtp.SendMail(addr, auth, s.config.SMTP.From, recipients, []byte(msg)); err != nil {
		log.WithFields(log.Fields{
			"error":      err,
			"flo_id":     floID,
			"recipients": recipients,
		}).Error("failed to send notification email")
	} else {
		log.WithFields(log.Fields{
			"flo_id":     floID,
			"recipients": recipients,
			"status":     completion,
		}).Info("notification email sent")
	}
}

const notificationEmailTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Flomation</title>
    <style>
        body { margin: 0; padding: 0; background-color: #f4f4f7; font-family: 'Helvetica Neue', Helvetica, Arial, sans-serif; -webkit-font-smoothing: antialiased; }
        table { border-spacing: 0; }
        td { padding: 0; }
        @media screen and (max-width: 600px) {
            .content { width: 100% !important; border-radius: 0 !important; }
            .wrapper { padding: 10px !important; }
        }
    </style>
</head>
<body>
<table width="100%" border="0" cellspacing="0" cellpadding="0" bgcolor="#f4f4f7" class="wrapper" style="padding: 40px 0;">
    <tr>
        <td align="center">
            <table width="600" border="0" cellspacing="0" cellpadding="0" class="content" style="background-color: #fff; border-radius: 12px; overflow: hidden; box-shadow: 0 4px 10px rgba(0,0,0,0.05);">
                <tr>
                    <td style="padding: 40px 40px 20px 40px; text-align: left;">
                        <a href="https://www.flomation.app"><img width="300" height="84" src="https://flomation-dev-static.s3.eu-west-2.amazonaws.com/flomation_logo_purple_300px.png" alt="Flomation" title="Flomation"/></a>
                    </td>
                </tr>
                <tr>
                    <td style="padding: 20px 40px 40px 40px;">
                        <h1 style="font-size: 28px; color: #1a1a1a; margin: 0 0 20px 0; line-height: 1.2;">
                            {{.Header}}
                        </h1>
                        <p style="font-size: 16px; line-height: 1.6; color: #4b5563; margin: 0 0 25px 0;">
                            {{.Message}}
                        </p>
                        <table border="0" cellspacing="0" cellpadding="0">
                            <tr>
                                <td align="center" bgcolor="#460070" style="border-radius: 8px;">
                                    <a href="{{.ButtonURL}}" target="_blank" style="font-size: 16px; font-weight: bold; color: #ffffff; text-decoration: none; padding: 14px 30px; display: inline-block;">
                                        {{.ButtonText}}
                                    </a>
                                </td>
                            </tr>
                        </table>
                    </td>
                </tr>
                <tr>
                    <td style="padding: 0 40px;">
                        <hr style="border: 0; border-top: 1px solid #e5e7eb; margin: 0;">
                    </td>
                </tr>
                <tr>
                    <td style="padding: 30px 40px 40px 40px; background-color: #fafafa;">
                        <table width="100%" border="0" cellspacing="0" cellpadding="0">
                            <tr>
                                <td style="font-size: 10px; color: #9ca3af; line-height: 1.5;">
                                    <strong>Flomation Ltd</strong><br>
                                    Ruscoe House, The Chequer, Whitchurch, Wrexham, Wales, SY13 2JJ<br/><br/>
                                    This is an automated notification from the Flomation workflow platform.<br/><br/>
                                    Execution ID: {{.TransactionID}}<br/>
                                    Sent: {{.TransactionTime}}
                                </td>
                            </tr>
                        </table>
                    </td>
                </tr>
            </table>
            <p style="font-size: 12px; color: #9ca3af; margin-top: 20px;">
                &copy; 2026 Flomation Ltd. All rights reserved.
            </p>
        </td>
    </tr>
</table>
</body>
</html>`

func renderNotificationEmail(header, message, executionID string) string {
	tmpl, err := htmltemplate.New("notification").Parse(notificationEmailTemplate)
	if err != nil {
		log.WithError(err).Error("failed to parse notification email template")
		return message // fallback to plain text
	}

	data := struct {
		Header          string
		Message         htmltemplate.HTML
		ButtonText      string
		ButtonURL       string
		TransactionID   string
		TransactionTime string
	}{
		Header:          header,
		Message:         htmltemplate.HTML(message), // #nosec G203 -- inputs are HTML-escaped via html.EscapeString before interpolation
		ButtonText:      "View Execution",
		ButtonURL:       "https://www.flomation.app",
		TransactionID:   executionID,
		TransactionTime: time.Now().UTC().Format(time.RFC1123),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		log.WithError(err).Error("failed to render notification email")
		return message
	}
	return buf.String()
}

// handleSuspendedExecution processes a suspended execution result from the runner.
// Stores the checkpoint, records the execution segment, and updates status.
func (s *Service) handleSuspendedExecution(c *gin.Context, id string, execution *api.Execution, result api.ExecutionResult) {
	// Extract checkpoint and suspend info from state
	stateMap, ok := result.State.(map[string]interface{})
	if !ok {
		// Try to unmarshal if it's raw JSON bytes
		if raw, ok := result.State.(json.RawMessage); ok {
			_ = json.Unmarshal(raw, &stateMap)
		}
	}

	// Store checkpoint from state
	var checkpoint json.RawMessage
	if stateMap != nil {
		if cp, ok := stateMap["checkpoint"]; ok {
			checkpoint, _ = json.Marshal(cp)
		}
	}

	// Record execution segment
	var durationMs int64
	if stateMap != nil {
		if d, ok := stateMap["duration"].(float64); ok {
			durationMs = int64(d)
		}
	}
	segment := api.ExecutionSegment{
		StartedAt:  execution.CreatedAt.Format(time.RFC3339),
		EndedAt:    time.Now().UTC().Format(time.RFC3339),
		DurationMs: durationMs,
		Status:     "suspended",
	}
	if execution.RunnerID != nil {
		segment.RunnerID = *execution.RunnerID
	}

	s.appendExecutionSegment(id, segment)

	// Store checkpoint
	if len(checkpoint) > 0 {
		if err := s.persistence.SaveExecutionCheckpoint(id, checkpoint); err != nil {
			log.WithError(err).Error("unable to save checkpoint")
		}
	}

	// Extract resume_at from suspend info
	if stateMap != nil {
		if cp, ok := stateMap["checkpoint"].(map[string]interface{}); ok {
			if si, ok := cp["suspend_info"].(map[string]interface{}); ok {
				if resumeAt, ok := si["resume_at"].(string); ok {
					if t, err := time.Parse(time.RFC3339, resumeAt); err == nil {
						if err := s.persistence.SetExecutionResumeAt(id, t); err != nil {
							log.WithError(err).Error("unable to set resume_at")
						}
					}
				}
				if triggerType, ok := si["resume_trigger_type"].(string); ok && triggerType != "" {
					matchJSON, _ := json.Marshal(si["resume_trigger_match"])
					if err := s.persistence.SetExecutionResumeTrigger(id, triggerType, matchJSON); err != nil {
						log.WithError(err).Error("unable to set resume trigger")
					}
				}
			}
		}
	}

	// Update status to suspended
	if err := s.persistence.UpdateExecutionStatus(id, "suspended"); err != nil {
		log.WithError(err).Error("unable to update execution status to suspended")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if err := s.persistence.UpdateCompletionStatus(id, "suspended"); err != nil {
		log.WithError(err).Error("unable to update completion status to suspended")
	}
	if err := s.persistence.IncrementSuspendCount(id); err != nil {
		log.WithError(err).Error("unable to increment suspend count")
	}

	// Store the result (without checkpoint, to keep the result column lean)
	j, _ := json.Marshal(result.State)
	_ = s.persistence.UpdateExecutionResult(id, j)

	// Accumulate billing duration (don't overwrite previous segments)
	if durationMs > 0 {
		_ = s.persistence.AccumulateBillingDuration(id, durationMs)
	}

	// Notify SSE subscribers
	s.logHub.Publish(id, []string{"__STATUS__:suspended"})

	log.WithFields(log.Fields{
		"execution_id": id,
		"duration_ms":  durationMs,
	}).Info("execution suspended")

	c.Status(http.StatusOK)
}

// resumeExecution handles POST /execution/:id/resume
func (s *Service) resumeExecution(c *gin.Context) {
	id := c.Param("id")

	execution, err := s.persistence.GetExecutionByID(id)
	if err != nil || execution == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if execution.ExecutionStatus != "suspended" {
		c.JSON(http.StatusConflict, gin.H{"error": "execution is not suspended"})
		return
	}

	// Optionally accept resume data (for event-based resumes)
	var resumeData map[string]interface{}
	_ = c.ShouldBindJSON(&resumeData)

	// Re-queue: set status back to created so runner picks it up
	if err := s.persistence.UpdateExecutionStatus(id, "created"); err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if err := s.persistence.UpdateCompletionStatus(id, "pending"); err != nil {
		log.WithError(err).Error("unable to update completion status")
	}

	// Clear resume_at (it's being resumed now)
	_ = s.persistence.ClearResumeAt(id)

	// Notify long-polling runners that work is available
	s.executionNotifier.Notify("")

	// Notify SSE subscribers
	s.logHub.Publish(id, []string{"__STATUS__:resuming"})

	log.WithFields(log.Fields{
		"execution_id": id,
	}).Info("execution resumed")

	c.Status(http.StatusOK)
}

// appendExecutionSegment adds a timing segment to the execution's segments array.
func (s *Service) appendExecutionSegment(id string, segment api.ExecutionSegment) {
	segJSON, _ := json.Marshal(segment)
	if err := s.persistence.AppendExecutionSegment(id, segJSON); err != nil {
		log.WithError(err).Error("unable to append execution segment")
	}
}
