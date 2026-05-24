package http

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// checkFacebookWebhook handles GET /api/v1/environment/:environment/facebook-webhook-check/:credentialName/:pageId.
// Verifies that:
// 1. The credential resolves to a valid token
// 2. The app has webhook subscriptions configured
// 3. The specific page is subscribed to the app
func (s *Service) checkFacebookWebhook(c *gin.Context) {
	envID := c.Param("environment")
	credentialName := c.Param("credentialName")
	pageID := c.Param("pageId")

	env, err := s.persistence.GetEnvironmentByIDDirect(envID)
	if err != nil || env == nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "Environment not found"})
		return
	}

	// Resolve user access token
	accessToken, err := s.persistence.GetCredentialByName(envID, credentialName, env.SecretKey)
	if err != nil || accessToken == nil || *accessToken == "" {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "Credential not found or has no access token"})
		return
	}

	// Look up app secret
	appSecret := ""
	sec, _ := s.persistence.GetEnvironmentSecretByName(envID, env.SecretKey, "FACEBOOK_APP_SECRET")
	if sec != nil {
		appSecret = sec.Value
	}
	if appSecret == "" {
		sec, _ = s.persistence.GetEnvironmentSecretByName(envID, env.SecretKey, "facebook_app_secret")
		if sec != nil {
			appSecret = sec.Value
		}
	}

	// Get the app ID from the OAuth config
	appID := ""
	if s.config.OAuth != nil {
		if fb, ok := s.config.OAuth["facebook"]; ok {
			appID = fb.ClientID
		}
	}

	client := &http.Client{Timeout: 15 * time.Second}
	type checkResult struct {
		Name    string `json:"name"`
		Status  string `json:"status"` // "ok", "error", "warning"
		Detail  string `json:"detail"`
	}

	var checks []checkResult

	// Check 1: Token validity — call /me to verify
	tokenURL := appendFBProof(
		fmt.Sprintf("https://graph.facebook.com/v19.0/me?access_token=%s&fields=id,name", *accessToken),
		appSecret, *accessToken,
	)
	if resp, err := client.Get(tokenURL); err == nil { // #nosec G107
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		if resp.StatusCode == http.StatusOK {
			var user struct{ Name string `json:"name"` }
			_ = json.Unmarshal(body, &user)
			checks = append(checks, checkResult{"User Token", "ok", fmt.Sprintf("Authenticated as %s", user.Name)})
		} else {
			checks = append(checks, checkResult{"User Token", "error", "Token is invalid or expired"})
			c.JSON(http.StatusOK, gin.H{"ok": false, "checks": checks, "error": "Token is invalid"})
			return
		}
	} else {
		checks = append(checks, checkResult{"User Token", "error", "Failed to connect to Facebook"})
	}

	// Check 2: Page access — verify the user manages this page
	pagesURL := appendFBProof(
		fmt.Sprintf("https://graph.facebook.com/v19.0/me/accounts?access_token=%s&fields=id,name", *accessToken),
		appSecret, *accessToken,
	)
	pageFound := false
	pageName := ""
	if resp, err := client.Get(pagesURL); err == nil { // #nosec G107
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var result struct {
			Data []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"data"`
		}
		_ = json.Unmarshal(body, &result)
		for _, p := range result.Data {
			if p.ID == pageID {
				pageFound = true
				pageName = p.Name
				break
			}
		}
		if pageFound {
			checks = append(checks, checkResult{"Page Access", "ok", fmt.Sprintf("Page \"%s\" found", pageName)})
		} else {
			checks = append(checks, checkResult{"Page Access", "error", fmt.Sprintf("Page %s not found in managed pages (%d available)", pageID, len(result.Data))})
		}
	}

	// Check 3: Page subscription — check if the page is subscribed to the app
	if pageFound {
		// Get page token first
		pageToken := ""
		ptURL := appendFBProof(
			fmt.Sprintf("https://graph.facebook.com/v19.0/me/accounts?access_token=%s", *accessToken),
			appSecret, *accessToken,
		)
		if resp, err := client.Get(ptURL); err == nil { // #nosec G107
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			var result struct {
				Data []struct {
					ID          string `json:"id"`
					AccessToken string `json:"access_token"`
				} `json:"data"`
			}
			_ = json.Unmarshal(body, &result)
			for _, p := range result.Data {
				if p.ID == pageID {
					pageToken = p.AccessToken
					break
				}
			}
		}

		if pageToken != "" {
			subURL := appendFBProof(
				fmt.Sprintf("https://graph.facebook.com/v19.0/%s/subscribed_apps?access_token=%s", pageID, pageToken),
				appSecret, pageToken,
			)
			if resp, err := client.Get(subURL); err == nil { // #nosec G107
				defer func() { _ = resp.Body.Close() }()
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
				var result struct {
					Data []struct {
						ID              string   `json:"id"`
						SubscribedFields []string `json:"subscribed_fields"`
					} `json:"data"`
				}
				_ = json.Unmarshal(body, &result)

				if len(result.Data) > 0 {
					fields := result.Data[0].SubscribedFields
					checks = append(checks, checkResult{"Page Subscription", "ok", fmt.Sprintf("Subscribed to: %v", fields)})
				} else {
					checks = append(checks, checkResult{"Page Subscription", "warning", "Page is not subscribed to any app webhook events. Save the flow to auto-subscribe."})
				}
			}
		}
	}

	// Check 4: App webhook — check if the app has webhook subscriptions
	if appID != "" {
		appToken := ""
		if s.config.OAuth != nil {
			if fb, ok := s.config.OAuth["facebook"]; ok && fb.ClientSecret != "" {
				appToken = fb.ClientID + "|" + fb.ClientSecret
			}
		}
		if appToken != "" {
			subURL := fmt.Sprintf("https://graph.facebook.com/v19.0/%s/subscriptions?access_token=%s", appID, appToken)
			if resp, err := client.Get(subURL); err == nil { // #nosec G107
				defer func() { _ = resp.Body.Close() }()
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
				var result struct {
					Data []struct {
						Object    string `json:"object"`
						Active    bool   `json:"active"`
						CallbackURL string `json:"callback_url"`
					} `json:"data"`
				}
				_ = json.Unmarshal(body, &result)

				pageWebhook := false
				for _, sub := range result.Data {
					if sub.Object == "page" {
						pageWebhook = true
						if sub.Active {
							checks = append(checks, checkResult{"App Webhook", "ok", fmt.Sprintf("Active — %s", sub.CallbackURL)})
						} else {
							checks = append(checks, checkResult{"App Webhook", "warning", "Page webhook subscription exists but is not active"})
						}
						break
					}
				}
				if !pageWebhook {
					checks = append(checks, checkResult{"App Webhook", "error", "No Page webhook subscription found in App Dashboard"})
				}
			}
		} else {
			log.Warn("cannot check app webhook subscriptions: no Facebook OAuth config")
		}
	}

	allOk := true
	for _, ch := range checks {
		if ch.Status == "error" {
			allOk = false
			break
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":     allOk,
		"checks": checks,
		"page_name": pageName,
		"app_dashboard_url": fmt.Sprintf("https://developers.facebook.com/apps/%s/webhooks/", appID),
	})
}

func appendFBProof(apiURL, appSecret, accessToken string) string {
	if appSecret == "" {
		return apiURL
	}
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write([]byte(accessToken))
	return apiURL + "&appsecret_proof=" + hex.EncodeToString(mac.Sum(nil))
}
