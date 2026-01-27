package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (s *Service) getDashboardData(c *gin.Context) {
	user := s.getUserFromContext(c)

	dashboard, err := s.persistence.GetUsage(user.ID, nil)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get user dashboard")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if dashboard == nil {
		c.Status(http.StatusNoContent)
		return
	}

	c.JSON(http.StatusOK, dashboard)
}
