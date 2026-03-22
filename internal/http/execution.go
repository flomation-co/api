package http

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
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

	c.Status(http.StatusOK)
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
	if result.HasErrored {
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

	if exec.Data != nil {
		var input interface{}
		if err := json.Unmarshal(exec.Data.([]byte), &input); err != nil {
			log.WithFields(log.Fields{
				"error": err,
			}).Error("unable to unmarshal input data")
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		exec.Data = input
	}

	if exec.Result != nil {
		var result interface{}
		if err := json.Unmarshal(exec.Result.([]byte), &result); err != nil {
			log.WithFields(log.Fields{
				"error": err,
			}).Error("unable to unmarshal result data")
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		exec.Result = result
	}

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

	executions, count, err := s.persistence.GetExecutions(offsetStr, limitStr, search, user.ID, orgID)
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
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", line)
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
