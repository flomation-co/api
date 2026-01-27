package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

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

	c.Status(http.StatusOK)
}

func (s *Service) getExecutionByID(c *gin.Context) {
	id := c.Param("id")

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

	executions, count, err := s.persistence.GetExecutions(offsetStr, limitStr, search, user.ID, nil)
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
