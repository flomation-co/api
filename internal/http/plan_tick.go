package http

// Agent Planning M1 — the orchestrator's tick endpoint. The Launch-
// side poller (M1 commit 8) wakes this on a schedule for every
// active plan; the completion writeback hook (M1 commit 6) wakes it
// reactively when a plan task's execution finishes.
//
// The handler is intentionally a thin shim over the persistence
// TickPlan orchestration method — all the transactional logic
// (FOR UPDATE lock, ready-task discovery, dispatch, status
// derivation, audit events) lives in plan_tick.go on the persistence
// service so the HTTP layer is free of database concerns and
// trivially mockable.

import (
	"errors"
	"net/http"

	"flomation.app/automate/api/internal/persistence"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// tickPlan handles POST /api/v1/internal/plan/:planID/tick.
//
// Status code map:
//   - 200 with TickPlanResult body when the tick completed (whether
//     or not anything fired).
//   - 204 when the plan is already in a terminal status — nothing to
//     do, the caller should stop polling it.
//   - 404 when the plan doesn't exist.
//   - 409 when another instance is currently ticking the same plan;
//     the poller's lease-based ownership normally prevents this but
//     a manual tick during a poll race can hit it.
//   - 500 on unexpected persistence failures.
func (s *Service) tickPlan(c *gin.Context) {
	planID := c.Param("planID")
	if planID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "planID required"})
		return
	}

	result, err := s.persistence.TickPlan(c.Request.Context(), planID)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, result)
	case errors.Is(err, persistence.ErrPlanNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "plan not found"})
	case errors.Is(err, persistence.ErrPlanTerminal):
		c.Status(http.StatusNoContent)
	case errors.Is(err, persistence.ErrPlanLocked):
		c.JSON(http.StatusConflict, gin.H{"error": "plan is being ticked by another instance"})
	default:
		log.WithFields(log.Fields{
			"plan_id": planID,
			"error":   err,
		}).Error("plan tick: persistence failed")
		c.AbortWithStatus(http.StatusInternalServerError)
	}
}
