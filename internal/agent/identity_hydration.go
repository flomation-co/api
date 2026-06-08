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
// externalIDs accepts one or more candidate identifiers tried in order
// for the declared-identity lookup. The FIRST element is treated as
// the canonical/stable identifier — it's what gets stored when an
// anonymous stub user is created.
//
// The multi-candidate shape exists because some channels deliver more
// than one usable identifier per sender:
//   - Telegram: stable numeric sender_id (rarely seen by humans) +
//     friendly @username (what users put in their profile). Pass
//     [numericID, username] so a user who declared "AndyEsser" is
//     matched, and anonymous stubs key on the stable numeric.
//   - Slack/Teams/Twilio: a single identifier (Slack U-ID, AAD Object
//     ID, phone number) — pass it alone.
//
// Behaviour:
//
//   - No candidates (all empty) → returns (nil, nil). The flow still
//     runs; ${flow.identities} resolves to [].
//
//   - Any candidate matches a declared user_identity for organisationID
//     → returns the platform user_id + the user's full declared-identity
//     snapshot scoped to organisationID.
//
//   - No declared match + organisationID non-empty → upserts an anonymous
//     stub user keyed on (organisationID, channelType, canonical). The
//     partial unique index on
//     (organisation_id, channel_type, channel_external_id)
//     WHERE is_anonymous=true keeps anonymous users isolated per-org.
//
//   - No declared match + personal mode (organisationID nil or "") →
//     returns (nil, nil). Personal mode does not create anonymous stubs
//     (the users table CHECK constraint requires organisation_id when
//     is_anonymous=true). ${flow.identities} resolves to [] and the
//     flow still runs with raw sender info available in triggerData.
//
// channelType is expected to already be normalised (telegram_voice →
// telegram, etc.) by the caller.
func ResolveTriggeringUser(
	p IdentityPersistence,
	organisationID *string,
	channelType, displayName string,
	externalIDs ...string,
) (*TriggeringUser, error) {
	// Filter empties while preserving order — first non-empty becomes
	// the canonical identifier for any anonymous-user creation.
	var candidates []string
	for _, e := range externalIDs {
		if e != "" {
			candidates = append(candidates, e)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// Try each candidate against declared user_identity rows. A hit on
	// any candidate wins — typical flow: a user declares "AndyEsser"
	// as their Telegram identity; webhook delivers numeric sender_id as
	// canonical + "AndyEsser" as alias; alias-match returns the
	// declared user.
	for _, ext := range candidates {
		declared, err := p.LookupUserIdentityByChannel(organisationID, channelType, ext)
		if err != nil {
			// Treat lookup failure for this candidate as "no match";
			// continue trying the others.
			continue
		}
		if declared != nil {
			identities, _ := p.GetUserIdentitiesByUserAndOrg(declared.UserID, organisationID)
			return &TriggeringUser{UserID: declared.UserID, Identities: identities}, nil
		}
	}

	// No declared match. Create anonymous stub keyed on the canonical
	// (first) candidate — the order chosen by the caller — so the stub
	// remains stable across username/handle changes.
	if organisationID == nil || *organisationID == "" {
		return nil, nil
	}
	anonID, err := p.UpsertAnonymousUser(*organisationID, channelType, candidates[0], displayName)
	if err != nil {
		return nil, err
	}
	if anonID == "" {
		return nil, nil
	}
	identities, _ := p.GetUserIdentitiesByUserAndOrg(anonID, organisationID)
	return &TriggeringUser{UserID: anonID, Identities: identities}, nil
}
