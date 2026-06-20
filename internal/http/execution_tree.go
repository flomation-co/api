package http

// Hierarchical executions endpoints. These three handlers are the
// read-side of the execution-hierarchy machinery introduced in
// migration 93:
//
//   GET /api/v1/execution-tree/:rootID       — full subtree (the
//                                              expanded-row payload)
//   GET /api/v1/execution/:id/ancestors      — chain root→parent
//                                              (drives the breadcrumb)
//   GET /api/v1/execution/:id/children       — direct children only
//                                              (lazy-load on expand)
//
// Each one gates access on the row's organisation_id so a user of
// org A can't read a tree belonging to org B even if they happen to
// know the root_execution_id.

import (
	"net/http"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/rbac"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// authoriseExecutionAccess loads the execution and confirms the
// requesting user can see it. Returns (true, nil) when access is
// granted, (false, _) when not — and writes the appropriate status
// to the response so callers can simply return.
func (s *Service) authoriseExecutionAccess(c *gin.Context, id string) bool {
	if !s.checkPermission(c, rbac.FlowExecute) {
		return false
	}
	exec, err := s.persistence.GetExecutionByID(id)
	if err != nil {
		log.WithFields(log.Fields{
			"execution_id": id,
			"error":        err,
		}).Error("unable to load execution for authorisation")
		c.AbortWithStatus(http.StatusInternalServerError)
		return false
	}
	if exec == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return false
	}
	user := s.getUserFromContext(c)
	if !s.verifyOrgAccess(user, exec.OrganisationID) {
		c.AbortWithStatus(http.StatusForbidden)
		return false
	}
	return true
}

// getExecutionTree returns the full subtree rooted at the given
// execution ID. Used by the editor's executions list when a root row
// is expanded.
func (s *Service) getExecutionTree(c *gin.Context) {
	rootID := c.Param("rootID")
	if !s.authoriseExecutionAccess(c, rootID) {
		return
	}
	rows, err := s.persistence.GetExecutionTree(rootID)
	if err != nil {
		log.WithFields(log.Fields{
			"root_id": rootID,
			"error":   err,
		}).Error("unable to fetch execution tree")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	enrichExecutionsWithCosts(s, rows)
	c.JSON(http.StatusOK, rows)
}

// enrichExecutionsWithCosts stamps credit_cost_pence onto each row
// in-place. Without it the COST column on every child row in the
// hierarchy view renders blank — the tree fetch goes through a
// different path to getExecutions which already enriches. Errors
// from the cost lookup are swallowed: an enriched response with
// missing costs is strictly better than a 500.
func enrichExecutionsWithCosts(s *Service, rows []*api.Execution) {
	if len(rows) == 0 {
		return
	}
	ids := make([]string, len(rows))
	for i, e := range rows {
		ids[i] = e.ID
	}
	costs, err := s.persistence.GetCreditCostsForExecutions(ids)
	if err != nil || len(costs) == 0 {
		return
	}
	for _, e := range rows {
		if cost, ok := costs[e.ID]; ok {
			e.CreditCostPence = &cost
		}
	}
}

// getExecutionAncestors returns the chain from the root down to (but
// excluding) the given execution. The editor's detail-view breadcrumb
// renders the result as "(root) ▸ parent ▸ …".
func (s *Service) getExecutionAncestors(c *gin.Context) {
	id := c.Param("id")
	if !s.authoriseExecutionAccess(c, id) {
		return
	}
	rows, err := s.persistence.GetExecutionAncestors(id)
	if err != nil {
		log.WithFields(log.Fields{
			"execution_id": id,
			"error":        err,
		}).Error("unable to fetch execution ancestors")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusOK, rows)
}

// getExecutionChildren returns the direct children of the given
// execution. Used by the editor when expanding an intermediate row
// inside an already-fetched subtree — though in practice the full
// tree fetch already returns these, this endpoint exists for callers
// (Agent Planning's tick endpoint, dashboards) that only need one
// level.
func (s *Service) getExecutionChildren(c *gin.Context) {
	id := c.Param("id")
	if !s.authoriseExecutionAccess(c, id) {
		return
	}
	rows, err := s.persistence.GetExecutionDirectChildren(id)
	if err != nil {
		log.WithFields(log.Fields{
			"execution_id": id,
			"error":        err,
		}).Error("unable to fetch execution children")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusOK, rows)
}
