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

	if req.Step < 0 || req.Step > 6 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "step must be between 0 and 6"})
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
