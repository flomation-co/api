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
	if tkn != nil {
		a, err := s.identity.GetAccount(*tkn)
		if err != nil {
			log.WithFields(log.Fields{
				"error": err,
			}).Warn("unable to get identity account, returning user without email")
		} else if a != nil {
			user.EmailAddress = &a.Username

			if a.DisplayName != nil && *a.DisplayName != "" {
				user.Name = *a.DisplayName
			}
		}
	}

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

	tkn := s.getTokenFromContext(c)
	if tkn != nil {
		if err := s.identity.UpdateDisplayName(*tkn, updatedUser.Name); err != nil {
			log.WithFields(log.Fields{
				"error": err,
			}).Warn("unable to sync display name to identity service")
		}
	}

	c.JSON(http.StatusOK, updatedUser)
}
