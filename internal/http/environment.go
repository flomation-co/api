package http

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/utils"
	log "github.com/sirupsen/logrus"

	"github.com/gin-gonic/gin"
)

// validateExecutionEnvironment checks that the requested environment matches
// the flow's assigned environment. Returns the environment if valid, or aborts
// the request and returns nil.
func (s *Service) validateExecutionEnvironment(c *gin.Context) *api.Environment {
	executionID := c.Param("id")
	environmentParam := c.Param("environment")

	execution, err := s.persistence.GetExecutionByID(executionID)
	if err != nil || execution == nil {
		log.WithFields(log.Fields{
			"error":        err,
			"execution_id": executionID,
		}).Error("unable to get execution for environment validation")
		c.AbortWithStatus(http.StatusForbidden)
		return nil
	}

	flo, err := s.persistence.GetFloByID(execution.FloID)
	if err != nil || flo == nil {
		log.WithFields(log.Fields{
			"error":  err,
			"flo_id": execution.FloID,
		}).Error("unable to get flow for environment validation")
		c.AbortWithStatus(http.StatusForbidden)
		return nil
	}

	if flo.EnvironmentID == nil {
		log.WithFields(log.Fields{
			"flo_id": flo.ID,
		}).Warn("flow has no environment assigned")
		c.AbortWithStatus(http.StatusForbidden)
		return nil
	}

	// Look up the environment directly — the flow-environment binding check below
	// provides the access control, so we don't need ownership filters here.
	// This ensures execution context can access the flow's environment regardless
	// of whether it's personal or org-owned.
	var env *api.Environment
	if err := uuid.Validate(environmentParam); err == nil {
		env, err = s.persistence.GetEnvironmentByIDDirect(environmentParam)
		if err != nil {
			log.WithFields(log.Fields{"error": err}).Error("unable to get environment by id")
			c.AbortWithStatus(http.StatusBadRequest)
			return nil
		}
	} else {
		// For name-based lookup, we still need user context
		user := s.getUserFromContext(c)
		if user == nil {
			c.AbortWithStatus(http.StatusBadRequest)
			return nil
		}
		var organisation *string
		if len(user.Organisations) > 0 {
			organisation = &user.Organisations[0].ID
		}
		env, err = s.persistence.GetEnvironmentByName(environmentParam, user.ID, organisation)
		if err != nil {
			log.WithFields(log.Fields{"error": err}).Error("unable to get environment by name")
			c.AbortWithStatus(http.StatusBadRequest)
			return nil
		}
	}

	if env == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return nil
	}

	// Verify the resolved environment matches the flow's assigned environment
	if env.ID != *flo.EnvironmentID {
		log.WithFields(log.Fields{
			"requested_env": env.ID,
			"flow_env":      *flo.EnvironmentID,
			"flo_id":        flo.ID,
			"execution_id":  executionID,
		}).Warn("execution attempted to access environment not assigned to flow")
		c.AbortWithStatus(http.StatusForbidden)
		return nil
	}

	return env
}

func (s *Service) getExecutionEnvironment(c *gin.Context) {
	env := s.validateExecutionEnvironment(c)
	if env == nil {
		return
	}

	c.JSON(http.StatusOK, env)
}

func (s *Service) getExecutionEnvironmentProperty(c *gin.Context) {
	env := s.validateExecutionEnvironment(c)
	if env == nil {
		return
	}

	name := c.Param("name")

	prop, err := s.persistence.GetEnvironmentPropertyByName(env.ID, env.SecretKey, name)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environment property")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if prop == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.JSON(http.StatusOK, prop)
}

func (s *Service) getExecutionEnvironmentSecret(c *gin.Context) {
	env := s.validateExecutionEnvironment(c)
	if env == nil {
		return
	}

	name := c.Param("name")

	prop, err := s.persistence.GetEnvironmentSecretByName(env.ID, env.SecretKey, name)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environment secret")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if prop == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	// Execution context always needs the decrypted value
	prop.DecryptedValue = &prop.Value

	c.JSON(http.StatusOK, prop)
}

func (s *Service) getEnvironments(c *gin.Context) {
	user := s.getUserFromContext(c)
	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}

	envs, err := s.persistence.GetEnvironments(user.ID, organisation)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environments for user")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if len(envs) == 0 {
		c.AbortWithStatus(http.StatusNoContent)
		return
	}

	c.JSON(http.StatusOK, envs)
}

func (s *Service) getEnvironmentByID(c *gin.Context) {
	id := c.Param("environment")
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}

	var env *api.Environment
	if err := uuid.Validate(id); err == nil {
		env, err = s.persistence.GetEnvironmentByID(id, user.ID, organisation)
		if err != nil {
			log.WithFields(log.Fields{
				"error": err,
			}).Error("unable to get environment by id")
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
	} else {
		env, err = s.persistence.GetEnvironmentByName(id, user.ID, organisation)
		if err != nil {
			log.WithFields(log.Fields{
				"error": err,
			}).Error("unable to get environment by name")
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
	}

	if env == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if !s.verifyOrgAccess(user, env.OrganisationID) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	c.JSON(http.StatusOK, env)
}

func (s *Service) createEnvironment(c *gin.Context) {
	user := s.getUserFromContext(c)
	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}

	var env api.Environment
	if err := c.BindJSON(&env); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to bind json")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	env.OwnerID = user.ID
	env.OrganisationID = organisation
	env.SecretKey = utils.GenerateRandomStringID(32)

	id, err := s.persistence.CreateEnvironment(env)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to create environment")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	createdEnv, err := s.persistence.GetEnvironmentByID(*id, user.ID, organisation)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get new environment")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.JSON(http.StatusOK, createdEnv)
}

