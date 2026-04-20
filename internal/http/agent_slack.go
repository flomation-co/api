package http

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// requiredSlackScopes lists all OAuth scopes needed for the full set of
// Slack actions available in Flomation.
var requiredSlackScopes = []struct {
	Scope       string `json:"scope"`
	Description string `json:"description"`
	Actions     string `json:"actions"`
}{
	{"chat:write", "Send messages", "messaging/slack, slack_rich_message"},
	{"channels:history", "Read public channel messages", "trigger/slack (Events API)"},
	{"groups:history", "Read private channel messages", "trigger/slack (Events API)"},
	{"im:history", "Read direct messages", "trigger/slack (Events API)"},
	{"mpim:history", "Read group DMs", "trigger/slack (Events API)"},
	{"app_mentions:read", "Detect @mentions", "trigger/slack (Events API)"},
	{"users:read", "List workspace members", "slack_users, slack_user_profile"},
	{"users:read.email", "See member email addresses", "slack_user_profile"},
	{"search:read", "Search messages", "slack_search"},
	{"channels:read", "List channels", "slack_channels"},
	{"groups:read", "List private channels", "slack_channels (private)"},
	{"files:write", "Upload files", "slack_file_upload"},
}

func (s *Service) checkSlackPermissions(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	agentID := c.Param("id")
	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if !s.canAccessAgent(user, agent) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	// Extract bot token from the agent's Slack channel config.
	var channels []struct {
		Type   string                 `json:"type"`
		Config map[string]interface{} `json:"config"`
	}
	if err := json.Unmarshal(agent.Channels, &channels); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"ok":    false,
			"error": "unable to parse agent channels config",
		})
		return
	}

	var botToken string
	for _, ch := range channels {
		if ch.Type == "slack" {
			if tok, ok := ch.Config["bot_token"].(string); ok {
				botToken = tok
			}
			break
		}
	}

	if botToken == "" {
		c.JSON(http.StatusOK, gin.H{
			"ok":    false,
			"error": "no Slack bot token configured",
		})
		return
	}

	// Call Slack's auth.test to verify the token and get scopes.
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost,
		"https://slack.com/api/auth.test", nil)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "request error"})
		return
	}
	req.Header.Set("Authorization", "Bearer "+botToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "Slack API unreachable: " + err.Error()})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))

	var authResult map[string]interface{}
	if err := json.Unmarshal(body, &authResult); err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "invalid Slack response"})
		return
	}

	if ok, _ := authResult["ok"].(bool); !ok {
		errMsg, _ := authResult["error"].(string)
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "Slack auth failed: " + errMsg})
		return
	}

	// Scopes are in the response header.
	grantedScopesRaw := resp.Header.Get("X-OAuth-Scopes")
	grantedSet := make(map[string]bool)
	for _, scope := range strings.Split(grantedScopesRaw, ",") {
		grantedSet[strings.TrimSpace(scope)] = true
	}

	teamName, _ := authResult["team"].(string)
	botUser, _ := authResult["user"].(string)
	botUserID, _ := authResult["user_id"].(string)
	botID, _ := authResult["bot_id"].(string)

	// Resolve app_id via bots.info so we can link to the Slack admin page.
	var appID string
	if botID != "" {
		botReq, _ := http.NewRequestWithContext(c.Request.Context(), http.MethodGet,
			"https://slack.com/api/bots.info?bot="+botID, nil)
		if botReq != nil {
			botReq.Header.Set("Authorization", "Bearer "+botToken)
			if botResp, err := http.DefaultClient.Do(botReq); err == nil {
				defer func() { _ = botResp.Body.Close() }()
				botBody, _ := io.ReadAll(io.LimitReader(botResp.Body, 8*1024))
				var botResult map[string]interface{}
				if json.Unmarshal(botBody, &botResult) == nil {
					if bot, ok := botResult["bot"].(map[string]interface{}); ok {
						appID, _ = bot["app_id"].(string)
					}
				}
			}
		}
	}

	type scopeCheck struct {
		Scope       string `json:"scope"`
		Description string `json:"description"`
		Actions     string `json:"actions"`
		Granted     bool   `json:"granted"`
	}

	var scopes []scopeCheck
	allGranted := true
	for _, req := range requiredSlackScopes {
		granted := grantedSet[req.Scope]
		if !granted {
			allGranted = false
		}
		scopes = append(scopes, scopeCheck{
			Scope:       req.Scope,
			Description: req.Description,
			Actions:     req.Actions,
			Granted:     granted,
		})
	}

	log.WithFields(log.Fields{
		"agent_id":    agentID,
		"team":        teamName,
		"bot_user":    botUser,
		"all_granted": allGranted,
		"granted":     grantedScopesRaw,
	}).Info("Slack permissions check completed")

	result := gin.H{
		"ok":          true,
		"team":        teamName,
		"bot_user":    botUser,
		"bot_user_id": botUserID,
		"scopes":      scopes,
		"all_granted": allGranted,
	}
	if appID != "" {
		result["app_id"] = appID
		result["oauth_url"] = "https://api.slack.com/apps/" + appID + "/oauth"
	}

	c.JSON(http.StatusOK, result)
}
