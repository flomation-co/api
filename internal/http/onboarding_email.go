package http

// The unsubscribe endpoint for onboarding email.
//
// Public — no JWT. The reader is in a mail client, not signed in, and
// requiring a login to stop email would be both hostile and, for somebody who
// has abandoned the account, impossible.
//
// The token is the only credential, and the only thing it authorises is
// stopping email. It cannot read anything, and it is a random UUID, so an
// enumeration attack buys an attacker the ability to unsubscribe strangers
// from three instructional emails.

import (
	"html/template"
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// unsubscribePage is deliberately two-step on GET.
//
// Mail clients and security appliances follow links in messages to scan them.
// If GET performed the unsubscribe, a scanner would silently opt people out of
// email they never chose to stop. So GET asks, and the button POSTs.
//
// Clients that implement RFC 8058 POST directly using the List-Unsubscribe-Post
// header, never see this page, and get the one-click behaviour they expect.
var unsubscribePage = template.Must(template.New("unsub").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta name="robots" content="noindex">
<title>{{.Title}}</title>
<style>
body { margin:0; font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif; background:#f4f4f7; color:#1a1a1a; }
main { max-width:520px; margin:12vh auto; background:#fff; border-radius:12px; padding:40px; box-shadow:0 4px 10px rgba(0,0,0,.05); }
h1 { font-size:22px; margin:0 0 14px; }
p { font-size:15px; line-height:1.6; color:#4b5563; margin:0 0 22px; }
button { font:inherit; font-weight:600; color:#fff; background:#460070; border:0; border-radius:8px; padding:13px 26px; cursor:pointer; }
small { display:block; margin-top:26px; font-size:12px; color:#9ca3af; }
</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
<p>{{.Body}}</p>
{{if .ShowButton}}<form method="POST"><button type="submit">Stop these emails</button></form>{{end}}
<small>Flomation Ltd</small>
</main>
</body>
</html>
`))

type unsubscribeView struct {
	Title      string
	Body       string
	ShowButton bool
}

func (s *Service) renderUnsubscribe(c *gin.Context, status int, v unsubscribeView) {
	c.Status(status)
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := unsubscribePage.Execute(c.Writer, v); err != nil {
		log.WithError(err).Error("unable to render unsubscribe page")
	}
}

// getOnboardingEmailUnsubscribe shows the confirmation page.
//
// It does not check the token. Answering "that link is not valid" here would
// turn the page into an oracle for which tokens exist, and the POST handler
// has to validate anyway.
func (s *Service) getOnboardingEmailUnsubscribe(c *gin.Context) {
	s.renderUnsubscribe(c, http.StatusOK, unsubscribeView{
		Title:      "Stop onboarding emails",
		Body:       "These are the short how-to emails sent after you create an account. Stopping them will not affect anything else — you will still get emails about your own flows and your account.",
		ShowButton: true,
	})
}

// postOnboardingEmailUnsubscribe performs the opt-out.
//
// The response is the same whether or not the token matched. A person who
// clicked a stale link has nothing to do differently, and an unknown token is
// not worth confirming as unknown.
func (s *Service) postOnboardingEmailUnsubscribe(c *gin.Context) {
	token := c.Param("token")

	found, err := s.persistence.OptOutOfOnboardingEmails(token)
	if err != nil {
		log.WithError(err).Error("unable to process onboarding email unsubscribe")
		s.renderUnsubscribe(c, http.StatusInternalServerError, unsubscribeView{
			Title: "Something went wrong",
			Body:  "We could not update your preferences just now. Please try again in a few minutes.",
		})
		return
	}
	if !found {
		log.Debug("onboarding email unsubscribe: token did not match")
	}

	s.renderUnsubscribe(c, http.StatusOK, unsubscribeView{
		Title: "Done",
		Body:  "You will not get any more onboarding emails.",
	})
}
