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

func TestMissingOrgLegalFields(t *testing.T) {
	RegisterTestingT(t)

	// Fully complete → nothing missing.
	complete := &api.Organisation{
		LegalName:     strptr("Acme Ltd"),
		CompanyNumber: strptr("12345678"),
		AddressLine1:  strptr("1 High Street"),
		Postcode:      strptr("M1 1AA"),
	}
	Expect(missingOrgLegalFields(complete)).To(BeEmpty())

	// Empty org → all four required fields flagged.
	Expect(missingOrgLegalFields(&api.Organisation{})).To(Equal(
		[]string{"legal_name", "company_number", "address_line_1", "postcode"}))

	// Whitespace-only values count as missing.
	Expect(missingOrgLegalFields(&api.Organisation{
		LegalName:     strptr("  "),
		CompanyNumber: strptr("87654321"),
		AddressLine1:  strptr("2 Low Road"),
		Postcode:      strptr("EH1 1AB"),
	})).To(Equal([]string{"legal_name"}))
}
