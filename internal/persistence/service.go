package persistence

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/config"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

// ErrInviteAlreadyAccepted is returned when an invite has already been accepted.
var ErrInviteAlreadyAccepted = errors.New("invite has already been accepted")

type Service struct {
	config *config.Config
	conn   *sqlx.DB

	stmtGetOrganisations           *sqlx.NamedStmt
	stmtGetOrganisationByID        *sqlx.NamedStmt
	stmtCreateOrganisation         *sqlx.NamedStmt
	stmtUpdateOrganisation         *sqlx.NamedStmt
	stmtAddUserToOrganisation      *sqlx.NamedStmt
	stmtGetOrganisationMembers     *sqlx.NamedStmt
	stmtRemoveUserFromOrganisation *sqlx.NamedStmt
	stmtGetUserRoleInOrganisation  *sqlx.NamedStmt
	stmtCreateOrganisationInvite   *sqlx.NamedStmt
	stmtGetOrganisationInvites     *sqlx.NamedStmt
	stmtGetInviteByCode            *sqlx.NamedStmt
	stmtGetInvitePreview           *sqlx.NamedStmt
	stmtAcceptInvite               *sqlx.NamedStmt
	stmtRevokeInvite               *sqlx.NamedStmt

	stmtGetUserByID      *sqlx.NamedStmt
	stmtCreateUser       *sqlx.NamedStmt
	stmtUpdateUser       *sqlx.NamedStmt
	stmtAcceptEula       *sqlx.NamedStmt
	stmtGetLatestEula    *sqlx.NamedStmt
	stmtUpdateOnboarding *sqlx.NamedStmt

	stmtGetMyFlos              *sqlx.NamedStmt
	stmtGetMyFlosWithFilter    *sqlx.NamedStmt
	stmtCountMyFlos            *sqlx.NamedStmt
	stmtCountMyFlosWithFilter  *sqlx.NamedStmt
	stmtGetOrgFlos             *sqlx.NamedStmt
	stmtGetOrgFlosWithFilter   *sqlx.NamedStmt
	stmtCountOrgFlos           *sqlx.NamedStmt
	stmtCountOrgFlosWithFilter *sqlx.NamedStmt
	stmtGetFloByID             *sqlx.NamedStmt
	stmtCreateFlo              *sqlx.NamedStmt
	stmtUpdateFlo              *sqlx.NamedStmt
	stmtDeleteFlo              *sqlx.NamedStmt

	stmtCreateFloRevision           *sqlx.NamedStmt
	stmtGetLatestFloRevisionByFloID *sqlx.NamedStmt
	stmtGetFloRevisions             *sqlx.NamedStmt
	stmtGetFloRevisionByID          *sqlx.NamedStmt

	stmtInsertDefaultTrigger *sqlx.NamedStmt
	stmtLinkFloToTrigger     *sqlx.NamedStmt
	stmtRemoveFloTriggerLink *sqlx.NamedStmt

	stmtGetFloTriggers *sqlx.NamedStmt

	stmtGetLatestExecutionForFlo     *sqlx.NamedStmt
	stmtGetRecentExecutionsForFlo    *sqlx.NamedStmt
	stmtGetExecutions                *sqlx.NamedStmt
	stmtGetExecutionsWithFilter      *sqlx.NamedStmt
	stmtCountExecutions              *sqlx.NamedStmt
	stmtCountExecutionsWithFilter    *sqlx.NamedStmt
	stmtGetOrgExecutions             *sqlx.NamedStmt
	stmtGetOrgExecutionsWithFilter   *sqlx.NamedStmt
	stmtCountOrgExecutions           *sqlx.NamedStmt
	stmtCountOrgExecutionsWithFilter *sqlx.NamedStmt

	stmtGetDefaultTriggerForFlo *sqlx.NamedStmt
	stmtGetTriggerForFlo        *sqlx.NamedStmt
	stmtInsertTriggerInvocation *sqlx.NamedStmt
	stmtGetTriggerInvocation    *sqlx.NamedStmt

	stmtGetFlosForTrigger *sqlx.NamedStmt

	stmtInsertFloExecution        *sqlx.NamedStmt
	stmtUpdateFloExecutionStatus  *sqlx.NamedStmt
	stmtUpdateFloCompletionStatus *sqlx.NamedStmt
	stmtUpdateExecutionResult     *sqlx.NamedStmt
	stmtUpdateExecutionRunnerID   *sqlx.NamedStmt
	stmtGetExecutionByID          *sqlx.NamedStmt

	stmtGetActions *sqlx.NamedStmt

	stmtGetRunnerByID          *sqlx.NamedStmt
	stmtGetRunnerByIdentifier  *sqlx.NamedStmt
	stmtGetRunners             *sqlx.NamedStmt
	stmtInsertRunner           *sqlx.NamedStmt
	stmtUpdateRunnerLastAccess *sqlx.NamedStmt
	stmtInsertQueueRunner      *sqlx.NamedStmt
	stmtCanRunnerAccessQueue   *sqlx.NamedStmt
	stmtRemoveQueueRunner      *sqlx.NamedStmt

	stmtGetQueueByRegistrationCode *sqlx.NamedStmt
	stmtGetQueuesByOrganisationID  *sqlx.NamedStmt
	stmtGetQueueByID               *sqlx.NamedStmt
	stmtCreateQueue                *sqlx.NamedStmt
	stmtDeleteQueue                *sqlx.NamedStmt
	stmtGetQueueRunners            *sqlx.NamedStmt

	stmtGetPendingExecutionByOrganisationID     *sqlx.NamedStmt
	stmtGetPendingExecutionByNullOrganisationID *sqlx.NamedStmt
	stmtGetOrganisationByRunnerIdentifier       *sqlx.NamedStmt

	stmtCreateEnvironment          *sqlx.NamedStmt
	stmtGetEnvironmentByID         *sqlx.NamedStmt
	stmtGetEnvironmentByIDDirect   *sqlx.NamedStmt
	stmtGetEnvironmentByName       *sqlx.NamedStmt
	stmtGetEnvironmentByIDAsRunner *sqlx.NamedStmt
	stmtGetAllEnvironments         *sqlx.NamedStmt
	stmtGetOrgEnvironments         *sqlx.NamedStmt
	stmtDeleteEnvironmentByID      *sqlx.NamedStmt

	stmtGetEnvironmentProperties     *sqlx.NamedStmt
	stmtGetEnvironmentPropertyByID   *sqlx.NamedStmt
	stmtGetEnvironmentPropertyByName *sqlx.NamedStmt
	stmtInsertEnvironmentProperty    *sqlx.NamedStmt
	stmtUpdateEnvironmentProperty    *sqlx.NamedStmt
	stmtDeleteEnvironmentProperty    *sqlx.NamedStmt

	stmtGetEnvironmentSecrets      *sqlx.NamedStmt
	stmtGetEnvironmentSecretByID   *sqlx.NamedStmt
	stmtGetEnvironmentSecretByName *sqlx.NamedStmt
	stmtInsertEnvironmentSecret    *sqlx.NamedStmt
	stmtDeleteEnvironmentSecret    *sqlx.NamedStmt
	stmtUpdateEnvironmentSecret    *sqlx.NamedStmt

	stmtGetUsageThisMonthForUserID *sqlx.NamedStmt
	stmtGetUsageThisMonthForOrgID  *sqlx.NamedStmt

	// Subscription entitlement cache (pushed from billing service).
	stmtUpsertEntitlement    *sqlx.NamedStmt
	stmtGetEntitlement       *sqlx.NamedStmt
	stmtGetAllEntitlements   *sqlx.NamedStmt
	stmtDeleteEntitlements   *sqlx.NamedStmt
	stmtGetAllowanceForOwner *sqlx.NamedStmt
	stmtGetAllowanceForOrg   *sqlx.NamedStmt

	stmtGetTriggers      *sqlx.NamedStmt
	stmtGetTriggerByID   *sqlx.NamedStmt
	stmtCreateTrigger    *sqlx.NamedStmt
	stmtUpdateTrigger    *sqlx.NamedStmt
	stmtDeleteTrigger    *sqlx.NamedStmt
	stmtDeleteFloTrigger *sqlx.NamedStmt

	stmtGetGroupsByOrgID        *sqlx.NamedStmt
	stmtGetGroupByID            *sqlx.NamedStmt
	stmtCreateGroup             *sqlx.NamedStmt
	stmtUpdateGroup             *sqlx.NamedStmt
	stmtDeleteGroup             *sqlx.NamedStmt
	stmtGetGroupMembers         *sqlx.NamedStmt
	stmtAddUserToGroup          *sqlx.NamedStmt
	stmtRemoveUserFromGroup     *sqlx.NamedStmt
	stmtGetGroupPermissions     *sqlx.NamedStmt
	stmtDeleteGroupPermissions  *sqlx.NamedStmt
	stmtInsertGroupPermission   *sqlx.NamedStmt
	stmtGetUserPermissionsInOrg *sqlx.NamedStmt
	stmtGetDefaultGroups        *sqlx.NamedStmt
	stmtCountUserGroupsInOrg    *sqlx.NamedStmt
	stmtCreateFeedback          *sqlx.NamedStmt

	stmtGetFloFavourites   *sqlx.NamedStmt
	stmtAddFloFavourite    *sqlx.NamedStmt
	stmtRemoveFloFavourite *sqlx.NamedStmt

	// Agent statements
	stmtGetAgents         *sqlx.NamedStmt
	stmtGetAgentsByOrgID  *sqlx.NamedStmt
	stmtGetAgentByID      *sqlx.NamedStmt
	stmtCreateAgent       *sqlx.NamedStmt
	stmtUpdateAgent       *sqlx.NamedStmt
	stmtArchiveAgent      *sqlx.NamedStmt
	stmtUpdateAgentStatus *sqlx.NamedStmt

	stmtCreateAgentSession          *sqlx.NamedStmt
	stmtEndAgentSession             *sqlx.NamedStmt
	stmtUpdateAgentSessionHeartbeat *sqlx.NamedStmt
	stmtGetAgentSessions            *sqlx.NamedStmt
	stmtGetAgentSessionByID         *sqlx.NamedStmt
	stmtGetActiveAgentSession       *sqlx.NamedStmt

	stmtGetAgentState       *sqlx.NamedStmt
	stmtGetAgentStateKey    *sqlx.NamedStmt
	stmtUpsertAgentState    *sqlx.NamedStmt
	stmtDeleteAgentStateKey *sqlx.NamedStmt

	stmtGetAgentMessages        *sqlx.NamedStmt
	stmtGetAgentSessionMessages *sqlx.NamedStmt
	stmtCreateAgentMessage      *sqlx.NamedStmt

	stmtGetAgentExecutions         *sqlx.NamedStmt
	stmtCreateAgentExecution       *sqlx.NamedStmt
	stmtUpdateAgentExecutionStatus *sqlx.NamedStmt
	stmtCountAgentExecutionsInHour *sqlx.NamedStmt

	// Agent Memory Phase 1: identity + conversation scoping.
	// See plans/agent_memory.md for the design and
	// internal/persistence/agent_memory.go for the corresponding methods.
	stmtGetAgentUserByID                 *sqlx.NamedStmt
	stmtCreateAgentUser                  *sqlx.NamedStmt
	stmtGetAgentIdentityByExternal       *sqlx.NamedStmt
	stmtCreateAgentIdentity              *sqlx.NamedStmt
	stmtLinkAgentIdentityToUser          *sqlx.NamedStmt
	stmtGetAgentConversationByID         *sqlx.NamedStmt
	stmtGetAgentConversationByKey        *sqlx.NamedStmt
	stmtCreateAgentConversation          *sqlx.NamedStmt
	stmtTouchAgentConversation           *sqlx.NamedStmt
	stmtGetAgentConversationMessages     *sqlx.NamedStmt
	stmtCreateAgentMessageInConversation *sqlx.NamedStmt
	stmtNextAgentConversationSequence    *sqlx.NamedStmt

	// Agent Memory Phase 2: memories, pending actions, commitments.
	// See plans/agent_memory.md and internal/persistence/agent_memory_phase2.go.
	stmtCreateAgentMemory            *sqlx.NamedStmt
	stmtGetAgentMemoryByID           *sqlx.NamedStmt
	stmtGetAgentMemoriesForUser      *sqlx.NamedStmt
	stmtDeleteAgentMemory            *sqlx.NamedStmt
	stmtTouchAgentMemoryLastUsed     *sqlx.NamedStmt
	stmtCreateAgentPendingAction     *sqlx.NamedStmt
	stmtGetAgentPendingActionByID    *sqlx.NamedStmt
	stmtGetOpenPendingActionsForUser *sqlx.NamedStmt
	stmtUpdatePendingActionStatus    *sqlx.NamedStmt
	stmtCreateAgentCommitment        *sqlx.NamedStmt
	stmtGetAgentCommitmentByID       *sqlx.NamedStmt
	stmtGetDueCommitments            *sqlx.NamedStmt
	stmtGetCommitmentsForUser        *sqlx.NamedStmt
	stmtUpdateCommitmentStatus       *sqlx.NamedStmt

	// Agent Memory Phase 4: semantic retrieval with pgvector.
	// See internal/persistence/agent_memory_phase4.go.
	stmtSearchMemoriesByEmbedding   *sqlx.NamedStmt
	stmtGetMemoriesWithoutEmbedding *sqlx.NamedStmt
	stmtUpdateMemoryEmbedding       *sqlx.NamedStmt

	// Agent Memory Phase 5: identity linking.
	stmtGetAgentIdentitiesByUserID    *sqlx.NamedStmt
	stmtGetPendingActionByUserAndType *sqlx.NamedStmt
}

// DB returns the underlying sqlx connection pool for use by metrics collectors
// and other infrastructure code that needs direct database access.
func (s *Service) DB() *sqlx.DB { return s.conn }

