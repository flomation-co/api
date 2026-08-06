package http

import (
	"testing"

	"flomation.app/automate/api"
	. "github.com/onsi/gomega"
)

func TestBuildDPAParams_Organisation(t *testing.T) {
	RegisterTestingT(t)

	user := &api.User{
		ID:           "user-1",
		Name:         "Andy Esser",
		FirstName:    strptr("Andy"),
		LastName:     strptr("Esser"),
		EmailAddress: strptr("andy@example.com"),
	}
	org := &api.Organisation{
		ID:            "0a1b2c3d-4e5f-6789-abcd-ef0123456789",
		Name:          "Acme Widgets",
		CompanyType:   strptr("limited_company"),
		LegalName:     strptr("Acme Widgets Limited"),
		CompanyNumber: strptr("12345678"),
		AddressLine1:  strptr("1 High Street"),
		City:          strptr("Manchester"),
		Postcode:      strptr("M1 1AA"),
		Country:       strptr("United Kingdom"),
	}

	p := buildDPAParams(user, org)

	Expect(p.ControllerType).To(Equal("organisation"))
	Expect(p.ControllerName).To(Equal("Acme Widgets"))
	Expect(p.ControllerLegal).To(Equal("Acme Widgets Limited"))
	Expect(p.CompanyType).To(Equal("limited_company"))
	Expect(p.CompanyNumber).To(Equal("12345678"))
	// Contact is the acting user.
	Expect(p.ContactName).To(Equal("Andy Esser"))
	Expect(p.ContactEmail).To(Equal("andy@example.com"))
	// Address assembled, blanks (line 2, region) elided.
	Expect(p.AddressLines).To(Equal([]string{"1 High Street", "Manchester", "M1 1AA", "United Kingdom"}))
	// Reference is derived from the org id (dashes stripped, upper, 8 chars).
	Expect(p.Reference).To(Equal("DPA-0A1B2C3D"))
	Expect(p.EffectiveDate.IsZero()).To(BeFalse())
}

func TestBuildDPAParams_OrganisationFallsBackToDisplayName(t *testing.T) {
	RegisterTestingT(t)

	// No legal_name set — the DPA still resolves a legal identity from the
	// organisation's display name.
	org := &api.Organisation{ID: "org-xyz", Name: "Beta Co"}
	p := buildDPAParams(&api.User{ID: "u", Name: "U"}, org)

	Expect(p.ControllerType).To(Equal("organisation"))
	Expect(p.ControllerLegal).To(Equal("Beta Co"))
	Expect(p.CompanyNumber).To(Equal(""))
	Expect(p.AddressLines).To(BeEmpty())
}

func TestBuildDPAParams_Individual(t *testing.T) {
	RegisterTestingT(t)

	user := &api.User{
		ID:           "11112222-3333",
		Name:         "Jane Doe",
		FirstName:    strptr("Jane"),
		LastName:     strptr("Doe"),
		EmailAddress: strptr("jane@example.com"),
		AddressLine1: strptr("22 Oak Lane"),
		Postcode:     strptr("LS1 2CD"),
	}

	p := buildDPAParams(user, nil)

	Expect(p.ControllerType).To(Equal("individual"))
	Expect(p.ControllerName).To(Equal("Jane Doe"))
	Expect(p.ControllerLegal).To(Equal("Jane Doe"))
	Expect(p.AddressLines).To(Equal([]string{"22 Oak Lane", "LS1 2CD"}))
	Expect(p.Reference).To(Equal("DPA-11112222"))
}

func TestOrganisationLegalComplete(t *testing.T) {
	RegisterTestingT(t)

	svc := &Service{persistence: &mockPersistence{organisations: map[string]*api.Organisation{
		// Limited company → company number required, address line 1 optional.
		"org-complete": {
			ID: "org-complete", Name: "Acme", CompanyType: strptr("limited_company"),
			LegalName: strptr("Acme Ltd"), CompanyNumber: strptr("12345678"),
			City: strptr("Manchester"), Postcode: strptr("M1 1AA"), Country: strptr("United Kingdom"),
		},
		// Sole trader → no company number required; still complete without one.
		"org-sole": {
			ID: "org-sole", Name: "Jo's Cafe", CompanyType: strptr("sole_trader"),
			LegalName: strptr("Josephine Bloggs"),
			City:      strptr("Leeds"), Postcode: strptr("LS1 2CD"), Country: strptr("United Kingdom"),
		},
		"org-partial": {ID: "org-partial", Name: "Beta", CompanyType: strptr("limited_company"), LegalName: strptr("Beta Ltd")},
	}}}

	complete, missing := svc.organisationLegalComplete("org-complete")
	Expect(complete).To(BeTrue())
	Expect(missing).To(BeEmpty())

	complete, _ = svc.organisationLegalComplete("org-sole")
	Expect(complete).To(BeTrue())

	complete, missing = svc.organisationLegalComplete("org-partial")
	Expect(complete).To(BeFalse())
	Expect(missing).To(ContainElement("company_number"))
	Expect(missing).To(ContainElement("city"))

	// Unknown organisation → treated as incomplete (fail closed).
	complete, missing = svc.organisationLegalComplete("does-not-exist")
	Expect(complete).To(BeFalse())
	Expect(missing).ToNot(BeEmpty())
}

func TestMissingOrgLegalFields(t *testing.T) {
	RegisterTestingT(t)

	// Fully complete limited company → nothing missing (address line 1 omitted).
	complete := &api.Organisation{
		CompanyType:   strptr("limited_company"),
		LegalName:     strptr("Acme Ltd"),
		CompanyNumber: strptr("12345678"),
		City:          strptr("Manchester"),
		Postcode:      strptr("M1 1AA"),
		Country:       strptr("United Kingdom"),
	}
	Expect(missingOrgLegalFields(complete)).To(BeEmpty())

	// Sole trader with no company number → still complete.
	sole := &api.Organisation{
		CompanyType: strptr("sole_trader"),
		LegalName:   strptr("Jo Bloggs"),
		City:        strptr("Leeds"),
		Postcode:    strptr("LS1 2CD"),
		Country:     strptr("United Kingdom"),
	}
	Expect(missingOrgLegalFields(sole)).To(BeEmpty())

	// Empty org → company_type, legal_name, city, postcode, country flagged
	// (company_number is not required until a type that has one is chosen).
	Expect(missingOrgLegalFields(&api.Organisation{})).To(Equal(
		[]string{"company_type", "legal_name", "city", "postcode", "country"}))

	// A limited company missing only its number.
	Expect(missingOrgLegalFields(&api.Organisation{
		CompanyType: strptr("limited_company"),
		LegalName:   strptr("Acme Ltd"),
		City:        strptr("Manchester"),
		Postcode:    strptr("M1 1AA"),
		Country:     strptr("United Kingdom"),
	})).To(Equal([]string{"company_number"}))
}
