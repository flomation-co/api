package http

import (
	"fmt"
	"net/http"
	"strings"

	"flomation.app/automate/api/internal/actions"
	"flomation.app/automate/api/internal/connector/identity"
	launchconnector "flomation.app/automate/api/internal/connector/launch"
	"github.com/flomation-co/sentinel-client"

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
	persistence Persistence
	identity    *identity.Connector
	launch      *launchconnector.Connector
	migrator    *actions.Migrator
	logHub      *LogHub
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
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	if strings.ToLower(headerParts[0]) != "bearer" {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	userID, err := sentinel.GetUser(s.config.Security.IdentityService, headerParts[1])
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
			"url":   s.config.Security.IdentityService,
		}).Error("unable to contact identity service")
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	c.Set("account_id", *userID)
	c.Set("jwt", headerParts[1])

	organisationID := c.Query("organisation")
	if organisationID != "" {
		c.Set("organisation_id", organisationID)
	}

	c.Next()
}

// streamAuthMiddleware authenticates SSE connections using a query parameter token,
// since EventSource does not support custom headers.
func (s *Service) streamAuthMiddleware(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	userID, err := sentinel.GetUser(s.config.Security.IdentityService, token)
	if err != nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	c.Set("account_id", *userID)
	c.Set("jwt", token)
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
		launch:      launchconnector.NewConnector(config),
		migrator:    m,
		logHub:      NewLogHub(),
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
	orgs.GET("/:ID/member", s.getOrganisationMembers)
	orgs.GET("/:ID/invite", s.getOrganisationInvites)

	orgs.POST("", s.createOrganisation)
	orgs.POST("/:ID", s.updateOrganisation)
	orgs.POST("/:ID/invite", s.createOrganisationInvite)

	orgs.DELETE("/:ID/member/:userID", s.removeOrganisationMember)
	orgs.DELETE("/:ID/invite/:inviteID", s.revokeOrganisationInvite)

	// RBAC Groups
	orgs.GET("/:ID/group", s.getOrganisationGroups)
	orgs.GET("/:ID/group/:groupID", s.getGroupByID)
	orgs.POST("/:ID/group", s.createOrganisationGroup)
	orgs.POST("/:ID/group/:groupID", s.updateGroup)
	orgs.DELETE("/:ID/group/:groupID", s.deleteGroup)
	orgs.GET("/:ID/group/:groupID/member", s.getGroupMembers)
	orgs.POST("/:ID/group/:groupID/member", s.addGroupMember)
	orgs.DELETE("/:ID/group/:groupID/member/:userID", s.removeGroupMember)
	orgs.POST("/:ID/group/:groupID/permission", s.setGroupPermissions)
	orgs.GET("/:ID/permissions", s.getMyPermissions)

	// Invite preview (public, no auth required)
	v1.GET("invite/:code", s.getInvitePreview)
	// Invite acceptance (authenticated but not org-scoped)
	v1.POST("invite/:code/accept", s.jwtMiddleware, s.acceptOrganisationInvite)

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

	flos.POST("/export", s.jwtMiddleware, s.exportFlos)
	flos.POST("/import", s.jwtMiddleware, s.importFlo)
	flos.POST("/:FloID/revision", s.jwtMiddleware, s.createFloRevision)

	flos.POST("/:FloID/execute", s.executeFlo)
	flos.POST("/:FloID/trigger/:TriggerID/execute", s.triggerFlo)

	favourites := v1.Group("favourite")
	favourites.Use(s.jwtMiddleware)
	favourites.GET("", s.getFloFavourites)
	favourites.POST("/:FloID", s.addFloFavourite)
	favourites.DELETE("/:FloID", s.removeFloFavourite)

	executions := v1.Group("execution")
	executions.POST("/:id/state", s.executionMiddleware, s.updateExecutionState)
	executions.POST("/:id/logs", s.executionMiddleware, s.appendExecutionLogs)
	executions.POST("/:id", s.executionMiddleware, s.updateExecution)

	executions.GET("", s.jwtMiddleware, s.getExecutions)
	executions.GET("/:id", s.jwtMiddleware, s.getExecutionByID)
	executions.GET("/:id/stream", s.streamAuthMiddleware, s.streamExecutionLogs)

	executions.GET("/:id/environment/:environment", s.executionMiddleware, s.getExecutionEnvironment)
	executions.GET("/:id/environment/:environment/property/:name", s.executionMiddleware, s.getExecutionEnvironmentProperty)
	executions.GET("/:id/environment/:environment/secret/:name", s.executionMiddleware, s.getExecutionEnvironmentSecret)

	runners := v1.Group("runner")
	runners.GET("", s.jwtMiddleware, s.getRunners)
	runners.POST("", s.registerRunner)
	runners.POST("/:id/execution", s.runnerMiddleware, s.checkForRunnerExecutions)
	runners.DELETE("/:id", s.jwtMiddleware, s.unregisterRunner)

	queue := v1.Group("queue")
	queue.Use(s.jwtMiddleware)
	queue.GET("", s.getQueues)
	queue.POST("", s.createQueue)
	queue.DELETE("/:id", s.deleteQueue)
	queue.GET("/:id/runner", s.getQueueRunners)
	queue.POST("/:id/runner", s.addRunnerToQueue)
	queue.DELETE("/:id/runner/:runnerID", s.removeRunnerFromQueue)

	triggers := v1.Group("trigger")
	triggers.GET("", s.jwtMiddleware, s.getTriggers)
	triggers.GET("/:id", s.jwtMiddleware, s.getTriggerByID)
	triggers.POST("", s.jwtMiddleware, s.createTrigger)
	triggers.POST("/:id", s.jwtMiddleware, s.updateTrigger)
	triggers.DELETE("/:id", s.jwtMiddleware, s.deleteTrigger)
	triggers.POST("/:id/resolve", s.resolveTriggerVariables)

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
	environment.POST("/:environment/secret/:id", s.jwtMiddleware, s.updateEnvironmentSecretByID)
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

// verifyOrgAccess checks that the resource's organisation_id matches the
// user's current org context. In personal mode (no org selected), only
// resources with null organisation_id are accessible. In org mode, only
// resources belonging to that organisation are accessible.
func (s *Service) verifyOrgAccess(user *api.User, resourceOrgID *string) bool {
	if len(user.Organisations) > 0 {
		// Org mode — resource must belong to this org
		return resourceOrgID != nil && *resourceOrgID == user.Organisations[0].ID
	}
	// Personal mode — resource must have no org
	return resourceOrgID == nil
}
