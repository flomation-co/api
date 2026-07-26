package http

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	api "flomation.app/automate/api"
)

// xeroConnectionsURL is the endpoint that lists the organisations (tenants) an
// access token is authorised for. Overridable in tests.
var xeroConnectionsURL = "https://api.xero.com/connections"

// captureProviderTenant records the per-account identifier that only becomes
// known AFTER authorisation:
//
//   - QuickBooks returns the company id as the realmId query parameter on the
//     OAuth callback redirect.
//
//   - Xero doesn't return the tenant on the callback at all; the access token
//     must be exchanged for the list of authorised organisations via
//     GET /connections. A consent can cover several organisations, so all are
//     stored and the first is made active (the editor lets a multi-org user
//     switch; the executor reads the active tenant_id).
//
//   - Salesforce returns the org's API host as instance_url on the TOKEN
//     response — it is not on the callback redirect and is not derivable from
//     the login host, because it is per-org and changes on My Domain setup,
//     sandbox refresh or org migration.
//
// The identifier is written to the credential's metadata JSONB; the executor
// resolves it via ${credentials.<name>.realm_id|tenant_id|instance_url}.
// Capture failure is logged but NOT fatal — the tokens are already stored, and
// the user can re-authorise; failing the callback here would strand a valid
// token behind an error page.
// The metadata it merges into is RE-READ here rather than accepted from the
// caller. It used to take the caller's snapshot — loaded at the top of the OAuth
// callback — which meant anything written to the credential between that load
// and this merge was silently clobbered. A PKCE verifier cleared earlier in the
// same callback was resurrected exactly that way: the clear ran, this function
// then wrote back the pre-clear blob, and the spent secret stayed at rest.
//
// The ordering contract that used to fix it ("writers must run after this
// function") lived only in the callers' heads and in a comment. Re-reading makes
// the invariant LOCAL: this function is now correct regardless of what any caller
// did first, which matters because the same path serves quickbooks and xero and
// the next person adding a provider has no reason to know the rule existed.
//
// Cost is one extra read on the OAuth callback — not a hot path, once per
// connect.
func (s *Service) captureProviderTenant(c *gin.Context, credID, providerSlug string, tokenResp *oauthTokenResponse) {
	var kv map[string]interface{}
	accessToken := tokenResp.AccessToken

	switch providerSlug {
	case "quickbooks":
		realmID := c.Query("realmId")
		if realmID == "" {
			log.WithField("credential_id", credID).Warn("QuickBooks callback missing realmId — company not captured")
			return
		}
		// Whether this credential lives in Intuit's sandbox is a property of the
		// configured app keys (Development keys are sandbox-only), so it's read
		// from the provider config and stored on the credential. The executor
		// reads it to pick the API host — flow authors never toggle it.
		kv = map[string]interface{}{
			"realm_id": realmID,
			"sandbox":  s.providerIsSandbox("quickbooks"),
		}

	case "xero":
		conns, err := fetchXeroConnections(accessToken)
		if err != nil || len(conns) == 0 {
			log.WithFields(log.Fields{"credential_id": credID, "error": err}).
				Warn("unable to fetch Xero connections — tenant not captured")
			return
		}
		kv = map[string]interface{}{
			"tenant_id":   conns[0].TenantID,
			"tenant_name": conns[0].TenantName,
			"connections": conns,
		}

	case "salesforce":
		if tokenResp.InstanceURL == "" {
			log.WithField("credential_id", credID).
				Warn("Salesforce token response missing instance_url — org API host not captured")
			return
		}
		// Bind the node's Instance URL input to
		// ${credentials.<name>.instance_url} and the operator never has to find
		// this value themselves — which matters, because the host in their
		// browser's address bar is the Lightning one
		// (mycompany.lightning.force.com), not the API one, and pasting the
		// wrong one fails in a way that reads as a broken integration.
		kv = map[string]interface{}{
			"instance_url": tokenResp.InstanceURL,
		}

	default:
		return
	}

	// Fresh read, deliberately AFTER the provider switch above: doing it here
	// means the extra query only happens for providers that actually have
	// something to store, and it is as late as possible before the write.
	cred, err := s.persistence.GetCredentialByID(credID)
	if err != nil {
		log.WithFields(log.Fields{"credential_id": credID, "error": err}).
			Error("unable to re-read the credential before storing its tenant metadata")
		return
	}
	if cred == nil {
		log.WithField("credential_id", credID).
			Warn("credential vanished before its tenant metadata could be stored")
		return
	}

	merged, err := api.MergeMetadata(cred.Metadata, kv)
	if err != nil {
		log.WithFields(log.Fields{"credential_id": credID, "error": err}).Error("unable to build credential metadata")
		return
	}
	if err := s.persistence.UpdateCredentialMetadata(credID, merged); err != nil {
		log.WithFields(log.Fields{"credential_id": credID, "error": err}).Error("unable to persist credential tenant metadata")
	}
}

// xeroConnection is one authorised organisation from Xero's /connections list.
type xeroConnection struct {
	ID         string `json:"id"`
	TenantID   string `json:"tenantId"`
	TenantType string `json:"tenantType"`
	TenantName string `json:"tenantName"`
}

// fetchXeroConnections lists the organisations an access token can act on.
func fetchXeroConnections(accessToken string) ([]xeroConnection, error) {
	req, err := http.NewRequest(http.MethodGet, xeroConnectionsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xero /connections returned %d: %s", resp.StatusCode, string(body))
	}

	var conns []xeroConnection
	if err := json.Unmarshal(body, &conns); err != nil {
		return nil, fmt.Errorf("parse xero connections: %w", err)
	}
	return conns, nil
}
