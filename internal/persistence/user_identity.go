package persistence

import (
	"database/sql"
	"errors"

	api "flomation.app/automate/api"
)

// CreateUserIdentity declares a new channel identity for a user in a
// specific organisation. OrganisationID may be nil for personal-mode
// declarations (migration 84). Returns a SQL unique-violation error when
// the (user, org-or-personal, channel, external) tuple already exists.
func (s *Service) CreateUserIdentity(in api.CreateUserIdentity) (*api.UserIdentity, error) {
	var out api.UserIdentity
	err := s.stmtCreateUserIdentity.Get(&out, in)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetUserIdentitiesByUserID returns every declared identity for a user
// across all organisations AND their personal-mode declarations. The
// editor profile UI calls this with the authenticated user's id; the
// caller filters by organisation in memory.
func (s *Service) GetUserIdentitiesByUserID(userID string) ([]*api.UserIdentity, error) {
	var out []*api.UserIdentity
	err := s.stmtGetUserIdentitiesByUserID.Select(&out, struct {
		UserID string `db:"user_id"`
	}{UserID: userID})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// orgStringFromPointer normalises a nullable org pointer to the empty
// string used by the user_identity statements' NULLIF(:organisation_id, '')
// expression. nil → "" → NULLIF → SQL NULL; non-nil → the UUID string →
// NULLIF leaves it alone → SQL NULL only when the pointer was nil.
func orgStringFromPointer(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// GetUserIdentitiesByUserAndOrg is the scope the executor uses when
// populating ${flow.identities}: just the identities relevant to the
// org the agent is running in. organisationID == nil means personal
// mode — matches rows where organisation_id IS NULL.
func (s *Service) GetUserIdentitiesByUserAndOrg(userID string, organisationID *string) ([]*api.UserIdentity, error) {
	var out []*api.UserIdentity
	err := s.stmtGetUserIdentitiesByUserAndOrg.Select(&out, struct {
		UserID         string `db:"user_id"`
		OrganisationID string `db:"organisation_id"`
	}{UserID: userID, OrganisationID: orgStringFromPointer(organisationID)})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// LookupUserIdentityByChannel resolves an incoming webhook sender:
// given the receiving organisation (nil for personal mode), channel
// type, and external ID, it returns the declared owner's user_id if any
// user has claimed it. Returns (nil, nil) when no declaration matches.
func (s *Service) LookupUserIdentityByChannel(organisationID *string, channelType, externalID string) (*api.UserIdentity, error) {
	var out api.UserIdentity
	err := s.stmtLookupUserIdentityByChannel.Get(&out, struct {
		OrganisationID string `db:"organisation_id"`
		ChannelType    string `db:"channel_type"`
		ExternalID     string `db:"external_id"`
	}{OrganisationID: orgStringFromPointer(organisationID), ChannelType: channelType, ExternalID: externalID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteUserIdentity removes a single declaration. organisationID == nil
// matches a personal-mode row (WHERE organisation_id IS NULL via the
// statement's IS NOT DISTINCT FROM). Returns the rows-affected count so
// the caller can surface 404 when the requested row didn't exist —
// without that check, a missed match returns success and the editor
// shows a green toast for a no-op.
func (s *Service) DeleteUserIdentity(userID string, organisationID *string, channelType, externalID string) (int64, error) {
	res, err := s.stmtDeleteUserIdentity.Exec(struct {
		UserID         string `db:"user_id"`
		OrganisationID string `db:"organisation_id"`
		ChannelType    string `db:"channel_type"`
		ExternalID     string `db:"external_id"`
	}{
		UserID:         userID,
		OrganisationID: orgStringFromPointer(organisationID),
		ChannelType:    channelType,
		ExternalID:     externalID,
	})
	if err != nil {
		return 0, err
	}
	n, raErr := res.RowsAffected()
	if raErr != nil {
		// Driver doesn't support RowsAffected — treat as success.
		return 1, nil
	}
	return n, nil
}

// UpsertAnonymousUser creates (or fetches) a stub user row for an
// unrecognised channel identity in a specific organisation. The same
// channel identity arriving at two different orgs produces two distinct
// anonymous users — enforced by the partial unique index on
// (organisation_id, channel_type, channel_external_id) WHERE is_anonymous.
// Personal-mode (no org) does NOT support anonymous users — the CHECK
// constraint on users requires organisation_id when is_anonymous = true.
func (s *Service) UpsertAnonymousUser(organisationID, channelType, externalID, displayName string) (string, error) {
	var id string
	err := s.stmtUpsertAnonymousUser.Get(&id, struct {
		OrganisationID    string `db:"organisation_id"`
		ChannelType       string `db:"channel_type"`
		ChannelExternalID string `db:"channel_external_id"`
		Name              string `db:"name"`
	}{
		OrganisationID:    organisationID,
		ChannelType:       channelType,
		ChannelExternalID: externalID,
		Name:              displayName,
	})
	return id, err
}
