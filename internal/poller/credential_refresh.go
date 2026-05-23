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

// CredentialRefreshPersistence defines the DB methods the refresh poller needs.
type CredentialRefreshPersistence interface {
	GetCredentialsNeedingRefresh(within time.Duration) ([]persistence.CredentialRefreshRow, error)
	StoreCredentialTokens(id, environmentKey, accessToken, refreshToken, clientID, clientSecret string, expiresAt *time.Time) error
	UpdateCredentialStatus(id, status string, lastError *string) error
}

// CredentialRefreshPoller proactively refreshes OAuth tokens before they expire.
type CredentialRefreshPoller struct {
	persistence CredentialRefreshPersistence
	client      *http.Client
}

// StartCredentialRefreshPoller creates and starts the refresh poller goroutine.
func StartCredentialRefreshPoller(p CredentialRefreshPersistence) *CredentialRefreshPoller {
	rp := &CredentialRefreshPoller{
		persistence: p,
		client:      &http.Client{Timeout: 15 * time.Second},
	}
	go rp.watch()
	return rp
}

func (rp *CredentialRefreshPoller) watch() {
	time.Sleep(10 * time.Second)

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	log.Info("credential refresh poller started (60s interval)")

	for range ticker.C {
		rp.poll()
	}
}

func (rp *CredentialRefreshPoller) poll() {
	// Find credentials expiring within the next 5 minutes
	rows, err := rp.persistence.GetCredentialsNeedingRefresh(5 * time.Minute)
	if err != nil {
		log.WithError(err).Warn("credential refresh: failed to query expiring credentials")
		return
	}

	for _, row := range rows {
		if row.RefreshToken == nil || *row.RefreshToken == "" {
			log.WithField("credential_id", row.ID).Debug("credential has no refresh token, skipping")
			continue
		}

		if err := rp.refreshToken(row); err != nil {
			errMsg := err.Error()
			log.WithFields(log.Fields{
				"credential_id": row.ID,
				"provider":      row.ProviderSlug,
				"error":         errMsg,
			}).Warn("credential refresh failed")
			_ = rp.persistence.UpdateCredentialStatus(row.ID, "error", &errMsg)
		}
	}
}

func (rp *CredentialRefreshPoller) refreshToken(row persistence.CredentialRefreshRow) error {
	clientID := ""
	if row.ClientID != nil {
		clientID = *row.ClientID
	}
	clientSecret := ""
	if row.ClientSecret != nil {
		clientSecret = *row.ClientSecret
	}

	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {*row.RefreshToken},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}

	req, err := http.NewRequest("POST", row.TokenURL, strings.NewReader(data.Encode()))
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
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
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

	// Use the new refresh token if provided, otherwise keep the existing one
	newRefresh := tokenResp.RefreshToken

	if err := rp.persistence.StoreCredentialTokens(
		row.ID, row.EnvironmentKey,
		tokenResp.AccessToken, newRefresh, clientID, clientSecret, expiresAt,
	); err != nil {
		return err
	}

	log.WithFields(log.Fields{
		"credential_id": row.ID,
		"provider":      row.ProviderSlug,
		"expires_in":    tokenResp.ExpiresIn,
	}).Info("credential token refreshed")

	return nil
}

type refreshError struct {
	StatusCode int
	Body       string
}

func (e *refreshError) Error() string {
	if e.StatusCode > 0 {
		return "token refresh returned " + http.StatusText(e.StatusCode) + ": " + e.Body
	}
	return e.Body
}
