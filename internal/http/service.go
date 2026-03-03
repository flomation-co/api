package http

import (
	"flomation.app/automate/api/internal/actions"
	"flomation.app/automate/api/internal/connector/identity"
	"fmt"
	"github.com/flomation-co/sentinel-client"
	"net/http"
	"strings"

	"flomation.app/automate/api/internal/version"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/config"
	"flomation.app/automate/api/internal/persistence"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type Service struct {
	config      *config.Config
	engine      *gin.Engine
	persistence *persistence.Service
	identity    *identity.Connector
	migrator    *actions.Migrator
}

func corsMiddleware(c *gin.Context) {
	c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
	c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
	c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With, X-Total-Items")
	c.Writer.Header().Set("Access-Control-Expose-Headers", "X-Total-Items")
	c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

	if c.Request.Method == "OPTIONS" {
		c.AbortWithStatus(204)
		return
	}

	c.Next()
}

func hstsMiddleware(c *gin.Context) {
	c.Writer.Header().Set("Strict-Transport-Security", "max-age=31536000")

	c.Next()
}

func (s *Service) jwtMiddleware(c *gin.Context) {
	header := c.GetHeader("Authorization")
	headerParts := strings.Split(header, " ")
	if len(headerParts) != 2 {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if strings.ToLower(headerParts[0]) != "bearer" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	userID, err := sentinel.GetUser(s.config.Security.IdentityService, headerParts[1])
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
			"url":   s.config.Security.IdentityService,
			"token": headerParts[1],
		}).Error("unable to contact identity service")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	c.Set("account_id", *userID)

	organisationID := c.Query("organisation")
	if organisationID != "" {
		c.Set("organisation_id", organisationID)
	}

	c.Next()
}

func NewService(config *config.Config, persistence *persistence.Service) *Service {
	m, err := actions.NewMigrator(config)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to create migration service")
		return nil
	}

	s := &Service{
		config:      config,
		engine:      gin.New(),
		persistence: persistence,
		identity:    identity.NewConnector(config),
		migrator:    m,
	}

	// API Group
	s.engine.Use(corsMiddleware, hstsMiddleware)

	s.engine.GET("version", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"version":    version.Version,
			"build_date": version.BuiltDate,
			"hash":       version.GetHash(),
		})
	})

	a := s.engine.Group("api")

	// v1 Group
	v1 := a.Group("v1")

	v1.GET("dashboard", s.jwtMiddleware, s.getDashboardData)

	// Organisations Group
	orgs := v1.Group("organisation")
	orgs.Use(s.jwtMiddleware)
	orgs.GET("", s.getMyOrganisations)
	orgs.GET("/:ID", s.getOrganisation)

	orgs.POST("", s.createOrganisation)
	orgs.POST("/:ID", s.updateOrganisation)

	users := v1.Group("user")
	users.Use(s.jwtMiddleware)
	users.GET("", s.getUser)
	users.GET("/:ID", s.getUserByID)

	users.POST("", s.createUser)
	users.POST("/:ID", s.updateUser)

	actions := v1.Group("action")
	actions.GET("", s.getActions)

	flos := v1.Group("flo")
	//flos.Use(s.jwtMiddleware)

	flos.GET("", s.jwtMiddleware, s.getMyFlos)
	flos.GET("/:FloID", s.jwtMiddleware, s.getFloByID)

	flos.POST("", s.jwtMiddleware, s.createFlo)
	flos.POST("/:FloID", s.jwtMiddleware, s.updateFlo)
	flos.DELETE("/:FloID", s.jwtMiddleware, s.deleteFlo)

	flos.POST("/:FloID/revision", s.jwtMiddleware, s.createFloRevision)

	flos.POST("/:FloID/trigger/:TriggerID/execute", s.triggerFlo)

	executions := v1.Group("execution")
	executions.POST("/:id/state", s.executionMiddleware, s.updateExecutionState)
	executions.POST("/:id", s.executionMiddleware, s.updateExecution)

	executions.GET("", s.jwtMiddleware, s.getExecutions)
	executions.GET("/:id", s.jwtMiddleware, s.getExecutionByID)

	runners := v1.Group("runner")
	runners.GET("", s.jwtMiddleware, s.getRunners)
	runners.POST("", s.registerRunner)
	runners.GET("/:id/execution", s.runnerMiddleware, s.checkForRunnerExecutions)
	runners.DELETE("/:id", s.jwtMiddleware, s.unregisterRunner)

	queue := v1.Group("queue")
	queue.GET("", s.jwtMiddleware, s.getQueues)

	environment := v1.Group("environment")
	environment.GET("", s.jwtMiddleware, s.getEnvironments)
	environment.GET("/:environment", s.jwtMiddleware, s.getEnvironmentByID)
	environment.POST("", s.jwtMiddleware, s.createEnvironment)
	environment.DELETE("/:environment", s.jwtMiddleware, s.deleteEnvironment)

	environment.GET("/:environment/property", s.jwtMiddleware, s.getEnvironmentProperties)
	environment.GET("/:environment/property/:name", s.jwtMiddleware, s.getEnvironmentPropertyByName)
	environment.POST("/:environment/property", s.jwtMiddleware, s.createEnvironmentProperty)
	environment.POST("/:environment/property/:id", s.jwtMiddleware, s.updateEnvironmentPropertyByID)
	environment.DELETE("/:environment/property/:id", s.jwtMiddleware, s.deleteEnvironmentPropertyByID)

	environment.GET("/:environment/secret", s.jwtMiddleware, s.getEnvironmentSecrets)
	environment.GET("/:environment/secret/:name", s.jwtMiddleware, s.getEnvironmentSecretByName)
	environment.POST("/:environment/secret", s.jwtMiddleware, s.createEnvironmentSecret)
	environment.DELETE("/:environment/secret/:id", s.jwtMiddleware, s.deleteEnvironmentSecretByID)

	return s
}

func (s *Service) Listen() error {
	return s.engine.Run(fmt.Sprintf("%v:%v", s.config.HttpListenConfig.Address, s.config.HttpListenConfig.Port))
}

func (s *Service) getTokenFromContext(c *gin.Context) *string {
	tkn, exists := c.Get("jwt")
	if !exists {
		return nil
	}

	token := tkn.(string)
	return &token
}

func (s *Service) getUserFromContext(c *gin.Context) *api.User {
	userIDFromContext, exists := c.Get("account_id")
	if !exists {
		log.Error("no user in context - SHOULD NOT HAPPEN")
		return nil
	}

	u, err := s.persistence.GetUserByID(userIDFromContext.(string))
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get user from context")
		return nil
	}

	if u == nil {
		userID, err := s.persistence.CreateUser(&api.User{
			ID:   userIDFromContext.(string),
			Name: "auto-generate",
		})
		if err != nil {
			log.WithFields(log.Fields{
				"error": err,
			}).Error("unable to get create new user by id")
			return nil
		}

		u, err = s.persistence.GetUserByID(*userID)
		if err != nil {
			log.WithFields(log.Fields{
				"error": err,
			}).Error("unable to get user from context")
			return nil
		}
	}

	organisationIDFromContext, exists := c.Get("organisation_id")
	if exists {
		u.Organisations = append(u.Organisations, api.Organisation{
			ID: organisationIDFromContext.(string),
		})
	}

	return u
}
