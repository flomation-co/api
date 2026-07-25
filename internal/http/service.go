package http

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"flomation.app/automate/api/internal/actions"
	"flomation.app/automate/api/internal/agent"
	"flomation.app/automate/api/internal/connector/identity"
	launchconnector "flomation.app/automate/api/internal/connector/launch"
	"flomation.app/automate/api/internal/embedding"
	appmetrics "flomation.app/automate/api/internal/metrics"
	"flomation.app/automate/api/internal/mtls"
	"github.com/flomation-co/sentinel-client"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"flomation.app/automate/api/internal/version"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/config"
	"flomation.app/automate/api/internal/persistence"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type Service struct {
	config            *config.Config
	engine            *gin.Engine
	internalEngine    *gin.Engine // mTLS-only listener for internal routes
	persistence       Persistence
	identity          *identity.Connector
	launch            *launchconnector.Connector
	migrator          *actions.Migrator
	logHub            *LogHub
	allowedOrigins    []string
	streamTokens      *StreamTokenStore
	executionNotifier *ExecutionNotifier
	// completionNotifier wakes /internal/execution/:id/wait long-polls the instant
	// an execution finishes. Separate from executionNotifier so completions don't
	// also wake idle runners (whose long-polls listen on the global "" key).
	completionNotifier *ExecutionNotifier
	agentSessionHub    *AgentSessionHub
	planEventHub       *PlanEventHub
	promptAssembler    *agent.SystemPromptAssembler
	embeddingProvider  embedding.Provider
	inboundHandler     *agent.InboundHandler
}

func (s *Service) corsMiddleware(c *gin.Context) {
	origin := c.GetHeader("Origin")
	allowedOrigin := s.matchOrigin(origin)

	if allowedOrigin != "" {
		c.Writer.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With, X-Total-Items, X-Flomation-Runner-Signature")
		c.Writer.Header().Set("Access-Control-Expose-Headers", "X-Total-Items")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, PATCH, DELETE")
		c.Writer.Header().Set("Vary", "Origin")
	}

	if c.Request.Method == "OPTIONS" {
		c.AbortWithStatus(204)
		return
	}

	c.Next()
}

// matchOrigin checks the request origin against the configured allowlist.
// Returns the matching origin or empty string if not allowed.
func (s *Service) matchOrigin(origin string) string {
	if origin == "" {
		return ""
	}
	if len(s.allowedOrigins) == 0 {
		// No allowlist configured — allow all (dev mode)
		return origin
	}
	for _, allowed := range s.allowedOrigins {
		if strings.EqualFold(origin, allowed) {
			return origin
		}
	}
	return ""
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

	go s.persistence.TouchUserActivity(*userID)

	organisationID := c.Query("organisation")
	if organisationID != "" {
		c.Set("organisation_id", organisationID)
	}

	c.Next()
}

// streamAuthMiddleware authenticates SSE connections. It first checks for an
// opaque stream token (issued via POST /auth/stream-token), then falls back
// to JWT validation. This avoids exposing long-lived JWTs in query parameters.
func (s *Service) streamAuthMiddleware(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	// Try opaque stream token first
	if userID, ok := s.streamTokens.Validate(token); ok {
		c.Set("account_id", userID)
		c.Next()
		return
	}

	// Fall back to JWT validation
	userID, err := sentinel.GetUser(s.config.Security.IdentityService, token)
	if err != nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	c.Set("account_id", *userID)
	c.Set("jwt", token)
	c.Next()
}

// flexAuthMiddleware accepts either JWT (browser/editor) or runner signature
// (service-to-service) authentication. Used for endpoints that both users and
// runners need to access (e.g. execute endpoints).
func (s *Service) flexAuthMiddleware(c *gin.Context) {
	// Try JWT first
	header := c.GetHeader("Authorization")
	if header != "" {
		headerParts := strings.Split(header, " ")
		if len(headerParts) == 2 && strings.ToLower(headerParts[0]) == "bearer" {
			userID, err := sentinel.GetUser(s.config.Security.IdentityService, headerParts[1])
			if err == nil && userID != nil {
				c.Set("account_id", *userID)
				c.Set("jwt", headerParts[1])

				organisationID := c.Query("organisation")
				if organisationID != "" {
					c.Set("organisation_id", organisationID)
				}

				c.Next()
				return
			}
		}
	}

	// Try runner signature — look for X-Flomation-Runner-Signature header
	sig := c.GetHeader("X-Flomation-Runner-Signature")
	if sig != "" {
		// For runner auth, we need to verify against a registered runner's public key.
		// The runner ID is passed via X-Flomation-Runner-ID header.
		runnerID := c.GetHeader("X-Flomation-Runner-ID")
		if runnerID != "" {
			runner, err := s.persistence.GetRunnerByIdentifier(runnerID)
			if err == nil && runner != nil && runner.PublicKey != nil {
				if err := s.verifyPayload(*runner.PublicKey, c); err == nil {
					c.Set("runner_auth", true)
					c.Next()
					return
				}
			}
		}
	}

	c.AbortWithStatus(http.StatusUnauthorized)
}

// StreamTokenStore manages short-lived opaque tokens for SSE authentication.
type StreamTokenStore struct {
	mu     sync.Mutex
	tokens map[string]streamTokenEntry
}

type streamTokenEntry struct {
	userID    string
	expiresAt time.Time
}

func NewStreamTokenStore() *StreamTokenStore {
	s := &StreamTokenStore{
		tokens: make(map[string]streamTokenEntry),
	}
	go s.cleanup()
	return s
}

// Issue creates a new stream token valid for 60 seconds.
func (s *StreamTokenStore) Issue(userID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	token := uuid.New().String()
	s.tokens[token] = streamTokenEntry{
		userID:    userID,
		expiresAt: time.Now().Add(60 * time.Second),
	}
	return token
}

// Validate checks and consumes a stream token (single-use).
func (s *StreamTokenStore) Validate(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.tokens[token]
	if !ok {
		return "", false
	}
	delete(s.tokens, token)

	if time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.userID, true
}

func (s *StreamTokenStore) cleanup() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		now := time.Now()
		for k, v := range s.tokens {
			if now.After(v.expiresAt) {
				delete(s.tokens, k)
			}
		}
		s.mu.Unlock()
	}
}

