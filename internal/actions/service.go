package actions

import (
	"encoding/json"
	"errors"
	"flomation.app/automate/api/internal/config"
	"fmt"
	"github.com/jmoiron/sqlx"
	"os"
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

type ActionDefinition struct {
	ID           string      `json:"id" db:"id"`
	Name         string      `json:"name" db:"name"`
	Hash         *string     `json:"hash" db:"hash"`
	Author       *string     `json:"author" db:"author"`
	Organisation *string     `json:"organisation" db:"organisation"`
	Description  *string     `json:"description" db:"description"`
	Website      *string     `json:"website" db:"website"`
	Icon         string      `json:"icon" db:"icon"`
	Date         *string     `json:"date" db:"date"`
	Type         int64       `json:"action_type" db:"action_type"`
	Ordering     int64       `json:"order" db:"ordering"`
	Verified     bool        `json:"verified" db:"verified"`
	Inputs       interface{} `json:"inputs" db:"inputs"`
	Outputs      interface{} `json:"outputs" db:"outputs"`
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

func (m *Migrator) Migrate(migrationPath string, apply bool) (*MigrationResult, error) {
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

	var manifestActions map[string]ActionDefinition
	if err := json.Unmarshal(b, &manifestActions); err != nil {
		return nil, err
	}

	fmt.Printf("Manifest Actions\n----------\n")
	for k, a := range manifestActions {
		a.ID = k
		fmt.Printf("\t%v (%v)\n", a.Name, a.ID)
	}

	databaseActions, err := m.selectExistingActions()
	if err != nil {
		return nil, err
	}

	fmt.Printf("\nDatabase Actions\n----------\n")
	for _, a := range databaseActions {
		fmt.Printf("\t%v (%v)\n", a.Name, a.ID)
	}

	return nil, nil
}

func (m *Migrator) selectExistingActions() ([]ActionDefinition, error) {
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
		WHERE
		    action_type = '2'
		ORDER BY 
		    id;
	`)
	if err != nil {
		return nil, err
	}

	var actions []ActionDefinition
	if err := stmt.Select(&actions, struct{}{}); err != nil {
		return nil, err
	}

	return actions, nil
}
