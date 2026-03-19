package http

import (
	"encoding/json"
	"net/http"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (s *Service) getTriggers(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	triggers, err := s.persistence.GetTriggers(user.ID)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get triggers")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if len(triggers) == 0 {
		c.AbortWithStatus(http.StatusNoContent)
		return
	}

	c.JSON(http.StatusOK, triggers)
}

func (s *Service) getTriggerByID(c *gin.Context) {
	id := c.Param("id")

	trigger, err := s.persistence.GetTriggerByID(id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get trigger by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if trigger == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.JSON(http.StatusOK, trigger)
}

func (s *Service) createTrigger(c *gin.Context) {
	var trigger api.Trigger
	if err := c.BindJSON(&trigger); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to bind JSON")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	trigger.OwnerID = &user.ID

	if len(user.Organisations) > 0 {
		trigger.OrganisationID = &user.Organisations[0].ID
	}

	if trigger.TypeName == "" {
		trigger.TypeName = "manual"
	}

	id, err := s.persistence.CreateTriggerWithType(trigger)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to create trigger")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Register with Launch Service (best-effort)
	s.registerTriggerWithLaunch(*id, trigger)

	created, err := s.persistence.GetTriggerByID(*id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get created trigger")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusCreated, created)
}

func (s *Service) updateTrigger(c *gin.Context) {
	id := c.Param("id")

	existing, err := s.persistence.GetTriggerByID(id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get trigger by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if existing == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var trigger api.Trigger
	if err := c.BindJSON(&trigger); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to bind JSON")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	trigger.ID = id

	if err := s.persistence.UpdateTrigger(trigger); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to update trigger")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Re-register with Launch Service (best-effort)
	s.registerTriggerWithLaunch(id, trigger)

	updated, err := s.persistence.GetTriggerByID(id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get updated trigger")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusOK, updated)
}

func (s *Service) deleteTrigger(c *gin.Context) {
	id := c.Param("id")

	existing, err := s.persistence.GetTriggerByID(id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get trigger by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if existing == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if err := s.persistence.DeleteTrigger(id); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to delete trigger")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Disable on Launch Service (best-effort)
	if err := s.launch.DisableTrigger(id); err != nil {
		log.WithFields(log.Fields{
			"error":      err,
			"trigger_id": id,
		}).Warn("unable to disable trigger on launch service")
	}

	c.Status(http.StatusOK)
}

func (s *Service) registerTriggerWithLaunch(id string, trigger api.Trigger) {
	var dataBytes []byte
	if trigger.Data != nil {
		var err error
		dataBytes, err = json.Marshal(trigger.Data)
		if err != nil {
			log.WithFields(log.Fields{
				"error":      err,
				"trigger_id": id,
			}).Warn("unable to marshal trigger data for launch service")
			return
		}
	}

	flowID := ""
	if trigger.FloID != nil {
		flowID = *trigger.FloID
	}

	if err := s.launch.RegisterTrigger(id, trigger.TypeName, dataBytes, flowID); err != nil {
		log.WithFields(log.Fields{
			"error":      err,
			"trigger_id": id,
		}).Warn("unable to register trigger with launch service")
	}
}
