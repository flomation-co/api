package http

import (
	"encoding/json"
	"fmt"
	"io"
	gohttp "net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
)

const twilioAPIBase = "https://api.twilio.com/2010-04-01"

type twilioVerifyCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

type twilioVerifyResult struct {
	OK            bool                `json:"ok"`
	Error         string              `json:"error,omitempty"`
	AccountName   string              `json:"account_name,omitempty"`
	AccountStatus string              `json:"account_status,omitempty"`
	Checks        []twilioVerifyCheck `json:"checks"`
}

// checkTwilioCredentials handles GET /api/v1/agent/:id/twilio-verify
// Verifies Twilio Account SID, Auth Token, and phone number configuration.
func (s *Service) checkTwilioCredentials(c *gin.Context) {
	user := s.getUserFromContext(c)
	if user == nil {
		c.AbortWithStatus(gohttp.StatusUnauthorized)
		return
	}

	agentID := c.Param("id")
	agent, err := s.persistence.GetAgentByID(agentID)
	if err != nil || agent == nil {
		c.JSON(gohttp.StatusOK, twilioVerifyResult{
			OK:    false,
			Error: "Agent not found",
		})
		return
	}

	// Extract Twilio config from agent channels
	accountSID, authToken, phoneNumber := extractTwilioConfig(agent.Channels)
	if accountSID == "" || authToken == "" {
		c.JSON(gohttp.StatusOK, twilioVerifyResult{
			OK:    false,
			Error: "No Twilio channel configured — add a Twilio SMS or Voice channel first",
		})
		return
	}

	result := verifyTwilioCredentials(accountSID, authToken, phoneNumber)
	c.JSON(gohttp.StatusOK, result)
}

func extractTwilioConfig(channels json.RawMessage) (accountSID, authToken, phoneNumber string) {
	var channelList []struct {
		Type   string `json:"type"`
		Config struct {
			AccountSID  string `json:"account_sid"`
			AuthToken   string `json:"auth_token"`
			PhoneNumber string `json:"phone_number"`
		} `json:"config"`
	}
	if err := json.Unmarshal(channels, &channelList); err != nil {
		return "", "", ""
	}
	for _, ch := range channelList {
		if ch.Type == "twilio_sms" || ch.Type == "twilio_voice" {
			if ch.Config.AccountSID != "" && ch.Config.AuthToken != "" {
				return ch.Config.AccountSID, ch.Config.AuthToken, ch.Config.PhoneNumber
			}
		}
	}
	return "", "", ""
}

