package http

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// completeWelcomeRequest is the post-EULA welcome modal's submit body.
// Display name is required (per design decision: hard-required to
// dismiss the modal); marketing opt-in defaults to false.
type completeWelcomeRequest struct {
	Name           string `json:"name" binding:"required"`
	MarketingOptIn bool   `json:"marketing_opt_in,omitempty"`
}

// completeWelcome handles POST /user/welcome-complete. Atomic write
// of the display name + marketing choice + a welcome_completed_at
// timestamp that stops the modal re-appearing. EmailOctopus delivery
// is queued via the retry poller — the user's save returns
// immediately regardless of EO's availability.
func (s *Service) completeWelcome(c *gin.Context) {
	u := s.getUserFromContext(c)
	if u == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var req completeWelcomeRequest
	if err := c.BindJSON(&req); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "display name is required"})
		return
	}

	if err := s.persistence.CompleteUserWelcome(u.ID, name, req.MarketingOptIn); err != nil {
		log.WithFields(log.Fields{
			"error":   err,
			"user_id": u.ID,
		}).Error("unable to complete welcome")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"name":             name,
		"marketing_opt_in": req.MarketingOptIn,
	})
}

// setMarketingOptInRequest is the profile Communications toggle's
// body — a single boolean.
type setMarketingOptInRequest struct {
	MarketingOptIn bool `json:"marketing_opt_in"`
}

// setMarketingOptIn handles POST /user/marketing-opt-in. Flips the
// marketing flag and resets the sync state so the EmailOctopus retry
// poller pushes the new value out on its next tick. Fire-and-forget
// per the design decision — a slow EO never blocks the user's save.
func (s *Service) setMarketingOptIn(c *gin.Context) {
	u := s.getUserFromContext(c)
	if u == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var req setMarketingOptInRequest
	if err := c.BindJSON(&req); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.persistence.SetUserMarketingOptIn(u.ID, req.MarketingOptIn); err != nil {
		log.WithFields(log.Fields{
			"error":   err,
			"user_id": u.ID,
		}).Error("unable to update marketing opt-in")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, gin.H{"marketing_opt_in": req.MarketingOptIn})
}
