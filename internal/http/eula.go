package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type acceptEulaRequest struct {
	Version int `json:"version" binding:"required"`
}

func (s *Service) getEula(c *gin.Context) {
	eula, err := s.persistence.GetLatestEula()
	if err != nil {
		log.WithField("error", err).Error("unable to fetch latest eula")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	if eula == nil {
		c.Status(http.StatusNoContent)
		return
	}

	c.JSON(http.StatusOK, eula)
}

func (s *Service) acceptEula(c *gin.Context) {
	u := s.getUserFromContext(c)
	if u == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var req acceptEulaRequest
	if err := c.BindJSON(&req); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Verify the version being accepted is the current one.
	latest, err := s.persistence.GetLatestEula()
	if err != nil || latest == nil {
		log.WithField("error", err).Error("unable to fetch latest eula for validation")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	if req.Version != latest.Version {
		c.JSON(http.StatusConflict, gin.H{
			"error":           "eula_version_mismatch",
			"current_version": latest.Version,
		})
		return
	}

	if err := s.persistence.AcceptEula(u.ID, req.Version); err != nil {
		log.WithFields(log.Fields{
			"error":   err,
			"user_id": u.ID,
			"version": req.Version,
		}).Error("unable to accept eula")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"accepted_version": req.Version,
	})
}
