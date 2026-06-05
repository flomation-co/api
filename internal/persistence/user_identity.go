package persistence

import (
	"database/sql"
	"errors"

	api "flomation.app/automate/api"
)

// CreateUserIdentity declares a new channel identity for a user in a
// specific organisation. Returns ErrUserIdentityExists when the
// (user_id, organisation_id, channel_type, external_id) tuple already
// exists — the caller can treat this as idempotent if they choose.
func (s *Service) CreateUserIdentity(in api.CreateUserIdentity) (*api.UserIdentity, error) {
	var out api.UserIdentity
	err := s.stmtCreateUserIdentity.Get(&out, in)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetUserIdentitiesByUserID returns every declared identity for a user
// across all organisations. The editor profile UI calls this with the
// authenticated user's id to render the per-org grouping.
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

// GetUserIdentitiesByUserAndOrg is the scope the executor uses when
// populating ${flow.identities}: just the identities relevant to the
// org the agent is running in.
func (s *Service) GetUserIdentitiesByUserAndOrg(userID, organisationID string) ([]*api.UserIdentity, error) {
	var out []*api.UserIdentity
	err := s.stmtGetUserIdentitiesByUserAndOrg.Select(&out, struct {
		UserID         string `db:"user_id"`
		OrganisationID string `db:"organisation_id"`
	}{UserID: userID, OrganisationID: organisationID})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// LookupUserIdentityByChannel resolves an incoming webhook sender:
// given the receiving organisation, channel type, and external ID, it
// returns the declared owner's user_id if any user has claimed it.
// Returns (nil, nil) when no declaration matches — webhook ingestion
// then falls through to UpsertAnonymousUser.
func (s *Service) LookupUserIdentityByChannel(organisationID, channelType, externalID string) (*api.UserIdentity, error) {
	var out api.UserIdentity
	err := s.stmtLookupUserIdentityByChannel.Get(&out, struct {
		OrganisationID string `db:"organisation_id"`
		ChannelType    string `db:"channel_type"`
		ExternalID     string `db:"external_id"`
	}{OrganisationID: organisationID, ChannelType: channelType, ExternalID: externalID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteUserIdentity removes a single declaration. Profile UI uses this
// to let users revoke a channel-identity association.
func (s *Service) DeleteUserIdentity(userID, organisationID, channelType, externalID string) error {
	_, err := s.stmtDeleteUserIdentity.Exec(struct {
		UserID         string `db:"user_id"`
		OrganisationID string `db:"organisation_id"`
		ChannelType    string `db:"channel_type"`
		ExternalID     string `db:"external_id"`
	}{
		UserID:         userID,
		OrganisationID: organisationID,
		ChannelType:    channelType,
		ExternalID:     externalID,
	})
	return err
}

// UpsertAnonymousUser creates (or fetches) a stub user row for an
// unrecognised channel identity in a specific organisation. The same
// channel identity arriving at two different orgs produces two distinct
// anonymous users — that's enforced by the partial unique index on
// (organisation_id, channel_type, channel_external_id) WHERE is_anonymous.
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