func NewService(config *config.Config) (*Service, error) {
	db, err := sqlx.Connect("postgres", fmt.Sprintf("postgres://%v:%v@%v:%d/%v?sslmode=%v",
		config.Database.Username,
		config.Database.Password,
		config.Database.Hostname,
		config.Database.Port,
		config.Database.Database,
		config.Database.SSLModeOverride))
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(config.Database.MaxOpenConnections)
	db.SetMaxIdleConns(config.Database.MaxIdleConnections)

	s := Service{
		config: config,
		conn:   db,
	}

	go s.connectionMonitor()

	s.stmtGetOrganisations, err = s.conn.PrepareNamed(`
		SELECT
		    o.id,
		    o.name,
		    o.icon,
		    o.created_at,
		    o.allow_public_runners,
		    ou.role
		FROM
		    organisation o
		INNER JOIN
		    organisation_user ou ON o.id = ou.organisation_id
		WHERE
		    ou.user_id = :user_id
		ORDER BY
		    o.name;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetOrganisationByID, err = s.conn.PrepareNamed(`
		SELECT
		    id,
		    name,
		    icon,
		    allow_public_runners,
		    created_at
		FROM
		    organisation
		WHERE
		    id = :id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCreateOrganisation, err = s.conn.PrepareNamed(`
		INSERT INTO organisation (
			name,
			icon
		) VALUES (
		    :name,
			:icon
		) RETURNING id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtUpdateOrganisation, err = s.conn.PrepareNamed(`
		UPDATE
		    organisation
		SET
			name = :name,
			icon = :icon,
			allow_public_runners = :allow_public_runners
		WHERE
		    id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtAddUserToOrganisation, err = s.conn.PrepareNamed(`
		INSERT INTO organisation_user (
			organisation_id,
			user_id,
			role
		) VALUES (
		    :organisation_id,
			:user_id,
			:role
		);
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetOrganisationMembers, err = s.conn.PrepareNamed(`
		SELECT
		    ou.user_id,
		    u.name,
		    PGP_SYM_DECRYPT(u.email_address, :encrypt_key) AS email_address,
		    ou.role
		FROM
		    organisation_user ou
		INNER JOIN
		    users u ON u.id = ou.user_id
		WHERE
		    ou.organisation_id = :organisation_id
		ORDER BY
		    ou.role, u.name;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtRemoveUserFromOrganisation, err = s.conn.PrepareNamed(`
		DELETE FROM organisation_user
		WHERE organisation_id = :organisation_id AND user_id = :user_id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetUserRoleInOrganisation, err = s.conn.PrepareNamed(`
		SELECT role FROM organisation_user
		WHERE organisation_id = :organisation_id AND user_id = :user_id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCreateOrganisationInvite, err = s.conn.PrepareNamed(`
		INSERT INTO organisation_invite (
			organisation_id, email, role, created_by
		) VALUES (
			:organisation_id, :email, :role, :created_by
		) RETURNING id, invite_code, created_at, expires_at;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetOrganisationInvites, err = s.conn.PrepareNamed(`
		SELECT
		    id, organisation_id, email, invite_code, role,
		    created_by, created_at, accepted_at, accepted_by, expires_at
		FROM
		    organisation_invite
		WHERE
		    organisation_id = :organisation_id
		    AND accepted_at IS NULL
		    AND expires_at > CURRENT_TIMESTAMP
		ORDER BY
		    created_at DESC;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetInviteByCode, err = s.conn.PrepareNamed(`
		SELECT
		    id, organisation_id, email, invite_code, role,
		    created_by, created_at, accepted_at, accepted_by, expires_at
		FROM
		    organisation_invite
		WHERE
		    invite_code = :invite_code
		    AND accepted_at IS NULL;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetInvitePreview, err = s.conn.PrepareNamed(`
		SELECT
		    o.name AS organisation_name,
		    oi.role
		FROM
		    organisation_invite oi
		    JOIN organisation o ON o.id = oi.organisation_id
		WHERE
		    oi.invite_code = :invite_code
		    AND oi.accepted_at IS NULL;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtAcceptInvite, err = s.conn.PrepareNamed(`
		UPDATE organisation_invite
		SET accepted_at = CURRENT_TIMESTAMP, accepted_by = :accepted_by
		WHERE id = :id AND accepted_at IS NULL;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtRevokeInvite, err = s.conn.PrepareNamed(`
		DELETE FROM organisation_invite WHERE id = :id AND organisation_id = :organisation_id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetUserByID, err = s.conn.PrepareNamed(`
		SELECT
		    id,
		    name,
		    created_at,
		    PGP_SYM_DECRYPT(email_address, :encrypt_key) AS email_address,
		    marketing_opt_in,
		    eula_version,
		    eula_accepted_at,
		    onboarding_step,
		    onboarding_completed_at,
		    checklist_flags
		FROM
		    users
		WHERE
		    id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCreateUser, err = s.conn.PrepareNamed(`
		INSERT INTO users (
		    id,
			name,
		    email_address,
		    marketing_opt_in
		) VALUES (
		  	:id,
			:name,
		    PGP_SYM_ENCRYPT(:email_address, :encrypt_key),
		    :marketing_opt_in
		) RETURNING id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtUpdateUser, err = s.conn.PrepareNamed(`
		UPDATE
		    users
		SET
		    name = :name,
		    email_address = PGP_SYM_ENCRYPT(:email_address, :encrypt_key),
		    marketing_opt_in = :marketing_opt_in
		WHERE
		    id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtAcceptEula, err = s.conn.PrepareNamed(`
		UPDATE users
		SET eula_version = :eula_version, eula_accepted_at = NOW()
		WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetLatestEula, err = s.conn.PrepareNamed(`
		SELECT id, version, content, created_at
		FROM eula
		ORDER BY version DESC
		LIMIT 1
	`)
	if err != nil {
		return nil, err
	}

	s.stmtUpdateOnboarding, err = s.conn.PrepareNamed(`
		UPDATE users
		SET onboarding_step = :onboarding_step,
		    onboarding_completed_at = :onboarding_completed_at
		WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetMyFlos, err = s.conn.PrepareNamed(`
		SELECT
		    f.id,
		    f.name,
		    f.organisation_id,
		    f.author_id,
		    f.created_at,
		    f.scale,
		    f.x,
		    f.y,
		    f.environment_id,
		    f.queue_id,
		    (SELECT name FROM environment e WHERE e.id = f.environment_id) AS environment_name,
			(SELECT
				 COUNT(1)
			 FROM
				 execution e
			 WHERE e.flo_id = f.id) AS execution_count,
			(SELECT
				CASE
					WHEN e.completed_at IS NULL THEN CEIL(EXTRACT(EPOCH FROM CURRENT_TIMESTAMP - e.created_at) / 60)
					ELSE CEIL(EXTRACT(EPOCH FROM e.completed_at - e.created_at) / 60)
				END
			FROM execution e
			WHERE e.flo_id = f.id
			ORDER BY created_at DESC
			LIMIT 1) AS duration,
			(SELECT
				e.created_at
			FROM execution e
			WHERE e.flo_id = f.id
			ORDER BY created_at DESC
			LIMIT 1) AS last_run
		FROM
		    flo f
		WHERE
		    author_id = :author_id
		    AND organisation_id IS NULL
		    AND f.archived_at IS NULL AND f.system_flow = FALSE
		ORDER BY
		    created_at DESC
		OFFSET :offset
		LIMIT :limit
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetMyFlosWithFilter, err = s.conn.PrepareNamed(`
		SELECT
		    f.id,
		    f.name,
		    f.organisation_id,
		    f.author_id,
		    f.created_at,
		    f.scale,
		    f.x,
		    f.y,
		    f.environment_id,
		    f.queue_id,
		    (SELECT name FROM environment e WHERE e.id = f.environment_id) AS environment_name,
			(SELECT
				 COUNT(1)
			 FROM
				 execution e
			 WHERE e.flo_id = f.id) AS execution_count,
			(SELECT
				CASE
					WHEN e.completed_at IS NULL THEN CEIL(EXTRACT(EPOCH FROM CURRENT_TIMESTAMP - e.created_at) / 60)
					ELSE CEIL(EXTRACT(EPOCH FROM e.completed_at - e.created_at) / 60)
				END
			FROM execution e
			WHERE e.flo_id = f.id
			ORDER BY created_at DESC
			LIMIT 1) AS duration,
			(SELECT
				e.created_at
			FROM execution e
			WHERE e.flo_id = f.id
			ORDER BY created_at DESC
			LIMIT 1) AS last_run
		FROM
		    flo f
		WHERE
		    author_id = :author_id
		    AND organisation_id IS NULL
		    AND f.archived_at IS NULL AND f.system_flow = FALSE
		AND
		    (
		    	LOWER(name) LIKE LOWER(:search)
			OR
		    	CAST(id AS TEXT) LIKE LOWER(:search)
		    )
		ORDER BY
		    created_at DESC
		OFFSET :offset
		LIMIT :limit
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCountMyFlos, err = s.conn.PrepareNamed(`
		SELECT
		    COUNT(1)
		FROM
		    flo f
		WHERE
		    author_id = :author_id
		    AND organisation_id IS NULL
		    AND f.archived_at IS NULL AND f.system_flow = FALSE
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCountMyFlosWithFilter, err = s.conn.PrepareNamed(`
		SELECT
		    COUNT(1)
		FROM
		    flo f
		WHERE
		    author_id = :author_id
		    AND organisation_id IS NULL
		    AND f.archived_at IS NULL AND f.system_flow = FALSE
		AND
		    (
		    	LOWER(name) LIKE LOWER(:search)
			OR
		    	CAST(id AS TEXT) LIKE LOWER(:search)
		    )
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetOrgFlos, err = s.conn.PrepareNamed(`
		SELECT
		    f.id,
		    f.name,
		    f.organisation_id,
		    f.author_id,
		    f.created_at,
		    f.scale,
		    f.x,
		    f.y,
		    f.environment_id,
		    f.queue_id,
		    (SELECT name FROM environment e WHERE e.id = f.environment_id) AS environment_name,
			(SELECT COUNT(1) FROM execution e WHERE e.flo_id = f.id) AS execution_count,
			(SELECT CASE WHEN e.completed_at IS NULL THEN CEIL(EXTRACT(EPOCH FROM CURRENT_TIMESTAMP - e.created_at) / 60) ELSE CEIL(EXTRACT(EPOCH FROM e.completed_at - e.created_at) / 60) END FROM execution e WHERE e.flo_id = f.id ORDER BY created_at DESC LIMIT 1) AS duration,
			(SELECT e.created_at FROM execution e WHERE e.flo_id = f.id ORDER BY created_at DESC LIMIT 1) AS last_run
		FROM
		    flo f
		WHERE
		    organisation_id = :organisation_id
		    AND f.archived_at IS NULL AND f.system_flow = FALSE
		ORDER BY
		    created_at DESC
		OFFSET :offset
		LIMIT :limit
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetOrgFlosWithFilter, err = s.conn.PrepareNamed(`
		SELECT
		    f.id,
		    f.name,
		    f.organisation_id,
		    f.author_id,
		    f.created_at,
		    f.scale,
		    f.x,
		    f.y,
		    f.environment_id,
		    f.queue_id,
		    (SELECT name FROM environment e WHERE e.id = f.environment_id) AS environment_name,
			(SELECT COUNT(1) FROM execution e WHERE e.flo_id = f.id) AS execution_count,
			(SELECT CASE WHEN e.completed_at IS NULL THEN CEIL(EXTRACT(EPOCH FROM CURRENT_TIMESTAMP - e.created_at) / 60) ELSE CEIL(EXTRACT(EPOCH FROM e.completed_at - e.created_at) / 60) END FROM execution e WHERE e.flo_id = f.id ORDER BY created_at DESC LIMIT 1) AS duration,
			(SELECT e.created_at FROM execution e WHERE e.flo_id = f.id ORDER BY created_at DESC LIMIT 1) AS last_run
		FROM
		    flo f
		WHERE
		    organisation_id = :organisation_id
		    AND f.archived_at IS NULL AND f.system_flow = FALSE
		AND
		    (LOWER(name) LIKE LOWER(:search) OR CAST(id AS TEXT) LIKE LOWER(:search))
		ORDER BY
		    created_at DESC
		OFFSET :offset
		LIMIT :limit
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCountOrgFlos, err = s.conn.PrepareNamed(`
		SELECT COUNT(1) FROM flo f WHERE organisation_id = :organisation_id AND f.archived_at IS NULL AND f.system_flow = FALSE
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCountOrgFlosWithFilter, err = s.conn.PrepareNamed(`
		SELECT COUNT(1) FROM flo f
		WHERE organisation_id = :organisation_id AND f.archived_at IS NULL AND f.system_flow = FALSE
		AND (LOWER(name) LIKE LOWER(:search) OR CAST(id AS TEXT) LIKE LOWER(:search))
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetFloByID, err = s.conn.PrepareNamed(`
		SELECT
		    f.id,
		    f.name,
		    f.organisation_id,
		    f.author_id,
		    f.created_at,
		    f.scale,
		    f.x,
		    f.y,
		    f.environment_id,
		    f.queue_id,
		    f.notify_on_success,
		    f.notify_on_failure,
		    f.notification_emails,
		    f.system_flow,
		    f.system_flow_purpose,
		    f.system_prompt,
		    (SELECT name FROM environment e WHERE e.id = f.environment_id) AS environment_name,
			(SELECT
				 COUNT(1)
			 FROM
				 execution e
			 WHERE e.flo_id = f.id) AS execution_count,
			(SELECT
				CASE
					WHEN e.completed_at IS NULL THEN CEIL(EXTRACT(EPOCH FROM CURRENT_TIMESTAMP - e.created_at) / 60)
					ELSE CEIL(EXTRACT(EPOCH FROM e.completed_at - e.created_at) / 60)
				END
			FROM execution e
			WHERE e.flo_id = f.id
			ORDER BY created_at DESC
			LIMIT 1) AS duration,
			(SELECT
				e.created_at
			FROM execution e
			WHERE e.flo_id = f.id
			ORDER BY created_at DESC
			LIMIT 1) AS last_run
		FROM
		    flo f
		WHERE
		    id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCreateFlo, err = s.conn.PrepareNamed(`
		INSERT INTO flo (
			name, 
			organisation_id, 
			author_id
		 ) VALUES ( 
			:name, 
			:organisation_id, 
			:author_id 		           
		 ) RETURNING id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtInsertDefaultTrigger, err = s.conn.PrepareNamed(`
		INSERT INTO trigger (
			name, 
		    owner_id,
			organisation_id, 
			type
		 ) VALUES ( 
			:name, 
			:owner_id,
			:organisation_id, 
			(SELECT id FROM trigger_type WHERE name = 'manual')           
		 ) RETURNING id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtLinkFloToTrigger, err = s.conn.PrepareNamed(`
		INSERT INTO flo_trigger (
			flo_id,
			trigger_id
		) VALUES (
		    :flo_id,
		    :trigger_id
		);
	`)
	if err != nil {
		return nil, err
	}

	s.stmtRemoveFloTriggerLink, err = s.conn.PrepareNamed(`
		DELETE FROM 
		   flo_trigger
		WHERE
		    flo_id = :flo_id
		AND
		    trigger_id = :trigger_id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetFloTriggers, err = s.conn.PrepareNamed(`
		SELECT
		    t.id,
			t.name,
			t.owner_id,
			t.organisation_id,
			t.type,
			tt.name AS type_name,
			t.data
		FROM
			trigger t
		INNER JOIN trigger_type tt ON t.type = tt.id
		INNER JOIN flo_trigger ft ON ft.trigger_id = t.id
		INNER JOIN flo f ON ft.flo_id = f.id
		WHERE
		    f.id = :id
		ORDER BY
		    t.name ASC
	`)
	if err != nil {
		return nil, err
	}

	s.stmtUpdateFlo, err = s.conn.PrepareNamed(`
		UPDATE flo
		SET
		    name = :name,
			organisation_id = :organisation_id,
			author_id = :author_id,
			scale = :scale,
			x = :x,
			y = :y,
			environment_id = :environment_id,
			queue_id = :queue_id,
			notify_on_success = :notify_on_success,
			notify_on_failure = :notify_on_failure,
			notification_emails = :notification_emails
		WHERE
		    id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtDeleteFlo, err = s.conn.PrepareNamed(`
		UPDATE flo SET archived_at = NOW()
		WHERE
		    id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCreateFloRevision, err = s.conn.PrepareNamed(`
		INSERT INTO revision (
			flo_id, 
			data 
		 ) VALUES ( 
			:flo_id, 
			:data	           
		 ) RETURNING id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetLatestFloRevisionByFloID, err = s.conn.PrepareNamed(`
		SELECT
		    id,
		    flo_id,
		    created_at,
		    data
		FROM
		    revision
		WHERE
		    flo_id = :flo_id
		ORDER BY
		    created_at DESC
		LIMIT 1
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetFloRevisions, err = s.conn.PrepareNamed(`
		SELECT
		    id,
		    flo_id,
		    created_at,
		    data
		FROM
		    revision
		WHERE
		    flo_id = :flo_id
		ORDER BY
		    created_at DESC
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetFloRevisionByID, err = s.conn.PrepareNamed(`
		SELECT
		    id,
		    flo_id,
		    created_at,
		    data
		FROM
		    revision
		WHERE
		    id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetLatestExecutionForFlo, err = s.conn.PrepareNamed(`
		SELECT
		    id,
		    flo_id,
		    name,
		    owner_id,
		    organisation_id,
		    created_at,
		    updated_at,
		    completed_at,
		    triggered_by,
		    execution_status,
		    completion_status,
		    result->'duration' AS duration,
		    result->'billingDuration' AS billing_duration
		FROM
		    execution
		WHERE
		    flo_id = :flo_id
		ORDER BY created_at DESC
		LIMIT 1
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetRecentExecutionsForFlo, err = s.conn.PrepareNamed(`
		SELECT id, execution_status, completion_status
		FROM execution
		WHERE flo_id = :flo_id
		ORDER BY created_at DESC
		LIMIT 5
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetExecutions, err = s.conn.PrepareNamed(`
		SELECT
		    e.id, e.flo_id, f.name, e.owner_id, e.organisation_id,
		    e.created_at, e.updated_at, e.completed_at, e.triggered_by,
		    e.execution_status, e.completion_status,
			e.runner_id, r.name AS runner_name,
			e.result->'duration' AS duration,
			e.result->'billingDuration' AS billing_duration,
			ROW_NUMBER() OVER (PARTITION BY e.flo_id ORDER BY e.created_at) AS sequence,
			tt.name AS trigger_type,
			e.agent_id
		FROM execution e
		INNER JOIN flo f ON f.id = e.flo_id AND f.archived_at IS NULL AND f.system_flow = FALSE
		LEFT JOIN runner r ON r.id = e.runner_id
		LEFT JOIN trigger_invocation ti ON ti.id = e.triggered_by
		LEFT JOIN trigger t ON t.id = ti.trigger_id
		LEFT JOIN trigger_type tt ON tt.id = t.type
		WHERE e.owner_id = :user_id AND e.organisation_id IS NULL
		ORDER BY e.created_at DESC
		OFFSET :offset LIMIT :limit
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetExecutionsWithFilter, err = s.conn.PrepareNamed(`
		SELECT
		    e.id, e.flo_id, f.name, e.owner_id, e.organisation_id,
		    e.created_at, e.updated_at, e.completed_at, e.triggered_by,
		    e.execution_status, e.completion_status,
			e.runner_id, r.name AS runner_name,
			e.result->'duration' AS duration, e.result->'billingDuration' AS billing_duration,
			ROW_NUMBER() OVER (PARTITION BY e.flo_id ORDER BY e.created_at) AS sequence,
			tt.name AS trigger_type,
			e.agent_id
		FROM execution e
		INNER JOIN flo f ON f.id = e.flo_id AND f.archived_at IS NULL AND f.system_flow = FALSE
		LEFT JOIN runner r ON r.id = e.runner_id
		LEFT JOIN trigger_invocation ti ON ti.id = e.triggered_by
		LEFT JOIN trigger t ON t.id = ti.trigger_id
		LEFT JOIN trigger_type tt ON tt.id = t.type
		WHERE (CAST(e.id AS TEXT) LIKE LOWER(:search) OR LOWER(f.name) LIKE LOWER(:search))
		AND e.owner_id = :user_id AND e.organisation_id IS NULL
		ORDER BY e.created_at DESC
		OFFSET :offset LIMIT :limit
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCountExecutions, err = s.conn.PrepareNamed(`
		SELECT COUNT(1) FROM execution e
		INNER JOIN flo f ON f.id = e.flo_id AND f.archived_at IS NULL AND f.system_flow = FALSE
		WHERE e.owner_id = :user_id AND e.organisation_id IS NULL
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCountExecutionsWithFilter, err = s.conn.PrepareNamed(`
		SELECT COUNT(1) FROM execution e
		INNER JOIN flo f ON f.id = e.flo_id AND f.archived_at IS NULL AND f.system_flow = FALSE
		WHERE (CAST(e.id AS TEXT) LIKE LOWER(:search) OR LOWER(f.name) LIKE LOWER(:search))
		AND e.owner_id = :user_id AND e.organisation_id IS NULL
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetOrgExecutions, err = s.conn.PrepareNamed(`
		SELECT
		    e.id, e.flo_id, f.name, e.owner_id, e.organisation_id,
		    e.created_at, e.updated_at, e.completed_at, e.triggered_by,
		    e.execution_status, e.completion_status,
			e.runner_id, r.name AS runner_name,
			e.result->'duration' AS duration, e.result->'billingDuration' AS billing_duration,
			ROW_NUMBER() OVER (PARTITION BY e.flo_id ORDER BY e.created_at) AS sequence,
			tt.name AS trigger_type,
			e.agent_id
		FROM execution e
		INNER JOIN flo f ON f.id = e.flo_id AND f.archived_at IS NULL AND f.system_flow = FALSE
		LEFT JOIN runner r ON r.id = e.runner_id
		LEFT JOIN trigger_invocation ti ON ti.id = e.triggered_by
		LEFT JOIN trigger t ON t.id = ti.trigger_id
		LEFT JOIN trigger_type tt ON tt.id = t.type
		WHERE e.organisation_id = :organisation_id
		ORDER BY e.created_at DESC
		OFFSET :offset LIMIT :limit
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetOrgExecutionsWithFilter, err = s.conn.PrepareNamed(`
		SELECT
		    e.id, e.flo_id, f.name, e.owner_id, e.organisation_id,
		    e.created_at, e.updated_at, e.completed_at, e.triggered_by,
		    e.execution_status, e.completion_status,
			e.runner_id, r.name AS runner_name,
			e.result->'duration' AS duration, e.result->'billingDuration' AS billing_duration,
			ROW_NUMBER() OVER (PARTITION BY e.flo_id ORDER BY e.created_at) AS sequence,
			tt.name AS trigger_type,
			e.agent_id
		FROM execution e
		INNER JOIN flo f ON f.id = e.flo_id AND f.archived_at IS NULL AND f.system_flow = FALSE
		LEFT JOIN runner r ON r.id = e.runner_id
		LEFT JOIN trigger_invocation ti ON ti.id = e.triggered_by
		LEFT JOIN trigger t ON t.id = ti.trigger_id
		LEFT JOIN trigger_type tt ON tt.id = t.type
		WHERE (CAST(e.id AS TEXT) LIKE LOWER(:search) OR LOWER(f.name) LIKE LOWER(:search))
		AND e.organisation_id = :organisation_id
		ORDER BY e.created_at DESC
		OFFSET :offset LIMIT :limit
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCountOrgExecutions, err = s.conn.PrepareNamed(`
		SELECT COUNT(1) FROM execution e
		INNER JOIN flo f ON f.id = e.flo_id AND f.archived_at IS NULL AND f.system_flow = FALSE
		WHERE e.organisation_id = :organisation_id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCountOrgExecutionsWithFilter, err = s.conn.PrepareNamed(`
		SELECT COUNT(1) FROM execution e
		INNER JOIN flo f ON f.id = e.flo_id AND f.archived_at IS NULL AND f.system_flow = FALSE
		WHERE (CAST(e.id AS TEXT) LIKE LOWER(:search) OR LOWER(f.name) LIKE LOWER(:search))
		AND e.organisation_id = :organisation_id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetDefaultTriggerForFlo, err = s.conn.PrepareNamed(`
		SELECT
			t.id AS trigger_id,
			t.owner_id AS owner_id,
			t.organisation_id AS organisation_id
		FROM
			flo f
		INNER JOIN flo_trigger ft ON ft.flo_id = f.id
		INNER JOIN trigger t ON t.id = ft.trigger_id
		INNER JOIN trigger_type tt on t.type = tt.id
		WHERE tt.name = 'manual'
		AND f.id = :flo_id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetTriggerForFlo, err = s.conn.PrepareNamed(`
		SELECT
			t.id AS trigger_id,
			t.owner_id AS owner_id,
			t.organisation_id AS organisation_id
		FROM
			flo f
		INNER JOIN flo_trigger ft ON ft.flo_id = f.id
		INNER JOIN trigger t ON t.id = ft.trigger_id		
		WHERE t.id = :trigger_id
		AND f.id = :flo_id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtInsertTriggerInvocation, err = s.conn.PrepareNamed(`
		INSERT INTO trigger_invocation (
			trigger_id, 
			owner_id, 
			organisation_id, 
			data
		) VALUES (
			:trigger_id, 
			:owner_id, 
			:organisation_id, 
			:data
		) RETURNING id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetTriggerInvocation, err = s.conn.PrepareNamed(`
		SELECT
		    id,
		    trigger_id,
		    owner_id,
		    organisation_id,
		    data
		FROM
		    trigger_invocation
		WHERE
		    id = :id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetFlosForTrigger, err = s.conn.PrepareNamed(`
		SELECT
		    flo_id
		FROM
		    flo_trigger
		WHERE
		    trigger_id = :trigger_id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtInsertFloExecution, err = s.conn.PrepareNamed(`
		INSERT INTO execution (
			flo_id, 
			name, 
		    owner_id,
			organisation_id,
		   	triggered_by,
			execution_status,
		   	completion_status,
			data
		) VALUES (
			:flo_id, 
			:name, 
		    :owner_id,
			:organisation_id,
		   	:triggered_by,
			:execution_status,
		   	:completion_status,
			:data
		) RETURNING id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtUpdateFloExecutionStatus, err = s.conn.PrepareNamed(`
		UPDATE execution
		SET
		    execution_status = :execution_status,
			updated_at = CURRENT_TIMESTAMP
		WHERE
		    id = :id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtUpdateFloCompletionStatus, err = s.conn.PrepareNamed(`
		UPDATE execution
		SET
		    completion_status = :completion_status,
			updated_at = CURRENT_TIMESTAMP
		WHERE
		    id = :id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtUpdateExecutionResult, err = s.conn.PrepareNamed(`
		UPDATE execution
		SET
		    result = :result,
			completed_at = CURRENT_TIMESTAMP
		WHERE
		    id = :id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtUpdateExecutionRunnerID, err = s.conn.PrepareNamed(`
		UPDATE execution
		SET
		    runner_id = :runner_id
		WHERE
		    id = :id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetExecutionByID, err = s.conn.PrepareNamed(`
		SELECT
			e.id,
			e.flo_id,
			e.name,
			e.owner_id,
			e.organisation_id,
			e.created_at,
			e.updated_at,
			e.completed_at,
			e.triggered_by,
			e.execution_status,
			e.completion_status,
			e.data,
			e.runner_id,
			r.name AS runner_name,
			e.result,
			e.result->'duration' AS duration,
			e.result->'billingDuration' AS billing_duration,
			(SELECT COUNT(1) FROM execution e2 WHERE e2.flo_id = e.flo_id AND e2.created_at <= e.created_at) AS sequence,
			e.agent_id,
			e.agent_session_id
		FROM
		    execution e
		LEFT JOIN runner r ON r.id = e.runner_id
		WHERE
		    e.id = :id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetActions, err = s.conn.PrepareNamed(`
		SELECT
			id,
			name,
			action_type,
			description,
			icon,
			plugin,
			ordering,
			inputs,
			outputs
		FROM 
			actions
		ORDER BY 
			action_type, ordering
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetRunnerByID, err = db.PrepareNamed(`
		SELECT
		    id,
		    identifier,
		    name,
		    registration_code,
		    enrolled_at,
		    last_contact_at,
		    ip,
			CASE
				WHEN (CURRENT_TIMESTAMP - last_contact_at) > '6 hours' THEN 'terminated'
				WHEN (CURRENT_TIMESTAMP - last_contact_at) > '1 hour' THEN 'suspended'
				ELSE 'active'
			END AS state,
		    active,
		    version,
		    executor_version,
		    public_key,
		    CASE
		    	WHEN public_key IS NOT NULL THEN true
				ELSE false
			END AS verified
		FROM
		    runner
		WHERE
		    id = :id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetRunnerByIdentifier, err = db.PrepareNamed(`
		SELECT
		    id,
		    identifier,
		    name,
		    registration_code,
		    enrolled_at,
		    last_contact_at,
		    ip,
			CASE
				WHEN (CURRENT_TIMESTAMP - last_contact_at) > '6 hours' THEN 'terminated'
				WHEN (CURRENT_TIMESTAMP - last_contact_at) > '1 hour' THEN 'suspended'
				ELSE 'active'
			END AS state,
		    active,
		    version,
		    executor_version,
		    public_key,
		    CASE
		    	WHEN public_key IS NOT NULL THEN true
				ELSE false
			END AS verified
		FROM
		    runner
		WHERE
		    identifier = :identifier;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetRunners, err = db.PrepareNamed(`
		SELECT
		    id,
		    identifier,
		    name,
		    registration_code,
		    enrolled_at,
		    last_contact_at,
		    ip,
			CASE
				WHEN (CURRENT_TIMESTAMP - last_contact_at) > '6 hours' THEN 'terminated'
				WHEN (CURRENT_TIMESTAMP - last_contact_at) > '1 hour' THEN 'suspended'
				ELSE 'active'
			END AS state,
		    active,
		    version,
		    executor_version,
		    public_key,
		    CASE
		    	WHEN public_key IS NOT NULL THEN true
				ELSE false
			END AS verified
		FROM
		    runner
		WHERE
		    (CURRENT_TIMESTAMP - last_contact_at) <= '6 hours'
		ORDER BY
		    last_contact_at 
		DESC
	`)
	if err != nil {
		return nil, err
	}

	s.stmtInsertRunner, err = db.PrepareNamed(`
		INSERT INTO runner (
			identifier,
			name,
			registration_code,
			last_contact_at,
			ip,
		    version,
		    executor_version,
			public_key
		) VALUES (
		    :identifier,
			:name,
			:registration_code,
			CURRENT_TIMESTAMP,
			:ip,
		    :version,
		    :executor_version,
			:public_key
		) RETURNING id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtUpdateRunnerLastAccess, err = db.PrepareNamed(`
		UPDATE 
		    runner
		SET
		    last_contact_at = CURRENT_TIMESTAMP,
		    ip = :ip
		WHERE
		    id = :id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtInsertQueueRunner, err = db.PrepareNamed(`
		INSERT INTO queue_runner (
			queue_id,
			runner_id
		) VALUES (
			:queue_id,
			:runner_id		          
		)
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCanRunnerAccessQueue, err = db.PrepareNamed(`
		SELECT 
		    COUNT(1) 
		FROM
		    queue_runner	
		WHERE
		    queue_id = :queue_id
		AND
		    runner_id = :runner_id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetQueueByRegistrationCode, err = db.PrepareNamed(`
		SELECT
		    id,
		    organisation_id,
		    name,
		    registration_code,
		    created_at,
		    location_code
		FROM
		    queue
		WHERE
		    registration_code = :registration_code
	`)
	if err != nil {
		return nil, err
	}

	s.stmtRemoveQueueRunner, err = db.PrepareNamed(`
		DELETE FROM queue_runner WHERE queue_id = :queue_id AND runner_id = :runner_id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetQueuesByOrganisationID, err = db.PrepareNamed(`
		SELECT id, organisation_id, parent_id, name, registration_code, created_at, location_code
		FROM queue WHERE organisation_id = :organisation_id ORDER BY name
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetQueueByID, err = db.PrepareNamed(`
		SELECT id, organisation_id, parent_id, name, registration_code, created_at, location_code
		FROM queue WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCreateQueue, err = db.PrepareNamed(`
		INSERT INTO queue (organisation_id, parent_id, name) VALUES (:organisation_id, :parent_id, :name)
		RETURNING id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtDeleteQueue, err = db.PrepareNamed(`
		DELETE FROM queue WHERE id = :id AND organisation_id = :organisation_id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetQueueRunners, err = db.PrepareNamed(`
		SELECT r.id, r.identifier, r.name, r.registration_code, r.enrolled_at, r.last_contact_at,
		       r.ip, r.active, r.version, r.executor_version, r.public_key,
		       CASE
		           WHEN (CURRENT_TIMESTAMP - r.last_contact_at) > '6 hours' THEN 'terminated'
		           WHEN (CURRENT_TIMESTAMP - r.last_contact_at) > '1 hour' THEN 'suspended'
		           ELSE 'active'
		       END AS state,
		       CASE WHEN r.public_key IS NOT NULL THEN true ELSE false END AS verified
		FROM runner r
		INNER JOIN queue_runner qr ON qr.runner_id = r.id
		WHERE qr.queue_id = :queue_id
		ORDER BY r.name
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetOrganisationByRunnerIdentifier, err = db.PrepareNamed(`
		SELECT
        	q.organisation_id
		FROM
			queue q
		INNER JOIN queue_runner qr ON qr.runner_id = :runner_id
	`)
	if err != nil {
		return nil, err
	}

	// Atomic claim with priority: agent flows run before system flows,
	// and only one system flow execution can run at a time. This prevents
	// the extraction pipeline from consuming all runner capacity and
	// blocking agent responses.
	s.stmtGetPendingExecutionByOrganisationID, err = db.PrepareNamed(`
		UPDATE execution
		SET execution_status = 'running'
		WHERE id = (
		    SELECT e.id FROM execution e
		    JOIN flo f ON f.id = e.flo_id
		    WHERE e.organisation_id = :organisation_id
		    AND e.execution_status = 'created'
		    AND (
		        f.system_flow = FALSE
		        OR NOT EXISTS (
		            SELECT 1 FROM execution e2
		            JOIN flo f2 ON f2.id = e2.flo_id
		            WHERE f2.system_flow = TRUE
		            AND e2.execution_status = 'running'
		        )
		    )
		    ORDER BY (CASE WHEN f.system_flow THEN 1 ELSE 0 END) ASC, e.created_at ASC
		    LIMIT 1
		    FOR UPDATE OF e SKIP LOCKED
		)
		RETURNING
		    id, flo_id, name, owner_id, organisation_id,
		    created_at, updated_at, completed_at, triggered_by,
		    execution_status, completion_status, data, runner_id, result,
		    result->'duration' AS duration,
		    result->'billingDuration' AS billing_duration,
		    (SELECT COUNT(1) FROM execution e2 WHERE e2.flo_id = execution.flo_id AND e2.created_at <= execution.created_at) AS sequence
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetPendingExecutionByNullOrganisationID, err = db.PrepareNamed(`
		UPDATE execution
		SET execution_status = 'running'
		WHERE id = (
		    SELECT e.id
		    FROM execution e
		    JOIN flo f ON f.id = e.flo_id
		    LEFT JOIN organisation o ON e.organisation_id = o.id
		    WHERE e.execution_status = 'created'
		    AND (e.organisation_id IS NULL OR o.allow_public_runners = true)
		    AND (
		        f.system_flow = FALSE
		        OR NOT EXISTS (
		            SELECT 1 FROM execution e2
		            JOIN flo f2 ON f2.id = e2.flo_id
		            WHERE f2.system_flow = TRUE
		            AND e2.execution_status = 'running'
		        )
		    )
		    ORDER BY (CASE WHEN f.system_flow THEN 1 ELSE 0 END) ASC, e.created_at ASC
		    LIMIT 1
		    FOR UPDATE OF e SKIP LOCKED
		)
		RETURNING
		    id, flo_id, name, owner_id, organisation_id,
		    created_at, updated_at, completed_at, triggered_by,
		    execution_status, completion_status, data, runner_id, result,
		    result->'duration' AS duration,
		    result->'billingDuration' AS billing_duration,
		    (SELECT COUNT(1) FROM execution e2 WHERE e2.flo_id = execution.flo_id AND e2.created_at <= execution.created_at) AS sequence
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCreateEnvironment, err = db.PrepareNamed(`
		INSERT INTO environment (
		    name,
			owner_id,
			organisation_id,
			secret_key
		) VALUES (
		    :name,
			:owner_id,
			:organisation_id,
			PGP_SYM_ENCRYPT(:secret_key, :encrypt_key) 
		) RETURNING id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetEnvironmentByID, err = db.PrepareNamed(`
		SELECT
		    id,
		    name,
		    owner_id,
		    organisation_id,
		    PGP_SYM_DECRYPT(secret_key, :encrypt_key) AS secret_key,
		    created_at
		FROM
		    environment
		WHERE
		    id = :id
		AND
		    (owner_id = :owner_id OR organisation_id = :organisation_id)
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetEnvironmentByIDDirect, err = db.PrepareNamed(`
		SELECT
		    id,
		    name,
		    owner_id,
		    organisation_id,
		    PGP_SYM_DECRYPT(secret_key, :encrypt_key) AS secret_key,
		    created_at
		FROM
		    environment
		WHERE
		    id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetEnvironmentByName, err = db.PrepareNamed(`
		SELECT
		    id,
		    name,
		    owner_id,
		    organisation_id,
		    PGP_SYM_DECRYPT(secret_key, :encrypt_key) AS secret_key,
		    created_at
		FROM
		    environment
		WHERE
		    name = :name
		AND
		    (owner_id = :owner_id OR organisation_id = :organisation_id)
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetEnvironmentByIDAsRunner, err = db.PrepareNamed(`
		SELECT
		    id,
		    name,
		    owner_id,
		    organisation_id,
		    PGP_SYM_DECRYPT(secret_key, :encrypt_key) AS secret_key,
		    created_at
		FROM
		    environment
		WHERE
		    id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetAllEnvironments, err = db.PrepareNamed(`
		SELECT
		    id,
		    name,
		    owner_id,
		    organisation_id,
		    secret_key,
		    created_at
		FROM
		    environment
		WHERE
		    owner_id = :owner_id
		    AND organisation_id IS NULL
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetOrgEnvironments, err = db.PrepareNamed(`
		SELECT
		    id,
		    name,
		    owner_id,
		    organisation_id,
		    secret_key,
		    created_at
		FROM
		    environment
		WHERE
		    organisation_id = :organisation_id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtDeleteEnvironmentByID, err = db.PrepareNamed(`
		DELETE FROM 
			environment
		WHERE 
		    id = :id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetEnvironmentProperties, err = db.PrepareNamed(`
		SELECT
		    id,
		    environment_id,
		    name,
		    PGP_SYM_DECRYPT(value, :environment_key) AS value,
		    created_at
		FROM
		    environment_property
		WHERE
		    environment_id = :environment_id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetEnvironmentPropertyByID, err = db.PrepareNamed(`
		SELECT
		    id,
		    environment_id,
		    name,
		    PGP_SYM_DECRYPT(value, :environment_key) AS value,
		    created_at
		FROM
		    environment_property
		WHERE
		    id = :id
		AND
		    environment_id = :environment_id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetEnvironmentPropertyByName, err = db.PrepareNamed(`
		SELECT
		    id,
		    environment_id,
		    name,
		    PGP_SYM_DECRYPT(value, :environment_key) AS value,
		    created_at
		FROM
		    environment_property
		WHERE
		    name = :name
		AND
		    environment_id = :environment_id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetEnvironmentPropertyByID, err = db.PrepareNamed(`
		SELECT
		    id,
		    environment_id,
		    name,
		    PGP_SYM_DECRYPT(value, :environment_key) AS value,
		    created_at
		FROM
		    environment_property
		WHERE
		    id = :id
		AND
		    environment_id = :environment_id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtInsertEnvironmentProperty, err = db.PrepareNamed(`
		INSERT INTO environment_property (
			environment_id,
		    name,
		    value
		) VALUES (
		    :environment_id,
		    :name,
		    PGP_SYM_ENCRYPT(:value, :environment_key)
		) RETURNING id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtUpdateEnvironmentProperty, err = db.PrepareNamed(`
		UPDATE environment_property
		SET 
		    name = :name,
		    value = PGP_SYM_ENCRYPT(:value, :environment_key)
		WHERE
		    id = :id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtDeleteEnvironmentProperty, err = db.PrepareNamed(`
		DELETE FROM environment_property
		WHERE id = :id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetEnvironmentSecrets, err = db.PrepareNamed(`
		SELECT
		    id,
		    environment_id,
		    name,
		    PGP_SYM_DECRYPT(value, :environment_key) AS value,
		    provider,
		    expires_at,
		    created_at
		FROM
		    environment_secret
		WHERE
		    environment_id = :environment_id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetEnvironmentSecretByID, err = db.PrepareNamed(`
		SELECT
		    id,
		    environment_id,
		    name,
		    PGP_SYM_DECRYPT(value, :environment_key) AS value,
		    provider,
		    expires_at,
		    created_at
		FROM
		    environment_secret
		WHERE
		    id = :id
		AND
		    environment_id = :environment_id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetEnvironmentSecretByName, err = db.PrepareNamed(`
		SELECT
		    id,
		    environment_id,
		    name,
		    PGP_SYM_DECRYPT(value, :environment_key) AS value,
		    provider,
		    expires_at,
		    created_at
		FROM
		    environment_secret
		WHERE
		    name = :name
		AND
		    environment_id = :environment_id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtInsertEnvironmentSecret, err = db.PrepareNamed(`
		INSERT INTO environment_secret (
			environment_id,
		    name,
		    value,
			provider,
			expires_at
		) VALUES (
		    :environment_id,
		    :name,
		    PGP_SYM_ENCRYPT(:value, :environment_key),
			:provider,
			:expires_at
		) RETURNING id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtDeleteEnvironmentSecret, err = db.PrepareNamed(`
		DELETE FROM environment_secret
		WHERE id = :id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtUpdateEnvironmentSecret, err = db.PrepareNamed(`
		UPDATE environment_secret
		SET value = PGP_SYM_ENCRYPT(:value, :environment_key)
		WHERE id = :id AND environment_id = :environment_id;
	`)
	if err != nil {
		return nil, err
	}

	// Personal mode: sum execution time for flows authored by this user
	s.stmtGetUsageThisMonthForUserID, err = db.PrepareNamed(`
		SELECT
			COALESCE(SUM(CASE
				WHEN e.result->'duration' IS NULL THEN 0
				ELSE CAST(e.result->>'duration' AS INT)
			END), 0) AS usage,
		    50 * 60 * 1000 AS allowance
		FROM
		    execution e
		INNER JOIN flo f ON f.id = e.flo_id
		WHERE
			e.created_at > cast(date_trunc('month', current_date) as date)
		AND
			f.author_id = :owner_id
		AND
			e.organisation_id IS NULL;
	`)
	if err != nil {
		return nil, err
	}

	// Organisation mode: sum execution time for flows this user triggered
	s.stmtGetUsageThisMonthForOrgID, err = db.PrepareNamed(`
		SELECT
			COALESCE(SUM(CASE
				WHEN e.result->'duration' IS NULL THEN 0
				ELSE CAST(e.result->>'duration' AS INT)
			END), 0) AS usage,
		    50 * 60 * 1000 AS allowance
		FROM
		    execution e
		WHERE
			e.created_at > cast(date_trunc('month', current_date) as date)
		AND
			e.owner_id = :owner_id
		AND
			e.organisation_id = :organisation_id;
	`)
	if err != nil {
		return nil, err
	}

	// ── Subscription entitlements ─────────────────────────────────────

	s.stmtUpsertEntitlement, err = db.PrepareNamed(`
		INSERT INTO subscription_entitlement (owner_id, organisation_id, plan_slug, entitlement_key, value_int, value_bool, value_json, subscription_status, period_end, updated_at)
		VALUES (:owner_id, :organisation_id, :plan_slug, :entitlement_key, :value_int, :value_bool, :value_json, :subscription_status, :period_end, NOW())
		ON CONFLICT (owner_id, organisation_id, entitlement_key)
		DO UPDATE SET
			plan_slug = EXCLUDED.plan_slug,
			value_int = EXCLUDED.value_int,
			value_bool = EXCLUDED.value_bool,
			value_json = EXCLUDED.value_json,
			subscription_status = EXCLUDED.subscription_status,
			period_end = EXCLUDED.period_end,
			updated_at = NOW()`)
	if err != nil {
		return nil, err
	}

	s.stmtGetEntitlement, err = db.PrepareNamed(`
		SELECT * FROM subscription_entitlement
		WHERE owner_id = :owner_id
		  AND entitlement_key = :entitlement_key
		  AND organisation_id IS NOT DISTINCT FROM CAST(:organisation_id AS VARCHAR)`)
	if err != nil {
		return nil, err
	}

	s.stmtGetAllEntitlements, err = db.PrepareNamed(`
		SELECT * FROM subscription_entitlement
		WHERE owner_id = :owner_id
		  AND organisation_id IS NOT DISTINCT FROM CAST(:organisation_id AS VARCHAR)
		ORDER BY entitlement_key`)
	if err != nil {
		return nil, err
	}

	s.stmtDeleteEntitlements, err = db.PrepareNamed(`
		DELETE FROM subscription_entitlement
		WHERE owner_id = :owner_id
		  AND organisation_id IS NOT DISTINCT FROM CAST(:organisation_id AS VARCHAR)`)
	if err != nil {
		return nil, err
	}

	// Allowance queries: read execution_minutes entitlement, fall back to
	// the hardcoded 50 minutes if no entitlement exists.
	// Usage queries: calculate from the billing period start (period_end - 1 month),
	// falling back to the calendar month if no subscription entitlement exists.
	s.stmtGetAllowanceForOwner, err = db.PrepareNamed(`
		SELECT
			COALESCE(SUM(CASE
				WHEN e.result->'duration' IS NULL THEN 0
				ELSE CAST(e.result->>'duration' AS INT)
			END), 0) AS usage,
			COALESCE(
				(SELECT se.value_int * 60 * 1000 FROM subscription_entitlement se
				 WHERE se.owner_id = :owner_id
				   AND se.organisation_id IS NULL
				   AND se.entitlement_key = 'execution_minutes'
				   AND se.subscription_status IN ('active', 'trialling', 'past_due')
				 LIMIT 1),
				50 * 60 * 1000
			) AS allowance
		FROM
			execution e
		INNER JOIN flo f ON f.id = e.flo_id
		WHERE
			e.created_at > COALESCE(
				(SELECT se.period_end - INTERVAL '1 month' FROM subscription_entitlement se
				 WHERE se.owner_id = :owner_id
				   AND se.organisation_id IS NULL
				   AND se.entitlement_key = 'execution_minutes'
				   AND se.subscription_status IN ('active', 'trialling', 'past_due')
				 LIMIT 1),
				cast(date_trunc('month', current_date) as date)
			)
		AND
			f.author_id = :owner_id
		AND
			e.organisation_id IS NULL`)
	if err != nil {
		return nil, err
	}

	s.stmtGetAllowanceForOrg, err = db.PrepareNamed(`
		SELECT
			COALESCE(SUM(CASE
				WHEN e.result->'duration' IS NULL THEN 0
				ELSE CAST(e.result->>'duration' AS INT)
			END), 0) AS usage,
			COALESCE(
				(SELECT se.value_int * 60 * 1000 FROM subscription_entitlement se
				 WHERE se.owner_id = :owner_id
				   AND se.organisation_id = :organisation_id
				   AND se.entitlement_key = 'execution_minutes'
				   AND se.subscription_status IN ('active', 'trialling', 'past_due')
				 LIMIT 1),
				50 * 60 * 1000
			) AS allowance
		FROM
			execution e
		WHERE
			e.created_at > COALESCE(
				(SELECT se.period_end - INTERVAL '1 month' FROM subscription_entitlement se
				 WHERE se.owner_id = :owner_id
				   AND se.organisation_id = :organisation_id
				   AND se.entitlement_key = 'execution_minutes'
				   AND se.subscription_status IN ('active', 'trialling', 'past_due')
				 LIMIT 1),
				cast(date_trunc('month', current_date) as date)
			)
		AND
			e.owner_id = :owner_id
		AND
			e.organisation_id = :organisation_id`)
	if err != nil {
		return nil, err
	}

	s.stmtGetTriggers, err = s.conn.PrepareNamed(`
		SELECT
		    t.id,
			t.name,
			t.owner_id,
			t.organisation_id,
			t.created_at,
			t.type,
			tt.name AS type_name,
			t.data,
			ft.flo_id
		FROM
			trigger t
		INNER JOIN trigger_type tt ON t.type = tt.id
		LEFT JOIN flo_trigger ft ON ft.trigger_id = t.id
		WHERE
		    t.owner_id = :owner_id
		ORDER BY
		    t.created_at DESC
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetTriggerByID, err = s.conn.PrepareNamed(`
		SELECT
		    t.id,
			t.name,
			t.owner_id,
			t.organisation_id,
			t.created_at,
			t.type,
			tt.name AS type_name,
			t.data,
			ft.flo_id
		FROM
			trigger t
		INNER JOIN trigger_type tt ON t.type = tt.id
		LEFT JOIN flo_trigger ft ON ft.trigger_id = t.id
		WHERE
		    t.id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCreateTrigger, err = s.conn.PrepareNamed(`
		INSERT INTO trigger (
			name,
			owner_id,
			organisation_id,
			type,
			data
		) VALUES (
			:name,
			:owner_id,
			:organisation_id,
			(SELECT id FROM trigger_type WHERE name = :type_name),
			:data
		) RETURNING id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtUpdateTrigger, err = s.conn.PrepareNamed(`
		UPDATE trigger
		SET
			name = :name,
			type = (SELECT id FROM trigger_type WHERE name = :type_name),
			data = :data
		WHERE
			id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtDeleteTrigger, err = s.conn.PrepareNamed(`
		DELETE FROM trigger
		WHERE
			id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtDeleteFloTrigger, err = s.conn.PrepareNamed(`
		DELETE FROM flo_trigger
		WHERE
			trigger_id = :trigger_id
	`)
	if err != nil {
		return nil, err
	}

	// RBAC: Groups
	s.stmtGetGroupsByOrgID, err = s.conn.PrepareNamed(`
		SELECT
			g.id, g.organisation_id, g.name, g.description, g.is_default, g.created_at,
			(SELECT COUNT(*) FROM organisation_group_member gm WHERE gm.group_id = g.id) AS member_count
		FROM organisation_group g
		WHERE g.organisation_id = :organisation_id
		ORDER BY g.name
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetGroupByID, err = s.conn.PrepareNamed(`
		SELECT
			g.id, g.organisation_id, g.name, g.description, g.is_default, g.created_at,
			(SELECT COUNT(*) FROM organisation_group_member gm WHERE gm.group_id = g.id) AS member_count
		FROM organisation_group g
		WHERE g.id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCreateGroup, err = s.conn.PrepareNamed(`
		INSERT INTO organisation_group (organisation_id, name, description, is_default)
		VALUES (:organisation_id, :name, :description, :is_default)
		RETURNING id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtUpdateGroup, err = s.conn.PrepareNamed(`
		UPDATE organisation_group
		SET name = :name, description = :description, is_default = :is_default
		WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtDeleteGroup, err = s.conn.PrepareNamed(`
		DELETE FROM organisation_group WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetGroupMembers, err = s.conn.PrepareNamed(`
		SELECT gm.user_id, u.name, gm.added_at
		FROM organisation_group_member gm
		INNER JOIN users u ON u.id = gm.user_id
		WHERE gm.group_id = :group_id
		ORDER BY u.name
	`)
	if err != nil {
		return nil, err
	}

	s.stmtAddUserToGroup, err = s.conn.PrepareNamed(`
		INSERT INTO organisation_group_member (group_id, user_id)
		VALUES (:group_id, :user_id)
		ON CONFLICT (group_id, user_id) DO NOTHING
	`)
	if err != nil {
		return nil, err
	}

	s.stmtRemoveUserFromGroup, err = s.conn.PrepareNamed(`
		DELETE FROM organisation_group_member
		WHERE group_id = :group_id AND user_id = :user_id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetGroupPermissions, err = s.conn.PrepareNamed(`
		SELECT permission FROM organisation_group_permission
		WHERE group_id = :group_id
		ORDER BY permission
	`)
	if err != nil {
		return nil, err
	}

	s.stmtDeleteGroupPermissions, err = s.conn.PrepareNamed(`
		DELETE FROM organisation_group_permission WHERE group_id = :group_id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtInsertGroupPermission, err = s.conn.PrepareNamed(`
		INSERT INTO organisation_group_permission (group_id, permission)
		VALUES (:group_id, :permission)
		ON CONFLICT (group_id, permission) DO NOTHING
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetUserPermissionsInOrg, err = s.conn.PrepareNamed(`
		SELECT DISTINCT gp.permission
		FROM organisation_group_permission gp
		INNER JOIN organisation_group_member gm ON gm.group_id = gp.group_id
		INNER JOIN organisation_group g ON g.id = gp.group_id
		WHERE g.organisation_id = :organisation_id AND gm.user_id = :user_id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetDefaultGroups, err = s.conn.PrepareNamed(`
		SELECT id FROM organisation_group
		WHERE organisation_id = :organisation_id AND is_default = true
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCountUserGroupsInOrg, err = s.conn.PrepareNamed(`
		SELECT COUNT(*)
		FROM organisation_group_member gm
		INNER JOIN organisation_group g ON g.id = gm.group_id
		WHERE g.organisation_id = :organisation_id AND gm.user_id = :user_id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCreateFeedback, err = s.conn.PrepareNamed(`
		INSERT INTO feedback (user_id, name, subject, category, message, url, user_agent)
		VALUES (:user_id, :name, :subject, :category, :message, :url, :user_agent)
	`)
	if err != nil {
		return nil, err
	}

	// Flo favourites
	s.stmtGetFloFavourites, err = s.conn.PrepareNamed(`
		SELECT flo_id FROM flo_favourite WHERE user_id = :user_id ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}

	s.stmtAddFloFavourite, err = s.conn.PrepareNamed(`
		INSERT INTO flo_favourite (user_id, flo_id)
		VALUES (:user_id, :flo_id)
		ON CONFLICT (user_id, flo_id) DO NOTHING
	`)
	if err != nil {
		return nil, err
	}

	s.stmtRemoveFloFavourite, err = s.conn.PrepareNamed(`
		DELETE FROM flo_favourite WHERE user_id = :user_id AND flo_id = :flo_id
	`)
	if err != nil {
		return nil, err
	}

	// --- Agent statements ---

	s.stmtGetAgents, err = s.conn.PrepareNamed(`
		SELECT a.*, NULL AS ai_api_key, COALESCE(mc.cnt, 0) AS message_count, COALESCE(ec.cnt, 0) AS execution_count
		FROM agent a
		LEFT JOIN LATERAL (SELECT COUNT(1) AS cnt FROM agent_message WHERE agent_id = a.id) mc ON true
		LEFT JOIN LATERAL (SELECT COUNT(1) AS cnt FROM agent_execution WHERE agent_id = a.id) ec ON true
		WHERE a.owner_id = :owner_id AND a.archived_at IS NULL
		ORDER BY a.updated_at DESC
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetAgentsByOrgID, err = s.conn.PrepareNamed(`
		SELECT a.*, NULL AS ai_api_key, COALESCE(mc.cnt, 0) AS message_count, COALESCE(ec.cnt, 0) AS execution_count
		FROM agent a
		LEFT JOIN LATERAL (SELECT COUNT(1) AS cnt FROM agent_message WHERE agent_id = a.id) mc ON true
		LEFT JOIN LATERAL (SELECT COUNT(1) AS cnt FROM agent_execution WHERE agent_id = a.id) ec ON true
		WHERE a.organisation_id = :organisation_id AND a.archived_at IS NULL
		ORDER BY a.updated_at DESC
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetAgentByID, err = s.conn.PrepareNamed(`
		SELECT a.*,
			CASE WHEN a.ai_api_key IS NOT NULL THEN PGP_SYM_DECRYPT(a.ai_api_key, :encrypt_key) ELSE NULL END AS ai_api_key,
			COALESCE(mc.cnt, 0) AS message_count,
			COALESCE(ec.cnt, 0) AS execution_count,
			f.name AS orchestrator_flow_name,
			e.name AS environment_name
		FROM agent a
		LEFT JOIN LATERAL (SELECT COUNT(1) AS cnt FROM agent_message WHERE agent_id = a.id) mc ON true
		LEFT JOIN LATERAL (SELECT COUNT(1) AS cnt FROM agent_execution WHERE agent_id = a.id) ec ON true
		LEFT JOIN flo f ON f.id = a.orchestrator_flow_id
		LEFT JOIN environment e ON e.id = a.environment_id
		WHERE a.id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCreateAgent, err = s.conn.PrepareNamed(`
		INSERT INTO agent (name, description, owner_id, organisation_id, environment_id, queue_id,
			system_prompt, orchestrator_flow_id, extraction_flow_id, ai_api_key,
			max_concurrent_executions, idle_timeout_seconds,
			channels, requires_approval, max_executions_per_hour)
		VALUES (:name, :description, :owner_id, :organisation_id, :environment_id, :queue_id,
			:system_prompt, :orchestrator_flow_id, :extraction_flow_id,
			PGP_SYM_ENCRYPT(:ai_api_key, :encrypt_key),
			:max_concurrent_executions, :idle_timeout_seconds,
			:channels, :requires_approval, :max_executions_per_hour)
		RETURNING id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtUpdateAgent, err = s.conn.PrepareNamed(`
		UPDATE agent SET
			name = :name, description = :description, environment_id = :environment_id,
			queue_id = :queue_id, system_prompt = :system_prompt,
			orchestrator_flow_id = :orchestrator_flow_id,
			ai_api_key = PGP_SYM_ENCRYPT(:ai_api_key, :encrypt_key),
			max_concurrent_executions = :max_concurrent_executions,
			idle_timeout_seconds = :idle_timeout_seconds, channels = :channels,
			requires_approval = :requires_approval,
			max_executions_per_hour = :max_executions_per_hour,
			updated_at = NOW()
		WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtArchiveAgent, err = s.conn.PrepareNamed(`
		UPDATE agent SET archived_at = NOW(), status = 'stopped', updated_at = NOW()
		WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtUpdateAgentStatus, err = s.conn.PrepareNamed(`
		UPDATE agent SET status = :status, started_at = :started_at, stopped_at = :stopped_at, updated_at = NOW()
		WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCreateAgentSession, err = s.conn.PrepareNamed(`
		INSERT INTO agent_session (agent_id) VALUES (:agent_id) RETURNING id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtEndAgentSession, err = s.conn.PrepareNamed(`
		UPDATE agent_session SET status = :status, ended_at = NOW(), error_message = :error_message
		WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtUpdateAgentSessionHeartbeat, err = s.conn.PrepareNamed(`
		UPDATE agent_session SET heartbeat_at = NOW(), summary = :summary WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetAgentSessions, err = s.conn.PrepareNamed(`
		SELECT s.*,
			COALESCE(mc.cnt, 0) AS message_count,
			COALESCE(ec.cnt, 0) AS execution_count
		FROM agent_session s
		LEFT JOIN LATERAL (SELECT COUNT(1) AS cnt FROM agent_message WHERE session_id = s.id) mc ON true
		LEFT JOIN LATERAL (SELECT COUNT(1) AS cnt FROM execution WHERE agent_session_id = s.id) ec ON true
		WHERE s.agent_id = :agent_id
		ORDER BY s.started_at DESC
		LIMIT :limit OFFSET :offset
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetAgentSessionByID, err = s.conn.PrepareNamed(`
		SELECT s.*,
			COALESCE(mc.cnt, 0) AS message_count,
			COALESCE(ec.cnt, 0) AS execution_count
		FROM agent_session s
		LEFT JOIN LATERAL (SELECT COUNT(1) AS cnt FROM agent_message WHERE session_id = s.id) mc ON true
		LEFT JOIN LATERAL (SELECT COUNT(1) AS cnt FROM execution WHERE agent_session_id = s.id) ec ON true
		WHERE s.id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetActiveAgentSession, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_session WHERE agent_id = :agent_id AND status = 'active' ORDER BY started_at DESC LIMIT 1
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetAgentState, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_state WHERE agent_id = :agent_id ORDER BY state_key
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetAgentStateKey, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_state WHERE agent_id = :agent_id AND state_key = :state_key
	`)
	if err != nil {
		return nil, err
	}

	s.stmtUpsertAgentState, err = s.conn.PrepareNamed(`
		INSERT INTO agent_state (agent_id, state_key, state_value)
		VALUES (:agent_id, :state_key, :state_value)
		ON CONFLICT (agent_id, state_key) DO UPDATE SET state_value = :state_value, updated_at = NOW()
	`)
	if err != nil {
		return nil, err
	}

	s.stmtDeleteAgentStateKey, err = s.conn.PrepareNamed(`
		DELETE FROM agent_state WHERE agent_id = :agent_id AND state_key = :state_key
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetAgentMessages, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_message WHERE agent_id = :agent_id ORDER BY created_at DESC LIMIT :limit OFFSET :offset
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetAgentSessionMessages, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_message WHERE session_id = :session_id ORDER BY created_at ASC LIMIT :limit OFFSET :offset
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCreateAgentMessage, err = s.conn.PrepareNamed(`
		INSERT INTO agent_message (agent_id, session_id, direction, channel_type, sender, content, metadata, execution_id)
		VALUES (:agent_id, :session_id, :direction, :channel_type, :sender, :content, :metadata, :execution_id)
		RETURNING id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetAgentExecutions, err = s.conn.PrepareNamed(`
		SELECT ae.*, f.name AS flow_name
		FROM agent_execution ae
		LEFT JOIN flo f ON f.id = ae.flow_id
		WHERE ae.agent_id = :agent_id
		ORDER BY ae.created_at DESC
		LIMIT :limit OFFSET :offset
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCreateAgentExecution, err = s.conn.PrepareNamed(`
		INSERT INTO agent_execution (agent_id, session_id, message_id, execution_id, flow_id, status, requires_approval)
		VALUES (:agent_id, :session_id, :message_id, :execution_id, :flow_id, :status, :requires_approval)
		RETURNING id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtUpdateAgentExecutionStatus, err = s.conn.PrepareNamed(`
		UPDATE agent_execution SET status = :status, approved_by = :approved_by, approved_at = :approved_at, completed_at = :completed_at
		WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCountAgentExecutionsInHour, err = s.conn.PrepareNamed(`
		SELECT COUNT(1) FROM agent_execution WHERE agent_id = :agent_id AND created_at > NOW() - INTERVAL '1 hour'
	`)
	if err != nil {
		return nil, err
	}

	// Agent Memory Phase 1 statements. See internal/persistence/agent_memory.go.

	s.stmtGetAgentUserByID, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_user WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCreateAgentUser, err = s.conn.PrepareNamed(`
		INSERT INTO agent_user (agent_id, organisation_id, display_name)
		VALUES (:agent_id, :organisation_id, :display_name)
		RETURNING id
	`)
	if err != nil {
		return nil, err
	}

	// Lookup by natural key (channel_type, channel_external_id, channel_scope).
	// COALESCE mirrors the unique index definition in migration 41 so a NULL
	// scope and an empty-string scope collapse to the same identity row.
	s.stmtGetAgentIdentityByExternal, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_identity
		WHERE channel_type = :channel_type
		  AND channel_external_id = :channel_external_id
		  AND COALESCE(channel_scope, '') = COALESCE(:channel_scope, '')
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCreateAgentIdentity, err = s.conn.PrepareNamed(`
		INSERT INTO agent_identity (agent_user_id, channel_type, channel_external_id, channel_scope, verified)
		VALUES (:agent_user_id, :channel_type, :channel_external_id, :channel_scope, :verified)
		RETURNING id
	`)
	if err != nil {
		return nil, err
	}

	// Re-point an existing identity at a different agent_user, used by the
	// natural-language identity linking flow that lands in Phase 5. Included
	// in Phase 1 so the function exists at the CRUD surface from day one.
	s.stmtLinkAgentIdentityToUser, err = s.conn.PrepareNamed(`
		UPDATE agent_identity
		SET agent_user_id = :agent_user_id,
		    verified = TRUE,
		    linked_at = NOW()
		WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	// Resolve an open conversation by its natural key. An open conversation
	// has ended_at IS NULL; the partial unique index in migration 41 enforces
	// at-most-one open conversation per (agent, channel_type, channel_id,
	// thread_id). A closed conversation with the same key is ignored — a
	// fresh conversation row will be created on the next turn.
	s.stmtGetAgentConversationByID, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_conversation WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetAgentConversationByKey, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_conversation
		WHERE agent_id = :agent_id
		  AND channel_type = :channel_type
		  AND channel_id = :channel_id
		  AND COALESCE(thread_id, '') = COALESCE(:thread_id, '')
		  AND ended_at IS NULL
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCreateAgentConversation, err = s.conn.PrepareNamed(`
		INSERT INTO agent_conversation (agent_id, agent_user_id, channel_type, channel_id, thread_id)
		VALUES (:agent_id, :agent_user_id, :channel_type, :channel_id, :thread_id)
		RETURNING id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtTouchAgentConversation, err = s.conn.PrepareNamed(`
		UPDATE agent_conversation SET last_message_at = NOW() WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	// Conversation-scoped message history: fetch the MOST RECENT N messages
	// but return them in chronological order (oldest-first) so the AI sees
	// turns in the correct sequence. The subquery grabs the latest N by
	// descending order, then the outer query re-sorts ascending.
	// Without the subquery, LIMIT with ASC order returns the OLDEST N
	// messages, causing the AI to lose context in long conversations.
	s.stmtGetAgentConversationMessages, err = s.conn.PrepareNamed(`
		SELECT * FROM (
			SELECT * FROM agent_message
			WHERE conversation_id = :conversation_id
			  AND direction IN ('inbound', 'outbound', 'system')
			ORDER BY sequence DESC, created_at DESC
			LIMIT :limit
		) recent
		ORDER BY sequence ASC, created_at ASC
	`)
	if err != nil {
		return nil, err
	}

	// Insert a message with explicit conversation scoping and sequence.
	// Callers compute the sequence via stmtNextAgentConversationSequence
	// before inserting. Doing this in two statements (rather than a single
	// INSERT ... SELECT MAX+1) is fine here because writes for the same
	// conversation are serialised by the per-user extraction lease in
	// Phase 2; Phase 1 inserts are infrequent enough that the small race
	// window (concurrent inserts before the lease exists) is acceptable
	// and will be resolved by the unique index retry in a later chunk.
	s.stmtCreateAgentMessageInConversation, err = s.conn.PrepareNamed(`
		INSERT INTO agent_message (
			agent_id, session_id, conversation_id, sequence,
			direction, channel_type, sender, content, metadata, execution_id
		) VALUES (
			:agent_id, :session_id, :conversation_id, :sequence,
			:direction, :channel_type, :sender, :content, :metadata, :execution_id
		)
		RETURNING id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtNextAgentConversationSequence, err = s.conn.PrepareNamed(`
		SELECT COALESCE(MAX(sequence), 0) + 1 AS next_sequence
		FROM agent_message
		WHERE conversation_id = :conversation_id
	`)
	if err != nil {
		return nil, err
	}

	// Agent Memory Phase 2 statements. See internal/persistence/agent_memory_phase2.go.

	s.stmtCreateAgentMemory, err = s.conn.PrepareNamed(`
		INSERT INTO agent_memory (
			agent_id, agent_user_id, scope, memory_type, title, body,
			source_conversation, source_message, confidence, pinned, expires_at, embedding, valid_until
		) VALUES (
			:agent_id, :agent_user_id, :scope, :memory_type, :title, :body,
			:source_conversation, :source_message, :confidence, :pinned, :expires_at, :embedding, :valid_until
		)
		RETURNING id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetAgentMemoryByID, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_memory WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	// Ordering puts pinned rows first so callers that take the head of
	// the result get the always-include set before any recency-sorted
	// fill. Type-filtered retrieval is deliberately NOT in Phase 2a —
	// it will be rewritten as part of Phase 4's pgvector top-K query,
	// and no Phase 2b/2c caller needs it (the system-prompt assembler
	// only requests pinned memories).
	s.stmtGetAgentMemoriesForUser, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_memory
		WHERE agent_user_id = :agent_user_id
		  AND status = 'active'
		  AND (NOT :pinned_only OR pinned = TRUE)
		  AND (expires_at IS NULL OR expires_at > NOW())
		ORDER BY pinned DESC, created_at DESC
		LIMIT :limit
	`)
	if err != nil {
		return nil, err
	}

	s.stmtDeleteAgentMemory, err = s.conn.PrepareNamed(`
		DELETE FROM agent_memory WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtTouchAgentMemoryLastUsed, err = s.conn.PrepareNamed(`
		UPDATE agent_memory SET last_used_at = NOW() WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCreateAgentPendingAction, err = s.conn.PrepareNamed(`
		INSERT INTO agent_pending_action (
			agent_id, agent_user_id, type, payload, evidence, status,
			source_conversation, source_message, expires_at
		) VALUES (
			:agent_id, :agent_user_id, :type, :payload, :evidence, :status,
			:source_conversation, :source_message, :expires_at
		)
		RETURNING id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetAgentPendingActionByID, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_pending_action WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	// "Open" = still awaiting something. Terminal states (executed, declined,
	// expired) are excluded. The partial index idx_agent_pending_action_user_open
	// covers exactly this filter so the query is constant-time per user.
	s.stmtGetOpenPendingActionsForUser, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_pending_action
		WHERE agent_user_id = :agent_user_id
		  AND status IN ('awaiting_confirmation', 'confirmed_here_awaiting_other_side')
		  AND created_at > NOW() - INTERVAL '24 hours'
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}

	// Terminal transitions stamp resolved_at; non-terminal transitions leave
	// it alone. The CASE is inline so the caller doesn't have to know which
	// states are terminal — the schema owns that truth.
	s.stmtUpdatePendingActionStatus, err = s.conn.PrepareNamed(`
		UPDATE agent_pending_action
		SET status = :status,
		    resolved_at = CASE
		        WHEN :status IN ('executed', 'declined', 'expired')
		            THEN NOW()
		        ELSE resolved_at
		    END
		WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCreateAgentCommitment, err = s.conn.PrepareNamed(`
		INSERT INTO agent_commitment (
			agent_id, agent_user_id, conversation_id, kind, description,
			payload, trigger_type, due_at, condition, status,
			source_conversation, source_message, made_by, expires_at
		) VALUES (
			:agent_id, :agent_user_id, :conversation_id, :kind, :description,
			:payload, :trigger_type, :due_at, :condition, :status,
			:source_conversation, :source_message, :made_by, :expires_at
		)
		RETURNING id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetAgentCommitmentByID, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_commitment WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	// Phase 3 commitment poller hot path. The partial index
	// idx_agent_commitment_due_pending covers this exactly.
	s.stmtGetDueCommitments, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_commitment
		WHERE status = 'pending' AND due_at IS NOT NULL AND due_at <= NOW()
		ORDER BY due_at ASC
		LIMIT :limit
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetCommitmentsForUser, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_commitment
		WHERE agent_user_id = :agent_user_id
		ORDER BY created_at DESC
		LIMIT :limit
	`)
	if err != nil {
		return nil, err
	}

	// Lifecycle transitions stamp the corresponding timestamp column.
	// Keeping this in the schema (not the caller) means every code path
	// that moves a commitment through its lifecycle gets the same audit
	// trail for free.
	s.stmtUpdateCommitmentStatus, err = s.conn.PrepareNamed(`
		UPDATE agent_commitment
		SET status = :status,
		    fired_at     = CASE WHEN :status = 'firing'    AND fired_at     IS NULL THEN NOW() ELSE fired_at     END,
		    fulfilled_at = CASE WHEN :status = 'fulfilled' AND fulfilled_at IS NULL THEN NOW() ELSE fulfilled_at END,
		    cancelled_at = CASE WHEN :status = 'cancelled' AND cancelled_at IS NULL THEN NOW() ELSE cancelled_at END
		WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	// Agent Memory Phase 4 statements. See internal/persistence/agent_memory_phase4.go.

	s.stmtSearchMemoriesByEmbedding, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_memory
		WHERE agent_id = :agent_id
		  AND agent_user_id = :agent_user_id
		  AND status = 'active'
		  AND embedding IS NOT NULL
		  AND (NOT :exclude_pinned OR pinned = FALSE)
		  AND (expires_at IS NULL OR expires_at > NOW())
		ORDER BY embedding <=> :query_embedding
		LIMIT :top_k
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetMemoriesWithoutEmbedding, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_memory
		WHERE embedding IS NULL
		  AND status = 'active'
		ORDER BY created_at DESC
		LIMIT :limit
	`)
	if err != nil {
		return nil, err
	}

	s.stmtUpdateMemoryEmbedding, err = s.conn.PrepareNamed(`
		UPDATE agent_memory SET embedding = :embedding WHERE id = :id
	`)
	if err != nil {
		return nil, err
	}

	// Agent Memory Phase 5 statements. See internal/persistence/agent_memory_phase5.go.

	s.stmtGetAgentIdentitiesByUserID, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_identity WHERE agent_user_id = :agent_user_id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetPendingActionByUserAndType, err = s.conn.PrepareNamed(`
		SELECT * FROM agent_pending_action
		WHERE agent_user_id = :agent_user_id
		  AND type = :type
		  AND status IN ('awaiting_confirmation', 'confirmed_here_awaiting_other_side')
		ORDER BY created_at DESC
		LIMIT 1
	`)
	if err != nil {
		return nil, err
	}

	return &s, nil
}

func (s *Service) connectionMonitor() {
	for {
		if err := s.conn.Ping(); err != nil {
			log.WithFields(log.Fields{
				"error": err,
			}).Error("error during database connection check - resetting connection")

		}

		time.Sleep(time.Second * 30)
	}
}

func (s *Service) GetMyOrganisations(userID string) ([]*api.Organisation, error) {
	var results []*api.Organisation

	if err := s.stmtGetOrganisations.Select(&results, struct {
		UserID string `db:"user_id"`
	}{
		UserID: userID,
	}); err != nil {
		return nil, err
	}

	return results, nil
}

func (s *Service) GetOrganisationByID(ID string) (*api.Organisation, error) {
	var result api.Organisation

	if err := s.stmtGetOrganisationByID.Get(&result, struct {
		ID string `db:"id"`
	}{
		ID: ID,
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}

		return nil, err
	}

	return &result, nil
}

func (s *Service) CreateOrganisation(organisation api.Organisation) (*string, error) {
	var ID string
	if err := s.stmtCreateOrganisation.Get(&ID, organisation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}

		return nil, err
	}

	return &ID, nil
}

func (s *Service) UpdateOrganisation(organisation api.Organisation) error {
	if _, err := s.stmtUpdateOrganisation.Exec(organisation); err != nil {
		return err
	}

	return nil
}

func (s *Service) AddUserToOrganisation(organisationID string, userID string, role ...string) error {
	r := "member"
	if len(role) > 0 {
		r = role[0]
	}

	if _, err := s.stmtAddUserToOrganisation.Exec(struct {
		OrganisationID string `db:"organisation_id"`
		UserID         string `db:"user_id"`
		Role           string `db:"role"`
	}{
		OrganisationID: organisationID,
		UserID:         userID,
		Role:           r,
	}); err != nil {
		return err
	}

	return nil
}

func (s *Service) GetOrganisationMembers(organisationID string) ([]*api.OrganisationMember, error) {
	var results []*api.OrganisationMember
	if err := s.stmtGetOrganisationMembers.Select(&results, struct {
		OrganisationID string `db:"organisation_id"`
		EncryptKey     string `db:"encrypt_key"`
	}{
		OrganisationID: organisationID,
		EncryptKey:     s.config.Database.EncryptionKey,
	}); err != nil {
		return nil, err
	}
	return results, nil
}

func (s *Service) RemoveUserFromOrganisation(organisationID string, userID string) error {
	_, err := s.stmtRemoveUserFromOrganisation.Exec(struct {
		OrganisationID string `db:"organisation_id"`
		UserID         string `db:"user_id"`
	}{
		OrganisationID: organisationID,
		UserID:         userID,
	})
	return err
}

func (s *Service) GetUserRoleInOrganisation(organisationID string, userID string) (*string, error) {
	var role string
	if err := s.stmtGetUserRoleInOrganisation.Get(&role, struct {
		OrganisationID string `db:"organisation_id"`
		UserID         string `db:"user_id"`
	}{
		OrganisationID: organisationID,
		UserID:         userID,
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &role, nil
}

func (s *Service) CreateOrganisationInvite(organisationID string, email *string, role string, createdBy string) (*api.OrganisationInvite, error) {
	var invite api.OrganisationInvite
	if err := s.stmtCreateOrganisationInvite.Get(&invite, struct {
		OrganisationID string  `db:"organisation_id"`
		Email          *string `db:"email"`
		Role           string  `db:"role"`
		CreatedBy      string  `db:"created_by"`
	}{
		OrganisationID: organisationID,
		Email:          email,
		Role:           role,
		CreatedBy:      createdBy,
	}); err != nil {
		return nil, err
	}
	invite.OrganisationID = organisationID
	invite.Email = email
	invite.Role = role
	invite.CreatedBy = createdBy
	return &invite, nil
}

func (s *Service) GetOrganisationInvites(organisationID string) ([]*api.OrganisationInvite, error) {
	var results []*api.OrganisationInvite
	if err := s.stmtGetOrganisationInvites.Select(&results, struct {
		OrganisationID string `db:"organisation_id"`
	}{
		OrganisationID: organisationID,
	}); err != nil {
		return nil, err
	}
	return results, nil
}

func (s *Service) GetInviteByCode(code string) (*api.OrganisationInvite, error) {
	var invite api.OrganisationInvite
	if err := s.stmtGetInviteByCode.Get(&invite, struct {
		InviteCode string `db:"invite_code"`
	}{
		InviteCode: code,
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &invite, nil
}

type InvitePreview struct {
	OrganisationName string `db:"organisation_name" json:"organisation_name"`
	Role             string `db:"role" json:"role"`
}

func (s *Service) GetInvitePreview(code string) (*InvitePreview, error) {
	var preview InvitePreview
	if err := s.stmtGetInvitePreview.Get(&preview, struct {
		InviteCode string `db:"invite_code"`
	}{
		InviteCode: code,
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &preview, nil
}

func (s *Service) AcceptInvite(inviteID string, acceptedBy string) error {
	result, err := s.stmtAcceptInvite.Exec(struct {
		ID         string `db:"id"`
		AcceptedBy string `db:"accepted_by"`
	}{
		ID:         inviteID,
		AcceptedBy: acceptedBy,
	})
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrInviteAlreadyAccepted
	}
	return nil
}

func (s *Service) RevokeInvite(inviteID string, organisationID string) error {
	_, err := s.stmtRevokeInvite.Exec(struct {
		ID             string `db:"id"`
		OrganisationID string `db:"organisation_id"`
	}{
		ID:             inviteID,
		OrganisationID: organisationID,
	})
	return err
}

func (s *Service) GetUserByID(ID string) (*api.User, error) {
	var result api.User

	if err := s.stmtGetUserByID.Get(&result, struct {
		ID            string `db:"id"`
		EncryptionKey string `db:"encrypt_key"`
	}{
		ID:            ID,
		EncryptionKey: s.config.Database.EncryptionKey,
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}

		return nil, err
	}

	return &result, nil
}

func (s *Service) CreateUser(user *api.User) (*string, error) {
	var id string

	if err := s.stmtCreateUser.Get(&id, struct {
		*api.User
		EncryptionKey string `db:"encrypt_key"`
	}{
		user,
		s.config.Database.EncryptionKey,
	}); err != nil {
		return nil, err
	}

	return &id, nil
}

func (s *Service) UpdateUser(user *api.User) error {
	if _, err := s.stmtUpdateUser.Exec(struct {
		*api.User
		EncryptionKey string `db:"encrypt_key"`
	}{
		user,
		s.config.Database.EncryptionKey,
	}); err != nil {
		return err
	}

	return nil
}

func (s *Service) AcceptEula(userID string, version int) error {
	if _, err := s.stmtAcceptEula.Exec(struct {
		ID          string `db:"id"`
		EulaVersion int    `db:"eula_version"`
	}{
		userID,
		version,
	}); err != nil {
		return err
	}
	return nil
}

func (s *Service) GetLatestEula() (*api.Eula, error) {
	var result api.Eula
	// Named query requires at least one param; use a dummy struct.
	if err := s.stmtGetLatestEula.Get(&result, struct{}{}); err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Service) UpdateOnboardingProgress(userID string, step int, completedAt *time.Time) error {
	if _, err := s.stmtUpdateOnboarding.Exec(struct {
		ID                    string     `db:"id"`
		OnboardingStep        int        `db:"onboarding_step"`
		OnboardingCompletedAt *time.Time `db:"onboarding_completed_at"`
	}{
		userID,
		step,
		completedAt,
	}); err != nil {
		return err
	}
	return nil
}

// SetChecklistFlag sets a bitmask flag on the user's checklist_flags using bitwise OR.
func (s *Service) SetChecklistFlag(userID string, flag int) error {
	_, err := s.conn.Exec(
		"UPDATE users SET checklist_flags = checklist_flags | $1 WHERE id = $2",
		flag, userID,
	)
	return err
}

// ClearChecklistFlag removes a bitmask flag from the user's checklist_flags using bitwise AND NOT.
func (s *Service) ClearChecklistFlag(userID string, flag int) error {
	_, err := s.conn.Exec(
		"UPDATE users SET checklist_flags = checklist_flags & ~$1 WHERE id = $2",
		flag, userID,
	)
	return err
}

func (s *Service) GetMyFlos(userID string, offset int64, limit int64, search string, organisationID ...string) ([]*api.Flo, int64, error) {
	var results []*api.Flo
	var count int64

	// Organisation-scoped queries
	if len(organisationID) > 0 && organisationID[0] != "" {
		orgID := organisationID[0]

		if search == "" {
			if err := s.stmtGetOrgFlos.Select(&results, struct {
				OrganisationID string `db:"organisation_id"`
				Offset         int64  `db:"offset"`
				Limit          int64  `db:"limit"`
			}{
				OrganisationID: orgID,
				Offset:         offset,
				Limit:          limit,
			}); err != nil {
				return nil, 0, err
			}

			if err := s.stmtCountOrgFlos.Get(&count, struct {
				OrganisationID string `db:"organisation_id"`
			}{
				OrganisationID: orgID,
			}); err != nil {
				return nil, 0, err
			}
		} else {
			if err := s.stmtGetOrgFlosWithFilter.Select(&results, struct {
				OrganisationID string `db:"organisation_id"`
				Offset         int64  `db:"offset"`
				Limit          int64  `db:"limit"`
				Search         string `db:"search"`
			}{
				OrganisationID: orgID,
				Offset:         offset,
				Limit:          limit,
				Search:         "%" + search + "%",
			}); err != nil {
				return nil, 0, err
			}

			if err := s.stmtCountOrgFlosWithFilter.Get(&count, struct {
				OrganisationID string `db:"organisation_id"`
				Search         string `db:"search"`
			}{
				OrganisationID: orgID,
				Search:         "%" + search + "%",
			}); err != nil {
				return nil, 0, err
			}
		}
	} else {
		// Personal flows — filter by author_id
		if search == "" {
			if err := s.stmtGetMyFlos.Select(&results, struct {
				AuthorID string `db:"author_id"`
				Offset   int64  `db:"offset"`
				Limit    int64  `db:"limit"`
			}{
				AuthorID: userID,
				Offset:   offset,
				Limit:    limit,
			}); err != nil {
				return nil, 0, err
			}

			if err := s.stmtCountMyFlos.Get(&count, struct {
				AuthorID string `db:"author_id"`
				Offset   int64  `db:"offset"`
				Limit    int64  `db:"limit"`
			}{
				AuthorID: userID,
				Offset:   offset,
				Limit:    limit,
			}); err != nil {
				return nil, 0, err
			}
		} else {
			if err := s.stmtGetMyFlosWithFilter.Select(&results, struct {
				AuthorID string `db:"author_id"`
				Offset   int64  `db:"offset"`
				Limit    int64  `db:"limit"`
				Search   string `db:"search"`
			}{
				AuthorID: userID,
				Offset:   offset,
				Limit:    limit,
				Search:   "%" + search + "%",
			}); err != nil {
				return nil, 0, err
			}

			if err := s.stmtCountMyFlosWithFilter.Get(&count, struct {
				AuthorID string `db:"author_id"`
				Offset   int64  `db:"offset"`
				Limit    int64  `db:"limit"`
				Search   string `db:"search"`
			}{
				AuthorID: userID,
				Offset:   offset,
				Limit:    limit,
				Search:   "%" + search + "%",
			}); err != nil {
				return nil, 0, err
			}
		}
	}

	for idx, r := range results {
		var triggers []*api.Trigger
		if err := s.stmtGetFloTriggers.Select(&triggers, struct {
			FloID string `db:"id"`
		}{
			FloID: r.ID,
		}); err != nil {
			log.WithFields(log.Fields{
				"error": err,
			}).Error("unable to get flo triggers")
		}

		results[idx].Triggers = triggers

		var execution api.Execution
		if err := s.stmtGetLatestExecutionForFlo.Get(&execution, struct {
			FloID string `db:"flo_id"`
		}{
			FloID: r.ID,
		}); err != nil {
			if err != sql.ErrNoRows {
				log.WithFields(log.Fields{
					"error": err,
				}).Error("unable to get flo execution")
			}
		}

		if execution.FloID == r.ID {
			results[idx].LastExecution = &execution
		}

		var recentExecs []api.ExecutionStatus
		if err := s.stmtGetRecentExecutionsForFlo.Select(&recentExecs, struct {
			FloID string `db:"flo_id"`
		}{
			FloID: r.ID,
		}); err != nil {
			if err != sql.ErrNoRows {
				log.WithFields(log.Fields{
					"error": err,
				}).Error("unable to get recent executions")
			}
		}
		if len(recentExecs) > 0 {
			results[idx].RecentExecutions = recentExecs
		}
	}

	return results, count, nil
}

func (s *Service) GetFloByID(floID string) (*api.Flo, error) {
	var result api.Flo

	if err := s.stmtGetFloByID.Get(&result, struct {
		FloID string `db:"id"`
	}{
		FloID: floID,
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}

		return nil, err
	}

	var triggers []*api.Trigger
	if err := s.stmtGetFloTriggers.Select(&triggers, struct {
		FloID string `db:"id"`
	}{
		FloID: floID,
	}); err != nil {
		return nil, err
	}

	result.Triggers = triggers

	return &result, nil
}

func (s *Service) CreateFlo(flo api.Flo) (*string, error) {
	var ID string
	if err := s.stmtCreateFlo.Get(&ID, flo); err != nil {
		return nil, err
	}

	var triggerID string
	if err := s.stmtInsertDefaultTrigger.Get(&triggerID, struct {
		Name           string  `db:"name"`
		OwnerID        *string `db:"owner_id"`
		OrganisationID *string `db:"organisation_id"`
	}{
		Name:           "Default Trigger",
		OwnerID:        flo.AuthorID,
		OrganisationID: flo.OrganisationID,
	}); err != nil {
		return nil, err
	}

	if triggerID != "" {
		if _, err := s.stmtLinkFloToTrigger.Exec(struct {
			FloID     string `db:"flo_id"`
			TriggerID string `db:"trigger_id"`
		}{
			FloID:     ID,
			TriggerID: triggerID,
		}); err != nil {
			return nil, err
		}
	}

	return &ID, nil
}

func (s *Service) UpdateFlo(flo api.Flo) error {
	if _, err := s.stmtUpdateFlo.Exec(flo); err != nil {
		return err
	}

	return nil
}

func (s *Service) DeleteFlo(flo api.Flo) error {
	if _, err := s.stmtDeleteFlo.Exec(flo); err != nil {
		return err
	}

	return nil
}

func (s *Service) CreateFloRevision(revision api.Revision) (*string, error) {
	var ID string
	if err := s.stmtCreateFloRevision.Get(&ID, revision); err != nil {
		return nil, err
	}

	return &ID, nil
}

func (s *Service) GetLatestRevisionByFloID(ID string) (*api.Revision, error) {
	var result api.Revision

	if err := s.stmtGetLatestFloRevisionByFloID.Get(&result, struct {
		ID string `db:"flo_id"`
	}{
		ID: ID,
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return &result, nil
}

func (s *Service) GetRevisionsByFloID(ID string) ([]*api.Revision, error) {
	var results []*api.Revision

	if err := s.stmtGetFloRevisions.Select(&results, struct {
		ID string `db:"flo_id"`
	}{
		ID: ID,
	}); err != nil {
		return nil, err
	}

	return results, nil
}

func (s *Service) GetRevisionByID(ID string) (*api.Revision, error) {
	var result api.Revision

	if err := s.stmtGetFloRevisionByID.Get(&result, struct {
		ID string `db:"id"`
	}{
		ID: ID,
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}

		return nil, err
	}

	return &result, nil
}

func (s *Service) GetExecutions(offset int64, limit int64, search string, userID string, organisationID *string, rootOnly ...bool) ([]*api.Execution, int64, error) {
	var results []*api.Execution
	var count int64

	filterRoot := len(rootOnly) > 0 && rootOnly[0]
	isOrg := organisationID != nil && *organisationID != ""

	// When filtering root-only, use dynamic SQL to add the parent_execution_id IS NULL condition.
	// This ensures correct pagination and counts.
	if filterRoot {
		return s.getExecutionsRootOnly(offset, limit, search, userID, organisationID, isOrg)
	}

	if isOrg {
		orgID := *organisationID
		if search != "" {
			if err := s.stmtGetOrgExecutionsWithFilter.Select(&results, struct {
				Offset         int64  `db:"offset"`
				Limited        int64  `db:"limit"`
				Search         string `db:"search"`
				OrganisationID string `db:"organisation_id"`
			}{Offset: offset, Limited: limit, Search: "%" + search + "%", OrganisationID: orgID}); err != nil {
				return nil, 0, err
			}
			if err := s.stmtCountOrgExecutionsWithFilter.Get(&count, struct {
				Search         string `db:"search"`
				OrganisationID string `db:"organisation_id"`
			}{Search: "%" + search + "%", OrganisationID: orgID}); err != nil {
				return nil, 0, err
			}
		} else {
			if err := s.stmtGetOrgExecutions.Select(&results, struct {
				Offset         int64  `db:"offset"`
				Limited        int64  `db:"limit"`
				OrganisationID string `db:"organisation_id"`
			}{Offset: offset, Limited: limit, OrganisationID: orgID}); err != nil {
				return nil, 0, err
			}
			if err := s.stmtCountOrgExecutions.Get(&count, struct {
				OrganisationID string `db:"organisation_id"`
			}{OrganisationID: orgID}); err != nil {
				return nil, 0, err
			}
		}
	} else {
		if search != "" {
			if err := s.stmtGetExecutionsWithFilter.Select(&results, struct {
				Offset  int64  `db:"offset"`
				Limited int64  `db:"limit"`
				Search  string `db:"search"`
				UserID  string `db:"user_id"`
			}{Offset: offset, Limited: limit, Search: "%" + search + "%", UserID: userID}); err != nil {
				return nil, 0, err
			}
			if err := s.stmtCountExecutionsWithFilter.Get(&count, struct {
				Search string `db:"search"`
				UserID string `db:"user_id"`
			}{Search: "%" + search + "%", UserID: userID}); err != nil {
				return nil, 0, err
			}
		} else {
			if err := s.stmtGetExecutions.Select(&results, struct {
				Offset  int64  `db:"offset"`
				Limited int64  `db:"limit"`
				UserID  string `db:"user_id"`
			}{Offset: offset, Limited: limit, UserID: userID}); err != nil {
				return nil, 0, err
			}
			if err := s.stmtCountExecutions.Get(&count, struct {
				UserID string `db:"user_id"`
			}{UserID: userID}); err != nil {
				return nil, 0, err
			}
		}
	}

	return results, count, nil
}

// getExecutionsRootOnly uses parameterised raw SQL to filter out child executions.
func (s *Service) getExecutionsRootOnly(offset int64, limit int64, search string, userID string, organisationID *string, isOrg bool) ([]*api.Execution, int64, error) {
	baseSelect := `SELECT e.id, e.flo_id, f.name, e.owner_id, e.organisation_id,
		e.created_at, e.updated_at, e.completed_at, e.triggered_by,
		e.execution_status, e.completion_status,
		e.result->'duration' AS duration, e.result->'billingDuration' AS billing_duration,
		ROW_NUMBER() OVER (PARTITION BY e.flo_id ORDER BY e.created_at) AS sequence,
		tt.name AS trigger_type,
		e.agent_id
	FROM execution e
	INNER JOIN flo f ON f.id = e.flo_id AND f.archived_at IS NULL AND f.system_flow = FALSE
	LEFT JOIN trigger_invocation ti ON ti.id = e.triggered_by
	LEFT JOIN trigger t ON t.id = ti.trigger_id
	LEFT JOIN trigger_type tt ON tt.id = t.type
	WHERE e.parent_execution_id IS NULL AND e.agent_id IS NULL`

	baseCount := `SELECT COUNT(1) FROM execution e
	INNER JOIN flo f ON f.id = e.flo_id AND f.archived_at IS NULL AND f.system_flow = FALSE
	WHERE e.parent_execution_id IS NULL AND e.agent_id IS NULL`

	var args []interface{}
	argIdx := 1

	if isOrg && organisationID != nil {
		baseSelect += fmt.Sprintf(" AND e.organisation_id = $%d", argIdx)
		baseCount += fmt.Sprintf(" AND e.organisation_id = $%d", argIdx)
		args = append(args, *organisationID)
		argIdx++
	} else {
		baseSelect += fmt.Sprintf(" AND e.owner_id = $%d", argIdx)
		baseCount += fmt.Sprintf(" AND e.owner_id = $%d", argIdx)
		args = append(args, userID)
		argIdx++
	}

	if search != "" {
		baseSelect += fmt.Sprintf(" AND (CAST(e.id AS TEXT) LIKE LOWER($%d) OR LOWER(f.name) LIKE LOWER($%d))", argIdx, argIdx)
		baseCount += fmt.Sprintf(" AND (CAST(e.id AS TEXT) LIKE LOWER($%d) OR LOWER(f.name) LIKE LOWER($%d))", argIdx, argIdx)
		args = append(args, "%"+search+"%")
		argIdx++
	}

	baseSelect += fmt.Sprintf(" ORDER BY e.created_at DESC OFFSET $%d LIMIT $%d", argIdx, argIdx+1)
	selectArgs := append(args, offset, limit)

	var results []*api.Execution
	if err := s.conn.Select(&results, baseSelect, selectArgs...); err != nil {
		return nil, 0, err
	}

	var count int64
	if err := s.conn.Get(&count, baseCount, args...); err != nil {
		return nil, 0, err
	}

	return results, count, nil
}

func (s *Service) TriggerExecution(floId string, triggerId string, data interface{}) (*string, error) {
	tx, err := s.conn.Beginx()
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = tx.Rollback()
	}()

	var invocation api.TriggerInvocation

	if triggerId == "default" {
		if err := tx.NamedStmt(s.stmtGetDefaultTriggerForFlo).Get(&invocation, struct {
			FloID string `db:"flo_id"`
		}{
			FloID: floId,
		}); err != nil {
			if err == sql.ErrNoRows {
				return nil, nil
			}

			return nil, err
		}
	} else {
		if err := tx.NamedStmt(s.stmtGetTriggerForFlo).Get(&invocation, struct {
			FloID     string `db:"flo_id"`
			TriggerID string `db:"trigger_id"`
		}{
			FloID:     floId,
			TriggerID: triggerId,
		}); err != nil {
			if err == sql.ErrNoRows {
				return nil, nil
			}

			return nil, err
		}
	}

	if data == nil {
		data = struct{}{}
	}

	j, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	invocation.Data = j

	if err := tx.NamedStmt(s.stmtInsertTriggerInvocation).Get(&invocation, invocation); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}

		return nil, err
	}

	var flos []string
	if err = tx.NamedStmt(s.stmtGetFlosForTrigger).Select(&flos, invocation); err != nil {
		return nil, err
	}

	var id string
	for _, f := range flos {

		var flo api.Flo

		if err = tx.NamedStmt(s.stmtGetFloByID).Get(&flo, struct {
			ID string `db:"id"`
		}{
			ID: f,
		}); err != nil {
			return nil, err
		}

		execution := api.Execution{
			FloID:            f,
			Name:             flo.Name,
			OwnerID:          derefOrEmpty(invocation.OwnerID),
			OrganisationID:   invocation.OrganisationID,
			TriggeredBy:      &invocation.ID,
			Data:             invocation.Data,
			ExecutionStatus:  "created",
			CompletionStatus: "pending",
		}

		if err = tx.NamedStmt(s.stmtInsertFloExecution).Get(&id, execution); err != nil {
			return nil, err
		}
	}

	return &id, tx.Commit()
}

func (s *Service) SetExecutionAgentID(executionID string, agentID string) error {
	_, err := s.conn.Exec("UPDATE execution SET agent_id = $1 WHERE id = $2", agentID, executionID)
	return err
}

func (s *Service) GetExecutionsBySessionID(sessionID string) ([]*api.Execution, error) {
	var results []*api.Execution
	err := s.conn.Select(&results, `
		SELECT e.id, e.flo_id, f.name, e.owner_id, e.organisation_id,
			e.created_at, e.completed_at, e.execution_status, e.completion_status,
			e.result, e.agent_id, e.agent_session_id,
			e.result->'duration' AS duration
		FROM execution e
		INNER JOIN flo f ON f.id = e.flo_id
		WHERE e.agent_session_id = $1
		ORDER BY e.created_at ASC`, sessionID)
	return results, err
}

func (s *Service) SetExecutionAgentSessionID(executionID string, sessionID string) error {
	_, err := s.conn.Exec("UPDATE execution SET agent_session_id = $1 WHERE id = $2", sessionID, executionID)
	return err
}

func (s *Service) UpdateExecutionStatus(ID string, status string) error {
	if _, err := s.stmtUpdateFloExecutionStatus.Exec(struct {
		ID              string `db:"id"`
		ExecutionStatus string `db:"execution_status"`
	}{
		ID:              ID,
		ExecutionStatus: status,
	}); err != nil {
		return err
	}

	return nil
}

func (s *Service) UpdateCompletionStatus(ID string, status string) error {
	if _, err := s.stmtUpdateFloCompletionStatus.Exec(struct {
		ID               string `db:"id"`
		CompletionStatus string `db:"completion_status"`
	}{
		ID:               ID,
		CompletionStatus: status,
	}); err != nil {
		return err
	}

	return nil
}

func (s *Service) UpdateExecutionResult(ID string, result interface{}) error {
	if _, err := s.stmtUpdateExecutionResult.Exec(struct {
		ID     string      `db:"id"`
		Result interface{} `db:"result"`
	}{
		ID:     ID,
		Result: result,
	}); err != nil {
		return err
	}

	return nil
}

func (s *Service) UpdateExecutionRunnerID(ID string, runnerID string) error {
	if _, err := s.stmtUpdateExecutionRunnerID.Exec(struct {
		ID       string `db:"id"`
		RunnerID string `db:"runner_id"`
	}{
		ID:       ID,
		RunnerID: runnerID,
	}); err != nil {
		return err
	}

	return nil
}

func (s *Service) GetTriggerInvocationById(id string) (*api.TriggerInvocation, error) {
	var invocation api.TriggerInvocation
	if err := s.stmtGetTriggerInvocation.Get(&invocation, struct {
		ID string `db:"id"`
	}{
		ID: id,
	}); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}

		return nil, err
	}

	return &invocation, nil
}

func (s *Service) GetActions() ([]*api.Action, error) {
	var results []*api.Action

	if err := s.stmtGetActions.Select(&results, struct{}{}); err != nil {
		return nil, err
	}

	return results, nil
}

func (s *Service) GetRunners() ([]*api.Runner, error) {
	var results []*api.Runner

	if err := s.stmtGetRunners.Select(&results, struct{}{}); err != nil {
		return nil, err
	}

	return results, nil
}

func (s *Service) GetRunnerByID(ID string) (*api.Runner, error) {
	var result api.Runner

	if err := s.stmtGetRunnerByID.Get(&result, struct {
		ID string `db:"id"`
	}{
		ID: ID,
	}); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	return &result, nil
}

func (s *Service) GetRunnerByIdentifier(identifier string) (*api.Runner, error) {
	var result api.Runner

	if err := s.stmtGetRunnerByIdentifier.Get(&result, struct {
		Identifier string `db:"identifier"`
	}{
		Identifier: identifier,
	}); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	return &result, nil
}

func (s *Service) EnrolRunner(runner api.Runner) (*string, error) {
	var result string
	if err := s.stmtInsertRunner.Get(&result, runner); err != nil {
		return nil, err
	}

	var queue api.Queue
	if err := s.stmtGetQueueByRegistrationCode.Get(&queue, struct {
		RegistrationCode string `db:"registration_code"`
	}{
		RegistrationCode: runner.RegistrationCode,
	}); err != nil {
		if err == sql.ErrNoRows {
			return nil, errors.New("missing queue registration")
		}
		return nil, err
	}

	if _, err := s.stmtInsertQueueRunner.Exec(struct {
		QueueID  string `db:"queue_id"`
		RunnerID string `db:"runner_id"`
	}{
		QueueID:  queue.ID,
		RunnerID: result,
	}); err != nil {
		return nil, err
	}

	return &result, nil
}

func (s *Service) GetExecutionByID(ID string) (*api.Execution, error) {
	var result api.Execution

	if err := s.stmtGetExecutionByID.Get(&result, struct {
		ID string `db:"id"`
	}{
		ID: ID,
	}); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	return &result, nil
}

func (s *Service) GetExecutionForRunnerID(ID string) (*api.Execution, error) {
	r, err := s.GetRunnerByIdentifier(ID)
	if err != nil {
		return nil, err
	}

	var organisationID *string
	if err := s.stmtGetOrganisationByRunnerIdentifier.Get(&organisationID, struct {
		ID string `db:"runner_id"`
	}{
		ID: r.ID,
	}); err != nil {
		if err != sql.ErrNoRows {
			return nil, err
		}
	}

	var execution api.Execution
	if organisationID != nil {
		if err := s.stmtGetPendingExecutionByOrganisationID.Get(&execution, struct {
			OrganisationID string `db:"organisation_id"`
		}{
			OrganisationID: *organisationID,
		}); err != nil {
			if err == sql.ErrNoRows {
				return nil, nil
			}
			return nil, err
		}
	} else {
		if err := s.stmtGetPendingExecutionByNullOrganisationID.Get(&execution, struct{}{}); err != nil {
			if err == sql.ErrNoRows {
				return nil, nil
			}
			return nil, err
		}
	}

	// Check queue assignment: if the flow has a queue_id, verify the runner is in that queue
	flo, err := s.GetFloByID(execution.FloID)
	if err != nil {
		return nil, err
	}

	if flo != nil && flo.QueueID != nil {
		// Walk queue hierarchy to check if runner is assigned
		matched := false
		queueID := *flo.QueueID

		for {
			var count int64
			if err := s.stmtCanRunnerAccessQueue.Get(&count, struct {
				QueueID  string `db:"queue_id"`
				RunnerID string `db:"runner_id"`
			}{QueueID: queueID, RunnerID: r.ID}); err == nil && count > 0 {
				matched = true
				break
			}

			// Check parent queue
			q, err := s.GetQueueByID(queueID)
			if err != nil || q == nil || q.ParentID == nil {
				break
			}
			queueID = *q.ParentID
		}

		if !matched {
			// Runner not in the flow's queue hierarchy — no execution for this runner
			return nil, nil
		}
	}

	return &execution, nil
}

func (s *Service) UpdateRunnerLastContact(ID string, IPAddress string) error {
	if _, err := s.stmtUpdateRunnerLastAccess.Exec(struct {
		ID        string `db:"id"`
		IPAddress string `db:"ip"`
	}{
		ID:        ID,
		IPAddress: IPAddress,
	}); err != nil {
		return err
	}

	return nil
}

func (s *Service) GetQueueByRegistrationCode(code string) (*api.Queue, error) {
	var queue api.Queue

	if err := s.stmtGetQueueByRegistrationCode.Get(&queue, struct {
		Code string `db:"registration_code"`
	}{
		Code: code,
	}); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	return &queue, nil
}

func (s *Service) GetQueuesByOrganisationID(organisationID string) ([]*api.Queue, error) {
	var results []*api.Queue
	if err := s.stmtGetQueuesByOrganisationID.Select(&results, struct {
		OrganisationID string `db:"organisation_id"`
	}{OrganisationID: organisationID}); err != nil {
		return nil, err
	}
	return results, nil
}

func (s *Service) GetQueueByID(id string) (*api.Queue, error) {
	var queue api.Queue
	if err := s.stmtGetQueueByID.Get(&queue, struct {
		ID string `db:"id"`
	}{ID: id}); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &queue, nil
}

func (s *Service) CreateQueue(organisationID string, name string, parentID *string) (*string, error) {
	var id string
	if err := s.stmtCreateQueue.Get(&id, struct {
		OrganisationID string  `db:"organisation_id"`
		ParentID       *string `db:"parent_id"`
		Name           string  `db:"name"`
	}{OrganisationID: organisationID, ParentID: parentID, Name: name}); err != nil {
		return nil, err
	}
	return &id, nil
}

func (s *Service) DeleteQueue(id string, organisationID string) error {
	_, err := s.stmtDeleteQueue.Exec(struct {
		ID             string `db:"id"`
		OrganisationID string `db:"organisation_id"`
	}{ID: id, OrganisationID: organisationID})
	return err
}

func (s *Service) GetQueueRunners(queueID string) ([]*api.Runner, error) {
	var results []*api.Runner
	if err := s.stmtGetQueueRunners.Select(&results, struct {
		QueueID string `db:"queue_id"`
	}{QueueID: queueID}); err != nil {
		return nil, err
	}
	return results, nil
}

func (s *Service) AddRunnerToQueue(queueID string, runnerID string) error {
	_, err := s.stmtInsertQueueRunner.Exec(struct {
		QueueID  string `db:"queue_id"`
		RunnerID string `db:"runner_id"`
	}{QueueID: queueID, RunnerID: runnerID})
	return err
}

func (s *Service) RemoveRunnerFromQueue(queueID string, runnerID string) error {
	_, err := s.stmtRemoveQueueRunner.Exec(struct {
		QueueID  string `db:"queue_id"`
		RunnerID string `db:"runner_id"`
	}{QueueID: queueID, RunnerID: runnerID})
	return err
}

func (s *Service) CreateEnvironment(environment api.Environment) (*string, error) {
	var id string
	if err := s.stmtCreateEnvironment.Get(&id, struct {
		api.Environment
		EncryptKey string `db:"encrypt_key"`
	}{
		environment,
		s.config.Database.EncryptionKey,
	}); err != nil {
		return nil, err
	}

	return &id, nil
}

func (s *Service) GetEnvironments(ownerID string, organisationID *string) ([]*api.Environment, error) {
	var results []*api.Environment

	if organisationID != nil && *organisationID != "" {
		err := s.stmtGetOrgEnvironments.Select(&results, struct {
			OrganisationID string `db:"organisation_id"`
		}{
			OrganisationID: *organisationID,
		})
		if err != nil {
			return nil, err
		}
	} else {
		err := s.stmtGetAllEnvironments.Select(&results, struct {
			OwnerID string `db:"owner_id"`
		}{
			OwnerID: ownerID,
		})
		if err != nil {
			return nil, err
		}
	}

	return results, nil
}

func (s *Service) GetEnvironmentByIDDirect(ID string) (*api.Environment, error) {
	var result api.Environment
	err := s.stmtGetEnvironmentByIDDirect.Get(&result, struct {
		ID         string `db:"id"`
		EncryptKey string `db:"encrypt_key"`
	}{
		ID:         ID,
		EncryptKey: s.config.Database.EncryptionKey,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &result, nil
}

func (s *Service) GetEnvironmentByID(ID string, ownerID string, organisationID *string) (*api.Environment, error) {
	var result api.Environment

	err := s.stmtGetEnvironmentByID.Get(&result, struct {
		ID             string  `db:"id"`
		OwnerID        string  `db:"owner_id"`
		OrganisationID *string `db:"organisation_id"`
		EncryptKey     string  `db:"encrypt_key"`
	}{
		ID:             ID,
		OwnerID:        ownerID,
		OrganisationID: organisationID,
		EncryptKey:     s.config.Database.EncryptionKey,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}

		return nil, err
	}

	return &result, nil
}

func (s *Service) GetEnvironmentByName(name string, ownerID string, organisationID *string) (*api.Environment, error) {
	var result api.Environment

	err := s.stmtGetEnvironmentByName.Get(&result, struct {
		Name           string  `db:"name"`
		OwnerID        string  `db:"owner_id"`
		OrganisationID *string `db:"organisation_id"`
		EncryptKey     string  `db:"encrypt_key"`
	}{
		Name:           name,
		OwnerID:        ownerID,
		OrganisationID: organisationID,
		EncryptKey:     s.config.Database.EncryptionKey,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}

		return nil, err
	}

	return &result, nil
}

func (s *Service) GetEnvironmentByIDAsRunner(ID string) (*api.Environment, error) {
	var result api.Environment

	err := s.stmtGetEnvironmentByIDAsRunner.Get(&result, struct {
		ID         string `db:"id"`
		EncryptKey string `db:"encrypt_key"`
	}{
		ID:         ID,
		EncryptKey: s.config.Database.EncryptionKey,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}

		return nil, err
	}

	return &result, nil
}

func (s *Service) DeleteEnvironmentByID(ID string) error {
	_, err := s.stmtDeleteEnvironmentByID.Exec(struct {
		ID string `db:"id"`
	}{
		ID: ID,
	})

	return err
}

func (s *Service) GetEnvironmentProperties(environmentID string, environmentKey string) ([]*api.EnvironmentProperty, error) {
	var results []*api.EnvironmentProperty

	if err := s.stmtGetEnvironmentProperties.Select(&results, struct {
		ID  string `db:"environment_id"`
		Key string `db:"environment_key"`
	}{
		ID:  environmentID,
		Key: environmentKey,
	}); err != nil {
		return nil, err
	}

	return results, nil
}

func (s *Service) GetEnvironmentPropertyByID(environmentID string, environmentKey string, id string) (*api.EnvironmentProperty, error) {
	var results api.EnvironmentProperty

	if err := s.stmtGetEnvironmentPropertyByID.Get(&results, struct {
		EnvironmentID string `db:"environment_id"`
		Key           string `db:"environment_key"`
		ID            string `db:"id"`
		EncryptKey    string `db:"encrypt_key"`
	}{
		EnvironmentID: environmentID,
		Key:           environmentKey,
		ID:            id,
		EncryptKey:    s.config.Database.EncryptionKey,
	}); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	return &results, nil
}

func (s *Service) GetEnvironmentPropertyByName(environmentID string, environmentKey string, name string) (*api.EnvironmentProperty, error) {
	var results api.EnvironmentProperty

	if err := s.stmtGetEnvironmentPropertyByName.Get(&results, struct {
		EnvironmentID string `db:"environment_id"`
		Key           string `db:"environment_key"`
		Name          string `db:"name"`
		EncryptKey    string `db:"encrypt_key"`
	}{
		EnvironmentID: environmentID,
		Key:           environmentKey,
		Name:          name,
		EncryptKey:    s.config.Database.EncryptionKey,
	}); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	return &results, nil
}

func (s *Service) CreateEnvironmentProperty(environmentID string, environmentKey string, property api.EnvironmentProperty) (*string, error) {
	property.EnvironmentID = environmentID
	query := struct {
		api.EnvironmentProperty
		EnvironmentID  string `db:"environment_id"`
		EnvironmentKey string `db:"environment_key"`
	}{
		property,
		environmentID,
		environmentKey,
	}

	var id string
	if err := s.stmtInsertEnvironmentProperty.Get(&id, query); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	return &id, nil
}

func (s *Service) UpdateEnvironmentProperty(environmentID string, environmentKey string, property api.EnvironmentProperty) error {
	query := struct {
		api.EnvironmentProperty
		EnvironmentID  string `db:"environment_id"`
		EnvironmentKey string `db:"environment_key"`
	}{
		property,
		environmentID,
		environmentKey,
	}

	if _, err := s.stmtUpdateEnvironmentProperty.Exec(query); err != nil {
		return err
	}

	return nil
}

func (s *Service) RemoveEnvironmentProperty(propertyID string) error {
	query := struct {
		ID string `db:"id"`
	}{
		ID: propertyID,
	}

	if _, err := s.stmtDeleteEnvironmentProperty.Exec(query); err != nil {
		return err
	}

	return nil
}

func (s *Service) GetEnvironmentSecrets(environmentID string, environmentKey string) ([]*api.EnvironmentSecret, error) {
	var results []*api.EnvironmentSecret

	if err := s.stmtGetEnvironmentSecrets.Select(&results, struct {
		ID  string `db:"environment_id"`
		Key string `db:"environment_key"`
	}{
		ID:  environmentID,
		Key: environmentKey,
	}); err != nil {
		return nil, err
	}

	return results, nil
}

func (s *Service) GetEnvironmentSecretByID(environmentID string, environmentKey string, id string) (*api.EnvironmentSecret, error) {
	var results api.EnvironmentSecret

	if err := s.stmtGetEnvironmentSecretByID.Get(&results, struct {
		EnvironmentID string `db:"environment_id"`
		Key           string `db:"environment_key"`
		ID            string `db:"id"`
		EncryptKey    string `db:"encrypt_key"`
	}{
		EnvironmentID: environmentID,
		Key:           environmentKey,
		ID:            id,
		EncryptKey:    s.config.Database.EncryptionKey,
	}); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	return &results, nil
}

func (s *Service) GetEnvironmentSecretByName(environmentID string, environmentKey string, name string) (*api.EnvironmentSecret, error) {
	var results api.EnvironmentSecret

	if err := s.stmtGetEnvironmentSecretByName.Get(&results, struct {
		EnvironmentID string `db:"environment_id"`
		Key           string `db:"environment_key"`
		Name          string `db:"name"`
		EncryptKey    string `db:"encrypt_key"`
	}{
		EnvironmentID: environmentID,
		Key:           environmentKey,
		Name:          name,
		EncryptKey:    s.config.Database.EncryptionKey,
	}); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	return &results, nil
}

func (s *Service) CreateEnvironmentSecret(environmentID string, environmentKey string, secret api.CreateEnvironmentSecret) (*string, error) {
	secret.EnvironmentID = environmentID
	query := struct {
		api.CreateEnvironmentSecret
		EnvironmentID  string `db:"environment_id"`
		EnvironmentKey string `db:"environment_key"`
	}{
		secret,
		environmentID,
		environmentKey,
	}

	var id string
	if err := s.stmtInsertEnvironmentSecret.Get(&id, query); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	return &id, nil
}

func (s *Service) UpdateEnvironmentSecret(environmentID string, environmentKey string, secretID string, value string) error {
	_, err := s.stmtUpdateEnvironmentSecret.Exec(struct {
		ID             string `db:"id"`
		EnvironmentID  string `db:"environment_id"`
		Value          string `db:"value"`
		EnvironmentKey string `db:"environment_key"`
	}{
		ID:             secretID,
		EnvironmentID:  environmentID,
		Value:          value,
		EnvironmentKey: environmentKey,
	})
	return err
}

func (s *Service) RemoveEnvironmentSecret(secretID string) error {
	query := struct {
		ID string `db:"id"`
	}{
		ID: secretID,
	}

	if _, err := s.stmtDeleteEnvironmentSecret.Exec(query); err != nil {
		return err
	}

	return nil
}

func (s *Service) GetTriggers(ownerID string) ([]*api.Trigger, error) {
	var results []*api.Trigger

	if err := s.stmtGetTriggers.Select(&results, struct {
		OwnerID string `db:"owner_id"`
	}{
		OwnerID: ownerID,
	}); err != nil {
		return nil, err
	}

	return results, nil
}

func (s *Service) GetTriggerByID(id string) (*api.Trigger, error) {
	var result api.Trigger

	if err := s.stmtGetTriggerByID.Get(&result, struct {
		ID string `db:"id"`
	}{
		ID: id,
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return &result, nil
}

func (s *Service) CreateTriggerWithType(trigger api.Trigger) (*string, error) {
	var ID string

	dataBytes, err := json.Marshal(trigger.Data)
	if err != nil {
		return nil, err
	}

	if trigger.Data == nil {
		dataBytes = nil
	}

	if err := s.stmtCreateTrigger.Get(&ID, struct {
		Name           string  `db:"name"`
		OwnerID        *string `db:"owner_id"`
		OrganisationID *string `db:"organisation_id"`
		TypeName       string  `db:"type_name"`
		Data           []byte  `db:"data"`
	}{
		Name:           trigger.Name,
		OwnerID:        trigger.OwnerID,
		OrganisationID: trigger.OrganisationID,
		TypeName:       trigger.TypeName,
		Data:           dataBytes,
	}); err != nil {
		return nil, err
	}

	return &ID, nil
}

func (s *Service) GetTriggersByFloID(floID string) ([]*api.Trigger, error) {
	var triggers []*api.Trigger
	if err := s.stmtGetFloTriggers.Select(&triggers, struct {
		FloID string `db:"id"`
	}{
		FloID: floID,
	}); err != nil {
		return nil, err
	}
	return triggers, nil
}

func (s *Service) LinkFloToTrigger(floID string, triggerID string) error {
	_, err := s.stmtLinkFloToTrigger.Exec(struct {
		FloID     string `db:"flo_id"`
		TriggerID string `db:"trigger_id"`
	}{
		FloID:     floID,
		TriggerID: triggerID,
	})
	return err
}

func (s *Service) UpdateTrigger(trigger api.Trigger) error {
	dataBytes, err := json.Marshal(trigger.Data)
	if err != nil {
		return err
	}

	if trigger.Data == nil {
		dataBytes = nil
	}

	if _, err := s.stmtUpdateTrigger.Exec(struct {
		ID       string `db:"id"`
		Name     string `db:"name"`
		TypeName string `db:"type_name"`
		Data     []byte `db:"data"`
	}{
		ID:       trigger.ID,
		Name:     trigger.Name,
		TypeName: trigger.TypeName,
		Data:     dataBytes,
	}); err != nil {
		return err
	}

	return nil
}

func (s *Service) DeleteTrigger(id string) error {
	if _, err := s.stmtDeleteFloTrigger.Exec(struct {
		TriggerID string `db:"trigger_id"`
	}{
		TriggerID: id,
	}); err != nil {
		return err
	}

	if _, err := s.stmtDeleteTrigger.Exec(struct {
		ID string `db:"id"`
	}{
		ID: id,
	}); err != nil {
		return err
	}

	return nil
}

func (s *Service) GetUsage(ownerID string, organisationID *string) (*api.UserDashboard, error) {
	var result api.UserDashboard
	var err error

	if organisationID != nil && *organisationID != "" {
		err = s.stmtGetAllowanceForOrg.Get(&result, struct {
			OwnerID        string `db:"owner_id"`
			OrganisationID string `db:"organisation_id"`
		}{
			OwnerID:        ownerID,
			OrganisationID: *organisationID,
		})
	} else {
		err = s.stmtGetAllowanceForOwner.Get(&result, struct {
			OwnerID string `db:"owner_id"`
		}{
			OwnerID: ownerID,
		})
	}

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	return &result, nil
}

// RBAC: Group operations

func (s *Service) GetGroupsByOrganisationID(orgID string) ([]*api.Group, error) {
	var results []*api.Group

	if err := s.stmtGetGroupsByOrgID.Select(&results, struct {
		OrganisationID string `db:"organisation_id"`
	}{
		OrganisationID: orgID,
	}); err != nil {
		return nil, err
	}

	// Load permissions for each group
	for _, g := range results {
		perms, err := s.GetGroupPermissions(g.ID)
		if err != nil {
			return nil, err
		}
		g.Permissions = perms
	}

	return results, nil
}

func (s *Service) GetGroupByID(groupID string) (*api.Group, error) {
	var result api.Group

	if err := s.stmtGetGroupByID.Get(&result, struct {
		ID string `db:"id"`
	}{
		ID: groupID,
	}); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	perms, err := s.GetGroupPermissions(result.ID)
	if err != nil {
		return nil, err
	}
	result.Permissions = perms

	return &result, nil
}

func (s *Service) CreateGroup(group api.Group) (*string, error) {
	var id string

	if err := s.stmtCreateGroup.Get(&id, group); err != nil {
		return nil, err
	}

	return &id, nil
}

func (s *Service) UpdateGroup(group api.Group) error {
	_, err := s.stmtUpdateGroup.Exec(group)
	return err
}

func (s *Service) DeleteGroup(groupID string) error {
	_, err := s.stmtDeleteGroup.Exec(struct {
		ID string `db:"id"`
	}{
		ID: groupID,
	})
	return err
}

func (s *Service) GetGroupMembers(groupID string) ([]*api.GroupMember, error) {
	var results []*api.GroupMember

	if err := s.stmtGetGroupMembers.Select(&results, struct {
		GroupID string `db:"group_id"`
	}{
		GroupID: groupID,
	}); err != nil {
		return nil, err
	}

	return results, nil
}

func (s *Service) AddUserToGroup(groupID, userID string) error {
	_, err := s.stmtAddUserToGroup.Exec(struct {
		GroupID string `db:"group_id"`
		UserID  string `db:"user_id"`
	}{
		GroupID: groupID,
		UserID:  userID,
	})
	return err
}

func (s *Service) RemoveUserFromGroup(groupID, userID string) error {
	_, err := s.stmtRemoveUserFromGroup.Exec(struct {
		GroupID string `db:"group_id"`
		UserID  string `db:"user_id"`
	}{
		GroupID: groupID,
		UserID:  userID,
	})
	return err
}

func (s *Service) GetGroupPermissions(groupID string) ([]string, error) {
	var results []string

	rows, err := s.stmtGetGroupPermissions.Queryx(struct {
		GroupID string `db:"group_id"`
	}{
		GroupID: groupID,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var perm string
		if err := rows.Scan(&perm); err != nil {
			return nil, err
		}
		results = append(results, perm)
	}

	return results, nil
}

func (s *Service) SetGroupPermissions(groupID string, permissions []string) error {
	// Delete existing permissions
	if _, err := s.stmtDeleteGroupPermissions.Exec(struct {
		GroupID string `db:"group_id"`
	}{
		GroupID: groupID,
	}); err != nil {
		return err
	}

	// Insert new permissions
	for _, perm := range permissions {
		if _, err := s.stmtInsertGroupPermission.Exec(struct {
			GroupID    string `db:"group_id"`
			Permission string `db:"permission"`
		}{
			GroupID:    groupID,
			Permission: perm,
		}); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) GetUserPermissionsInOrganisation(orgID, userID string) ([]string, error) {
	var results []string

	rows, err := s.stmtGetUserPermissionsInOrg.Queryx(struct {
		OrganisationID string `db:"organisation_id"`
		UserID         string `db:"user_id"`
	}{
		OrganisationID: orgID,
		UserID:         userID,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var perm string
		if err := rows.Scan(&perm); err != nil {
			return nil, err
		}
		results = append(results, perm)
	}

	return results, nil
}

func (s *Service) GetDefaultGroupsForOrganisation(orgID string) ([]string, error) {
	var results []string

	rows, err := s.stmtGetDefaultGroups.Queryx(struct {
		OrganisationID string `db:"organisation_id"`
	}{
		OrganisationID: orgID,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		results = append(results, id)
	}

	return results, nil
}

func (s *Service) CountUserGroupsInOrganisation(orgID, userID string) (int, error) {
	var count int

	if err := s.stmtCountUserGroupsInOrg.Get(&count, struct {
		OrganisationID string `db:"organisation_id"`
		UserID         string `db:"user_id"`
	}{
		OrganisationID: orgID,
		UserID:         userID,
	}); err != nil {
		return 0, err
	}

	return count, nil
}

// Flo favourites

func (s *Service) GetFloFavourites(userID string) ([]string, error) {
	var results []string

	rows, err := s.stmtGetFloFavourites.Queryx(struct {
		UserID string `db:"user_id"`
	}{
		UserID: userID,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var floID string
		if err := rows.Scan(&floID); err != nil {
			return nil, err
		}
		results = append(results, floID)
	}

	return results, nil
}

func (s *Service) AddFloFavourite(userID, floID string) error {
	_, err := s.stmtAddFloFavourite.Exec(struct {
		UserID string `db:"user_id"`
		FloID  string `db:"flo_id"`
	}{
		UserID: userID,
		FloID:  floID,
	})
	return err
}

func (s *Service) RemoveFloFavourite(userID, floID string) error {
	_, err := s.stmtRemoveFloFavourite.Exec(struct {
		UserID string `db:"user_id"`
		FloID  string `db:"flo_id"`
	}{
		UserID: userID,
		FloID:  floID,
	})
	return err
}

func (s *Service) CreateFeedback(feedback api.Feedback) error {
	_, err := s.stmtCreateFeedback.Exec(feedback)
	return err
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ── Subscription entitlements ─────────────────────────────────────────

// UpsertEntitlement creates or updates a single entitlement record.
func (s *Service) UpsertEntitlement(ent *api.SubscriptionEntitlement) error {
	_, err := s.stmtUpsertEntitlement.Exec(ent)
	return err
}

// GetEntitlement returns a single entitlement by key for an owner.
func (s *Service) GetEntitlement(ownerID string, orgID *string, key string) (*api.SubscriptionEntitlement, error) {
	var ent api.SubscriptionEntitlement
	if err := s.stmtGetEntitlement.Get(&ent, map[string]interface{}{
		"owner_id":        ownerID,
		"organisation_id": orgID,
		"entitlement_key": key,
	}); err != nil {
		return nil, err
	}
	return &ent, nil
}

// GetAllEntitlements returns all entitlements for an owner.
func (s *Service) GetAllEntitlements(ownerID string, orgID *string) ([]*api.SubscriptionEntitlement, error) {
	var ents []*api.SubscriptionEntitlement
	if err := s.stmtGetAllEntitlements.Select(&ents, map[string]interface{}{
		"owner_id":        ownerID,
		"organisation_id": orgID,
	}); err != nil {
		return nil, err
	}
	return ents, nil
}

// DeleteEntitlements removes all entitlements for an owner.
func (s *Service) DeleteEntitlements(ownerID string, orgID *string) error {
	_, err := s.stmtDeleteEntitlements.Exec(map[string]interface{}{
		"owner_id":        ownerID,
		"organisation_id": orgID,
	})
	return err
}