func (s *Service) deleteEnvironment(c *gin.Context) {
	id := c.Param("environment")
	user := s.getUserFromContext(c)
	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}

	env, err := s.persistence.GetEnvironmentByID(id, user.ID, organisation)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environment by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if env == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if err := s.persistence.DeleteEnvironmentByID(id); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to delete environment")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusOK)
}

func (s *Service) getEnvironmentSecrets(c *gin.Context) {
	environmentID := c.Param("environment")
	user := s.getUserFromContext(c)
	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}

	env, err := s.persistence.GetEnvironmentByID(environmentID, user.ID, organisation)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environment by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if env == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	props, err := s.persistence.GetEnvironmentSecrets(environmentID, env.SecretKey)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environment secrets")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if len(props) == 0 {
		c.Status(http.StatusNoContent)
		return
	}

	c.JSON(http.StatusOK, props)
}

func (s *Service) getEnvironmentProperties(c *gin.Context) {
	environmentID := c.Param("environment")
	user := s.getUserFromContext(c)
	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}

	env, err := s.persistence.GetEnvironmentByID(environmentID, user.ID, organisation)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environment by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if env == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	props, err := s.persistence.GetEnvironmentProperties(environmentID, env.SecretKey)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environment properties")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if len(props) == 0 {
		c.Status(http.StatusNoContent)
		return
	}

	c.JSON(http.StatusOK, props)
}

func (s *Service) getEnvironmentSecretByName(c *gin.Context) {
	environmentID := c.Param("environment")
	user := s.getUserFromContext(c)
	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}

	env, err := s.persistence.GetEnvironmentByID(environmentID, user.ID, organisation)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environment by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if env == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	name := c.Param("name")

	prop, err := s.persistence.GetEnvironmentSecretByName(env.ID, env.SecretKey, name)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environment secret")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if prop == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	decryptQueryParameter := c.DefaultQuery("decrypt", "false")

	decrypt, err := strconv.ParseBool(decryptQueryParameter)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to parse decrypt parameter")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if decrypt {
		prop.DecryptedValue = &prop.Value
	}

	c.JSON(http.StatusOK, prop)
}

func (s *Service) getEnvironmentPropertyByName(c *gin.Context) {
	environmentID := c.Param("environment")
	user := s.getUserFromContext(c)
	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}

	env, err := s.persistence.GetEnvironmentByID(environmentID, user.ID, organisation)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environment by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if env == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	name := c.Param("name")

	prop, err := s.persistence.GetEnvironmentPropertyByName(env.ID, env.SecretKey, name)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environment property")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if prop == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.JSON(http.StatusOK, prop)
}

func (s *Service) createEnvironmentSecret(c *gin.Context) {
	environmentID := c.Param("environment")
	user := s.getUserFromContext(c)
	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}

	env, err := s.persistence.GetEnvironmentByID(environmentID, user.ID, organisation)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environment by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if env == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var property api.CreateEnvironmentSecret
	if err := c.BindJSON(&property); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to bind json")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	property.Provider = "KeyValue"

	_, err = s.persistence.CreateEnvironmentSecret(environmentID, env.SecretKey, property)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to create environment secret")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusCreated)
}

func (s *Service) createEnvironmentProperty(c *gin.Context) {
	environmentID := c.Param("environment")
	user := s.getUserFromContext(c)
	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}

	env, err := s.persistence.GetEnvironmentByID(environmentID, user.ID, organisation)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environment by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if env == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var property api.EnvironmentProperty
	if err := c.BindJSON(&property); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to bind json")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	_, err = s.persistence.CreateEnvironmentProperty(environmentID, env.SecretKey, property)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to create environment property")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusCreated)
}

func (s *Service) deleteEnvironmentSecretByID(c *gin.Context) {
	environmentID := c.Param("environment")
	user := s.getUserFromContext(c)
	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}

	env, err := s.persistence.GetEnvironmentByID(environmentID, user.ID, organisation)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environment by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if env == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	id := c.Param("id")

	prop, err := s.persistence.GetEnvironmentSecretByID(env.ID, env.SecretKey, id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environment secret")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if prop == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if err := s.persistence.RemoveEnvironmentSecret(id); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to remove environment secret")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusOK)
}

func (s *Service) updateEnvironmentPropertyByID(c *gin.Context) {
	environmentID := c.Param("environment")
	user := s.getUserFromContext(c)
	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}

	env, err := s.persistence.GetEnvironmentByID(environmentID, user.ID, organisation)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environment by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if env == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	id := c.Param("id")

	prop, err := s.persistence.GetEnvironmentPropertyByID(env.ID, env.SecretKey, id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environment property")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if prop == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var property api.EnvironmentProperty
	if err := c.BindJSON(&property); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to bind json")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	property.ID = id

	err = s.persistence.UpdateEnvironmentProperty(environmentID, env.SecretKey, property)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to update environment property")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusOK)
}

func (s *Service) deleteEnvironmentPropertyByID(c *gin.Context) {
	environmentID := c.Param("environment")
	user := s.getUserFromContext(c)
	var organisation *string
	if len(user.Organisations) > 0 {
		organisation = &user.Organisations[0].ID
	}

	env, err := s.persistence.GetEnvironmentByID(environmentID, user.ID, organisation)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environment by id")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if env == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	id := c.Param("id")

	prop, err := s.persistence.GetEnvironmentPropertyByID(env.ID, env.SecretKey, id)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get environment property")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if prop == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if err := s.persistence.RemoveEnvironmentProperty(id); err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to remove environment property")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Status(http.StatusOK)
}
