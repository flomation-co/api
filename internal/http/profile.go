package http

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// profileRequest mirrors the writable extended-profile columns. Each field
// is a pointer so omitted JSON keys differ from explicitly-cleared ones —
// though we treat both as "set to NULL" for simplicity. If we ever want
// PATCH-merge semantics, switch this to a map[string]any.
type profileRequest struct {
	Salutation   *string `json:"salutation"`
	FirstName    *string `json:"first_name"`
	LastName     *string `json:"last_name"`
	JobTitle     *string `json:"job_title"`
	AddressLine1 *string `json:"address_line_1"`
	AddressLine2 *string `json:"address_line_2"`
	City         *string `json:"city"`
	Region       *string `json:"region"`
	Postcode     *string `json:"postcode"`
	Country      *string `json:"country"`
}

// updateProfile handles PUT /api/v1/user/profile. Authenticated, scoped to
// the calling user. Replaces all extended-profile columns in one shot;
// fields omitted from the body clear to NULL.
func (s *Service) updateProfile(c *gin.Context) {
	userIDFromContext, exists := c.Get("account_id")
	if !exists {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	userID := userIDFromContext.(string)

	var req profileRequest
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	user, err := s.persistence.GetUserByID(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	user.Salutation = trimOrNil(req.Salutation)
	user.FirstName = trimOrNil(req.FirstName)
	user.LastName = trimOrNil(req.LastName)
	user.JobTitle = trimOrNil(req.JobTitle)
	user.AddressLine1 = trimOrNil(req.AddressLine1)
	user.AddressLine2 = trimOrNil(req.AddressLine2)
	user.City = trimOrNil(req.City)
	user.Region = trimOrNil(req.Region)
	user.Postcode = trimOrNil(req.Postcode)
	user.Country = trimOrNil(req.Country)

	if err := s.persistence.UpdateUserProfile(user); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, user)
}

// trimOrNil strips whitespace and returns nil for blank values so empty
// strings round-trip cleanly to NULL in Postgres.
func trimOrNil(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil
	}
	return &t
}

// getUserVariablesInternal handles GET /api/v1/internal/user/:id/variables.
// Called by the executor at execution-context bootstrap to populate the
// ${user.X} substitution namespace. Returns a flat map of variable name
// to string value (empty for unset fields), with composed full_name and
// full_address computed server-side.
func (s *Service) getUserVariablesInternal(c *gin.Context) {
	userID := c.Param("id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user id required"})
		return
	}

	user, err := s.persistence.GetUserByID(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if user == nil {
		// Anonymous / unknown user → empty map. Lets the executor's
		// substitution proceed cleanly without nil-deref.
		c.JSON(http.StatusOK, map[string]string{})
		return
	}

	vars := map[string]string{
		"id":             user.ID,
		"name":           user.Name,
		"salutation":     deref(user.Salutation),
		"first_name":     deref(user.FirstName),
		"last_name":      deref(user.LastName),
		"job_title":      deref(user.JobTitle),
		"address_line_1": deref(user.AddressLine1),
		"address_line_2": deref(user.AddressLine2),
		"city":           deref(user.City),
		"region":         deref(user.Region),
		"postcode":       deref(user.Postcode),
		"country":        deref(user.Country),
	}
	if user.EmailAddress != nil {
		vars["email"] = *user.EmailAddress
	}
	vars["full_name"] = composeFullName(vars["salutation"], vars["first_name"], vars["last_name"], user.Name)
	vars["full_address"] = composeFullAddress(vars["address_line_1"], vars["address_line_2"],
		vars["city"], vars["region"], vars["postcode"], vars["country"])

	c.JSON(http.StatusOK, vars)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// composeFullName joins salutation/first/last, falling back to display name
// if all three are blank.
func composeFullName(salutation, first, last, displayName string) string {
	parts := make([]string, 0, 3)
	for _, p := range []string{salutation, first, last} {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return displayName
	}
	return strings.Join(parts, " ")
}

// composeFullAddress renders a UK-style multi-line address, skipping any
// empty fields.
func composeFullAddress(line1, line2, city, region, postcode, country string) string {
	lines := make([]string, 0, 6)
	for _, p := range []string{line1, line2, city, region, postcode, country} {
		if p = strings.TrimSpace(p); p != "" {
			lines = append(lines, p)
		}
	}
	return strings.Join(lines, "\n")
}
