package http

import (
	"testing"
	"time"

	"flomation.app/automate/api"
	. "github.com/onsi/gomega"
)

// A personal account's Data Processing Agreement names the account holder as
// the Controller. Two things used to be wrong with that:
//
//  1. users.name is the literal string "auto-generate" until the person sets
//     something, and userFullName returned it — so the contract named the
//     Controller "auto-generate". 25 of 69 live accounts carried that value.
//  2. The effective date was time.Now() at download, so the agreement
//     re-dated itself every time anybody fetched it.

func TestUserFullName_NeverReturnsThePlaceholder(t *testing.T) {
	RegisterTestingT(t)

	email := "grace@example.com"
	user := &api.User{Name: PlaceholderUserName, EmailAddress: &email}

	// The placeholder is a marker, not a name. Falling through to the email
	// address gives a Controller that is at least unambiguously this person.
	Expect(userFullName(user)).To(Equal("grace@example.com"))
	Expect(userFullName(user)).ToNot(Equal(PlaceholderUserName))
}

func TestUserFullName_PrefersTheUsernameOverTheEmail(t *testing.T) {
	RegisterTestingT(t)

	email := "grace@example.com"
	Expect(userFullName(&api.User{Name: "Grace Beckett", EmailAddress: &email})).
		To(Equal("Grace Beckett"))
}

func TestUserFullName_PrefersARealNameOverEverything(t *testing.T) {
	RegisterTestingT(t)

	first, last := "Grace", "Beckett"
	email := "grace@example.com"
	user := &api.User{
		Name: "gracieb", FirstName: &first, LastName: &last, EmailAddress: &email,
	}
	Expect(userFullName(user)).To(Equal("Grace Beckett"))
}

func TestUserFullName_WhitespaceIsNotAName(t *testing.T) {
	RegisterTestingT(t)

	blank := "   "
	email := "grace@example.com"
	user := &api.User{Name: "  ", FirstName: &blank, LastName: &blank, EmailAddress: &email}
	Expect(userFullName(user)).To(Equal("grace@example.com"))
}

func TestBuildDPAParams_IndividualUsesTheRecordedEffectiveDate(t *testing.T) {
	RegisterTestingT(t)

	// The date a contract came into being must not move.
	registered := time.Date(2026, 3, 4, 9, 30, 0, 0, time.UTC)
	email := "grace@example.com"
	user := &api.User{
		ID: "11112222-3333-4444-5555-666677778888", Name: "gracieb",
		EmailAddress: &email, DPAEffectiveFrom: &registered,
	}

	p := buildDPAParams(user, nil)
	Expect(p.ControllerType).To(Equal("individual"))
	Expect(p.ControllerName).To(Equal("gracieb"))
	Expect(p.ControllerLegal).To(Equal("gracieb"))
	Expect(p.EffectiveDate).To(Equal(registered))

	// Twice, a moment apart, is the same agreement.
	again := buildDPAParams(user, nil)
	Expect(again.EffectiveDate).To(Equal(p.EffectiveDate))
	Expect(again.Reference).To(Equal(p.Reference))
}

func TestBuildDPAParams_IndividualWithoutARecordFallsBackToNow(t *testing.T) {
	RegisterTestingT(t)

	// An account provisioned before the date was recorded keeps the old
	// behaviour rather than getting a wrong date or an empty one.
	email := "grace@example.com"
	user := &api.User{ID: "11112222-3333-4444-5555-666677778888", EmailAddress: &email}

	p := buildDPAParams(user, nil)
	Expect(p.EffectiveDate).To(BeTemporally("~", time.Now(), 5*time.Second))
	Expect(p.ControllerName).To(Equal("grace@example.com"))
}

func TestBuildDPAParams_OrganisationIsUnaffected(t *testing.T) {
	RegisterTestingT(t)

	// The personal change must not reach organisation mode, which has its own
	// legal-details flow and its own effective date.
	registered := time.Date(2026, 3, 4, 9, 30, 0, 0, time.UTC)
	email := "grace@example.com"
	user := &api.User{ID: "u1", Name: "gracieb", EmailAddress: &email, DPAEffectiveFrom: &registered}
	org := &api.Organisation{ID: "o1", Name: "Acme", LegalName: strptr("Acme Ltd")}

	p := buildDPAParams(user, org)
	Expect(p.ControllerType).To(Equal("organisation"))
	Expect(p.ControllerLegal).To(Equal("Acme Ltd"))
	Expect(p.EffectiveDate).To(BeTemporally("~", time.Now(), 5*time.Second),
		"an organisation's date does not come from the user's record")
}
