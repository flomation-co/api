package http

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type updateOnboardingRequest struct {
	Step      int  `json:"step" binding:"required"`
	Completed bool `json:"completed,omitempty"`
}

func (s *Service) updateOnboardingProgress(c *gin.Context) {
	u := s.getUserFromContext(c)
	if u == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var req updateOnboardingRequest
	if err := c.BindJSON(&req); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if req.Step < 0 || req.Step > 7 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "step must be between 0 and 7"})
		return
	}

	var completedAt *time.Time
	if req.Completed {
		now := time.Now()
		completedAt = &now
	}

	if err := s.persistence.UpdateOnboardingProgress(u.ID, req.Step, completedAt); err != nil {
		log.WithFields(log.Fields{
			"error":   err,
			"user_id": u.ID,
			"step":    req.Step,
		}).Error("unable to update onboarding progress")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"onboarding_step": req.Step,
		"completed":       req.Completed,
	})
}

type updateChecklistRequest struct {
	Flag  int  `json:"flag" binding:"required"`
	Clear bool `json:"clear,omitempty"`
}

func (s *Service) updateChecklist(c *gin.Context) {
	u := s.getUserFromContext(c)
	if u == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var req updateChecklistRequest
	if err := c.BindJSON(&req); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Bitmask: only allow known flags (bits 0-4)
	if req.Flag < 1 || req.Flag > 16 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid flag value"})
		return
	}

	var err error
	if req.Clear {
		err = s.persistence.ClearChecklistFlag(u.ID, req.Flag)
	} else {
		err = s.persistence.SetChecklistFlag(u.ID, req.Flag)
	}
	if err != nil {
		log.WithFields(log.Fields{
			"error":   err,
			"user_id": u.ID,
			"flag":    req.Flag,
		}).Error("unable to update checklist flag")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, gin.H{"checklist_flags": u.ChecklistFlags | req.Flag})
}
