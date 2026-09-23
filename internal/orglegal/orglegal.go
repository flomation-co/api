// Package orglegal holds the one definition of "has this organisation
// provided the legal identity its Data Processing Agreement needs".
//
// It lives in its own package because two very different layers need
// the same answer: the HTTP compliance endpoints, which tell an admin
// what is still missing, and the execution dispatch queue, which holds
// an organisation's work back until it is complete. A second copy of
// this rule would eventually disagree with the first, and the way it
// would show up is an organisation being told it is compliant while its
// flows quietly never run.
package orglegal

import (
	"strings"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/dpa"
)

// Missing lists the legal fields an organisation still needs, for a
// fully-populated DPA. A company number is required only for registered
// entity types (Ltd, LLP, PLC); a sole trader or partnership has none.
// Address line 1 is optional — city, postcode and country give a usable
// registered address.
func Missing(org *api.Organisation) []string {
	if org == nil {
		return RequiredFields()
	}

	var missing []string
	companyType := strings.TrimSpace(deref(org.CompanyType))
	if companyType == "" {
		missing = append(missing, "company_type")
	}
	if strings.TrimSpace(deref(org.LegalName)) == "" {
		missing = append(missing, "legal_name")
	}
	if dpa.RequiresCompanyNumber(companyType) && strings.TrimSpace(deref(org.CompanyNumber)) == "" {
		missing = append(missing, "company_number")
	}
	if strings.TrimSpace(deref(org.City)) == "" {
		missing = append(missing, "city")
	}
	if strings.TrimSpace(deref(org.Postcode)) == "" {
		missing = append(missing, "postcode")
	}
	if strings.TrimSpace(deref(org.Country)) == "" {
		missing = append(missing, "country")
	}
	return missing
}

// Complete reports whether nothing is missing.
func Complete(org *api.Organisation) bool {
	return len(Missing(org)) == 0
}

// RequiredFields is what an organisation we cannot read is assumed to
// be missing — every field that is always required regardless of
// company type.
func RequiredFields() []string {
	return []string{"company_type", "legal_name", "city", "postcode", "country"}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
