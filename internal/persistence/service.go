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

type Service struct {
	config *config.Config
	conn   *sqlx.DB

	stmtGetOrganisations      *sqlx.NamedStmt
	stmtGetOrganisationByID   *sqlx.NamedStmt
	stmtCreateOrganisation    *sqlx.NamedStmt
	stmtUpdateOrganisation    *sqlx.NamedStmt
	stmtAddUserToOrganisation *sqlx.NamedStmt

	stmtGetUserByID *sqlx.NamedStmt
	stmtCreateUser  *sqlx.NamedStmt
	stmtUpdateUser  *sqlx.NamedStmt

	stmtGetMyFlos             *sqlx.NamedStmt
	stmtGetMyFlosWithFilter   *sqlx.NamedStmt
	stmtCountMyFlos           *sqlx.NamedStmt
	stmtCountMyFlosWithFilter *sqlx.NamedStmt
	stmtGetFloByID            *sqlx.NamedStmt
	stmtCreateFlo             *sqlx.NamedStmt
	stmtUpdateFlo             *sqlx.NamedStmt
	stmtDeleteFlo             *sqlx.NamedStmt

	stmtCreateFloRevision           *sqlx.NamedStmt
	stmtGetLatestFloRevisionByFloID *sqlx.NamedStmt
	stmtGetFloRevisions             *sqlx.NamedStmt
	stmtGetFloRevisionByID          *sqlx.NamedStmt

	stmtInsertDefaultTrigger *sqlx.NamedStmt
	stmtLinkFloToTrigger     *sqlx.NamedStmt
	stmtRemoveFloTriggerLink *sqlx.NamedStmt

	stmtGetFloTriggers *sqlx.NamedStmt

	stmtGetLatestExecutionForFlo  *sqlx.NamedStmt
	stmtGetExecutions             *sqlx.NamedStmt
	stmtGetExecutionsWithFilter   *sqlx.NamedStmt
	stmtCountExecutions           *sqlx.NamedStmt
	stmtCountExecutionsWithFilter *sqlx.NamedStmt

	stmtGetDefaultTriggerForFlo *sqlx.NamedStmt
	stmtGetTriggerForFlo        *sqlx.NamedStmt
	stmtInsertTriggerInvocation *sqlx.NamedStmt

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

	stmtGetQueueByRegistrationCode *sqlx.NamedStmt

	stmtGetPendingExecutionByOrganisationID     *sqlx.NamedStmt
	stmtGetPendingExecutionByNullOrganisationID *sqlx.NamedStmt
	stmtGetOrganisationByRunnerIdentifier       *sqlx.NamedStmt

	stmtCreateEnvironment          *sqlx.NamedStmt
	stmtGetEnvironmentByID         *sqlx.NamedStmt
	stmtGetEnvironmentByName       *sqlx.NamedStmt
	stmtGetEnvironmentByIDAsRunner *sqlx.NamedStmt
	stmtGetAllEnvironments         *sqlx.NamedStmt
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

	stmtGetUsageThisMonthForUserID *sqlx.NamedStmt
}

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
		    id,
		    name,
		    icon,
		    created_at
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
			icon = :icon
		WHERE 
		    id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtAddUserToOrganisation, err = s.conn.PrepareNamed(`
		INSERT INTO organisation_user (
			organisation_id,
			user_id
		) VALUES (
		    :organisation_id,
			:user_id
		);
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
		    marketing_opt_in
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
			t.type
		FROM
			trigger t
		INNER JOIN flo_trigger ft ON ft.trigger_id = t.id
		INNER JOIN flo f ON ft.flo_id = f.id
		WHERE 
		    f.id = :id
		ORDER BY
		    name ASC
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
			environment_id = :environment_id
		WHERE
		    id = :id
	`)
	if err != nil {
		return nil, err
	}

	s.stmtDeleteFlo, err = s.conn.PrepareNamed(`
		DELETE FROM flo 
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
		    completion_status
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

	s.stmtGetExecutions, err = s.conn.PrepareNamed(`
		SELECT
		    e.id,
		    e.flo_id,
		    f.name,
		    e.owner_id,
		    e.organisation_id,
		    e.created_at,
		    e.updated_at,
		    e.completed_at,
		    e.triggered_by,
		    e.execution_status,
		    e.completion_status,
			e.result->'duration' AS duration,
			e.result->'billingDuration' AS billing_duration,
    		(SELECT COUNT(1) FROM execution e2 WHERE e2.flo_id = e.flo_id AND e2.created_at <= e.created_at) AS sequence
		FROM
		    execution e
		INNER JOIN
			flo f ON f.id = e.flo_id
		WHERE
		    (e.owner_id = :user_id
		OR
		     e.organisation_id = :organisation_id)
		ORDER BY 
		    e.created_at DESC
		OFFSET :offset
		LIMIT :limit
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetExecutionsWithFilter, err = s.conn.PrepareNamed(`
		SELECT
		    e.id,
		    e.flo_id,
		    f.name,
		    e.owner_id,
		    e.organisation_id,
		    e.created_at,
		    e.updated_at,
		    e.completed_at,
		    e.triggered_by,
		    e.execution_status,
		    e.completion_status,
			e.result->'duration' AS duration,
			e.result->'billingDuration' AS billing_duration,
    		(SELECT COUNT(1) FROM execution e2 WHERE e2.flo_id = e.flo_id AND e2.created_at <= e.created_at) AS sequence
		FROM
		    execution e
		INNER JOIN
			flo f ON f.id = e.flo_id
		WHERE
		    (CAST(e.id AS TEXT) LIKE LOWER(:search)
		OR
		    LOWER(f.name) LIKE LOWER(:search))
		AND
		    (e.owner_id = :user_id
		OR
		     e.organisation_id = :organisation_id)
		ORDER BY 
		    e.created_at DESC
		OFFSET :offset
		LIMIT :limit
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCountExecutions, err = s.conn.PrepareNamed(`
		SELECT
		    COUNT(1)
		FROM
		    execution e
		INNER JOIN
			flo f ON f.id = e.flo_id
		WHERE
		    (e.owner_id = :user_id
		OR
		     e.organisation_id = :organisation_id)
	`)
	if err != nil {
		return nil, err
	}

	s.stmtCountExecutionsWithFilter, err = s.conn.PrepareNamed(`
		SELECT
		    COUNT(1)
		FROM
		    execution e
		INNER JOIN
			flo f ON f.id = e.flo_id
		WHERE
		    (CAST(e.id AS TEXT) LIKE LOWER(:search)
		OR
		    LOWER(f.name) LIKE LOWER(:search))
		AND
		    (e.owner_id = :user_id
		OR
		     e.organisation_id = :organisation_id)
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
		    runner_id = :runner_id,
			completed_at = CURRENT_TIMESTAMP
		WHERE
		    id = :id;
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetExecutionByID, err = s.conn.PrepareNamed(`
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
			data,
			runner_id,
			result,
			result->'duration' AS duration,
			result->'billingDuration' AS billing_duration,
			(SELECT COUNT(1) FROM execution e2 WHERE e2.flo_id = e.flo_id AND e2.created_at <= e.created_at) AS sequence
		FROM
		    execution e
		WHERE
		    id = :id;
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
		    version
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
		    version
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
		    version
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
		    version
		) VALUES (
		    :identifier,
			:name,
			:registration_code,
			CURRENT_TIMESTAMP,
			:ip,
		    :version
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

	s.stmtGetPendingExecutionByOrganisationID, err = db.PrepareNamed(`
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
		    data,
		    runner_id,
		    result,
			result->'duration' AS duration,
			result->'billingDuration' AS billing_duration
		FROM
		    execution e
		WHERE
		    organisation_id = :organisation_id
		AND
		    execution_status = 'created'
		ORDER BY created_at DESC
		LIMIT 1
	`)
	if err != nil {
		return nil, err
	}

	s.stmtGetPendingExecutionByNullOrganisationID, err = db.PrepareNamed(`
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
		    data,
		    runner_id,
		    result,
			result->'duration' AS duration,
			result->'billingDuration' AS billing_duration
		FROM
		    execution e
		WHERE
		    organisation_id IS NULL
		AND
		    execution_status = 'created'
		ORDER BY created_at DESC
		LIMIT 1
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
		    (owner_id = :owner_id)
		OR 
		    (organisation_id = :organisation_id)
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

	s.stmtGetUsageThisMonthForUserID, err = db.PrepareNamed(`
		SELECT
			SUM(CASE
				WHEN e.result->'duration' IS NULL THEN 0
				ELSE CAST(e.result->>'duration' AS INT)
			END) AS usage,
		    50 * 1000 AS allowance
		FROM 
		    execution e
		WHERE 
			created_at > cast(date_trunc('month', current_date) as date)
		AND
			(owner_id = :owner_id
		OR
			organisation_id = :organisation_id);
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

func (s *Service) AddUserToOrganisation(organisationID string, userID string) error {
	if _, err := s.stmtAddUserToOrganisation.Exec(struct {
		OrganisationID string `db:"organisation_id"`
		UserID         string `db:"user_id"`
	}{
		OrganisationID: organisationID,
		UserID:         userID,
	}); err != nil {
		return err
	}

	return nil
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

func (s *Service) GetMyFlos(userID string, offset int64, limit int64, search string) ([]*api.Flo, int64, error) {
	var results []*api.Flo
	var count int64

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

func (s *Service) GetExecutions(offset int64, limit int64, search string, userID string, organisationID *string) ([]*api.Execution, int64, error) {
	var results []*api.Execution
	var count int64

	if search != "" {
		if err := s.stmtGetExecutionsWithFilter.Select(&results, struct {
			Offset         int64   `db:"offset"`
			Limited        int64   `db:"limit"`
			Search         string  `db:"search"`
			UserID         string  `db:"user_id"`
			OrganisationID *string `db:"organisation_id"`
		}{
			Offset:         offset,
			Limited:        limit,
			Search:         "%" + search + "%",
			UserID:         userID,
			OrganisationID: organisationID,
		}); err != nil {
			log.Info("1")
			return nil, 0, err
		}

		if err := s.stmtCountExecutionsWithFilter.Get(&count, struct {
			Offset         int64   `db:"offset"`
			Limited        int64   `db:"limit"`
			Search         string  `db:"search"`
			UserID         string  `db:"user_id"`
			OrganisationID *string `db:"organisation_id"`
		}{
			Offset:         offset,
			Limited:        limit,
			Search:         "%" + search + "%",
			UserID:         userID,
			OrganisationID: organisationID,
		}); err != nil {
			log.Info("2")
			return nil, 0, err
		}
	} else {
		if err := s.stmtGetExecutions.Select(&results, struct {
			Offset         int64   `db:"offset"`
			Limited        int64   `db:"limit"`
			UserID         string  `db:"user_id"`
			OrganisationID *string `db:"organisation_id"`
		}{
			Offset:         offset,
			Limited:        limit,
			UserID:         userID,
			OrganisationID: organisationID,
		}); err != nil {
			log.Info("3")
			return nil, 0, err
		}

		if err := s.stmtCountExecutions.Get(&count, struct {
			Offset         int64   `db:"offset"`
			Limited        int64   `db:"limit"`
			UserID         string  `db:"user_id"`
			OrganisationID *string `db:"organisation_id"`
		}{
			Offset:         offset,
			Limited:        limit,
			UserID:         userID,
			OrganisationID: organisationID,
		}); err != nil {
			log.Info("4")
			return nil, 0, err
		}
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
			OwnerID:          *invocation.OwnerID,
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

	err := s.stmtGetAllEnvironments.Select(&results, struct {
		OwnerID        string  `db:"owner_id"`
		OrganisationID *string `db:"organisation_id"`
	}{
		OwnerID:        ownerID,
		OrganisationID: organisationID,
	})
	if err != nil {
		return nil, err
	}

	return results, nil
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

func (s *Service) GetUsage(ownerID string, organisationID *string) (*api.UserDashboard, error) {
	var result api.UserDashboard
	err := s.stmtGetUsageThisMonthForUserID.Get(&result, struct {
		OwnerID        string  `db:"owner_id"`
		OrganisationID *string `db:"organisation_id"`
	}{
		OwnerID:        ownerID,
		OrganisationID: organisationID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	return &result, nil
}
