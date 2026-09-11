package persistence

import (
	"database/sql"
	"errors"
	"fmt"

	"flomation.app/automate/api"
	"github.com/jmoiron/sqlx"
	log "github.com/sirupsen/logrus"
)

// ErrProjectCycle is returned when a move would put a project underneath one of
// its own descendants (which would create a cycle in the tree).
var ErrProjectCycle = errors.New("cannot move a project under itself or one of its descendants")

// projectScope appends the owner/organisation scoping clause used by every
// project query. Org mode filters by organisation_id; personal mode filters by
// owner_id with a NULL organisation_id (mirrors the environment table scoping).
// Placeholders start at the given index; returns the clause and the args.
func projectScope(startIdx int, ownerID string, orgID *string) (string, []interface{}) {
	if orgID != nil && *orgID != "" {
		return fmt.Sprintf("organisation_id = $%d", startIdx), []interface{}{*orgID}
	}
	return fmt.Sprintf("owner_id = $%d AND organisation_id IS NULL", startIdx), []interface{}{ownerID}
}

// CreateProject inserts a new project and returns its id.
func (s *Service) CreateProject(p api.Project) (*string, error) {
	var id string
	err := s.conn.Get(&id, `
		INSERT INTO project (name, description, parent_id, organisation_id, owner_id)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, p.Name, p.Description, p.ParentID, p.OrganisationID, p.OwnerID)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// GetProjectByID fetches a single (non-archived) project. Returns (nil, nil)
// when not found so callers can 404 cleanly.
func (s *Service) GetProjectByID(id string) (*api.Project, error) {
	var p api.Project
	err := s.conn.Get(&p, `
		SELECT id, name, description, parent_id, organisation_id, owner_id, created_at
		FROM project
		WHERE id = $1 AND archived_at IS NULL
	`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetProjects returns every non-archived project the user may access, each
// carrying a direct-flow count and its restricted/effective-role verdict.
// Restricted projects the user has no grant on (and isn't admin/owner of) are
// omitted entirely. The caller assembles the tree from parent_id.
func (s *Service) GetProjects(ownerID string, orgID *string, isAdmin bool) ([]*api.Project, error) {
	scope, args := projectScope(1, ownerID, orgID)
	query := `
		SELECT
		    p.id, p.name, p.description, p.parent_id, p.organisation_id, p.owner_id, p.created_at,
		    (SELECT COUNT(1) FROM flo f WHERE f.project_id = p.id AND f.archived_at IS NULL AND f.system_flow = FALSE) AS flow_count
		FROM project p
		WHERE p.archived_at IS NULL AND ` + scope + `
		ORDER BY p.name ASC`

	var results []*api.Project
	if err := s.conn.Select(&results, query, args...); err != nil {
		return nil, err
	}

	access, err := s.GetProjectAccess(ownerID, orgID, isAdmin)
	if err != nil {
		return nil, err
	}

	filtered := make([]*api.Project, 0, len(results))
	for _, p := range results {
		a := access[p.ID]
		if !a.Accessible {
			continue
		}
		p.Restricted = a.Restricted
		p.EffectiveRole = a.Role
		filtered = append(filtered, p)
	}
	return filtered, nil
}

// projectDescendantIDs returns the id plus all transitive descendant ids of a
// project, used for cycle detection on move and cascade on archive.
func (s *Service) projectDescendantIDs(id string) ([]string, error) {
	var ids []string
	err := s.conn.Select(&ids, `
		WITH RECURSIVE tree AS (
		    SELECT id FROM project WHERE id = $1
		    UNION ALL
		    SELECT p.id FROM project p JOIN tree t ON p.parent_id = t.id
		)
		SELECT id FROM tree
	`, id)
	return ids, err
}

// UpdateProject renames/re-describes/moves a project. Moving under one of its
// own descendants is rejected to keep the tree acyclic.
func (s *Service) UpdateProject(id, name string, description *string, parentID *string) error {
	if parentID != nil {
		if *parentID == id {
			return ErrProjectCycle
		}
		descendants, err := s.projectDescendantIDs(id)
		if err != nil {
			return err
		}
		for _, d := range descendants {
			if d == *parentID {
				return ErrProjectCycle
			}
		}
	}
	_, err := s.conn.Exec(`
		UPDATE project SET name = $1, description = $2, parent_id = $3
		WHERE id = $4 AND archived_at IS NULL
	`, name, description, parentID, id)
	return err
}

// ArchiveProject soft-deletes a project, reparenting its direct child projects
// and flows up to its own parent (NULL if it was top-level) so nothing is
// orphaned or accidentally hidden. Runs in one transaction.
func (s *Service) ArchiveProject(id string) error {
	var parentID *string
	if err := s.conn.Get(&parentID, `SELECT parent_id FROM project WHERE id = $1`, id); err != nil {
		return err
	}

	tx, err := s.conn.Beginx()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`UPDATE project SET parent_id = $1 WHERE parent_id = $2`, parentID, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE flo SET project_id = $1 WHERE project_id = $2`, parentID, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE project SET archived_at = NOW() WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// MoveFlosToProject assigns a set of flows to a project (or ungroups them when
// projectID is nil). Scoped to the caller's org/owner so a user cannot move
// flows they can't otherwise see. (The flo table's author column is author_id,
// not owner_id.)
func (s *Service) MoveFlosToProject(floIDs []string, projectID *string, ownerID string, orgID *string) error {
	if len(floIDs) == 0 {
		return nil
	}

	scope := "author_id = ? AND organisation_id IS NULL"
	var scopeArg interface{} = ownerID
	if orgID != nil && *orgID != "" {
		scope = "organisation_id = ?"
		scopeArg = *orgID
	}

	query := "UPDATE flo SET project_id = ? WHERE " + scope + " AND id IN (?)"
	query, args, err := sqlx.In(query, projectID, scopeArg, floIDs)
	if err != nil {
		return err
	}
	query = s.conn.Rebind(query)
	_, err = s.conn.Exec(query, args...)
	return err
}

// GetProjectFlos returns fully-enriched flows within a project (projectID set)
// or ungrouped flows (projectID nil), scoped to the caller and paginated.
func (s *Service) GetProjectFlos(userID string, orgID *string, projectID *string, offset, limit int64, search string) ([]*api.Flo, int64, error) {
	var where string
	args := []interface{}{}
	idx := 1

	if orgID != nil && *orgID != "" {
		where = fmt.Sprintf("f.organisation_id = $%d", idx)
		args = append(args, *orgID)
	} else {
		where = fmt.Sprintf("f.author_id = $%d AND f.organisation_id IS NULL", idx)
		args = append(args, userID)
	}
	idx++

	if projectID != nil {
		where += fmt.Sprintf(" AND f.project_id = $%d", idx)
		args = append(args, *projectID)
		idx++
	} else {
		where += " AND f.project_id IS NULL"
	}

	if search != "" {
		where += fmt.Sprintf(" AND (LOWER(f.name) LIKE LOWER($%d) OR CAST(f.id AS TEXT) LIKE LOWER($%d))", idx, idx)
		args = append(args, "%"+search+"%")
		idx++
	}

	base := `
		SELECT
		    f.id, f.name, f.organisation_id, f.author_id, f.created_at,
		    f.scale, f.x, f.y, f.environment_id, f.queue_id, f.project_id,
		    (SELECT name FROM environment e WHERE e.id = f.environment_id) AS environment_name,
		    (SELECT COUNT(1) FROM execution e WHERE e.flo_id = f.id) AS execution_count,
		    (SELECT CASE WHEN e.completed_at IS NULL THEN CEIL(EXTRACT(EPOCH FROM CURRENT_TIMESTAMP - e.created_at) / 60) ELSE CEIL(EXTRACT(EPOCH FROM e.completed_at - e.created_at) / 60) END FROM execution e WHERE e.flo_id = f.id ORDER BY created_at DESC LIMIT 1) AS duration,
		    (SELECT e.created_at FROM execution e WHERE e.flo_id = f.id ORDER BY created_at DESC LIMIT 1) AS last_run
		FROM flo f
		WHERE ` + where + ` AND f.archived_at IS NULL AND f.system_flow = FALSE
		ORDER BY f.created_at DESC
		OFFSET $%d LIMIT $%d`

	query := fmt.Sprintf(base, idx, idx+1)
	args = append(args, offset, limit)

	var results []*api.Flo
	if err := s.conn.Select(&results, query, args...); err != nil {
		return nil, 0, err
	}

	countQuery := "SELECT COUNT(1) FROM flo f WHERE " + where + " AND f.archived_at IS NULL AND f.system_flow = FALSE"
	var count int64
	if err := s.conn.Get(&count, countQuery, args[:idx-1]...); err != nil {
		return nil, 0, err
	}

	s.enrichFlos(results)
	return results, count, nil
}

// enrichFlos populates triggers, last execution and recent execution status
// for each flo, reusing the prepared statements the flat flow list uses.
func (s *Service) enrichFlos(results []*api.Flo) {
	for idx, r := range results {
		var triggers []*api.Trigger
		if err := s.stmtGetFloTriggers.Select(&triggers, struct {
			FloID string `db:"id"`
		}{FloID: r.ID}); err != nil {
			log.WithFields(log.Fields{"error": err}).Error("unable to get flo triggers")
		}
		results[idx].Triggers = triggers

		var execution api.Execution
		if err := s.stmtGetLatestExecutionForFlo.Get(&execution, struct {
			FloID string `db:"flo_id"`
		}{FloID: r.ID}); err != nil {
			if err != sql.ErrNoRows {
				log.WithFields(log.Fields{"error": err}).Error("unable to get flo execution")
			}
		}
		if execution.FloID == r.ID {
			results[idx].LastExecution = &execution
		}

		var recentExecs []api.ExecutionStatus
		if err := s.stmtGetRecentExecutionsForFlo.Select(&recentExecs, struct {
			FloID string `db:"flo_id"`
		}{FloID: r.ID}); err != nil {
			if err != sql.ErrNoRows {
				log.WithFields(log.Fields{"error": err}).Error("unable to get recent executions")
			}
		}
		if len(recentExecs) > 0 {
			results[idx].RecentExecutions = recentExecs
		}
	}
}
