package poller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	api "flomation.app/automate/api"
	log "github.com/sirupsen/logrus"
)

// CreditSyncPersistence defines the DB methods the credit sync poller needs.
type CreditSyncPersistence interface {
	GetUnsyncedDeductions() ([]*api.CreditDeduction, error)
	MarkDeductionSynced(id string, amountPence int64) error
}

// CreditSyncPoller pushes unsynced credit deductions to the billing
// service. Runs every 30 seconds.
type CreditSyncPoller struct {
	persistence CreditSyncPersistence
	billingURL  string
	client      *http.Client
}

// StartCreditSyncPoller creates and starts the credit sync poller goroutine.
func StartCreditSyncPoller(p CreditSyncPersistence, billingURL string, client *http.Client) *CreditSyncPoller {
	cp := &CreditSyncPoller{
		persistence: p,
		billingURL:  billingURL,
		client:      client,
	}
	go cp.watch()
	return cp
}

func (cp *CreditSyncPoller) watch() {
	time.Sleep(15 * time.Second)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	log.Info("credit sync poller started (API-side, 30s interval)")

	for range ticker.C {
		cp.poll()
	}
}

func (cp *CreditSyncPoller) poll() {
	deductions, err := cp.persistence.GetUnsyncedDeductions()
	if err != nil {
		log.WithError(err).Warn("credit sync: failed to fetch unsynced deductions")
		return
	}

	if len(deductions) == 0 {
		return
	}

	synced := 0
	for _, d := range deductions {
		amountPence, err := cp.pushDeduction(d)
		if err != nil {
			log.WithFields(log.Fields{
				"deduction_id": d.ID,
				"execution_id": d.ExecutionID,
				"error":        err,
			}).Warn("credit sync: failed to push deduction, will retry next cycle")
			continue
		}

		if err := cp.persistence.MarkDeductionSynced(d.ID, amountPence); err != nil {
			log.WithFields(log.Fields{
				"deduction_id": d.ID,
				"error":        err,
			}).Warn("credit sync: failed to mark deduction as synced")
			continue
		}

		synced++
	}

	if synced > 0 {
		log.WithFields(log.Fields{
			"synced": synced,
			"total":  len(deductions),
		}).Info("credit sync: pushed deductions to billing service")
	}
}

// creditDeductPayload matches the billing service's creditDeductRequest.
// The billing service calculates the actual cost using its dynamic rate schedule.
type creditDeductPayload struct {
	OwnerID        string  `json:"owner_id"`
	OrganisationID *string `json:"organisation_id,omitempty"`
	ExecutionID    string  `json:"execution_id"`
	ExecutionLabel *string `json:"execution_label,omitempty"`
	DurationMs     int64   `json:"duration_ms"`
}

func (cp *CreditSyncPoller) pushDeduction(d *api.CreditDeduction) (int64, error) {
	payload := creditDeductPayload{
		OwnerID:        d.OwnerID,
		OrganisationID: d.OrganisationID,
		ExecutionID:    d.ExecutionID,
		ExecutionLabel: d.ExecutionLabel,
		DurationMs:     d.DurationMs,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal: %w", err)
	}

	url := cp.billingURL + "/api/v1/billing/internal/credit/deduct"
	resp, err := cp.client.Post(url, "application/json", bytes.NewReader(body)) // #nosec G107 — internal service-to-service call
	if err != nil {
		return 0, fmt.Errorf("http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		return 0, fmt.Errorf("billing returned %d", resp.StatusCode)
	}

	// Read the cost calculated by the billing service.
	var result struct {
		AmountPence int64 `json:"amount_pence"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&result); err != nil {
		return 0, nil // Synced successfully but couldn't read amount — not fatal.
	}

	return result.AmountPence, nil
}
