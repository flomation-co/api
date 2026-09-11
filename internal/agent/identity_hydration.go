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
	"strings"

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

// identityChannelTypes returns the user_identity channel types to try
// for an inbound channel, in priority order.
//
// The transport name and the name people declare an identity under are
// not always the same word. A call or text arrives as "twilio", but the
// profile screen offers "Mobile" and "Phone" — there is no Twilio option,
// and there never was a `twilio` row in the table. Every inbound call
// therefore missed its owner and ran as an anonymous stub, with
// ${flow.identities} empty and the agent unable to recognise a caller it
// already knows by email and Slack.
//
// Mobile is tried first: an inbound call or text is far more often from
// a mobile than a landline, and a user who declared both gets the same
// account either way.
func identityChannelTypes(channelType string) []string {
	switch channelType {
	case "twilio":
		return []string{"mobile", "phone", "twilio"}
	default:
		return []string{channelType}
	}
}

// phoneVariants expands a phone number into the forms it may have been
// declared in. Only formatting is normalised — spaces, dashes, brackets
// and dots — plus the bare-digits form for a number stored without its
// leading "+".
//
// It deliberately does NOT convert between international and national
// form (+447926248382 ↔ 07926248382): that needs a country, and guessing
// one risks matching a different person's number in another country. A
// profile holding a national-format number still will not match, which
// is an argument for normalising to E.164 when the identity is saved.
func phoneVariants(externalID string) []string {
	cleaned := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "", "\u00a0", "").Replace(externalID)
	if cleaned == "" {
		return nil
	}
	variants := []string{}
	if cleaned != externalID {
		variants = append(variants, cleaned)
	}
	if digits := strings.TrimPrefix(cleaned, "+"); digits != cleaned {
		variants = append(variants, digits)
	} else {
		variants = append(variants, "+"+cleaned)
	}
	return variants
}

// lookupCandidates expands the caller-supplied identifiers into every
// form worth trying, preserving order and dropping duplicates. The first
// element stays the canonical one, so anonymous stubs keep keying on the
// identifier the caller chose.
func lookupCandidates(channelType string, externalIDs []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	for _, e := range externalIDs {
		add(e)
		if channelType == "twilio" {
			for _, v := range phoneVariants(e) {
				add(v)
			}
		}
	}
	return out
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
	// the canonical identifier for any anonymous-user creation — then
	// expand into the forms the identity may have been declared in.
	var supplied []string
	for _, e := range externalIDs {
		if e != "" {
			supplied = append(supplied, e)
		}
	}
	if len(supplied) == 0 {
		return nil, nil
	}
	candidates := lookupCandidates(channelType, supplied)

	// Try each candidate against declared user_identity rows. A hit on
	// any candidate wins — typical flow: a user declares "AndyEsser"
	// as their Telegram identity; webhook delivers numeric sender_id as
	// canonical + "AndyEsser" as alias; alias-match returns the
	// declared user.
	for _, lookupType := range identityChannelTypes(channelType) {
		for _, ext := range candidates {
			declared, err := p.LookupUserIdentityByChannel(organisationID, lookupType, ext)
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
	}

	// No declared match. Create anonymous stub keyed on the canonical
	// (first) candidate — the order chosen by the caller — so the stub
	// remains stable across username/handle changes.
	if organisationID == nil || *organisationID == "" {
		return nil, nil
	}
	anonID, err := p.UpsertAnonymousUser(*organisationID, channelType, supplied[0], displayName)
	if err != nil {
		return nil, err
	}
	if anonID == "" {
		return nil, nil
	}
	identities, _ := p.GetUserIdentitiesByUserAndOrg(anonID, organisationID)
	return &TriggeringUser{UserID: anonID, Identities: identities}, nil
}