func NewService(config *config.Config, persistence *persistence.Service) *Service {
	m, err := actions.NewMigrator(config)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to create migration service")
		return nil
	}

	// Parse allowed origins from config
	var allowedOrigins []string
	if config.Security.AllowedOrigins != "" {
		for _, o := range strings.Split(config.Security.AllowedOrigins, ",") {
			o = strings.TrimSpace(o)
			if o != "" {
				allowedOrigins = append(allowedOrigins, o)
			}
		}
	}

	if len(allowedOrigins) == 0 {
		log.Warn("ALLOWED_ORIGINS not configured — CORS will allow all origins. Set ALLOWED_ORIGINS for production use.")
	}

	s := &Service{
		config:             config,
		engine:             gin.New(),
		persistence:        persistence,
		identity:           identity.NewConnector(config),
		launch:             launchconnector.NewConnector(config),
		migrator:           m,
		logHub:             NewLogHub(),
		allowedOrigins:     allowedOrigins,
		streamTokens:       NewStreamTokenStore(),
		executionNotifier:  NewExecutionNotifier(),
		completionNotifier: NewExecutionNotifier(),
		agentSessionHub:    NewAgentSessionHub(),
		planEventHub:       NewPlanEventHub(),
	}

	// Agent Planning M2 — wire the persistence layer's post-commit
	// event listener to the hub. Persistence calls this listener
	// only AFTER successful tx.Commit() so SSE subscribers see only
	// events that actually persisted (a rollback path silently
	// drops them).
	persistence.SetPlanEventListener(s.planEventHub.Publish)

	// Initialise the system prompt assembler with optional embedding provider.
	s.promptAssembler = s.initPromptAssembler(config)

	// Phase 3+4: initialise inbound message handler with direct dispatch.
	directDispatcher := agent.NewDirectFlowDispatcher(persistence, s.executionNotifier)
	s.inboundHandler = agent.NewInboundHandler(
		&inboundPersistenceAdapter{Service: persistence, notifier: s.executionNotifier, launchURL: config.InternalLaunchURL(), launchClient: s.launch.Client()},
		s.promptAssembler,
		directDispatcher,
	)

	// Start API-side pollers (Phase 2 of Launch → API migration).
	// These replace the pollers that previously ran in Launch and made
	// HTTP calls back to the API — now they use direct DB access.
	s.startPollers(config, persistence)

	s.registerRoutes(config)

	return s
}

