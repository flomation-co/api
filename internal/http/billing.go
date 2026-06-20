package http

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	api "flomation.app/automate/api"
)

// ── Internal: entitlement sync (called by billing service via mTLS) ───

type entitlementSyncRequest struct {
	OwnerID            string             `json:"owner_id" binding:"required"`
	OrganisationID     *string            `json:"organisation_id"`
	PlanSlug           string             `json:"plan_slug" binding:"required"`
	SubscriptionStatus string             `json:"subscription_status" binding:"required"`
	PeriodEnd          *string            `json:"period_end"`
	Entitlements       []entitlementEntry `json:"entitlements" binding:"required"`
}

type entitlementEntry struct {
	Key       string           `json:"entitlement_key" binding:"required"`
	ValueInt  *int64           `json:"value_int"`
	ValueBool *bool            `json:"value_bool"`
	ValueJSON *json.RawMessage `json:"value_json"`
}

// syncEntitlementsInternal receives a full entitlement set from the
// billing service and upserts it into the local cache. Any keys not
// included in the payload are preserved (the billing service sends
// only the keys that belong to the plan).
func (s *Service) syncEntitlementsInternal(c *gin.Context) {
	var req entitlementSyncRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	logger := log.WithFields(log.Fields{
		"owner_id":  req.OwnerID,
		"plan_slug": req.PlanSlug,
		"status":    req.SubscriptionStatus,
		"count":     len(req.Entitlements),
	})

	for _, entry := range req.Entitlements {
		ent := &api.SubscriptionEntitlement{
			OwnerID:            req.OwnerID,
			OrganisationID:     req.OrganisationID,
			PlanSlug:           req.PlanSlug,
			EntitlementKey:     entry.Key,
			ValueInt:           entry.ValueInt,
			ValueBool:          entry.ValueBool,
			ValueJSON:          entry.ValueJSON,
			SubscriptionStatus: req.SubscriptionStatus,
		}

		if err := s.persistence.UpsertEntitlement(ent); err != nil {
			logger.WithError(err).WithField("key", entry.Key).Error("failed to upsert entitlement")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to sync entitlement: " + entry.Key})
			return
		}
	}

	logger.Info("entitlements synced from billing service")
	c.Status(http.StatusNoContent)
}

// ── Internal: credit balance sync (called by billing service via mTLS) ──

type creditBalanceSyncRequest struct {
	OwnerID        string  `json:"owner_id" binding:"required"`
	OrganisationID *string `json:"organisation_id"`
	BalancePence   int64   `json:"balance_pence"`
}

func (s *Service) syncCreditBalanceInternal(c *gin.Context) {
	var req creditBalanceSyncRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := s.persistence.UpsertCreditBalance(req.OwnerID, req.OrganisationID, req.BalancePence); err != nil {
		log.WithError(err).Error("failed to upsert credit balance")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to sync credit balance"})
		return
	}

	log.WithFields(log.Fields{
		"owner_id": req.OwnerID,
		"balance":  req.BalancePence,
	}).Info("credit balance synced from billing service")

	c.Status(http.StatusNoContent)
}

// ── Public: quota endpoint ────────────────────────────────────────────

// getQuota returns the current entitlements and usage for the
// authenticated user/organisation. Used by the Editor to display
// usage dashboards and upgrade prompts.
func (s *Service) getQuota(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		// Defensive: every getUserFromContext caller should nil-check
		// before dereferencing. This one historically didn't, and a
		// fresh-user race on the lazy-create path produced a nil deref
		// that 500-ed the entire response with no body — which the
		// editor surfaced as a React #418 hydration mismatch on first
		// page load. The persistence-side ON CONFLICT in stmtCreateUser
		// closes the race; this guard is belt-and-braces.
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var orgID *string
	if len(user.Organisations) > 0 {
		orgID = &user.Organisations[0].ID
	}

	ents, err := s.persistence.GetAllEntitlements(user.ID, orgID)
	if err != nil {
		// No entitlements cached — return defaults.
		ents = []*api.SubscriptionEntitlement{}
	}

	// Get current usage.
	usage, err := s.persistence.GetUsage(user.ID, orgID)
	if err != nil {
		log.WithError(err).Error("failed to get usage for quota")
	}

	// Build a structured response.
	entitlementMap := make(map[string]interface{})
	planSlug := "start" // default
	status := "active"

	for _, e := range ents {
		planSlug = e.PlanSlug
		status = e.SubscriptionStatus
		if e.ValueInt != nil {
			entitlementMap[e.EntitlementKey] = *e.ValueInt
		} else if e.ValueBool != nil {
			entitlementMap[e.EntitlementKey] = *e.ValueBool
		} else if e.ValueJSON != nil {
			entitlementMap[e.EntitlementKey] = e.ValueJSON
		}
	}

	response := gin.H{
		"plan_slug":    planSlug,
		"status":       status,
		"entitlements": entitlementMap,
	}

	if usage != nil {
		response["usage_ms"] = usage.Usage
		response["allowance_ms"] = usage.Allowance
	}

	// Include credit balance if available.
	if credit, err := s.persistence.GetCreditBalance(user.ID, orgID); err == nil && credit != nil {
		response["credit_balance_pence"] = credit.BalancePence
	}

	c.JSON(http.StatusOK, response)
}
