package agent

import (
	"testing"

	api "flomation.app/automate/api"
	"github.com/onsi/gomega"
)

// stubIdentityPersistence answers lookups from a fixed table of declared
// identities keyed by "channelType|externalID".
type stubIdentityPersistence struct {
	declared   map[string]string // key → user id
	identities map[string][]*api.UserIdentity
	lookups    []string
	anonCalls  []string
	anonID     string
}

func (s *stubIdentityPersistence) LookupUserIdentityByChannel(_ *string, channelType, externalID string) (*api.UserIdentity, error) {
	key := channelType + "|" + externalID
	s.lookups = append(s.lookups, key)
	if userID, ok := s.declared[key]; ok {
		return &api.UserIdentity{UserID: userID, ChannelType: channelType, ExternalID: externalID}, nil
	}
	return nil, nil
}

func (s *stubIdentityPersistence) UpsertAnonymousUser(_, channelType, externalID, _ string) (string, error) {
	s.anonCalls = append(s.anonCalls, channelType+"|"+externalID)
	return s.anonID, nil
}

func (s *stubIdentityPersistence) GetUserIdentitiesByUserAndOrg(userID string, _ *string) ([]*api.UserIdentity, error) {
	return s.identities[userID], nil
}

func org() *string {
	id := "org-1"
	return &id
}

// The live failure: the number is declared as "mobile" on the profile
// screen, the call arrives as "twilio", and nothing bridged the two — so
// every caller ran as an anonymous stub with ${flow.identities} empty.
func TestTwilioCallMatchesAMobileIdentity(t *testing.T) {
	gomega.RegisterTestingT(t)

	p := &stubIdentityPersistence{
		declared:   map[string]string{"mobile|+447926248382": "user-andy"},
		identities: map[string][]*api.UserIdentity{"user-andy": {{UserID: "user-andy", ChannelType: "mobile", ExternalID: "+447926248382"}}},
	}

	tu, err := ResolveTriggeringUser(p, org(), "twilio", "+447926248382", "+447926248382")

	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	gomega.Expect(tu).ToNot(gomega.BeNil())
	gomega.Expect(tu.UserID).To(gomega.Equal("user-andy"))
	gomega.Expect(tu.Identities).To(gomega.HaveLen(1))
	gomega.Expect(p.anonCalls).To(gomega.BeEmpty(), "a declared caller must never get an anonymous stub")
}

func TestTwilioCallMatchesAPhoneIdentity(t *testing.T) {
	gomega.RegisterTestingT(t)

	p := &stubIdentityPersistence{declared: map[string]string{"phone|+442079460000": "user-desk"}}

	tu, err := ResolveTriggeringUser(p, org(), "twilio", "+442079460000", "+442079460000")

	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	gomega.Expect(tu.UserID).To(gomega.Equal("user-desk"))
}

func TestTwilioTriesMobileBeforePhone(t *testing.T) {
	gomega.RegisterTestingT(t)

	p := &stubIdentityPersistence{declared: map[string]string{
		"mobile|+447926248382": "user-mobile",
		"phone|+447926248382":  "user-landline",
	}}

	tu, _ := ResolveTriggeringUser(p, org(), "twilio", "x", "+447926248382")

	gomega.Expect(tu.UserID).To(gomega.Equal("user-mobile"))
	gomega.Expect(p.lookups[0]).To(gomega.Equal("mobile|+447926248382"))
}

func TestSpacedNumberStillMatches(t *testing.T) {
	gomega.RegisterTestingT(t)

	// Declared tidily, delivered with spacing (or the other way round).
	p := &stubIdentityPersistence{declared: map[string]string{"mobile|+447926248382": "user-andy"}}

	tu, _ := ResolveTriggeringUser(p, org(), "twilio", "x", "+44 7926 248382")

	gomega.Expect(tu).ToNot(gomega.BeNil())
	gomega.Expect(tu.UserID).To(gomega.Equal("user-andy"))
}

func TestNumberDeclaredWithoutItsPlusStillMatches(t *testing.T) {
	gomega.RegisterTestingT(t)

	p := &stubIdentityPersistence{declared: map[string]string{"mobile|447926248382": "user-andy"}}

	tu, _ := ResolveTriggeringUser(p, org(), "twilio", "x", "+447926248382")

	gomega.Expect(tu).ToNot(gomega.BeNil())
	gomega.Expect(tu.UserID).To(gomega.Equal("user-andy"))
}

// National form is deliberately NOT guessed — it needs a country, and a
// wrong guess matches somebody else's number.
func TestNationalFormIsNotGuessed(t *testing.T) {
	gomega.RegisterTestingT(t)

	p := &stubIdentityPersistence{declared: map[string]string{"mobile|07926248382": "user-andy"}, anonID: "stub-1"}

	tu, _ := ResolveTriggeringUser(p, org(), "twilio", "x", "+447926248382")

	gomega.Expect(tu.UserID).To(gomega.Equal("stub-1"))
}

func TestUnknownCallerStillGetsAnAnonymousStubKeyedOnTheTransport(t *testing.T) {
	gomega.RegisterTestingT(t)

	p := &stubIdentityPersistence{declared: map[string]string{}, anonID: "stub-1"}

	tu, err := ResolveTriggeringUser(p, org(), "twilio", "+447000000000", "+447000000000")

	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	gomega.Expect(tu.UserID).To(gomega.Equal("stub-1"))
	gomega.Expect(p.anonCalls).To(gomega.Equal([]string{"twilio|+447000000000"}),
		"the stub keys on the transport and the identifier as delivered")
}

// Other channels keep exactly the behaviour they had.
func TestNonPhoneChannelsLookUpOnlyTheirOwnType(t *testing.T) {
	gomega.RegisterTestingT(t)

	p := &stubIdentityPersistence{declared: map[string]string{"telegram|AndyEsser": "user-andy"}}

	tu, _ := ResolveTriggeringUser(p, org(), "telegram", "Andy", "12345", "AndyEsser")

	gomega.Expect(tu.UserID).To(gomega.Equal("user-andy"))
	for _, l := range p.lookups {
		gomega.Expect(l).To(gomega.HavePrefix("telegram|"))
	}
}
