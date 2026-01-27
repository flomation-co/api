package http

import (
	"net/http"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (s *Service) getUser(c *gin.Context) {

	u := s.getUserFromContext(c)
	if u == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	user, err := s.persistence.GetUserByID(u.ID)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get user by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if user == nil {
		c.Status(http.StatusNoContent)
		return
	}

	tkn := s.getTokenFromContext(c)

	a, err := s.identity.GetAccount(*tkn)
	if err != nil {
		log.WithFields(log.Fields{
			"Error": err,
		}).Error("unable to get identity account")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	user.EmailAddress = &a.Username

	c.JSON(http.StatusOK, user)
}

func (s *Service) getUserByID(c *gin.Context) {
	userID := c.Param("ID")

	user, err := s.persistence.GetUserByID(userID)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get user by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if user == nil {
		c.Status(http.StatusNoContent)
		return
	}

	c.JSON(http.StatusOK, user)
}

func (s *Service) createUser(c *gin.Context) {
	c.AbortWithStatus(http.StatusNotImplemented)
}

func (s *Service) updateUser(c *gin.Context) {
	u := s.getUserFromContext(c)
	if u == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var updatedUser api.User
	if err := c.BindJSON(&updatedUser); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to bind json")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	updatedUser.ID = u.ID

	if err := s.persistence.UpdateUser(&updatedUser); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to update user")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusOK, updatedUser)
}
