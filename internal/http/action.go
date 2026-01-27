package http

import (
	"encoding/json"
	"net/http"
	"strconv"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (s *Service) getActions(c *gin.Context) {
	actions, err := s.persistence.GetActions()
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get actions")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	mappedActions := make(map[string]api.ActionDefinition)

	for _, a := range actions {
		a.Type, _ = strconv.ParseInt(a.ActionType, 10, 64)

		if a.Inputs != nil {
			var inputs []api.InputDefinition
			if err := json.Unmarshal(a.Inputs.([]byte), &inputs); err != nil {
				log.WithFields(log.Fields{
					"error": err,
				}).Error("unable to get actions")
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}
			a.Inputs = inputs
		}

		if a.Outputs != nil {
			var outputs []api.OutputDefinition
			if err := json.Unmarshal(a.Outputs.([]byte), &outputs); err != nil {
				log.WithFields(log.Fields{
					"error": err,
				}).Error("unable to get actions")
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}
			a.Outputs = outputs
		}

		mappedActions[a.ID] = *a
	}

	c.JSON(http.StatusOK, mappedActions)
}
