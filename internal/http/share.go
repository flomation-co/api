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
			"<p style=\"font-size:15px;line-height:1.6;color:#6b7280;margin:0 0 25px;padding:16px;background:#f9fafb;border-left:3px solid #460070;border-radius:4px;\"><em>&ldquo;%s&rdquo;</em></p>",
			html.EscapeString(req.Message),
		)
	}

	body := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Flomation</title>
<style>
body { margin: 0; padding: 0; background-color: #f4f4f7; font-family: 'Helvetica Neue', Helvetica, Arial, sans-serif; -webkit-font-smoothing: antialiased; }
table { border-spacing: 0; }
td { padding: 0; }
@media screen and (max-width: 600px) {
    .content { width: 100%% !important; border-radius: 0 !important; }
    .wrapper { padding: 10px !important; }
}
</style>
</head>
<body>
<table width="100%%" border="0" cellspacing="0" cellpadding="0" bgcolor="#f4f4f7" class="wrapper" style="padding: 40px 0;">
<tr><td align="center">
<table width="600" border="0" cellspacing="0" cellpadding="0" class="content" style="background-color: #fff; border-radius: 12px; overflow: hidden; box-shadow: 0 4px 10px rgba(0,0,0,0.05);">

<tr><td style="padding: 40px 40px 20px 40px; text-align: left;">
<a href="https://www.flomation.app"><img width="300" height="84" src="https://flomation-dev-static.s3.eu-west-2.amazonaws.com/flomation_logo_purple_300px.png" alt="Flomation" title="Flomation"/></a>
</td></tr>

<tr><td style="padding: 20px 40px 40px 40px;">
<h1 style="font-size: 28px; color: #1a1a1a; margin: 0 0 20px 0; line-height: 1.2;">
You've been invited to Flomation
</h1>
<p style="font-size: 16px; line-height: 1.6; color: #4b5563; margin: 0 0 25px 0;">
Hello there,<br><br>
%s thinks you'd love Flomation — a powerful workflow automation platform that connects your tools, teams, and processes into seamless flows. With a drag-and-drop editor, 100+ connectors, and AI-powered agents, you can automate anything.
</p>
%s
<table border="0" cellspacing="0" cellpadding="0">
<tr><td align="center" bgcolor="#460070" style="border-radius: 8px;">
<a href="https://www.flomation.co" target="_blank" style="font-size: 16px; font-weight: bold; color: #ffffff; text-decoration: none; padding: 14px 30px; display: inline-block;">
Get Started
</a>
</td></tr>
</table>
</td></tr>

<tr><td style="padding: 0 40px;">
<hr style="border: 0; border-top: 1px solid #e5e7eb; margin: 0;">
</td></tr>

<tr><td style="padding: 30px 40px 40px 40px; background-color: #fafafa;">
<table width="100%%" border="0" cellspacing="0" cellpadding="0">
<tr><td style="font-size: 10px; color: #9ca3af; line-height: 1.5;">
<strong>Flomation Ltd</strong><br>
Ruscoe House, The Chequer, Whitchurch, Wrexham, Wales, SY13 2JJ<br /><br />
Sent by %s via Flomation<br/><br/>
</td></tr>
</table>
</td></tr>

</table>
<p style="font-size: 12px; color: #9ca3af; margin-top: 20px;">
© 2026 Flomation Ltd. All rights reserved.
</p>
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
