package http

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// createWebThreadInternal mints a web-invoke conversation thread, binding it to
// the forwarded end-user when a valid Sentinel token is present (anonymous
// otherwise). Internal (mTLS) — called by the Launch invoke handler.
// POST /api/v1/internal/web-thread  { flow_id }  → { thread_id }
func (s *Service) createWebThreadInternal(c *gin.Context) {
	var body struct {
		FlowID string `json:"flow_id"`
	}
	_ = c.ShouldBindJSON(&body)
	if body.FlowID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "flow_id required"})
		return
	}
	var userID *string
	if uid := s.forwardedUserID(c); uid != "" {
		userID = &uid
	}
	id, err := s.persistence.CreateWebThread(body.FlowID, userID)
	if err != nil {
		log.WithError(err).Error("web thread: create failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"thread_id": id})
}

// getWebThreadHistoryInternal returns the recent turns of a thread, oldest first.
// GET /api/v1/internal/web-thread/:id/history?limit=N  → { turns: [{role,content}] }
func (s *Service) getWebThreadHistoryInternal(c *gin.Context) {
	limit := 20
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	turns, err := s.persistence.GetWebThreadHistory(c.Param("id"), limit)
	if err != nil {
		log.WithError(err).Error("web thread: history failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusOK, gin.H{"turns": turns})
}

// appendWebThreadTurnInternal records one turn on a thread.
// POST /api/v1/internal/web-thread/:id/turn  { role, content }
func (s *Service) appendWebThreadTurnInternal(c *gin.Context) {
	var body struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || (body.Role != "user" && body.Role != "assistant") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "role must be user or assistant"})
		return
	}
	if err := s.persistence.AppendWebThreadTurn(c.Param("id"), body.Role, body.Content); err != nil {
		log.WithError(err).Error("web thread: append failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.Status(http.StatusCreated)
}