// registerRoutes wires every HTTP route onto the service engines. It is kept
// separate from NewService so route registration can be exercised in tests
// without standing up persistence, pollers, or the prompt assembler — see
// service_routes_test.go, which guards against duplicate-route panics such as
// the /trigger/:id/resolve collision that crashed startup when mTLS was off.
func (s *Service) registerRoutes(config *config.Config) {
	if config.Metrics.Enabled {
		s.engine.Use(appmetrics.RequestMetricsMiddleware())
		s.engine.GET("metrics", appmetrics.IPRestrictionMiddleware(config.Metrics.AllowedIPs), gin.WrapH(promhttp.Handler()))
	}

	// API Group
	s.engine.Use(s.corsMiddleware, hstsMiddleware)

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
	v1.GET("quota", s.jwtMiddleware, s.getQuota)
	v1.GET("config/platform", s.jwtMiddleware, s.getPlatformConfig)

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
	orgs.POST("/:ID/group/:groupID/agent", s.addAgentToGroup)
	orgs.DELETE("/:ID/group/:groupID/agent/:agentID", s.removeAgentFromGroup)
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
	users.POST("/eula/accept", s.acceptEula)
	users.POST("/onboarding", s.updateOnboardingProgress)
	users.POST("/checklist", s.updateChecklist)
	users.GET("/checklist", s.getChecklist)
	users.POST("/share", s.sendShareEmail)

	// Welcome modal completion — sets display name + marketing opt-in
	// atomically and stamps welcome_completed_at so the modal stops
	// re-appearing. Mounted in the editor's root layout, gated on
	// (eula_accepted_at NOT NULL AND welcome_completed_at IS NULL).
	users.POST("/welcome-complete", s.completeWelcome)

	// Marketing toggle — the profile Communications section's
	// single-action endpoint. Flips marketing_opt_in and queues an
	// EmailOctopus sync via the retry poller.
	users.POST("/marketing-opt-in", s.setMarketingOptIn)

	// Extended profile fields (salutation, names, address). Surfaces in
	// flows as ${user.X} variables. The PUT endpoint accepts a full
	// payload — fields omitted from the body are treated as cleared
	// (NULL). Compare to /welcome-complete which is partial-merge.
	users.PUT("/profile", s.updateProfile)

	// User-declared channel identities (R2). Replaces the AI-initiated
	// [LINK_OFFER] linking flow with explicit user opt-in declarations.
	users.GET("/identity", s.listUserIdentities)
	users.POST("/identity", s.createUserIdentity)
	users.DELETE("/identity", s.deleteUserIdentity)

	eula := v1.Group("eula")
	eula.GET("", s.getEula)

	actions := v1.Group("action")
	actions.GET("", s.getActions)
	// Dynamic dropdown options for action inputs; see dynamicOptionsMetadata
	// in action.go. The upstream data is public, but the endpoint is
	// auth-gated like the other editor option-fetch proxies.
	actions.GET("/options/openrouter-models", s.jwtMiddleware, s.getOpenRouterModels)
	actions.GET("/options/ollama-models", s.jwtMiddleware, s.getOllamaModels)
	actions.GET("/options/zendesk-groups", s.jwtMiddleware, s.getZendeskGroups)
	actions.GET("/options/zendesk-organizations", s.jwtMiddleware, s.getZendeskOrganizations)
	actions.GET("/options/woocommerce-categories", s.jwtMiddleware, s.getWooCommerceCategories)
	actions.GET("/options/woocommerce-tags", s.jwtMiddleware, s.getWooCommerceTags)
	actions.GET("/options/wordpress-categories", s.jwtMiddleware, s.getWordPressCategories)
	actions.GET("/options/wordpress-tags", s.jwtMiddleware, s.getWordPressTags)
	actions.GET("/options/wordpress-authors", s.jwtMiddleware, s.getWordPressAuthors)
	actions.GET("/options/oracle-compartments", s.jwtMiddleware, s.getOracleCompartments)
	actions.GET("/options/oracle-availability-domains", s.jwtMiddleware, s.getOracleAvailabilityDomains)
	actions.GET("/options/oracle-shapes", s.jwtMiddleware, s.getOracleShapes)
	actions.GET("/options/oracle-images", s.jwtMiddleware, s.getOracleImages)
	actions.GET("/options/oracle-vcns", s.jwtMiddleware, s.getOracleVcns)
	actions.GET("/options/oracle-subnets", s.jwtMiddleware, s.getOracleSubnets)
	actions.GET("/options/oracle-route-tables", s.jwtMiddleware, s.getOracleRouteTables)
	actions.GET("/options/oracle-volumes", s.jwtMiddleware, s.getOracleVolumes)
	actions.GET("/options/oracle-boot-volumes", s.jwtMiddleware, s.getOracleBootVolumes)
	actions.GET("/options/oracle-volume-groups", s.jwtMiddleware, s.getOracleVolumeGroups)
	actions.GET("/options/oracle-load-balancers", s.jwtMiddleware, s.getOracleLoadBalancers)
	actions.GET("/options/oracle-lb-backend-sets", s.jwtMiddleware, s.getOracleLbBackendSets)
	actions.GET("/options/oracle-lb-certificates", s.jwtMiddleware, s.getOracleLbCertificates)
	actions.GET("/options/oracle-lb-shapes", s.jwtMiddleware, s.getOracleLbShapes)
	actions.GET("/options/oracle-lb-policies", s.jwtMiddleware, s.getOracleLbPolicies)
	actions.GET("/options/oracle-lb-protocols", s.jwtMiddleware, s.getOracleLbProtocols)
	actions.GET("/options/oracle-network-load-balancers", s.jwtMiddleware, s.getOracleNetworkLoadBalancers)
	actions.GET("/options/oracle-nlb-backend-sets", s.jwtMiddleware, s.getOracleNlbBackendSets)
	actions.GET("/options/oracle-nlb-policies", s.jwtMiddleware, s.getOracleNlbPolicies)
	actions.GET("/options/oracle-nlb-protocols", s.jwtMiddleware, s.getOracleNlbProtocols)
	actions.GET("/options/oracle-instances", s.jwtMiddleware, s.getOracleInstances)
	actions.GET("/options/oracle-backup-policies", s.jwtMiddleware, s.getOracleBackupPolicies)
	actions.GET("/options/oracle-dns-zones", s.jwtMiddleware, s.getOracleDnsZones)
	actions.GET("/options/oracle-dns-steering-policies", s.jwtMiddleware, s.getOracleDnsSteeringPolicies)
	actions.GET("/options/oracle-dns-steering-policy-attachments", s.jwtMiddleware, s.getOracleDnsSteeringPolicyAttachments)
	actions.GET("/options/oracle-dns-views", s.jwtMiddleware, s.getOracleDnsViews)
	actions.GET("/options/oracle-dns-resolvers", s.jwtMiddleware, s.getOracleDnsResolvers)
	actions.GET("/options/oracle-dns-resolver-endpoints", s.jwtMiddleware, s.getOracleDnsResolverEndpoints)
	actions.GET("/options/oracle-dns-tsig-keys", s.jwtMiddleware, s.getOracleDnsTsigKeys)
	actions.GET("/options/oracle-iam-users", s.jwtMiddleware, s.getOracleIamUsers)
	actions.GET("/options/oracle-iam-groups", s.jwtMiddleware, s.getOracleIamGroups)
	actions.GET("/options/oracle-iam-policies", s.jwtMiddleware, s.getOracleIamPolicies)
	actions.GET("/options/oracle-iam-dynamic-groups", s.jwtMiddleware, s.getOracleIamDynamicGroups)
	actions.GET("/options/oracle-iam-network-sources", s.jwtMiddleware, s.getOracleIamNetworkSources)
	actions.GET("/options/oracle-iam-tag-namespaces", s.jwtMiddleware, s.getOracleIamTagNamespaces)
	actions.GET("/options/oracle-iam-identity-providers", s.jwtMiddleware, s.getOracleIamIdentityProviders)
	actions.GET("/options/oracle-fss-file-systems", s.jwtMiddleware, s.getOracleFssFileSystems)
	actions.GET("/options/oracle-fss-mount-targets", s.jwtMiddleware, s.getOracleFssMountTargets)
	actions.GET("/options/oracle-fss-export-sets", s.jwtMiddleware, s.getOracleFssExportSets)
	actions.GET("/options/oracle-fss-exports", s.jwtMiddleware, s.getOracleFssExports)
	actions.GET("/options/oracle-fss-snapshots", s.jwtMiddleware, s.getOracleFssSnapshots)
	actions.GET("/options/oracle-fss-snapshot-policies", s.jwtMiddleware, s.getOracleFssSnapshotPolicies)
	actions.GET("/options/oracle-fss-replications", s.jwtMiddleware, s.getOracleFssReplications)
	actions.GET("/options/oracle-fss-outbound-connectors", s.jwtMiddleware, s.getOracleFssOutboundConnectors)
	actions.GET("/options/oracle-vault-vaults", s.jwtMiddleware, s.getOracleVaultVaults)
	actions.GET("/options/oracle-vault-keys", s.jwtMiddleware, s.getOracleVaultKeys)
	actions.GET("/options/oracle-vault-key-versions", s.jwtMiddleware, s.getOracleVaultKeyVersions)
	actions.GET("/options/oracle-vault-secrets", s.jwtMiddleware, s.getOracleVaultSecrets)
	actions.GET("/options/oracle-notifications-topics", s.jwtMiddleware, s.getOracleNotificationsTopics)
	actions.GET("/options/oracle-notifications-subscriptions", s.jwtMiddleware, s.getOracleNotificationsSubscriptions)
	actions.GET("/options/oracle-oke-clusters", s.jwtMiddleware, s.getOracleOKEClusters)
	actions.GET("/options/oracle-oke-node-pools", s.jwtMiddleware, s.getOracleOKENodePools)
	actions.GET("/options/oracle-oke-virtual-node-pools", s.jwtMiddleware, s.getOracleOKEVirtualNodePools)
	actions.GET("/options/oracle-exadata-infrastructures", s.jwtMiddleware, s.getOracleExadataInfrastructures)
	actions.GET("/options/oracle-exadata-vm-clusters", s.jwtMiddleware, s.getOracleExadataVmClusters)
	actions.GET("/options/oracle-streaming-streams", s.jwtMiddleware, s.getOracleStreamingStreams)
	actions.GET("/options/oracle-streaming-stream-pools", s.jwtMiddleware, s.getOracleStreamingStreamPools)
	actions.GET("/options/oracle-streaming-connect-harnesses", s.jwtMiddleware, s.getOracleStreamingConnectHarnesses)
	actions.GET("/options/oracle-events-rules", s.jwtMiddleware, s.getOracleEventsRules)
	actions.GET("/options/oracle-queue-queues", s.jwtMiddleware, s.getOracleQueueQueues)
	actions.GET("/options/oracle-functions-applications", s.jwtMiddleware, s.getOracleFunctionsApplications)
	actions.GET("/options/oracle-monitoring-alarms", s.jwtMiddleware, s.getOracleMonitoringAlarms)
	actions.GET("/options/oracle-logging-log-groups", s.jwtMiddleware, s.getOracleLoggingLogGroups)
	actions.GET("/options/oracle-apigateway-gateways", s.jwtMiddleware, s.getOracleApiGatewayGateways)
	actions.GET("/options/oracle-waf-firewalls", s.jwtMiddleware, s.getOracleWafFirewalls)
	actions.GET("/options/oracle-certificates-certificates", s.jwtMiddleware, s.getOracleCertificatesCertificates)
	actions.GET("/options/oracle-email-domains", s.jwtMiddleware, s.getOracleEmailDomains)
	actions.GET("/options/oracle-nosql-tables", s.jwtMiddleware, s.getOracleNosqlTables)
	actions.GET("/options/oracle-mysql-db-systems", s.jwtMiddleware, s.getOracleMysqlDbSystems)
	actions.GET("/options/oracle-dataflow-applications", s.jwtMiddleware, s.getOracleDataflowApplications)
	actions.GET("/options/oracle-datacatalog-catalogs", s.jwtMiddleware, s.getOracleDatacatalogCatalogs)
	actions.GET("/options/oracle-generativeai-models", s.jwtMiddleware, s.getOracleGenerativeAiModels)
	actions.GET("/options/oracle-language-projects", s.jwtMiddleware, s.getOracleLanguageProjects)
	actions.GET("/options/oracle-vision-projects", s.jwtMiddleware, s.getOracleVisionProjects)
	actions.GET("/options/oracle-documentunderstanding-projects", s.jwtMiddleware, s.getOracleDocumentUnderstandingProjects)
	actions.GET("/options/oracle-speech-transcription-jobs", s.jwtMiddleware, s.getOracleSpeechTranscriptionJobs)
	actions.GET("/options/oracle-bastions", s.jwtMiddleware, s.getOracleBastions)
	actions.GET("/options/oracle-waa-accelerations", s.jwtMiddleware, s.getOracleWaaAccelerations)
	actions.GET("/options/oracle-vss-host-scan-recipes", s.jwtMiddleware, s.getOracleVssHostScanRecipes)
	actions.GET("/options/oracle-cloudguard-detector-recipes", s.jwtMiddleware, s.getOracleCloudGuardDetectorRecipes)
	actions.GET("/options/jenkins-jobs", s.jwtMiddleware, s.getJenkinsJobs)
	actions.GET("/options/jira-projects", s.jwtMiddleware, s.getJiraProjects)
	actions.GET("/options/jira-issue-types", s.jwtMiddleware, s.getJiraIssueTypes)
	actions.GET("/options/jira-priorities", s.jwtMiddleware, s.getJiraPriorities)
	actions.GET("/options/jira-users", s.jwtMiddleware, s.getJiraUsers)
	actions.GET("/options/jira-statuses", s.jwtMiddleware, s.getJiraStatuses)
	actions.GET("/options/trello-boards", s.jwtMiddleware, s.getTrelloBoards)
	actions.GET("/options/trello-lists", s.jwtMiddleware, s.getTrelloLists)
	actions.GET("/options/trello-labels", s.jwtMiddleware, s.getTrelloLabels)
	actions.GET("/options/trello-members", s.jwtMiddleware, s.getTrelloMembers)
	actions.GET("/options/asana-workspaces", s.jwtMiddleware, s.getAsanaWorkspaces)
	actions.GET("/options/asana-projects", s.jwtMiddleware, s.getAsanaProjects)
	actions.GET("/options/asana-users", s.jwtMiddleware, s.getAsanaUsers)
	actions.GET("/options/asana-sections", s.jwtMiddleware, s.getAsanaSections)
	actions.GET("/options/asana-tags", s.jwtMiddleware, s.getAsanaTags)
	actions.GET("/options/asana-teams", s.jwtMiddleware, s.getAsanaTeams)
	actions.GET("/options/monday-boards", s.jwtMiddleware, s.getMondayBoards)
	actions.GET("/options/monday-workspaces", s.jwtMiddleware, s.getMondayWorkspaces)
	actions.GET("/options/monday-groups", s.jwtMiddleware, s.getMondayGroups)
	actions.GET("/options/monday-columns", s.jwtMiddleware, s.getMondayColumns)
	actions.GET("/options/intercom-admins", s.jwtMiddleware, s.getIntercomAdmins)
	actions.GET("/options/intercom-teams", s.jwtMiddleware, s.getIntercomTeams)
	actions.GET("/options/intercom-tags", s.jwtMiddleware, s.getIntercomTags)
	actions.GET("/options/intercom-ticket-types", s.jwtMiddleware, s.getIntercomTicketTypes)
	actions.GET("/options/intercom-ticket-states", s.jwtMiddleware, s.getIntercomTicketStates)
	actions.GET("/options/intercom-segments", s.jwtMiddleware, s.getIntercomSegments)
	actions.GET("/options/intercom-companies", s.jwtMiddleware, s.getIntercomCompanies)
	actions.GET("/options/intercom-collections", s.jwtMiddleware, s.getIntercomCollections)
	actions.GET("/options/sendgrid-lists", s.jwtMiddleware, s.getSendGridLists)
	actions.GET("/options/sendgrid-templates", s.jwtMiddleware, s.getSendGridTemplates)
	actions.GET("/options/sendgrid-asm-groups", s.jwtMiddleware, s.getSendGridAsmGroups)
	actions.GET("/options/sendgrid-segments", s.jwtMiddleware, s.getSendGridSegments)
	// Kubernetes pickers are served by one generic handler parameterised by kind
	// (see k8sOptionResources); containers and Helm releases need bespoke reads.
	for _, slug := range []string{
		"namespaces", "nodes", "pods", "services", "configmaps", "secrets", "pvcs",
		"serviceaccounts", "deployments", "statefulsets", "daemonsets", "jobs",
		"cronjobs", "ingresses", "hpas",
	} {
		actions.GET("/options/kubernetes-"+slug, s.jwtMiddleware, s.kubernetesOptions(slug))
	}
	actions.GET("/options/kubernetes-containers", s.jwtMiddleware, s.getKubernetesContainers)
	actions.GET("/options/helm-releases", s.jwtMiddleware, s.getHelmReleases)
	// AAP / AWX pickers: sixteen are served by one generic handler parameterised by
	// the AWX collection (see awxOptionResources); the ad-hoc module list is
	// bespoke, because it reads an admin-editable settings key rather than a
	// paginated collection. awxOptionRouteSlugs is the shared source of truth, so a
	// resource can't be added without a route (which would be a silent 404).
	for _, slug := range awxOptionRouteSlugs {
		actions.GET("/options/awx-"+slug, s.jwtMiddleware, s.awxOptions(slug))
	}
	actions.GET("/options/awx-adhoc-modules", s.jwtMiddleware, s.getAWXAdHocModules)
	// AWS resource pickers (security groups, subnets, KMS keys, IAM roles, SNS
	// topics, RDS subnet groups) for the aws/* actions. awsResourceInputs is the
	// shared source of truth for both the routes and the rule-based marker
	// injection (aws_options.go).
	// Several input names can map to the same picker slug (e.g. both subnet_id and
	// subnet_ids → "subnets"), so register each unique slug ONCE — gin panics on a
	// duplicate route.
	registeredAWS := map[string]bool{}
	for _, slug := range awsResourceInputs {
		if registeredAWS[slug] {
			continue
		}
		registeredAWS[slug] = true
		actions.GET("/options/aws-"+slug, s.jwtMiddleware, s.awsOptions(slug))
	}
	// pgvector pickers open a raw Postgres connection to a caller-named host —
	// the only option proxy that is not HTTP, and the only one that could be
	// aimed at the api's own control-plane database. See pgvector_options.go for
	// the host validation and dial guard that stop that.
	actions.GET("/options/pgvector-schemas", s.jwtMiddleware, s.getPGVectorSchemas)
	actions.GET("/options/pgvector-tables", s.jwtMiddleware, s.getPGVectorTables)
	actions.GET("/options/pgvector-columns", s.jwtMiddleware, s.getPGVectorColumns)
	// Azure pickers: Storage containers (SharedKey-signed or Entra), Cosmos DB
	// databases/containers (master-key-signed or Entra), Entra ID groups/users
	// (app-only Graph), Azure OpenAI deployments and AI Search indexes
	// (api-key). See azure_options.go for the host validation, signing, and
	// dial guard.
	actions.GET("/options/azure-storage-containers", s.jwtMiddleware, s.getAzureStorageContainers)
	actions.GET("/options/azure-cosmos-databases", s.jwtMiddleware, s.getAzureCosmosDatabases)
	actions.GET("/options/azure-cosmos-containers", s.jwtMiddleware, s.getAzureCosmosContainers)
	actions.GET("/options/azure-entra-groups", s.jwtMiddleware, s.getAzureEntraGroups)
	actions.GET("/options/azure-entra-users", s.jwtMiddleware, s.getAzureEntraUsers)
	actions.GET("/options/azure-openai-deployments", s.jwtMiddleware, s.getAzureOpenAIDeployments)
	actions.GET("/options/azure-aisearch-indexes", s.jwtMiddleware, s.getAzureAISearchIndexes)
	actions.GET("/options/azure-files-shares", s.jwtMiddleware, s.getAzureFilesShares)
	actions.GET("/options/azure-tables-tables", s.jwtMiddleware, s.getAzureTablesTables)
	actions.GET("/options/azure-servicebus-queues", s.jwtMiddleware, s.getAzureServiceBusQueues)
	actions.GET("/options/azure-servicebus-topics", s.jwtMiddleware, s.getAzureServiceBusTopics)
	actions.GET("/options/azure-servicebus-subscriptions", s.jwtMiddleware, s.getAzureServiceBusSubscriptions)
	actions.GET("/options/azuredevops-projects", s.jwtMiddleware, s.getAzureDevOpsProjects)
	actions.GET("/options/azuredevops-repositories", s.jwtMiddleware, s.getAzureDevOpsRepositories)
	actions.GET("/options/azuredevops-pipelines", s.jwtMiddleware, s.getAzureDevOpsPipelines)
	actions.GET("/options/azuredevops-release-definitions", s.jwtMiddleware, s.getAzureDevOpsReleaseDefinitions)
	actions.GET("/options/azuredevops-teams", s.jwtMiddleware, s.getAzureDevOpsTeams)
	// Salesforce pickers. Eleven proxies back all 429 markers registered from
	// salesforce_options_markers.go — record ids and picklist API names are the
	// two things a non-technical operator cannot be asked to look up, and they
	// are most of a Salesforce action's inputs. The org's instance_url is
	// caller-supplied and becomes the request host, so see salesforce_options.go
	// for the Salesforce-suffix validation, dial guard and SOQL escaping.
	actions.GET("/options/salesforce-objects", s.jwtMiddleware, s.getSalesforceObjects)
	actions.GET("/options/salesforce-fields", s.jwtMiddleware, s.getSalesforceFields)
	actions.GET("/options/salesforce-picklist", s.jwtMiddleware, s.getSalesforcePicklistValues)
	actions.GET("/options/salesforce-external-id-fields", s.jwtMiddleware, s.getSalesforceExternalIDFields)
	actions.GET("/options/salesforce-record-types", s.jwtMiddleware, s.getSalesforceRecordTypes)
	actions.GET("/options/salesforce-lookup", s.jwtMiddleware, s.getSalesforceLookup)
	actions.GET("/options/salesforce-users", s.jwtMiddleware, s.getSalesforceUsers)
	actions.GET("/options/salesforce-owners", s.jwtMiddleware, s.getSalesforceOwners)
	actions.GET("/options/salesforce-campaign-member-status", s.jwtMiddleware, s.getSalesforceCampaignMemberStatus)
	actions.GET("/options/salesforce-list-views", s.jwtMiddleware, s.getSalesforceListViews)
	actions.GET("/options/salesforce-reports", s.jwtMiddleware, s.getSalesforceReports)

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

	flos.POST("/:FloID/execute", s.flexAuthMiddleware, s.executeFlo)
	flos.POST("/:FloID/trigger/:TriggerID/execute", s.flexAuthMiddleware, s.triggerFlo)

	favourites := v1.Group("favourite")
	favourites.Use(s.jwtMiddleware)
	favourites.GET("", s.getFloFavourites)
	favourites.POST("/:FloID", s.addFloFavourite)
	favourites.DELETE("/:FloID", s.removeFloFavourite)

	executions := v1.Group("execution")
	executions.POST("/:id/state", s.executionMiddleware, s.updateExecutionState)
	executions.POST("/:id/logs", s.executionMiddleware, s.appendExecutionLogs)
	executions.POST("/:id/cancel", s.jwtMiddleware, s.cancelExecution)
	executions.POST("/:id/resume", s.jwtMiddleware, s.resumeExecution)
	executions.POST("/:id", s.executionMiddleware, s.updateExecution)

	executions.GET("", s.jwtMiddleware, s.getExecutions)
	executions.GET("/:id", s.jwtMiddleware, s.getExecutionByID)
	executions.GET("/:id/ancestors", s.jwtMiddleware, s.getExecutionAncestors)
	executions.GET("/:id/children", s.jwtMiddleware, s.getExecutionChildren)

	// /execution-tree/:rootID lives outside the executions group
	// because the rootID parameter would collide with /:id otherwise
	// (gin treats /:id and /:rootID as the same path segment).
	v1.GET("/execution-tree/:rootID", s.jwtMiddleware, s.getExecutionTree)
	executions.GET("/:id/status", s.executionMiddleware, s.getExecutionStatus)
	executions.GET("/:id/stream", s.streamAuthMiddleware, s.streamExecutionLogs)

	executions.GET("/:id/environment/:environment", s.executionMiddleware, s.getExecutionEnvironment)
	executions.GET("/:id/environment/:environment/property/:name", s.executionMiddleware, s.getExecutionEnvironmentProperty)
	executions.GET("/:id/environment/:environment/secret/:name", s.executionMiddleware, s.getExecutionEnvironmentSecret)
	executions.GET("/:id/environment/:environment/credential/:name", s.executionMiddleware, s.getExecutionEnvironmentCredential)

	// JWT-protected blob fetch — the editor's execution-viewer media
	// inspector uses this to fetch off-loaded media (audio/video/image
	// bytes that the executor tokenised into flo:blob:HANDLE references)
	// without going through the internal mTLS endpoint. Authorisation
	// is scope-based: the persistence layer returns 404 for blobs
	// outside the user's org/owner scope. See blob_public.go.
	v1.GET("blob/:handle", s.jwtMiddleware, s.getBlobPublic)
	// Editor upload of flow assets (logo/PSD/image) → a permanent asset blob,
	// returned as a flo:blob: token the Asset node wires into file inputs.
	v1.POST("asset", s.jwtMiddleware, s.putAssetPublic)

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
	// POST /trigger/:id/resolve is registered below on internalRouter so it
	// lands on the mTLS-only engine when enabled. Registering it here too
	// duplicates the route on the main engine and panics gin at startup when
	// mTLS is disabled (internalRouter == v1).

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

	environment.GET("/:environment/elevenlabs-voices/:credential", s.jwtMiddleware, s.getElevenLabsVoices)
	environment.GET("/:environment/elevenlabs-models/:credential", s.jwtMiddleware, s.getElevenLabsModels)
	environment.GET("/:environment/elevenlabs-shared-voices/:credential", s.jwtMiddleware, s.getElevenLabsSharedVoices)
	environment.POST("/:environment/elevenlabs-add-voice/:credential", s.jwtMiddleware, s.addElevenLabsVoice)
	environment.GET("/:environment/facebook-pages/:credentialName", s.jwtMiddleware, s.getFacebookPages)
	environment.GET("/:environment/facebook-webhook-check/:credentialName/:pageId", s.jwtMiddleware, s.checkFacebookWebhook)
	environment.GET("/:environment/credential", s.jwtMiddleware, s.getEnvironmentCredentials)
	environment.POST("/:environment/credential", s.jwtMiddleware, s.createEnvironmentCredential)
	environment.POST("/:environment/credential/:id/reauthorise", s.jwtMiddleware, s.reauthoriseCredential)
	environment.PUT("/:environment/credential/:id/aws-role", s.jwtMiddleware, s.setAWSRoleARN)
	environment.PUT("/:environment/credential/:id/aws-permissions", s.jwtMiddleware, s.updateAWSRolePermissions)
	environment.POST("/:environment/credential/:id/aws-role/test", s.jwtMiddleware, s.testAWSRoleAccess)
	environment.PUT("/:environment/credential/:id/oci-connection", s.jwtMiddleware, s.setOCIConnection)
	environment.POST("/:environment/credential/:id/oci-key/test", s.jwtMiddleware, s.testOCIAccess)
	environment.DELETE("/:environment/credential/:id", s.jwtMiddleware, s.deleteEnvironmentCredential)

	environment.GET("/:environment/secret", s.jwtMiddleware, s.getEnvironmentSecrets)
	environment.GET("/:environment/secret/:name", s.jwtMiddleware, s.getEnvironmentSecretByName)
	environment.POST("/:environment/secret", s.jwtMiddleware, s.createEnvironmentSecret)
	environment.POST("/:environment/secret/:id", s.jwtMiddleware, s.updateEnvironmentSecretByID)
	environment.DELETE("/:environment/secret/:id", s.jwtMiddleware, s.deleteEnvironmentSecretByID)

	// Stream token exchange for SSE authentication
	v1.POST("auth/stream-token", s.jwtMiddleware, s.issueStreamToken)

	v1.POST("feedback", s.jwtMiddleware, s.submitFeedback)

	// Embed apps — publishable-key control plane for the developer SDK.
	v1.GET("embed/app", s.jwtMiddleware, s.listEmbedApps)
	v1.POST("embed/app", s.jwtMiddleware, s.createEmbedApp)
	v1.GET("embed/app/:id", s.jwtMiddleware, s.getEmbedApp)
	v1.DELETE("embed/app/:id", s.jwtMiddleware, s.deleteEmbedApp)
	v1.POST("embed/app/:id/origin", s.jwtMiddleware, s.addEmbedOrigin)
	v1.DELETE("embed/app/:id/origin", s.jwtMiddleware, s.removeEmbedOrigin)
	v1.POST("embed/app/:id/resource", s.jwtMiddleware, s.setEmbedResource)

	// Flomation Gateway — developer-defined HTTP APIs that route to flows.
	v1.GET("gateway", s.jwtMiddleware, s.listGatewayAPIs)
	v1.POST("gateway", s.jwtMiddleware, s.createGatewayAPI)
	v1.GET("gateway/:id", s.jwtMiddleware, s.getGatewayAPI)
	v1.PATCH("gateway/:id", s.jwtMiddleware, s.updateGatewayAPI)
	v1.DELETE("gateway/:id", s.jwtMiddleware, s.deleteGatewayAPI)
	v1.POST("gateway/:id/endpoint", s.jwtMiddleware, s.createGatewayEndpoint)
	v1.PATCH("gateway/:id/endpoint/:eid", s.jwtMiddleware, s.updateGatewayEndpoint)
	v1.DELETE("gateway/:id/endpoint/:eid", s.jwtMiddleware, s.deleteGatewayEndpoint)

	// Credentials
	v1.GET("credential/providers", s.jwtMiddleware, s.getCredentialProviders)
	v1.GET("credential/callback", s.credentialOAuthCallback) // No auth — OAuth redirect
	// Distinct top-level path (not under credential/) so gin's radix tree doesn't
	// conflict the :id param with the static credential/providers|callback routes.
	// (OCI connect stacks are hosted on Object Storage via a PAR — RM only fetches
	// zipUrls from supported providers, not a self-served endpoint.)

	// Agents
	agents := v1.Group("agent")
	agents.Use(s.jwtMiddleware)
	agents.GET("", s.getAgents)
	agents.GET("/:id", s.getAgentByID)
	agents.POST("", s.createAgent)
	agents.POST("/:id", s.updateAgent)
	agents.DELETE("/:id", s.archiveAgent)
	agents.POST("/:id/start", s.startAgent)
	agents.POST("/:id/stop", s.stopAgent)
	agents.POST("/:id/pause", s.pauseAgent)
	agents.GET("/:id/session", s.getAgentSessions)
	agents.GET("/:id/session/:sessionId", s.getAgentSessionByID)
	agents.GET("/:id/state", s.getAgentState)
	agents.GET("/:id/state/:key", s.getAgentStateKey)
	agents.POST("/:id/state/:key", s.setAgentStateKey)
	agents.DELETE("/:id/state/:key", s.deleteAgentStateKey)
	agents.GET("/:id/message", s.getAgentMessages)
	agents.POST("/:id/message", s.createAgentMessage)
	agents.GET("/:id/execution", s.getAgentExecutions)
	agents.GET("/:id/session/:sessionId/stream", s.streamAgentSession)

	// Agent Planning M2 — editor-facing plan reads. SSE stream lives
	// alongside these in M2 commit 3. See internal/http/agent_plan_read.go.
	agents.GET("/:id/plan", s.getAgentPlans)
	agents.GET("/:id/plan/:planID", s.getAgentPlan)
	agents.GET("/:id/plan/:planID/event", s.getAgentPlanEvents)
	// Agent Planning M3 — editor-facing cancel button.
	agents.POST("/:id/plan/:planID/cancel", s.cancelAgentPlan)
	// Agent Planning M4 — editor-facing Start button (transitions
	// a draft plan to active).
	agents.POST("/:id/plan/:planID/start", s.startAgentPlan)
	// Agent Planning M5 — editor-facing revise endpoint (add /
	// remove / update tasks on non-terminal plans).
	agents.POST("/:id/plan/:planID/revise", s.reviseAgentPlan)

	// SSE — browsers' EventSource can't set Authorization headers, so
	// streamAuthMiddleware exchanges a JWT for a short-lived opaque
	// token via POST /auth/stream-token (same pattern as
	// /execution/:id/stream). Mounted on v1 directly (NOT inside the
	// agents group) so the group-level jwtMiddleware doesn't 401
	// before streamAuthMiddleware has a chance to read the token
	// from the query string. See internal/http/plan_stream.go.
	v1.GET("/agent/:id/plan/:planID/stream", s.streamAuthMiddleware, s.streamAgentPlan)

	// Agent Memory Phase 6: user-facing memory management.
	agents.GET("/:id/my-memories", s.getMyAgentMemories)
	agents.PATCH("/:id/my-memories/:memoryId", s.updateMyAgentMemory)
	agents.DELETE("/:id/my-memories/:memoryId", s.deleteMyAgentMemory)
	agents.POST("/:id/my-memories/forget-all", s.forgetAllMyAgentMemories)
	agents.POST("/:id/my-memories/export", s.exportMyAgentData)
	agents.GET("/:id/my-identities", s.getMyAgentIdentities)
	agents.DELETE("/:id/my-identities/:identityId", s.unlinkMyAgentIdentity)
	agents.GET("/:id/my-audit-log", s.getMyAgentAuditLog)
	agents.GET("/:id/audit-log", s.getAgentAuditLog)
	agents.GET("/:id/users", s.getAgentUsers)
	agents.PATCH("/:id/retention", s.updateAgentRetention)
	agents.PATCH("/:id/max-pinned-memories", s.updateMaxPinnedMemories)
	agents.GET("/:id/schedule", s.getAgentSchedules)
	agents.GET("/:id/slack-permissions", s.checkSlackPermissions)
	agents.GET("/:id/twilio-verify", s.checkTwilioCredentials)

	// Google account management (browser-accessible, JWT-auth'd).
	// Uses agent ID as the trigger_google_account scope key.
	agents.GET("/:id/google-accounts", s.getAgentGoogleAccounts)
	agents.DELETE("/:id/google-account/:email", s.deleteAgentGoogleAccount)

	// Internal endpoints — no JWT, service-to-service calls.
	// When mTLS is enabled, these register on a separate Gin engine
	// served on the internal port with client certificate verification.
	internalRouter := v1 // default: same engine (backward compat)
	if config.TLS != nil && config.TLS.Enabled {
		gin.SetMode(gin.ReleaseMode)
		s.internalEngine = gin.New()
		internalRouter = s.internalEngine.Group("api/v1")
		// Tag every request that arrived on the mTLS-only engine.
		// Handlers that share a function across engines (triggerFlo,
		// executeFlo, …) read this via isInternalRequest before
		// honouring service-to-service-only headers like
		// X-Flomation-Parent-Execution-ID. The middleware is
		// intentionally NOT added in single-engine dev mode — without
		// mTLS we cannot distinguish internal from external callers,
		// so the safe default is "everyone is external".
		internalRouter.Use(func(c *gin.Context) {
			c.Set("internal_mtls", true)
			c.Next()
		})
	}
	// Trigger variable resolution (called by Launch for ${secrets.X}, ${credentials.X})
	internalRouter.POST("/trigger/:id/resolve", s.resolveTriggerVariables)

	internal := internalRouter.Group("internal")
	internal.POST("/agent/:id/message", s.createAgentMessageInternal)
	internal.GET("/agent/:id/state/:key", s.getAgentStateInternal)
	internal.POST("/agent/:id/state/:key", s.setAgentStateInternal)
	internal.POST("/flo/:FloID/execute", s.executeFlo)
	internal.POST("/flo/:FloID/trigger/:TriggerID/execute", s.triggerFlo)
	internal.GET("/flo/:FloID/web-trigger", s.getWebTriggerConfigInternal)
	internal.POST("/trigger/:id/dispatch", s.dispatchTrigger)
	internal.GET("/execution/:id", s.getExecutionByID)
	internal.GET("/execution/:id/wait", s.getExecutionWaitInternal)
	internal.GET("/agent/:id/session/:sessionId/stream", s.streamAgentSession)

	// Embed edge gate: Launch resolves a publishable key (+ origin + resource)
	// here so it never touches the embed tables directly.
	internal.POST("/embed/resolve", s.resolveEmbedKey)
	internal.GET("/gateway/:apiId/resolve", s.resolveGatewayAPIInternal)
	internal.POST("/gateway/:apiId/verify-session", s.verifyGatewaySessionInternal)

	// Human-in-the-Loop: the executor registers an Await request; Launch
	// reports the human's response. Both bypass JWT (service-to-service).
	internal.POST("/hitl/request", s.createHITLRequestInternal)
	internal.POST("/hitl/respond", s.respondHITLInternal)

	// Agent Memory Phase 1: identity + conversation resolution endpoints.
	// Called by Launch on every incoming webhook to resolve the identity
	// and open conversation before dispatching the orchestrator flow.
	// See plans/agent_memory.md.
	internal.POST("/agent/:id/resolve-identity", s.resolveAgentIdentityInternal)
	internal.POST("/agent/:id/conversation", s.resolveAgentConversationInternal)
	internal.GET("/conversation/:id", s.getAgentConversationInternal)
	internal.GET("/conversation/:id/history", s.getAgentConversationHistoryInternal)
	internal.POST("/conversation/:id/message", s.createAgentConversationMessageInternal)

	// Web threads — generic web-invoke conversation history (Web Trigger).
	internal.POST("/web-thread", s.createWebThreadInternal)
	internal.GET("/web-thread/:id/history", s.getWebThreadHistoryInternal)
	internal.POST("/web-thread/:id/turn", s.appendWebThreadTurnInternal)

	// AI tool-loop relay recording: when the executor's flow engine
	// detects a tool call to a messaging action (send-slack, send-
	// telegram, etc.), it posts here so the outbound is recorded
	// against the *recipient's* conversation. This keeps cross-user
	// relays — Andy on Telegram telling the agent "tell Bob on Slack"
	// — symmetrically visible to both sides. See
	// internal/http/agent_record_outbound.go.
	internal.POST("/agent/:id/record-outbound", s.recordAgentOutboundInternal)

	// User profile variables — surfaced as ${user.X} at execution-context
	// bootstrap. Returns the full profile map (salutation, names, address
	// fields) so the executor can populate substitution without round-
	// tripping per-variable. See internal/http/profile.go.
	internal.GET("/user/:id/variables", s.getUserVariablesInternal)

	// Agent Memory Phase 2: memories, pending actions, commitments.
	// Called by Launch's system prompt assembler, by the extraction
	// System Flow, and by the executor actions agent/remember,
	// agent/recall, agent/forget. See internal/http/agent_memory_phase2.go.
	internal.POST("/agent/:id/memory", s.createAgentMemoryInternal)
	internal.GET("/agent/:id/memory", s.listAgentMemoriesInternal)
	internal.GET("/agent/:id/prior-conversations", s.getAgentPriorConversationsInternal)
	internal.POST("/agent/:id/conversation/:conv_id/messages", s.getAgentConversationMessagesInternal)
	internal.POST("/agent/:id/calendar/events", s.getAgentCalendarEventsInternal)
	internal.GET("/memory/:id", s.getAgentMemoryInternal)
	internal.DELETE("/memory/:id", s.deleteAgentMemoryInternal)
	internal.POST("/agent/:id/pending-action", s.createAgentPendingActionInternal)
	internal.GET("/agent/:id/pending-action", s.listOpenPendingActionsInternal)
	internal.GET("/pending-action/:id", s.getPendingActionInternal)
	internal.PATCH("/pending-action/:id", s.updatePendingActionStatusInternal)
	internal.POST("/agent/:id/commitment", s.createAgentCommitmentInternal)
	internal.GET("/commitment/due", s.listDueCommitmentsInternal)
	internal.GET("/agent/:id/commitment", s.listCommitmentsForUserInternal)
	internal.PATCH("/commitment/:id", s.updateCommitmentStatusInternal)

	// Agent schedules — AI-managed recurring flow execution.
	internal.POST("/agent/:id/schedule", s.createAgentScheduleInternal)
	internal.GET("/agent/:id/schedule", s.listAgentSchedulesInternal)
	internal.PATCH("/schedule/:id", s.updateAgentScheduleInternal)
	internal.DELETE("/schedule/:id", s.deleteAgentScheduleInternal)
	internal.DELETE("/agent/:id/schedule/by-name/*name", s.deleteAgentScheduleByNameInternal)

	// Agent Memory Phase 4: semantic retrieval with pgvector.
	internal.POST("/agent/:id/memory/search", s.searchAgentMemoriesInternal)
	internal.GET("/memory/unembedded", s.getUnembeddedMemoriesInternal)
	internal.PATCH("/memory/:id/embedding", s.updateMemoryEmbeddingInternal)

	// Agent Memory Phase 5: identity linking.
	internal.GET("/agent/:id/identity", s.listIdentitiesInternal)
	internal.POST("/agent/:id/identity/lookup", s.lookupIdentityInternal)
	internal.POST("/agent/:id/identity/merge", s.mergeIdentityInternal)
	internal.GET("/agent/:id/pending-action/match", s.matchPendingActionInternal)
	internal.POST("/agent/:id/identity/request-verification", s.requestVerificationInternal)
	internal.GET("/agent/:id/tool-summary", s.getAgentToolSummaryInternal)

	// System prompt assembly — Phase 1 of Launch → API migration.
	internal.POST("/agent/:id/assemble-system-prompt", s.assembleSystemPromptInternal)

	// Phase 3: inbound message pipeline (replaces Launch's 7-step pipeline).
	internal.POST("/agent/:id/inbound-message", s.handleInboundMessageInternal)

	// Pending action poller support (Phase 5).
	internal.GET("/pending-action/unnotified", s.listUnnotifiedPendingActionsInternal)
	internal.PATCH("/pending-action/:id/notified", s.markPendingActionNotifiedInternal)

	// Channel actions — proxied to Launch (typing indicators, etc.)
	internal.POST("/agent/:id/channel-action", s.channelActionInternal)

	// Agent Memory Phase 7: memory hygiene (internal).
	internal.POST("/agent/:id/memory/check-hygiene", s.checkHygieneInternal)
	internal.POST("/agent/:id/memory/supersede", s.supersedeMemoryInternal)
	internal.POST("/agent/:id/memory/merge", s.mergeMemoryInternal)
	internal.GET("/agent/:id/memory/pinned-count", s.pinnedCountInternal)
	internal.POST("/agent/:id/memory/enforce-pin-limit", s.enforcePinLimitInternal)

	// Agent Memory Phase 6: retention poller + audit log (internal).
	internal.GET("/memory/expired", s.getExpiredMemoriesInternal)
	internal.GET("/agent/retention-policies", s.getAgentRetentionPoliciesInternal)
	internal.POST("/memory/bulk-delete", s.bulkDeleteExpiredMemoriesInternal)
	internal.POST("/audit-log", s.createAuditLogEntryInternal)

	// Blob store service — mTLS-only file storage tier used by the
	// executor (tool-output tokenisation) and Launch (inbound
	// attachment dispatch). See internal/http/blob.go.
	internal.POST("/blob", s.putBlobInternal)
	internal.GET("/blob/:handle", s.getBlobInternal)
	internal.HEAD("/blob/:handle", s.headBlobInternal)
	internal.GET("/blob/:handle/metadata", s.headBlobInternal)
	internal.DELETE("/blob/:handle", s.deleteBlobInternal)
	// Trigger-scoped anonymous upload — Launch calls this when a public
	// form submitter uploads a file (eSignature, camera photo, arbitrary
	// file). Scope is derived server-side from the flow's owner, so
	// Launch doesn't need to know / relay the org/owner header.
	internal.POST("/flo/:FloID/trigger/:TriggerID/upload", s.putBlobForTrigger)

	// Agent Planning M1 — the agent-facing plan/create endpoint.
	// The only caller is the executor's agent/create_plan action.
	// See plans/agent_planning_m1.md and internal/http/agent_plan.go.
	internal.POST("/agent/:id/plan", s.createPlan)

	// Agent Planning M1 — the orchestrator's tick endpoint. Polled
	// by Launch's plan-tick service (M1 commit 8); also woken
	// reactively by the completion writeback (M1 commit 6).
	internal.POST("/plan/:planID/tick", s.tickPlan)

	// Agent Planning M1.5 — the plan/block AI escape hatch. Called
	// by the executor's plan/block action when the AI decides it
	// cannot make progress. See internal/http/plan_block.go.
	internal.POST("/plan_task/:planTaskID/block", s.blockPlanTask)

	// Agent Planning M3 — AI-facing cancel + get-status. mTLS twins
	// of the editor's cancel POST and M2's plan-read GET. Both
	// still verify plan.agent_id == :id (mTLS proves "an executor",
	// not "the right executor"). See internal/http/agent_plan_cancel.go.
	internal.POST("/agent/:id/plan/:planID/cancel", s.cancelAgentPlanInternal)
	internal.GET("/agent/:id/plan/:planID", s.getAgentPlanInternal)

	// Agent Planning M4 — AI-facing start (transitions a draft
	// plan to active). mTLS twin of the editor's start POST.
	internal.POST("/agent/:id/plan/:planID/start", s.startAgentPlanInternal)

	// Agent Planning M5 — AI-facing revise. mTLS twin of the
	// editor's revise POST.
	internal.POST("/agent/:id/plan/:planID/revise", s.reviseAgentPlanInternal)

	// Billing: entitlement sync (pushed from billing service).
	internal.POST("/entitlements/sync", s.syncEntitlementsInternal)
	internal.POST("/credit/sync", s.syncCreditBalanceInternal)

	// Agent Memory Phase 2d-α: the extract-dispatch endpoint.
	// Called by Launch after storing an inbound message and by the
	// executor's assistant-reply hook after storing an outbound reply.
	// Returns 204 as a no-op if the agent has no extraction flow
	// configured, so callers can invoke it unconditionally.
	// See internal/http/agent_memory_phase2d.go.
	internal.POST("/agent/:id/extract", s.extractAgentInternal)

	// User-identity OAuth callbacks (R3 Phase 2). Launch's identity-purpose
	// OAuth flows post the resolved external_id here after consenting the
	// user; no client-supplied user_id is trusted — the user_id comes from
	// the JWT-cookie session that Launch's identity initiate handler validated.
	internal.POST("/user-identity", s.upsertUserIdentityInternal)

	// Google Calendar: per-user account management (called by Launch OAuth callback
	// and by the executor's calendar tool actions for token exchange).
	internal.POST("/agent-user/:id/google-account", s.upsertGoogleAccountInternal)
	internal.GET("/agent-user/:id/google-accounts", s.getGoogleAccountsInternal)
	internal.GET("/agent-user/:id/google-tokens", s.getGoogleTokensInternal)
	internal.GET("/agent-user/:id/google-refresh-tokens", s.getGoogleRefreshTokensInternal)
	internal.DELETE("/agent-user/:id/google-account/:email", s.deleteGoogleAccountInternal)

	// Trigger-scoped Google accounts (email triggers in standalone flows)
	internal.POST("/trigger/:id/google-account", s.upsertTriggerGoogleAccountInternal)
	internal.GET("/trigger/:id/google-accounts", s.getTriggerGoogleAccountsInternal)
	internal.GET("/trigger/:id/google-tokens", s.getTriggerGoogleTokensInternal)
	internal.GET("/trigger/:id/google-refresh-tokens", s.getTriggerGoogleRefreshTokensInternal)
	internal.DELETE("/trigger/:id/google-account/:email", s.deleteTriggerGoogleAccountInternal)

	// Voice session WebSocket proxy (executor ↔ Launch)
	internal.GET("/voice-session/:session_id", s.handleVoiceSessionProxy)
	internal.POST("/voice-session/:session_id/register", s.handleVoiceSessionRegister)
}

func (s *Service) Listen() error {
	if s.internalEngine != nil {
		go s.listenInternal()
	}
	return s.engine.Run(fmt.Sprintf("%v:%v", s.config.HttpListenConfig.Address, s.config.HttpListenConfig.Port))
}

// listenInternal starts the mTLS-protected internal listener on a
// separate port. Only clients presenting a valid certificate signed
// by the platform CA are accepted.
func (s *Service) listenInternal() {
	tlsCfg, err := mtls.NewServerTLSConfig(s.config.TLS)
	if err != nil {
		log.WithError(err).Fatal("unable to configure mTLS server")
	}

	addr := fmt.Sprintf("%v:%d", s.config.HttpListenConfig.Address, s.config.TLS.InternalPort)
	server := &http.Server{
		Addr:              addr,
		Handler:           s.internalEngine,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.WithFields(log.Fields{
		"address": addr,
	}).Info("starting mTLS internal listener")

	if err := server.ListenAndServeTLS(s.config.TLS.CertFile, s.config.TLS.KeyFile); err != nil && err != http.ErrServerClosed {
		log.WithError(err).Fatal("mTLS internal listener failed")
	}
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
