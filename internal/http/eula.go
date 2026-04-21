package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type eulaResponse struct {
	Version int    `json:"version"`
	Content string `json:"content"`
}

type acceptEulaRequest struct {
	Version int `json:"version" binding:"required"`
}

func (s *Service) getEula(c *gin.Context) {
	c.JSON(http.StatusOK, eulaResponse{
		Version: s.config.Eula.Version,
		Content: s.config.Eula.Content,
	})
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

	if req.Version != s.config.Eula.Version {
		c.JSON(http.StatusConflict, gin.H{
			"error":           "eula_version_mismatch",
			"current_version": s.config.Eula.Version,
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