func verifyTwilioCredentials(accountSID, authToken, phoneNumber string) *twilioVerifyResult {
	result := &twilioVerifyResult{OK: true, Checks: []twilioVerifyCheck{}}
	client := &gohttp.Client{Timeout: 10 * time.Second}

	// Check 1: Verify credentials
	accountURL := fmt.Sprintf("%s/Accounts/%s.json", twilioAPIBase, accountSID)
	req, _ := gohttp.NewRequest(gohttp.MethodGet, accountURL, nil)
	req.SetBasicAuth(accountSID, authToken)

	resp, err := client.Do(req)
	if err != nil {
		result.OK = false
		result.Error = "Unable to reach Twilio API"
		result.Checks = append(result.Checks, twilioVerifyCheck{
			Name: "Credentials", Passed: false,
			Detail: fmt.Sprintf("Connection failed: %v", err),
		})
		return result
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))

	if resp.StatusCode == 401 {
		result.OK = false
		result.Error = "Invalid credentials"
		result.Checks = append(result.Checks, twilioVerifyCheck{
			Name: "Credentials", Passed: false,
			Detail: "Authentication failed — check Account SID and Auth Token",
		})
		return result
	}
	if resp.StatusCode != 200 {
		result.OK = false
		result.Checks = append(result.Checks, twilioVerifyCheck{
			Name: "Credentials", Passed: false,
			Detail: fmt.Sprintf("Twilio returned status %d", resp.StatusCode),
		})
		return result
	}

	var account struct {
		FriendlyName string `json:"friendly_name"`
		Status       string `json:"status"`
	}
	_ = json.Unmarshal(body, &account)
	result.AccountName = account.FriendlyName
	result.AccountStatus = account.Status

	credOK := account.Status == "active"
	result.Checks = append(result.Checks, twilioVerifyCheck{
		Name: "Credentials", Passed: credOK,
		Detail: fmt.Sprintf("%s (%s)", account.FriendlyName, account.Status),
	})
	if !credOK {
		result.OK = false
	}

	// Check 2: Phone number lookup
	if phoneNumber == "" {
		result.Checks = append(result.Checks, twilioVerifyCheck{
			Name: "Phone Number", Passed: false,
			Detail: "No phone number configured",
		})
		result.OK = false
		return result
	}

	phoneURL := fmt.Sprintf("%s/Accounts/%s/IncomingPhoneNumbers.json?PhoneNumber=%s",
		twilioAPIBase, accountSID, url.QueryEscape(phoneNumber))
	req2, _ := gohttp.NewRequest(gohttp.MethodGet, phoneURL, nil)
	req2.SetBasicAuth(accountSID, authToken)

	resp2, err := client.Do(req2)
	if err != nil {
		result.Checks = append(result.Checks, twilioVerifyCheck{
			Name: "Phone Number", Passed: false,
			Detail: fmt.Sprintf("Lookup failed: %v", err),
		})
		result.OK = false
		return result
	}
	defer func() { _ = resp2.Body.Close() }()
	body2, _ := io.ReadAll(io.LimitReader(resp2.Body, 8192))

	var phoneResult struct {
		IncomingPhoneNumbers []struct {
			PhoneNumber  string `json:"phone_number"`
			FriendlyName string `json:"friendly_name"`
			Capabilities struct {
				Voice bool `json:"voice"`
				SMS   bool `json:"sms"`
				MMS   bool `json:"mms"`
			} `json:"capabilities"`
			VoiceURL string `json:"voice_url"`
			SMSURL   string `json:"sms_url"`
		} `json:"incoming_phone_numbers"`
	}
	_ = json.Unmarshal(body2, &phoneResult)

	if len(phoneResult.IncomingPhoneNumbers) == 0 {
		result.Checks = append(result.Checks, twilioVerifyCheck{
			Name: "Phone Number", Passed: false,
			Detail: fmt.Sprintf("%s not found in this account", phoneNumber),
		})
		result.OK = false
		return result
	}

	phone := phoneResult.IncomingPhoneNumbers[0]
	result.Checks = append(result.Checks, twilioVerifyCheck{
		Name: "Phone Number", Passed: true,
		Detail: fmt.Sprintf("%s (%s)", phone.PhoneNumber, phone.FriendlyName),
	})

	// Check 3: Voice capability
	result.Checks = append(result.Checks, twilioVerifyCheck{
		Name: "Voice Capability", Passed: phone.Capabilities.Voice,
		Detail: ternary(phone.Capabilities.Voice, "Voice calls enabled", "Voice not available on this number"),
	})

	// Check 4: SMS capability
	result.Checks = append(result.Checks, twilioVerifyCheck{
		Name: "SMS Capability", Passed: phone.Capabilities.SMS,
		Detail: ternary(phone.Capabilities.SMS, "SMS messaging enabled", "SMS not available on this number"),
	})

	// Check 5: Voice webhook
	if phone.VoiceURL != "" {
		result.Checks = append(result.Checks, twilioVerifyCheck{
			Name: "Voice Webhook", Passed: true,
			Detail: phone.VoiceURL,
		})
	} else {
		result.Checks = append(result.Checks, twilioVerifyCheck{
			Name: "Voice Webhook", Passed: false,
			Detail: "Not configured — set in Twilio Console",
		})
	}

	// Check 6: SMS webhook
	if phone.SMSURL != "" {
		result.Checks = append(result.Checks, twilioVerifyCheck{
			Name: "SMS Webhook", Passed: true,
			Detail: phone.SMSURL,
		})
	} else {
		result.Checks = append(result.Checks, twilioVerifyCheck{
			Name: "SMS Webhook", Passed: false,
			Detail: "Not configured — set in Twilio Console",
		})
	}

	return result
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
