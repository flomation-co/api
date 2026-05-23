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

// GoogleAccountRefreshPersistence defines the DB methods the poller needs.
type GoogleAccountRefreshPersistence interface {
	// Agent-user scoped accounts
	GetGoogleAccountsNeedingRefresh(within time.Duration) ([]persistence.GoogleAccountRefreshRow, error)
	StoreGoogleAccountAccessToken(id, accessToken string, expiresAt *time.Time) error
	UpdateGoogleAccountStatus(id, status string, lastError *string) error
	// Trigger-scoped accounts
	GetTriggerGoogleAccountsNeedingRefresh(within time.Duration) ([]persistence.GoogleAccountRefreshRow, error)
	StoreTriggerGoogleAccountAccessToken(id, accessToken string, expiresAt *time.Time) error
	UpdateTriggerGoogleAccountStatus(id, status string, lastError *string) error
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
func StartGoogleAccountRefreshPoller(p GoogleAccountRefreshPersistence, clientID, clientSecret string) *GoogleAccountRefreshPoller {
	if clientID == "" || clientSecret == "" {
		log.Warn("Google OAuth credentials not configured — agent Google account refresh disabled")
		return nil
	}
	rp := &GoogleAccountRefreshPoller{
		persistence:  p,
		clientID:     clientID,
		clientSecret: clientSecret,
		client:       &http.Client{Timeout: 15 * time.Second},
	}
	go rp.watch()
	return rp
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
				errMsg := err.Error()
				log.WithFields(log.Fields{
					"account_id": row.ID,
					"email":      row.Email,
					"purpose":    row.Purpose,
					"scope":      "agent_user",
					"error":      errMsg,
				}).Warn("Google account refresh failed")
				_ = rp.persistence.UpdateGoogleAccountStatus(row.ID, "error", &errMsg)
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
				errMsg := err.Error()
				log.WithFields(log.Fields{
					"account_id": row.ID,
					"email":      row.Email,
					"purpose":    row.Purpose,
					"scope":      "trigger",
					"error":      errMsg,
				}).Warn("Google account refresh failed")
				_ = rp.persistence.UpdateTriggerGoogleAccountStatus(row.ID, "error", &errMsg)
			}
		}
	}
}

func (rp *GoogleAccountRefreshPoller) refreshAccount(row persistence.GoogleAccountRefreshRow, isTrigger bool) error {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {row.RefreshToken},
		"client_id":     {rp.clientID},
		"client_secret": {rp.clientSecret},
	}

	req, err := http.NewRequest("POST", googleTokenURL, strings.NewReader(data.Encode()))
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
