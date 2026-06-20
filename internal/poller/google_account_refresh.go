package poller

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"flomation.app/automate/api/internal/persistence"
	log "github.com/sirupsen/logrus"
)

const googleTokenURL = "https://oauth2.googleapis.com/token" // #nosec G101 — public endpoint

// googleTokenURLOverride is the test seam for redirecting the refresh
// POST at a stub server. Empty in production; tests set it before
// invoking poll() and restore the original after. Keeping it as a
// package-level var rather than wiring a constructor argument means
// the production startup path stays unchanged.
var googleTokenURLOverride string

func googleTokenEndpoint() string {
	if googleTokenURLOverride != "" {
		return googleTokenURLOverride
	}
	return googleTokenURL
}

// GoogleAccountRefreshPersistence defines the DB methods the poller needs.
type GoogleAccountRefreshPersistence interface {
	// Agent-user scoped accounts
	GetGoogleAccountsNeedingRefresh(within time.Duration) ([]persistence.GoogleAccountRefreshRow, error)
	StoreGoogleAccountAccessToken(id, accessToken string, expiresAt *time.Time) error
	UpdateGoogleAccountStatus(id, status string, lastError *string) error
	RecordGoogleAccountRefreshFailure(id, lastError string, permanent bool, threshold int) (string, error)
	// Trigger-scoped accounts
	GetTriggerGoogleAccountsNeedingRefresh(within time.Duration) ([]persistence.GoogleAccountRefreshRow, error)
	StoreTriggerGoogleAccountAccessToken(id, accessToken string, expiresAt *time.Time) error
	UpdateTriggerGoogleAccountStatus(id, status string, lastError *string) error
	RecordTriggerGoogleAccountRefreshFailure(id, lastError string, permanent bool, threshold int) (string, error)
}

// GoogleAccountRefreshPoller proactively refreshes Google account tokens.
type GoogleAccountRefreshPoller struct {
	persistence  GoogleAccountRefreshPersistence
	clientID     string
	clientSecret string
	client       *http.Client
}

// StartGoogleAccountRefreshPoller creates and starts the refresh poller.
// Returns nil if no Google OAuth credentials are configured.
//
// Logs a redacted form of the configured client_id at startup so an
// operator can compare it against Launch's google.client_id and catch
// the case where the two services are pointed at different OAuth
// clients. A drift there is silent and lethal: refresh tokens minted
// against Launch's client_id will be rejected with invalid_client
// the moment the poller tries to use them under a different one.
func StartGoogleAccountRefreshPoller(p GoogleAccountRefreshPersistence, clientID, clientSecret string) *GoogleAccountRefreshPoller {
	if clientID == "" || clientSecret == "" {
		log.Warn("Google OAuth credentials not configured — agent Google account refresh disabled")
		return nil
	}
	log.WithField("client_id", redactClientID(clientID)).Info(
		"Google account refresh poller: using OAuth client (compare against Launch's google.client_id to ensure they match)")
	rp := &GoogleAccountRefreshPoller{
		persistence:  p,
		clientID:     clientID,
		clientSecret: clientSecret,
		client:       &http.Client{Timeout: 15 * time.Second},
	}
	go rp.watch()
	return rp
}

// redactClientID keeps the leading 12 chars (enough to fingerprint a
// Google OAuth client_id, which always starts with the project number)
// and replaces the rest. We never want the full secret-adjacent
// identifier in logs even though Google client_ids aren't strictly
// secret — being conservative here is cheap.
func redactClientID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12] + "…"
}

func (rp *GoogleAccountRefreshPoller) watch() {
	// Stagger start from the credential poller
	time.Sleep(15 * time.Second)

	// Run immediately on first tick to populate any missing access tokens
	rp.poll()

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	log.Info("Google account refresh poller started (60s interval)")

	for range ticker.C {
		rp.poll()
	}
}

