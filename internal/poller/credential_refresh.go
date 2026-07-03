package poller

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/persistence"
	log "github.com/sirupsen/logrus"
)

// CredentialRefreshPersistence defines the DB methods the refresh poller needs.
type CredentialRefreshPersistence interface {
	GetCredentialsNeedingRefresh(within time.Duration) ([]persistence.CredentialRefreshRow, error)
	StoreCredentialTokens(id, environmentKey, accessToken, refreshToken, clientID, clientSecret string, expiresAt *time.Time) error
	UpdateCredentialStatus(id, status string, lastError *string) error
	RecordCredentialRefreshFailure(id, lastError string, permanent bool, threshold int) (string, error)
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
			permanent := classifyRefreshError(err)
			newStatus, dbErr := rp.persistence.RecordCredentialRefreshFailure(
				row.ID, errMsg, permanent, MaxConsecutiveRefreshFailures)
			if dbErr != nil {
				log.WithError(dbErr).Warn("credential refresh: failed to record failure")
			}
			fields := log.Fields{
				"credential_id": row.ID,
				"provider":      row.ProviderSlug,
				"permanent":     permanent,
				"new_status":    newStatus,
				"error":         errMsg,
			}
			if newStatus == "revoked" {
				log.WithFields(fields).Error("credential marked revoked — refresh attempts will stop until the user re-authorises")
			} else {
				log.WithFields(fields).Warn("credential refresh failed")
			}
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

	// Substitute per-tenant URL variables (e.g. the shop subdomain) into the
	// token URL. Fixed-URL providers are unaffected; per-tenant providers that
	// actually expire (unlike Shopify's permanent tokens) refresh correctly.
	tokenURL, err := api.SubstituteURLVariables(row.TokenURL, api.URLVarsFromMetadata(row.Metadata))
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(data.Encode()))
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

// MaxConsecutiveRefreshFailures is the upper bound on transient
// failures we'll tolerate before treating a credential as permanently
// broken and surrendering. At a 60s poll interval, 10 failures is
// about 10 minutes of OAuth provider unavailability — comfortably
// long enough to ride out a typical outage but short enough that a
// truly dead token doesn't keep generating warnings forever.
const MaxConsecutiveRefreshFailures = 10

// permanentRefreshErrorMarkers are substrings the OAuth providers we
// integrate with use to signal that the refresh token is unusable
// and will never work again until the user re-grants consent. We
// match on the raw body so we don't have to parse a JSON shape that
// varies across providers — Google returns the marker as
// `"error":"invalid_grant"`, generic OAuth providers do the same.
var permanentRefreshErrorMarkers = []string{
	`"invalid_grant"`,       // refresh token was revoked, expired, or never valid
	`"unauthorized_client"`, // OAuth client id changed under the refresh token
	`"invalid_client"`,      // OAuth client deleted / secret rotated
}

// IsPermanent reports whether the refresh failure should immediately
// transition the credential to status='revoked' instead of being
// counted against the consecutive-failure threshold. A nil receiver
// is treated as transient (network errors, malformed JSON) — those
// are the textbook case for retrying.
func (e *refreshError) IsPermanent() bool {
	if e == nil {
		return false
	}
	for _, marker := range permanentRefreshErrorMarkers {
		if strings.Contains(e.Body, marker) {
			return true
		}
	}
	return false
}

// classifyRefreshError reports whether err is a permanent OAuth
// failure. Wraps the type-assertion so callers can pass plain `error`
// without having to know about refreshError's concrete shape.
func classifyRefreshError(err error) bool {
	if err == nil {
		return false
	}
	if rerr, ok := err.(*refreshError); ok {
		return rerr.IsPermanent()
	}
	return false
}
