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
	Flag           int     `json:"flag" binding:"required"`
	Clear          bool    `json:"clear,omitempty"`
	OrganisationID *string `json:"organisation_id,omitempty"`
}

// Bitmask catalogue — must match the editor's checklist widget.
// Splits cleanly into two scopes:
//
//   * GLOBAL — properties of the human, not the org. One value
//     across every context. Lives on users.checklist_flags.
//
//   * ORG-SCOPED — properties of the work the user has done in
//     a specific (user, org) context. Lives on
//     user_checklist_state, keyed by (user_id, organisation_id)
//     with NULL org for personal mode.
const (
	checklistFlagProfileName   = 1
	checklistFlagCreateFlow    = 2
	checklistFlagExecuteFlow   = 4
	checklistFlagConfigureEnv  = 8
	checklistFlagInviteTeam    = 16
	checklistFlagEnableMFA     = 32

	checklistGlobalMask   = checklistFlagProfileName | checklistFlagEnableMFA
	checklistOrgMask      = checklistFlagCreateFlow | checklistFlagExecuteFlow | checklistFlagConfigureEnv | checklistFlagInviteTeam
	checklistAllValidMask = checklistGlobalMask | checklistOrgMask
)

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

	if req.Flag < 1 || req.Flag > checklistAllValidMask || (req.Flag&(req.Flag-1)) != 0 {
		// Reject anything outside the catalogue or composite values
		// (only single-bit values are meaningful for the per-call API).
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid flag value"})
		return
	}

	// Route by scope. Global bits write to users.checklist_flags;
	// org-scoped bits write to user_checklist_state with the caller's
	// current org context (NULL = personal mode).
	var err error
	if req.Flag&checklistGlobalMask != 0 {
		if req.Clear {
			err = s.persistence.ClearChecklistFlag(u.ID, req.Flag)
		} else {
			err = s.persistence.SetChecklistFlag(u.ID, req.Flag)
		}
	} else {
		if req.Clear {
			err = s.persistence.ClearUserChecklistFlagForOrg(u.ID, req.OrganisationID, req.Flag)
		} else {
			err = s.persistence.SetUserChecklistFlagForOrg(u.ID, req.OrganisationID, req.Flag)
		}
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

// getChecklist returns the combined effective flags (global OR org-
// scoped) for the user in the given org context. Caller passes
// organisation_id as a query param; absent or empty = personal mode.
//
// This is the source of truth the editor's checklist widget reads on
// mount + on every org switch — it composes the two storage layers
// into the single bitmask the UI renders.
func (s *Service) getChecklist(c *gin.Context) {
	u := s.getUserFromContext(c)
	if u == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var organisationID *string
	if q := c.Query("organisation_id"); q != "" {
		organisationID = &q
	}

	orgFlags, err := s.persistence.GetUserChecklistStateForOrg(u.ID, organisationID)
	if err != nil {
		log.WithFields(log.Fields{
			"error":   err,
			"user_id": u.ID,
		}).Error("unable to read org checklist state")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	// Global bits come straight off the user row; org-scoped bits
	// come from the table. Mask each side so a stale bit in the
	// wrong column (e.g. an unmigrated org-scoped bit still on
	// users.checklist_flags during the rollout window) doesn't leak
	// across contexts.
	combined := (u.ChecklistFlags & checklistGlobalMask) | (orgFlags & checklistOrgMask)

	c.JSON(http.StatusOK, gin.H{
		"checklist_flags": combined,
		"organisation_id": organisationID,
	})
}
