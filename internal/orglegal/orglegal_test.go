package orglegal

import (
	"testing"

	"flomation.app/automate/api"
	. "github.com/onsi/gomega"
)

func strptr(s string) *string { return &s }

func completeLimitedCompany() *api.Organisation {
	return &api.Organisation{
		ID: "org", Name: "Acme",
		CompanyType: strptr("limited_company"), LegalName: strptr("Acme Ltd"),
		CompanyNumber: strptr("12345678"),
		City:          strptr("Manchester"), Postcode: strptr("M1 1AA"), Country: strptr("United Kingdom"),
	}
}

func TestCompleteLimitedCompany(t *testing.T) {
	RegisterTestingT(t)

	Expect(Missing(completeLimitedCompany())).To(BeEmpty())
	Expect(Complete(completeLimitedCompany())).To(BeTrue())
}

func TestCompanyNumberOnlyRequiredForRegisteredTypes(t *testing.T) {
	RegisterTestingT(t)

	// A sole trader has no company number to give, so demanding one
	// would hold their executions for ever.
	soleTrader := &api.Organisation{
		ID: "org", Name: "Jo's Cafe",
		CompanyType: strptr("sole_trader"), LegalName: strptr("Josephine Bloggs"),
		City: strptr("Leeds"), Postcode: strptr("LS1 2CD"), Country: strptr("United Kingdom"),
	}
	Expect(Complete(soleTrader)).To(BeTrue())

	withoutNumber := completeLimitedCompany()
	withoutNumber.CompanyNumber = nil
	Expect(Missing(withoutNumber)).To(ContainElement("company_number"))
}

func TestAddressLineOneIsOptional(t *testing.T) {
	RegisterTestingT(t)

	// City, postcode and country give a usable registered address.
	org := completeLimitedCompany()
	org.AddressLine1 = nil
	Expect(Complete(org)).To(BeTrue())
}

func TestWhitespaceDoesNotCountAsProvided(t *testing.T) {
	RegisterTestingT(t)

	org := completeLimitedCompany()
	org.LegalName = strptr("   ")
	org.Postcode = strptr("\t")
	Expect(Missing(org)).To(ContainElement("legal_name"))
	Expect(Missing(org)).To(ContainElement("postcode"))
}

func TestUnknownOrganisationFailsClosed(t *testing.T) {
	RegisterTestingT(t)

	// An organisation we cannot read is held, not released — the hold
	// is lossless, so failing closed costs a delay rather than data.
	Expect(Complete(nil)).To(BeFalse())
	Expect(Missing(nil)).To(Equal(RequiredFields()))
}
