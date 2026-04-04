package http

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"flomation.app/automate/api"
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

	c.Status(http.StatusOK)
}

func (s *Service) getExecutionByID(c *gin.Context) {
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
	}

	c.Status(http.StatusOK)
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

	if !shouldNotify || flo.NotificationEmails == nil || *flo.NotificationEmails == "" {
		return
	}

	if s.config.SMTP.Host == "" {
		log.Warn("notification email configured but SMTP not set up")
		return
	}

	recipients := strings.Split(*flo.NotificationEmails, ",")
	for i := range recipients {
		recipients[i] = strings.TrimSpace(recipients[i])
	}

	status := "succeeded"
	if completion == "fail" {
		status = "failed"
	}

	subject := fmt.Sprintf("Flow %q %s", flo.Name, status)
	body := fmt.Sprintf(
		"Flow: %s\nExecution ID: %s\nStatus: %s\nTimestamp: %s\n\n—\nFlomation · www.flomation.co",
		flo.Name,
		execution.ID,
		strings.ToUpper(completion),
		time.Now().UTC().Format(time.RFC3339),
	)

	msg := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		s.config.SMTP.From,
		strings.Join(recipients, ", "),
		subject,
		body,
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
