// Package agent — identity_hydration.go
//
// Shared identity-resolution helper that powers both:
//   - The agent inbound pipeline (api/internal/agent/inbound.go)
//   - The standalone trigger dispatch path
//     (api/internal/http/trigger_dispatch.go)
//
// Previously this logic was inlined inside HandleInboundMessage, making
// ${flow.identities} an agent-only feature. Extracting it lets every
// channel-originated trigger fire — agent or standalone — hydrate the
// same identity context for its flow.

package agent

import (
	api "flomation.app/automate/api"
)

// IdentityPersistence is the narrow persistence surface ResolveTriggeringUser
// requires. Implemented by the full Persistence interface in both the
// agent and http packages.
type IdentityPersistence interface {
	LookupUserIdentityByChannel(organisationID *string, channelType, externalID string) (*api.UserIdentity, error)
	UpsertAnonymousUser(organisationID, channelType, externalID, displayName string) (string, error)
	GetUserIdentitiesByUserAndOrg(userID string, organisationID *string) ([]*api.UserIdentity, error)
}

// TriggeringUser is the resolved identity behind a channel-originated
// trigger fire. UserID is the platform users.id (either a declared
// user's ID or an anonymous stub user's ID). Identities is the snapshot
// of the user's declared identities scoped to organisationID — empty
// for anonymous users and for personal-mode triggers with undeclared
// senders.
type TriggeringUser struct {
	UserID     string
	Identities []*api.UserIdentity
}

// ResolveTriggeringUser is the single source of truth for resolving the
// platform user behind an inbound channel event.
//
// Behaviour:
//
//   - externalID empty → returns (nil, nil). No user, no identities.
//     The flow still runs; ${flow.identities} resolves to [].
//
//   - Declared identity exists for (organisationID, channelType, externalID)
//     → returns the platform user_id + the user's full declared-identity
//     snapshot scoped to organisationID.
//
//   - Undeclared + organisationID non-empty → upserts an anonymous stub
//     user keyed on (organisationID, channelType, externalID) and returns
//     its ID with an empty identities slice. The partial unique index
//     on (organisation_id, channel_type, channel_external_id)
//     WHERE is_anonymous=true keeps anonymous users isolated per-org.
//
//   - Undeclared + personal mode (organisationID nil or "") → returns
//     (nil, nil). Personal mode does not create anonymous stubs (the
//     users table CHECK constraint requires organisation_id when
//     is_anonymous=true). ${flow.identities} resolves to [] and the
//     flow still runs with raw sender info available in triggerData.
//
// channelType is expected to already be normalised (telegram_voice →
// telegram, etc.) by the caller via normaliseChannelType, since the
// normaliser lives in inbound.go and not all callers want the dep.
func ResolveTriggeringUser(
	p IdentityPersistence,
	organisationID *string,
	channelType, externalID, displayName string,
) (*TriggeringUser, error) {
	if externalID == "" {
		return nil, nil
	}

	declared, err := p.LookupUserIdentityByChannel(organisationID, channelType, externalID)
	if err != nil {
		// Treat a lookup failure as "no declared identity" rather than
		// failing the trigger entirely — the caller logs the warning.
		declared = nil
	}

	var userID string
	if declared != nil {
		userID = declared.UserID
	} else if organisationID != nil && *organisationID != "" {
		anonID, upsertErr := p.UpsertAnonymousUser(*organisationID, channelType, externalID, displayName)
		if upsertErr != nil {
			return nil, upsertErr
		}
		userID = anonID
	}

	if userID == "" {
		return nil, nil
	}

	identities, err := p.GetUserIdentitiesByUserAndOrg(userID, organisationID)
	if err != nil {
		// Identity fetch is best-effort — return the user_id alone.
		return &TriggeringUser{UserID: userID, Identities: nil}, nil
	}
	return &TriggeringUser{UserID: userID, Identities: identities}, nil
}
