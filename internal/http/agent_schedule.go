package http

import (
	"net/http"
	"time"

	api "flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// ── Internal endpoints (no JWT) ─────────────────────────────────────

type createAgentScheduleRequest struct {
	AgentUserID    *string `json:"agent_user_id,omitempty"`
	ConversationID *string `json:"conversation_id,omitempty"`
	Name           string  `json:"name" binding:"required"`
	Description    string  `json:"description" binding:"required"`
	ScheduleMode   string  `json:"schedule_mode" binding:"required"`
	IntervalVal    *string `json:"interval_val,omitempty"`
	Unit           *string `json:"unit,omitempty"`
	TimeOfDay      *string `json:"time_of_day,omitempty"`
	DaysOfWeek     *string `json:"days_of_week,omitempty"`
	Timezone       string  `json:"timezone,omitempty"`
	SourceChannel  *string `json:"source_channel,omitempty"`
}

func (s *Service) createAgentScheduleInternal(c *gin.Context) {
	agentID := c.Param("id")

	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var body createAgentScheduleRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Validate schedule_mode and required fields per mode.
	switch body.ScheduleMode {
	case "interval":
		if body.IntervalVal == nil || body.Unit == nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error": "interval_val and unit are required for interval mode",
			})
			return
		}
	case "daily":
		if body.TimeOfDay == nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error": "time_of_day is required for daily mode",
			})
			return
		}
	case "weekly":
		if body.TimeOfDay == nil || body.DaysOfWeek == nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error": "time_of_day and days_of_week are required for weekly mode",
			})
			return
		}
	default:
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"error": "schedule_mode must be interval, daily, or weekly",
		})
		return
	}

	// Validate timezone if provided.
	tz := body.Timezone
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"error": "invalid timezone: " + tz,
		})
		return
	}

	id, err := s.persistence.CreateAgentSchedule(api.AgentSchedule{
		AgentID:        agentID,
		AgentUserID:    body.AgentUserID,
		ConversationID: body.ConversationID,
		Name:           body.Name,
		Description:    body.Description,
		ScheduleMode:   body.ScheduleMode,
		IntervalVal:    body.IntervalVal,
		Unit:           body.Unit,
		TimeOfDay:      body.TimeOfDay,
		DaysOfWeek:     body.DaysOfWeek,
		Timezone:       tz,
		SourceChannel:  body.SourceChannel,
	})
	if err != nil {
		log.WithFields(log.Fields{
			"error":    err,
			"agent_id": agentID,
		}).Error("unable to create agent schedule")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": *id})
}

func (s *Service) listAgentSchedulesInternal(c *gin.Context) {
	agentID := c.Param("id")

	schedules, err := s.persistence.GetAgentSchedules(agentID)
	if err != nil {
		log.WithFields(log.Fields{
			"error":    err,
			"agent_id": agentID,
		}).Error("unable to list agent schedules")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if schedules == nil {
		schedules = []*api.AgentSchedule{}
	}

	c.JSON(http.StatusOK, schedules)
}

type updateAgentScheduleRequest struct {
	Name         *string `json:"name,omitempty"`
	Description  *string `json:"description,omitempty"`
	ScheduleMode *string `json:"schedule_mode,omitempty"`
	IntervalVal  *string `json:"interval_val,omitempty"`
	Unit         *string `json:"unit,omitempty"`
	TimeOfDay    *string `json:"time_of_day,omitempty"`
	DaysOfWeek   *string `json:"days_of_week,omitempty"`
	Timezone     *string `json:"timezone,omitempty"`
	Enabled      *bool   `json:"enabled,omitempty"`
}

func (s *Service) updateAgentScheduleInternal(c *gin.Context) {
	id := c.Param("id")

	existing, err := s.persistence.GetAgentScheduleByID(id)
	if err != nil || existing == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var body updateAgentScheduleRequest
	if err := c.BindJSON(&body); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Merge updates into existing.
	if body.Name != nil {
		existing.Name = *body.Name
	}
	if body.Description != nil {
		existing.Description = *body.Description
	}
	if body.ScheduleMode != nil {
		existing.ScheduleMode = *body.ScheduleMode
	}
	if body.IntervalVal != nil {
		existing.IntervalVal = body.IntervalVal
	}
	if body.Unit != nil {
		existing.Unit = body.Unit
	}
	if body.TimeOfDay != nil {
		existing.TimeOfDay = body.TimeOfDay
	}
	if body.DaysOfWeek != nil {
		existing.DaysOfWeek = body.DaysOfWeek
	}
	if body.Timezone != nil {
		if _, err := time.LoadLocation(*body.Timezone); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error": "invalid timezone: " + *body.Timezone,
			})
			return
		}
		existing.Timezone = *body.Timezone
	}
	if body.Enabled != nil {
		existing.Enabled = *body.Enabled
	}

	if err := s.persistence.UpdateAgentSchedule(*existing); err != nil {
		log.WithFields(log.Fields{
			"error":       err,
			"schedule_id": id,
		}).Error("unable to update agent schedule")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.Status(http.StatusNoContent)
}

func (s *Service) deleteAgentScheduleInternal(c *gin.Context) {
	id := c.Param("id")

	if err := s.persistence.DeleteAgentSchedule(id); err != nil {
		log.WithFields(log.Fields{
			"error":       err,
			"schedule_id": id,
		}).Error("unable to delete agent schedule")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.Status(http.StatusNoContent)
}

func (s *Service) deleteAgentScheduleByNameInternal(c *gin.Context) {
	agentID := c.Param("id")
	name := c.Param("name")

	if err := s.persistence.DeleteAgentScheduleByName(agentID, name); err != nil {
		log.WithFields(log.Fields{
			"error":    err,
			"agent_id": agentID,
			"name":     name,
		}).Error("unable to delete agent schedule by name")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.Status(http.StatusNoContent)
}

// ── JWT-protected endpoint (editor UI) ──────────────────────────────

func (s *Service) getAgentSchedules(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	agentID := c.Param("id")
	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if !s.canAccessAgent(user, agent) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	schedules, err := s.persistence.GetAgentSchedules(agentID)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if schedules == nil {
		schedules = []*api.AgentSchedule{}
	}

	c.JSON(http.StatusOK, schedules)
}
