package persistence

import "flomation.app/automate/api"

// GetSSOGroupMappings lists an org's IdP-group → Team mappings (with Team names).
func (s *Service) GetSSOGroupMappings(orgID string) ([]*api.SSOGroupMapping, error) {
	var out []*api.SSOGroupMapping
	err := s.conn.Select(&out, `
		SELECT m.id, m.organisation_id, m.idp_group, m.organisation_group_id, g.name AS group_name
		FROM sso_group_mapping m
		JOIN organisation_group g ON g.id = m.organisation_group_id
		WHERE m.organisation_id = $1
		ORDER BY m.idp_group, g.name
	`, orgID)
	return out, err
}

// CreateSSOGroupMapping adds a mapping (idempotent on the unique triple).
func (s *Service) CreateSSOGroupMapping(orgID, idpGroup, groupID string) error {
	_, err := s.conn.Exec(`
		INSERT INTO sso_group_mapping (organisation_id, idp_group, organisation_group_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (organisation_id, idp_group, organisation_group_id) DO NOTHING
	`, orgID, idpGroup, groupID)
	return err
}

// DeleteSSOGroupMapping removes a mapping by id (org-scoped).
func (s *Service) DeleteSSOGroupMapping(id, orgID string) error {
	_, err := s.conn.Exec(`DELETE FROM sso_group_mapping WHERE id=$1 AND organisation_id=$2`, id, orgID)
	return err
}

// ReconcileSSOGroups syncs a user's Team memberships from their IdP groups.
//
// Only Teams that appear as a mapping target ("SSO-managed") are touched: the
// user is added to those their IdP groups map to, and removed from those they no
// longer map to. Teams in no mapping (manually managed) are left alone — so
// SSO-driven and hand-assigned membership coexist.
func (s *Service) ReconcileSSOGroups(orgID, userID string, idpGroups []string) error {
	mappings, err := s.GetSSOGroupMappings(orgID)
	if err != nil {
		return err
	}
	if len(mappings) == 0 {
		return nil
	}
	current, err := s.GetUserGroupIDs(orgID, userID)
	if err != nil {
		return err
	}

	add, remove := ssoGroupReconcileDecision(mappings, idpGroups, current)
	for _, teamID := range add {
		if err := s.AddUserToGroup(teamID, userID); err != nil {
			return err
		}
	}
	for _, teamID := range remove {
		if err := s.RemoveUserFromGroup(teamID, userID); err != nil {
			return err
		}
	}
	return nil
}

// ssoGroupReconcileDecision is the pure heart of group→Team sync. Given the org's
// mappings, the user's IdP groups and their current Team ids, it returns the
// Teams to add to and remove from — considering ONLY Teams that are a mapping
// target ("SSO-managed"). Teams in no mapping are never touched.
func ssoGroupReconcileDecision(mappings []*api.SSOGroupMapping, idpGroups, current []string) (add, remove []string) {
	idpSet := make(map[string]bool, len(idpGroups))
	for _, g := range idpGroups {
		idpSet[g] = true
	}
	managed := map[string]bool{} // every Team that is a mapping target
	target := map[string]bool{}  // Teams the user qualifies for via their IdP groups
	for _, m := range mappings {
		managed[m.OrganisationGroupID] = true
		if idpSet[m.IDPGroup] {
			target[m.OrganisationGroupID] = true
		}
	}
	currentSet := make(map[string]bool, len(current))
	for _, g := range current {
		currentSet[g] = true
	}
	for teamID := range managed {
		switch {
		case target[teamID] && !currentSet[teamID]:
			add = append(add, teamID)
		case !target[teamID] && currentSet[teamID]:
			remove = append(remove, teamID)
		}
	}
	return add, remove
}
