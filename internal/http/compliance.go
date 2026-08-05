package http

import (
	"net/http"
	"strings"
	"time"

	"flomation.app/automate/api"
	"flomation.app/automate/api/internal/dpa"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// getComplianceStatus returns metadata about the customer's Data Processing
// Agreement so the editor can describe it and flag whether the organisation's
// legal details are complete. The DPA itself is generated fresh on download.
func (s *Service) getComplianceStatus(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	org := s.resolveActingOrg(c, user)
	params := buildDPAParams(user, org)

	legalComplete := true
	var missing []string
	if org != nil {
		missing = missingOrgLegalFields(org)
		legalComplete = len(missing) == 0
	}

	c.JSON(http.StatusOK, gin.H{
		"template_version":       dpa.TemplateVersion,
		"controller_type":        params.ControllerType,
		"controller_name":        params.ControllerName,
		"reference":              params.Reference,
		"legal_details_complete": legalComplete,
		"missing_legal_fields":   missing,
		"processor":              dpa.ProcessorName,
		"processor_company_no":   dpa.ProcessorCompanyNumber,
	})
}

// getDPA generates the customer-specific Data Processing Agreement and returns
// it as a PDF attachment. It always regenerates from the current template, so
// template changes take effect on the next download.
func (s *Service) getDPA(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	org := s.resolveActingOrg(c, user)
	params := buildDPAParams(user, org)

	pdfBytes, err := dpa.GeneratePDF(params)
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("unable to generate DPA PDF")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	filename := dpa.GenerateFilename(params)
	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}

// resolveActingOrg returns the full organisation record the user is acting as
// (from the ?organisation query param, plucked into context by jwtMiddleware),
// or nil in personal mode.
func (s *Service) resolveActingOrg(c *gin.Context, user *api.User) *api.Organisation {
	if len(user.Organisations) == 0 {
		return nil
	}
	orgID := user.Organisations[0].ID
	org, err := s.persistence.GetOrganisationByID(orgID)
	if err != nil {
		log.WithFields(log.Fields{"error": err, "org": orgID}).Error("unable to load organisation for compliance")
		return nil
	}
	return org
}

// buildDPAParams maps the acting user/organisation onto the DPA Controller
// identity. In organisation mode the Controller is the organisation (using its
// legal details, falling back to its display name); in personal mode it is the
// individual account holder using their profile details. Pure and testable.
func buildDPAParams(user *api.User, org *api.Organisation) dpa.Params {
	now := time.Now()
	p := dpa.Params{
		EffectiveDate: now,
		ContactName:   userFullName(user),
		ContactEmail:  deref(user.EmailAddress),
	}

	if org != nil {
		p.ControllerType = "organisation"
		p.ControllerName = org.Name
		p.ControllerLegal = firstNonEmpty(deref(org.LegalName), org.Name)
		p.CompanyNumber = deref(org.CompanyNumber)
		p.AddressLines = assembleAddress(
			deref(org.AddressLine1), deref(org.AddressLine2),
			deref(org.City), deref(org.Region), deref(org.Postcode), deref(org.Country),
		)
		p.Reference = "DPA-" + shortID(org.ID)
		return p
	}

	p.ControllerType = "individual"
	p.ControllerName = userFullName(user)
	p.ControllerLegal = userFullName(user)
	p.AddressLines = assembleAddress(
		deref(user.AddressLine1), deref(user.AddressLine2),
		deref(user.City), deref(user.Region), deref(user.Postcode), deref(user.Country),
	)
	p.Reference = "DPA-" + shortID(user.ID)
	return p
}

// missingOrgLegalFields lists the legal fields an organisation still needs to
// complete for a fully-populated DPA. The DPA still generates without them
// (falling back to the display name), but the editor nudges admins to finish.
func missingOrgLegalFields(org *api.Organisation) []string {
	var missing []string
	if strings.TrimSpace(deref(org.LegalName)) == "" {
		missing = append(missing, "legal_name")
	}
	if strings.TrimSpace(deref(org.CompanyNumber)) == "" {
		missing = append(missing, "company_number")
	}
	if strings.TrimSpace(deref(org.AddressLine1)) == "" {
		missing = append(missing, "address_line_1")
	}
	if strings.TrimSpace(deref(org.Postcode)) == "" {
		missing = append(missing, "postcode")
	}
	return missing
}

func userFullName(user *api.User) string {
	first := strings.TrimSpace(deref(user.FirstName))
	last := strings.TrimSpace(deref(user.LastName))
	full := strings.TrimSpace(first + " " + last)
	if full != "" {
		return full
	}
	if strings.TrimSpace(user.Name) != "" {
		return user.Name
	}
	return deref(user.EmailAddress)
}

func assembleAddress(parts ...string) []string {
	var lines []string
	for _, part := range parts {
		if p := strings.TrimSpace(part); p != "" {
			lines = append(lines, p)
		}
	}
	return lines
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func shortID(id string) string {
	id = strings.ReplaceAll(id, "-", "")
	if len(id) > 8 {
		id = id[:8]
	}
	return strings.ToUpper(id)
}
