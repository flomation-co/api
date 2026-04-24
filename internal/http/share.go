package http

import (
	"fmt"
	"html"
	"net/http"
	"net/smtp"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type shareEmailRequest struct {
	To      string `json:"to" binding:"required"`
	Message string `json:"message,omitempty"`
}

func (s *Service) sendShareEmail(c *gin.Context) {
	u := s.getUserFromContext(c)
	if u == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var req shareEmailRequest
	if err := c.BindJSON(&req); err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Basic email validation
	if !strings.Contains(req.To, "@") || !strings.Contains(req.To, ".") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email address"})
		return
	}

	senderName := u.Name
	if senderName == "" || senderName == "auto-generate" {
		senderName = "A colleague"
	}

	personalMessage := ""
	if req.Message != "" {
		personalMessage = fmt.Sprintf(
			"<p style=\"color:#d1d5db;font-size:14px;line-height:1.6;margin:0 0 20px;\"><em>\"%s\"</em></p>",
			html.EscapeString(req.Message),
		)
	}

	body := fmt.Sprintf(`<!DOCTYPE html>
<html>
<body style="margin:0;padding:0;background:#0a0a0f;font-family:'Inter',Arial,sans-serif;">
<table width="100%%" cellpadding="0" cellspacing="0" style="background:#0a0a0f;padding:40px 20px;">
<tr><td align="center">
<table width="560" cellpadding="0" cellspacing="0" style="background:#1a1a2e;border-radius:16px;border:1px solid rgba(255,255,255,0.08);overflow:hidden;">
<tr><td style="padding:32px 40px 24px;">
<h1 style="margin:0 0 8px;font-size:22px;font-weight:700;color:#e5e7eb;">You've been invited to Flomation</h1>
<p style="margin:0 0 24px;font-size:14px;color:rgba(255,255,255,0.4);">%s thinks you'd love Flomation</p>
%s
<p style="color:#d1d5db;font-size:14px;line-height:1.6;margin:0 0 28px;">
Flomation is a powerful workflow automation platform that connects your tools, teams, and processes into seamless flows.
With a drag-and-drop editor, 100+ connectors, and AI-powered agents, you can automate anything.
</p>
<a href="https://www.flomation.co" style="display:inline-block;padding:12px 28px;background:#00aa9c;color:#fff;text-decoration:none;border-radius:8px;font-weight:600;font-size:14px;">Get Started</a>
</td></tr>
<tr><td style="padding:20px 40px;border-top:1px solid rgba(255,255,255,0.04);">
<p style="margin:0;font-size:11px;color:rgba(255,255,255,0.2);">Sent by %s via Flomation &middot; <a href="https://www.flomation.co" style="color:rgba(255,255,255,0.3);">www.flomation.co</a></p>
</td></tr>
</table>
</td></tr>
</table>
</body>
</html>`, html.EscapeString(senderName), personalMessage, html.EscapeString(senderName))

	subject := fmt.Sprintf("%s invited you to Flomation", senderName)

	addr := fmt.Sprintf("%s:%d", s.config.SMTP.Host, s.config.SMTP.Port)
	auth := smtp.PlainAuth("", s.config.SMTP.Username, s.config.SMTP.Password, s.config.SMTP.Host)

	msg := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=\"UTF-8\"\r\n\r\n%s",
		s.config.SMTP.From,
		req.To,
		subject,
		body,
	)

	if err := smtp.SendMail(addr, auth, s.config.SMTP.Username, []string{req.To}, []byte(msg)); err != nil {
		log.WithFields(log.Fields{
			"error": err,
			"to":    req.To,
		}).Error("unable to send share email")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send email"})
		return
	}

	log.WithFields(log.Fields{
		"from": u.ID,
		"to":   req.To,
	}).Info("share email sent")

	c.JSON(http.StatusOK, gin.H{"sent": true})
}
