package actions

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/config"
	"github.com/jmoiron/sqlx"
	log "github.com/sirupsen/logrus"
)

type Migrator struct {
	config *config.Config
	conn   *sqlx.DB
}

type MigrationResult struct {
	Created int64
	Updated int64
	Removed int64
}

type ConnectionDefinition struct {
}

func NewMigrator(config *config.Config) (*Migrator, error) {
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

	return &Migrator{
		config: config,
		conn:   db,
	}, nil
}

func (m *Migrator) MigrateFromFile(migrationPath string, apply bool) (*MigrationResult, error) {
	r, err := os.OpenRoot(".")
	if err != nil {
		return nil, err
	}

	b, err := r.ReadFile(migrationPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}

		return nil, err
	}

	var manifestActions map[string]api.ActionDefinition
	if err := json.Unmarshal(b, &manifestActions); err != nil {
		return nil, err
	}

	return m.Migrate(manifestActions, apply)
}

func (m *Migrator) Migrate(manifestActions map[string]api.ActionDefinition, apply bool) (*MigrationResult, error) {
	databaseActions, err := m.selectExistingActions()
	if err != nil {
		return nil, err
	}

	var toCreate []api.ActionDefinition
	var toUpdate []api.ActionDefinition

	for k, a := range manifestActions {
		a.ID = k
		if containsAction(a.ID, databaseActions) {
			existing := getAction(a.ID, databaseActions)
			if existing.Hash != a.Hash {
				toUpdate = append(toUpdate, a)
			}
		} else {
			toCreate = append(toCreate, a)
		}
	}

	for _, a := range toCreate {
		if err := m.insertAction(a); err != nil {
			log.WithFields(log.Fields{
				"error":  err,
				"action": a.ID,
			}).Error("unable to create action")
		}
	}

	for _, a := range toUpdate {
		if err := m.updateAction(a); err != nil {
			log.WithFields(log.Fields{
				"error":  err,
				"action": a.ID,
			}).Error("unable to update action")
		}
	}

	return nil, nil
}

func (m *Migrator) selectExistingActions() ([]api.ActionDefinition, error) {
	stmt, err := m.conn.PrepareNamed(`
		SELECT
		    id,
		    name,
		    action_type,
		    description,
		    icon,
		    ordering,
		    inputs, 
		    outputs,
		    hash,
		    author,
		    organisation,
		    website,
		    date,
		    verified
		FROM
		    actions
		ORDER BY 
		    id;
	`)
	if err != nil {
		return nil, err
	}

	var actions []api.ActionDefinition
	if err := stmt.Select(&actions, struct{}{}); err != nil {
		return nil, err
	}

	return actions, nil
}

func (m *Migrator) insertAction(action api.ActionDefinition) error {
	in, err := json.Marshal(action.Inputs)
	if err != nil {
		return err
	}

	action.Inputs = in

	out, err := json.Marshal(action.Outputs)
	if err != nil {
		return err
	}

	action.Outputs = out
	action.Verified = true
	action.Plugin = &action.ID

	stmt, err := m.conn.PrepareNamed(`
		INSERT INTO actions (
		    id,
			name,
			hash,
			author,
			organisation,
			description,
			website,
			icon,
		    plugin,
			date,
			action_type,
			verified,
			inputs,
			outputs
		) VALUES (
		    :id,
			:name,
			:hash,
			:author,
			:organisation,
			:description,
			:website,
			:icon,
		    :plugin,
			:date,
			:action_type,
			:verified,
			:inputs,
			:outputs
		)
	`)
	if err != nil {
		return err
	}

	_, err = stmt.Exec(action)
	if err != nil {
		return err
	}

	return nil
}

func (m *Migrator) updateAction(action api.ActionDefinition) error {
	in, err := json.Marshal(action.Inputs)
	if err != nil {
		return err
	}

	action.Inputs = in

	out, err := json.Marshal(action.Outputs)
	if err != nil {
		return err
	}

	action.Outputs = out
	action.Verified = true
	action.Plugin = &action.ID

	stmt, err := m.conn.PrepareNamed(`
		UPDATE 
		    actions
		SET
			name = :name,
			hash = :hash,
			author = :author,
			organisation = :organisation,
			description = :description,
			website = :website,
			icon = :icon,
		    plugin = :plugin,
			date = :date,
			action_type = :action_type,
			verified = :verified,
			inputs = :inputs,
			outputs = :outputs
		WHERE id = :id;
	`)
	if err != nil {
		return err
	}

	_, err = stmt.Exec(action)
	if err != nil {
		return err
	}

	return nil
}

func containsAction(id string, actions []api.ActionDefinition) bool {
	for _, a := range actions {
		if a.ID == id {
			return true
		}
	}
	return false
}

func getAction(id string, actions []api.ActionDefinition) *api.ActionDefinition {
	for _, a := range actions {
		if a.ID == id {
			return &a
		}
	}

	return nil
}
