package http

import (
	"encoding/json"
	"net/http"
	"strings"

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
	s.registerTriggerWithLaunch(*id, trigger, s.extractAuthToken(c))

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
	s.registerTriggerWithLaunch(id, trigger, s.extractAuthToken(c))

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
	if err := s.launch.DisableTrigger(id, s.extractAuthToken(c)); err != nil {
		log.WithFields(log.Fields{
			"error":      err,
			"trigger_id": id,
		}).Warn("unable to disable trigger on launch service")
	}

	c.Status(http.StatusOK)
}

// resolveTriggerVariables resolves ${secrets.X} and ${env.X} references
// for a trigger using its flow's assigned environment.
func (s *Service) resolveTriggerVariables(c *gin.Context) {
	triggerID := c.Param("id")

	trigger, err := s.persistence.GetTriggerByID(triggerID)
	if err != nil || trigger == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if trigger.FloID == nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	flo, err := s.persistence.GetFloByID(*trigger.FloID)
	if err != nil || flo == nil || flo.EnvironmentID == nil {
		// No environment — return empty resolution
		c.JSON(http.StatusOK, gin.H{})
		return
	}

	env, err := s.persistence.GetEnvironmentByIDDirect(*flo.EnvironmentID)
	if err != nil || env == nil {
		c.JSON(http.StatusOK, gin.H{})
		return
	}

	var request struct {
		Variables []string `json:"variables"`
	}
	if err := c.BindJSON(&request); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	resolved := make(map[string]string)
	for _, v := range request.Variables {
		if strings.HasPrefix(v, "secrets.") || strings.HasPrefix(v, "secret.") {
			name := strings.TrimPrefix(v, "secrets.")
			name = strings.TrimPrefix(name, "secret.")
			sec, err := s.persistence.GetEnvironmentSecretByName(env.ID, env.SecretKey, name)
			if err == nil && sec != nil {
				resolved[v] = sec.Value
			}
		} else if strings.HasPrefix(v, "env.") {
			name := strings.TrimPrefix(v, "env.")
			prop, err := s.persistence.GetEnvironmentPropertyByName(env.ID, env.SecretKey, name)
			if err == nil && prop != nil {
				resolved[v] = prop.Value
			}
		}
	}

	c.JSON(http.StatusOK, resolved)
}

func (s *Service) extractAuthToken(c *gin.Context) string {
	header := c.GetHeader("Authorization")
	parts := strings.Split(header, " ")
	if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" {
		return parts[1]
	}
	return ""
}

func (s *Service) registerTriggerWithLaunch(id string, trigger api.Trigger, authToken string) {
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

	if err := s.launch.RegisterTrigger(id, trigger.TypeName, dataBytes, flowID, authToken); err != nil {
		log.WithFields(log.Fields{
			"error":      err,
			"trigger_id": id,
		}).Warn("unable to register trigger with launch service")
	}
}