func (rp *GoogleAccountRefreshPoller) poll() {
	// Refresh agent-user scoped accounts
	rows, err := rp.persistence.GetGoogleAccountsNeedingRefresh(5 * time.Minute)
	if err != nil {
		log.WithError(err).Warn("Google account refresh: failed to query agent accounts")
	} else {
		for _, row := range rows {
			if row.RefreshToken == "" {
				continue
			}
			if err := rp.refreshAccount(row, false); err != nil {
				rp.handleRefreshFailure(row, err, false)
			}
		}
	}

	// Refresh trigger-scoped accounts
	triggerRows, err := rp.persistence.GetTriggerGoogleAccountsNeedingRefresh(5 * time.Minute)
	if err != nil {
		log.WithError(err).Warn("Google account refresh: failed to query trigger accounts")
	} else {
		for _, row := range triggerRows {
			if row.RefreshToken == "" {
				continue
			}
			if err := rp.refreshAccount(row, true); err != nil {
				rp.handleRefreshFailure(row, err, true)
			}
		}
	}
}

// handleRefreshFailure records the failure (incrementing the
// consecutive counter, potentially flipping to 'revoked') and logs
// it. The log line is WARN for transient failures (status='error')
// and ERROR for the moment a row transitions to revoked — operators
// generally want to see a one-time error rather than dig through
// 60s-cadence WARN spam to notice their integration is broken.
func (rp *GoogleAccountRefreshPoller) handleRefreshFailure(row persistence.GoogleAccountRefreshRow, err error, isTrigger bool) {
	errMsg := err.Error()
	permanent := classifyRefreshError(err)
	scope := "agent_user"
	if isTrigger {
		scope = "trigger"
	}

	var newStatus string
	var dbErr error
	if isTrigger {
		newStatus, dbErr = rp.persistence.RecordTriggerGoogleAccountRefreshFailure(
			row.ID, errMsg, permanent, MaxConsecutiveRefreshFailures)
	} else {
		newStatus, dbErr = rp.persistence.RecordGoogleAccountRefreshFailure(
			row.ID, errMsg, permanent, MaxConsecutiveRefreshFailures)
	}
	if dbErr != nil {
		log.WithError(dbErr).Warn("Google account refresh: failed to record failure")
	}

	fields := log.Fields{
		"account_id": row.ID,
		"email":      row.Email,
		"purpose":    row.Purpose,
		"scope":      scope,
		"permanent":  permanent,
		"new_status": newStatus,
		"error":      errMsg,
	}
	if newStatus == "revoked" {
		log.WithFields(fields).Error("Google account marked revoked — refresh attempts will stop until the user re-authorises")
	} else {
		log.WithFields(fields).Warn("Google account refresh failed")
	}
}

func (rp *GoogleAccountRefreshPoller) refreshAccount(row persistence.GoogleAccountRefreshRow, isTrigger bool) error {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {row.RefreshToken},
		"client_id":     {rp.clientID},
		"client_secret": {rp.clientSecret},
	}

	req, err := http.NewRequest("POST", googleTokenEndpoint(), strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := rp.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return &refreshError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return err
	}

	if tokenResp.AccessToken == "" {
		return &refreshError{Body: "empty access_token in response"}
	}

	var expiresAt *time.Time
	if tokenResp.ExpiresIn > 0 {
		t := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		expiresAt = &t
	}

	if isTrigger {
		if err := rp.persistence.StoreTriggerGoogleAccountAccessToken(row.ID, tokenResp.AccessToken, expiresAt); err != nil {
			return err
		}
	} else {
		if err := rp.persistence.StoreGoogleAccountAccessToken(row.ID, tokenResp.AccessToken, expiresAt); err != nil {
			return err
		}
	}

	scope := "agent_user"
	if isTrigger {
		scope = "trigger"
	}
	log.WithFields(log.Fields{
		"account_id": row.ID,
		"email":      row.Email,
		"purpose":    row.Purpose,
		"scope":      scope,
		"expires_in": tokenResp.ExpiresIn,
	}).Info("Google account token refreshed")

	return nil
}
