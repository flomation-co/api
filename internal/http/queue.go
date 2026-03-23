package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (s *Service) getQueues(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	if len(user.Organisations) == 0 {
		c.Status(http.StatusNoContent)
		return
	}

	queues, err := s.persistence.GetQueuesByOrganisationID(user.Organisations[0].ID)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get queues")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if len(queues) == 0 {
		c.Status(http.StatusNoContent)
		return
	}

	c.JSON(http.StatusOK, queues)
}

type CreateQueueRequest struct {
	Name     string  `json:"name"`
	ParentID *string `json:"parent_id"`
}

func (s *Service) createQueue(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	if len(user.Organisations) == 0 {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	orgID := user.Organisations[0].ID

	role, err := s.persistence.GetUserRoleInOrganisation(orgID, user.ID)
	if err != nil || role == nil || *role != "admin" {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	var req CreateQueueRequest
	if err := c.BindJSON(&req); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	id, err := s.persistence.CreateQueue(orgID, req.Name, req.ParentID)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to create queue")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	queue, err := s.persistence.GetQueueByID(*id)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusCreated, queue)
}

func (s *Service) deleteQueue(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	if len(user.Organisations) == 0 {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	orgID := user.Organisations[0].ID

	role, err := s.persistence.GetUserRoleInOrganisation(orgID, user.ID)
	if err != nil || role == nil || *role != "admin" {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	queueID := c.Param("id")
	if err := s.persistence.DeleteQueue(queueID, orgID); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to delete queue")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusOK)
}

func (s *Service) getQueueRunners(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	queueID := c.Param("id")
	runners, err := s.persistence.GetQueueRunners(queueID)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to get queue runners")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if len(runners) == 0 {
		c.Status(http.StatusNoContent)
		return
	}

	c.JSON(http.StatusOK, runners)
}

type QueueRunnerRequest struct {
	RunnerID string `json:"runner_id"`
}

func (s *Service) addRunnerToQueue(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	if len(user.Organisations) == 0 {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	role, err := s.persistence.GetUserRoleInOrganisation(user.Organisations[0].ID, user.ID)
	if err != nil || role == nil || *role != "admin" {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	queueID := c.Param("id")
	var req QueueRunnerRequest
	if err := c.BindJSON(&req); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.persistence.AddRunnerToQueue(queueID, req.RunnerID); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to add runner to queue")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusCreated)
}

func (s *Service) removeRunnerFromQueue(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	if len(user.Organisations) == 0 {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	role, err := s.persistence.GetUserRoleInOrganisation(user.Organisations[0].ID, user.ID)
	if err != nil || role == nil || *role != "admin" {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	queueID := c.Param("id")
	runnerID := c.Param("runnerID")

	if err := s.persistence.RemoveRunnerFromQueue(queueID, runnerID); err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to remove runner from queue")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusOK)
}
