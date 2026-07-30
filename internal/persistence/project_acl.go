package persistence

import (
	"flomation.app/automate/api"
)

// ProjectAccess is the computed access verdict for one project.
type ProjectAccess struct {
	Accessible bool
	Restricted bool
	Role       string // view | edit | manage ("" when inaccessible)
}

// projectRow / projectGrant are the minimal shapes the accessibility computation
// needs. Kept unexported — the pure computeProjectAccess function operates on
// them so it can be unit-tested without a database.
type projectRow struct {
	ID       string  `db:"id"`
	ParentID *string `db:"parent_id"`
	OwnerID  *string `db:"owner_id"`
}

type projectGrant struct {
	ProjectID string `db:"project_id"`
	GroupID   string `db:"group_id"`
	Role      string `db:"role"`
}

func roleRank(role string) int {
	switch role {
	case "manage":
		return 3
	case "edit":
		return 2
	case "view":
		return 1
	default:
		return 0
	}
}

func roleName(rank int) string {
	switch rank {
	case 3:
		return "manage"
	case 2:
		return "edit"
	case 1:
		return "view"
	default:
		return ""
	}
}

// computeProjectAccess is the pure heart of per-project RBAC. For each project it
// unions the grants on the project and ALL its ancestors (grants inherit down):
//   - no effective grant  → open: accessible to everyone (role "view")
//   - has effective grant → restricted: accessible only to members of a granted
//     team (role = the highest granted role across the chain for the user's teams)
//
// Org admins and the project owner always get full "manage" access. Kept free of
// any DB dependency so it is directly unit-testable.
func computeProjectAccess(rows []projectRow, grants []projectGrant, userGroups map[string]bool, userID string, isAdmin bool) map[string]ProjectAccess {
	parent := make(map[string]*string, len(rows))
	owner := make(map[string]*string, len(rows))
	for _, r := range rows {
		parent[r.ID] = r.ParentID
		owner[r.ID] = r.OwnerID
	}
	grantsByProject := make(map[string][]projectGrant)
	for _, g := range grants {
		grantsByProject[g.ProjectID] = append(grantsByProject[g.ProjectID], g)
	}

	out := make(map[string]ProjectAccess, len(rows))
	for _, r := range rows {
		// Walk self → ancestors, unioning grants. seen guards against a cycle
		// (which the API prevents on move, but defence in depth is cheap).
		var effective []projectGrant
		seen := map[string]bool{}
		cur := &r.ID
		for cur != nil && *cur != "" && !seen[*cur] {
			seen[*cur] = true
			effective = append(effective, grantsByProject[*cur]...)
			cur = parent[*cur]
		}

		restricted := len(effective) > 0

		// Admin / owner → full access regardless of grants.
		if isAdmin || (owner[r.ID] != nil && *owner[r.ID] == userID) {
			out[r.ID] = ProjectAccess{Accessible: true, Restricted: restricted, Role: "manage"}
			continue
		}

		if !restricted {
			// Open project — visible to everyone (org-level ProjectView gates the
			// feature; access here is "view").
			out[r.ID] = ProjectAccess{Accessible: true, Restricted: false, Role: "view"}
			continue
		}

		// Restricted — accessible iff the user is in a granted team.
		best := 0
		for _, g := range effective {
			if userGroups[g.GroupID] {
				if rk := roleRank(g.Role); rk > best {
					best = rk
				}
			}
		}
		out[r.ID] = ProjectAccess{Accessible: best > 0, Restricted: true, Role: roleName(best)}
	}
	return out
}

// GetProjectAccess loads the scoped projects, their grants and the user's team
// memberships, then computes the access verdict for every project in scope.
func (s *Service) GetProjectAccess(userID string, orgID *string, isAdmin bool) (map[string]ProjectAccess, error) {
	scope, args := projectScope(1, userID, orgID)

	var rows []projectRow
	if err := s.conn.Select(&rows, `SELECT id, parent_id, owner_id FROM project WHERE archived_at IS NULL AND `+scope, args...); err != nil {
		return nil, err
	}

	var grants []projectGrant
	if err := s.conn.Select(&grants, `
		SELECT pg.project_id, pg.group_id, pg.role
		FROM project_group pg
		JOIN project p ON p.id = pg.project_id
		WHERE p.archived_at IS NULL AND p.`+scope, args...); err != nil {
		return nil, err
	}

	userGroups := map[string]bool{}
	if orgID != nil && *orgID != "" {
		ids, err := s.GetUserGroupIDs(*orgID, userID)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			userGroups[id] = true
		}
	}

	return computeProjectAccess(rows, grants, userGroups, userID, isAdmin), nil
}

// GetUserGroupIDs returns the ids of the Teams (organisation_group) the user
// belongs to within the org.
func (s *Service) GetUserGroupIDs(orgID, userID string) ([]string, error) {
	var ids []string
	err := s.conn.Select(&ids, `
		SELECT gm.group_id
		FROM organisation_group_member gm
		JOIN organisation_group g ON g.id = gm.group_id
		WHERE g.organisation_id = $1 AND gm.user_id = $2
	`, orgID, userID)
	return ids, err
}

// GetProjectACL returns the direct grants on a project plus the inherited grants
// (those on its ancestors), each as a Team id + role. Inherited entries let the
// UI show why a project is restricted even when the grant lives on a parent.
func (s *Service) GetProjectACL(projectID string) (direct []api.ProjectGrant, inherited []api.ProjectGrant, err error) {
	if err = s.conn.Select(&direct, `
		SELECT pg.group_id, g.name AS group_name, pg.role
		FROM project_group pg
		JOIN organisation_group g ON g.id = pg.group_id
		WHERE pg.project_id = $1
		ORDER BY g.name
	`, projectID); err != nil {
		return nil, nil, err
	}

	// Inherited = grants on strict ancestors of this project.
	if err = s.conn.Select(&inherited, `
		WITH RECURSIVE ancestors AS (
		    SELECT parent_id FROM project WHERE id = $1
		    UNION ALL
		    SELECT p.parent_id FROM project p JOIN ancestors a ON p.id = a.parent_id
		)
		SELECT pg.group_id, g.name AS group_name, pg.role
		FROM project_group pg
		JOIN organisation_group g ON g.id = pg.group_id
		WHERE pg.project_id IN (SELECT parent_id FROM ancestors WHERE parent_id IS NOT NULL)
		ORDER BY g.name
	`, projectID); err != nil {
		return nil, nil, err
	}
	return direct, inherited, nil
}

// SetProjectGroupRole grants (or updates) a Team's role on a project.
func (s *Service) SetProjectGroupRole(projectID, groupID, role string) error {
	_, err := s.conn.Exec(`
		INSERT INTO project_group (project_id, group_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (project_id, group_id) DO UPDATE SET role = EXCLUDED.role
	`, projectID, groupID, role)
	return err
}

// RemoveProjectGroup revokes a Team's direct grant on a project.
func (s *Service) RemoveProjectGroup(projectID, groupID string) error {
	_, err := s.conn.Exec(`DELETE FROM project_group WHERE project_id = $1 AND group_id = $2`, projectID, groupID)
	return err
}
