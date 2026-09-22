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
			// Whether we already held an address has to be read before the
			// response is patched, since the patch overwrites it in place.
			missingLocally := user.EmailAddress == nil

			user.EmailAddress = &a.Username

			if a.DisplayName != nil && *a.DisplayName != "" {
				user.Name = *a.DisplayName
			}

			// Top up the stored copy. This handler has always patched the
			// address into the response without writing it down, which is how
			// users.email_address came to be NULL for most accounts while the
			// editor happily displayed an email. The marketing sync reads the
			// stored column, so those users could never be subscribed or
			// unsubscribed — it failed with "missing email address" instead.
			//
			// Gated on the address being absent, so this is one write per
			// account rather than a write on every profile load. Best-effort:
			// the response is already correct either way, and a failure just
			// means the next request tries again.
			if missingLocally && a.Username != "" {
				if _, err := s.persistence.SetUserEmailAddressIfMissing(user.ID, a.Username); err != nil {
					log.WithFields(log.Fields{
						"error":   err,
						"user_id": user.ID,
					}).Warn("unable to persist email address from identity service")
				}
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
