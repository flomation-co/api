package http

import (
	"encoding/json"
	"net/http"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (s *Service) unregisterRunner(c *gin.Context) {

}

func (s *Service) getRunners(c *gin.Context) {
	runners, err := s.persistence.GetRunners()
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get runners")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if len(runners) == 0 {
		c.Status(http.StatusNoContent)
		return
	}

	c.JSON(http.StatusOK, runners)
}

func (s *Service) registerRunner(c *gin.Context) {
	var request api.Runner

	if err := c.BindJSON(&request); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to bind json")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	r, err := s.persistence.GetRunnerByIdentifier(request.Identifier)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to check existing runner")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if r != nil {
		if err := s.persistence.UpdateRunnerLastContact(r.ID, c.ClientIP()); err != nil {
			log.WithFields(log.Fields{
				"error": err,
			}).Error("unable to update existing runner")
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}

		c.Status(http.StatusCreated)
		return
	}

	q, err := s.persistence.GetQueueByRegistrationCode(request.RegistrationCode)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to find queue")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if q == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	id, err := s.persistence.EnrolRunner(request)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to enrol runner")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if id == nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	runner, err := s.persistence.GetRunnerByID(*id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
			"id":    id,
		}).Error("unable to get runner")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusOK, runner)
}

func (s *Service) checkForRunnerExecutions(c *gin.Context) {
	id := c.Param("id")

	runner, err := s.persistence.GetRunnerByIdentifier(id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("invalid runner")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.persistence.UpdateRunnerLastContact(runner.ID, c.ClientIP()); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to update runner last contact")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if !runner.Active {
		c.Status(http.StatusNoContent)
		return
	}

	execution, err := s.persistence.GetExecutionForRunnerID(id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to check for execution")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if execution == nil {
		c.Status(http.StatusNoContent)
		return
	}

	flow, err := s.persistence.GetFloByID(execution.FloID)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to load flow")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	rev, err := s.persistence.GetLatestRevisionByFloID(flow.ID)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get latest revision for Flo")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if rev != nil {
		if rev.Data != nil {
			var revision interface{}

			if err := json.Unmarshal(rev.Data.([]byte), &revision); err != nil {
				log.WithFields(log.Fields{
					"error": err,
				}).Error("unable to unmarshal revision data")
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}

			rev.Data = revision
		}
	}

	if err := s.persistence.UpdateExecutionStatus(execution.ID, "allocated"); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to mark execution")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := s.persistence.UpdateExecutionRunnerID(execution.ID, runner.ID); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to execution runner ID")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	pe := api.PendingExecution{
		Flow:      *flow,
		Execution: *execution,
		Data:      rev.Data,
	}

	c.JSON(http.StatusOK, pe)
}
