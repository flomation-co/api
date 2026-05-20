package http

import (
	"fmt"

	"flomation.app/automate/api"
	log "github.com/sirupsen/logrus"
)

// checkQuota verifies that the flow's owner has remaining execution capacity.
// Returns (true, message) if the execution should be blocked, (false, "") otherwise.
//
// Quota enforcement is only active when configured. When enabled, an execution
// is blocked only if the subscription allowance is exhausted AND the credit
// balance is zero or negative.
func (s *Service) checkQuota(floID string) (bool, string) {
	if !s.config.Billing.QuotaEnforcementEnabled {
		return false, ""
	}

	flo, err := s.persistence.GetFloByID(floID)
	if err != nil || flo == nil {
		// If we can't resolve the flow, don't block — fail open.
		return false, ""
	}

	ownerID := ""
	if flo.AuthorID != nil {
		ownerID = *flo.AuthorID
	}
	if ownerID == "" {
		return false, ""
	}

	usage, err := s.persistence.GetUsage(ownerID, flo.OrganisationID)
	if err != nil || usage == nil {
		// No usage data — no entitlements configured, allow execution.
		return false, ""
	}

	// If no allowance is set or usage is within it, allow.
	if usage.Allowance == nil || *usage.Allowance <= 0 {
		return false, ""
	}
	usageMs := int64(0)
	if usage.Usage != nil {
		usageMs = *usage.Usage
	}
	if usageMs < *usage.Allowance {
		return false, ""
	}

	// Allowance exhausted — check credit balance.
	credit, err := s.persistence.GetCreditBalance(ownerID, flo.OrganisationID)
	if err != nil {
		log.WithError(err).Warn("quota: failed to check credit balance, allowing execution")
		return false, ""
	}

	if credit == nil {
		// No cached credit record — the billing service may not have synced yet.
		// Fail open to avoid blocking executions due to stale cache.
		log.WithFields(log.Fields{
			"owner_id":        ownerID,
			"organisation_id": flo.OrganisationID,
		}).Debug("quota: no cached credit balance, allowing execution")
		return false, ""
	}

	if credit.BalancePence > 0 {
		// Has credit — allow execution, it will be billed on completion.
		return false, ""
	}

	return true, "Your subscription allowance has been exhausted and you have no remaining credit. Please upgrade your plan or add credit to continue."
}

// processPostExecutionCredit records overage duration for execution time
// that exceeded the subscription allowance. The actual cost calculation
// and balance deduction is handled by the billing service when the
// deduction sync poller pushes this record. This keeps rate logic
// (including time-of-day pricing) in the billing service where it belongs.
func (s *Service) processPostExecutionCredit(executionID string) {
	if !s.config.Billing.QuotaEnforcementEnabled {
		return
	}

	execution, err := s.persistence.GetExecutionByID(executionID)
	if err != nil || execution == nil {
		return
	}

	// Determine the billable duration: prefer BillingDuration, fall back to Duration.
	var durationMs int64
	if execution.BillingDuration != nil && *execution.BillingDuration > 0 {
		durationMs = *execution.BillingDuration
	} else if execution.Duration != nil && *execution.Duration > 0 {
		durationMs = *execution.Duration
	} else {
		return
	}

	ownerID := execution.OwnerID
	orgID := execution.OrganisationID

	// Check if the user has a credit balance at all.
	credit, err := s.persistence.GetCreditBalance(ownerID, orgID)
	if err != nil || credit == nil {
		return
	}

	// Check if usage exceeds the subscription allowance.
	usage, err := s.persistence.GetUsage(ownerID, orgID)
	if err != nil || usage == nil {
		return
	}

	// If no allowance set, or still within it, no overage to record.
	if usage.Allowance == nil || *usage.Allowance <= 0 {
		return
	}
	usageMs := int64(0)
	if usage.Usage != nil {
		usageMs = *usage.Usage
	}
	if usageMs <= *usage.Allowance {
		return
	}

	// Calculate overage: the lesser of (total overage) and (this execution's duration).
	overageMs := usageMs - *usage.Allowance
	if overageMs > durationMs {
		overageMs = durationMs
	}

	// Build execution label (flow name + sequence) for billing line items.
	var execLabel *string
	if flo, err := s.persistence.GetFloByID(execution.FloID); err == nil && flo != nil {
		label := fmt.Sprintf("%s #%d", flo.Name, execution.Sequence)
		execLabel = &label
	}

	// Record the overage for async sync to the billing service.
	// The billing service will calculate the actual cost using its
	// dynamic rate schedule and deduct from the real balance.
	deduction := &api.CreditDeduction{
		OwnerID:        ownerID,
		OrganisationID: orgID,
		ExecutionID:    executionID,
		ExecutionLabel: execLabel,
		DurationMs:     overageMs,
	}
	if err := s.persistence.RecordCreditDeduction(deduction); err != nil {
		log.WithFields(log.Fields{
			"execution_id": executionID,
			"owner_id":     ownerID,
			"error":        err,
		}).Error("failed to record credit deduction")
		return
	}

	log.WithFields(log.Fields{
		"execution_id": executionID,
		"owner_id":     ownerID,
		"overage_ms":   overageMs,
	}).Info("credit overage recorded for billing sync")
}
