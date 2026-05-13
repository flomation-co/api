package persistence

import (
	"database/sql"

	"flomation.app/automate/api"
	"github.com/jmoiron/sqlx"
)

// ── Credit balance (local cache of billing service state) ────────────

// UpsertCreditBalance creates or updates the cached credit balance for an owner.
func (s *Service) UpsertCreditBalance(ownerID string, orgID *string, balancePence int64) error {
	_, err := s.conn.Exec(`
		INSERT INTO credit_balance (owner_id, organisation_id, balance_pence, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (owner_id, organisation_id)
		DO UPDATE SET
			balance_pence = EXCLUDED.balance_pence,
			updated_at = NOW()`,
		ownerID, orgID, balancePence)
	return err
}

// GetCreditBalance returns the cached credit balance for an owner, or nil if none exists.
func (s *Service) GetCreditBalance(ownerID string, orgID *string) (*api.CreditBalance, error) {
	var cb api.CreditBalance
	err := s.conn.Get(&cb, `
		SELECT * FROM credit_balance
		WHERE owner_id = $1
		  AND organisation_id IS NOT DISTINCT FROM $2`,
		ownerID, orgID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &cb, nil
}

// ── Credit deductions (overage records for billing sync) ─────────────

// RecordCreditDeduction stores an overage record for async sync to the billing service.
func (s *Service) RecordCreditDeduction(deduction *api.CreditDeduction) error {
	_, err := s.conn.Exec(`
		INSERT INTO credit_deduction (owner_id, organisation_id, execution_id, duration_ms)
		VALUES ($1, $2, $3, $4)`,
		deduction.OwnerID, deduction.OrganisationID, deduction.ExecutionID,
		deduction.DurationMs)
	return err
}

// GetUnsyncedDeductions returns all deductions not yet synced to the billing service.
func (s *Service) GetUnsyncedDeductions() ([]*api.CreditDeduction, error) {
	var deductions []*api.CreditDeduction
	err := s.conn.Select(&deductions, `
		SELECT * FROM credit_deduction
		WHERE NOT synced
		ORDER BY created_at ASC
		LIMIT 100`)
	if err != nil {
		return nil, err
	}
	return deductions, nil
}

// MarkDeductionSynced marks a deduction as synced and records the cost calculated by billing.
func (s *Service) MarkDeductionSynced(id string, amountPence int64) error {
	_, err := s.conn.Exec(`UPDATE credit_deduction SET synced = TRUE, amount_pence = $2 WHERE id = $1`, id, amountPence)
	return err
}

// GetCreditCostsForExecutions returns a map of execution_id → total credit cost in pence
// for the given execution IDs. Only includes synced deductions where the billing
// service has calculated and confirmed the cost.
func (s *Service) GetCreditCostsForExecutions(executionIDs []string) (map[string]int64, error) {
	if len(executionIDs) == 0 {
		return nil, nil
	}

	query, args, err := sqlx.In(`
		SELECT execution_id, SUM(COALESCE(amount_pence, 0)) AS total
		FROM credit_deduction
		WHERE execution_id IN (?)
		  AND synced = TRUE
		  AND amount_pence IS NOT NULL
		GROUP BY execution_id`, executionIDs)
	if err != nil {
		return nil, err
	}

	query = s.conn.Rebind(query)

	var rows []struct {
		ExecutionID string `db:"execution_id"`
		Total       int64  `db:"total"`
	}
	if err := s.conn.Select(&rows, query, args...); err != nil {
		return nil, err
	}

	result := make(map[string]int64, len(rows))
	for _, r := range rows {
		result[r.ExecutionID] = r.Total
	}
	return result, nil
}
