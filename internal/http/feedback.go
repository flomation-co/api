package http

import (
	"net/http"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (s *Service) submitFeedback(c *gin.Context) {
	var feedback api.Feedback
	if err := c.BindJSON(&feedback); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to bind feedback")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if feedback.Subject == "" || feedback.Category == "" || feedback.Message == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "subject, category and message are required"})
		return
	}

	user := s.getUserFromContext(c)
	if user != nil {
		feedback.UserID = &user.ID
	}

	if err := s.persistence.CreateFeedback(feedback); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to save feedback")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusCreated, gin.H{"status": "ok"})
}
