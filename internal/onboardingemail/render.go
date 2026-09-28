package onboardingemail

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
)

// Rendered is a message ready to hand to an SMTP client.
type Rendered struct {
	Subject string
	HTML    string
	Text    string
	// UnsubscribeURL is repeated here so the caller can set the
	// List-Unsubscribe headers without re-deriving it.
	UnsubscribeURL string
}

// Links is everything about the recipient and the environment that the copy
// does not carry itself.
type Links struct {
	// AppURL is the editor's base URL, no trailing slash.
	AppURL string
	// UnsubscribeURL stops the rest of the sequence. It is a POST target as
	// well as a page: see the HTML below and the handler it points at.
	UnsubscribeURL string
	// Greeting is the recipient's name, or empty for no name at all. An email
	// that opens "Hi auto-generate" is worse than one that opens "Hi there".
	Greeting string
}

// brandPurple and the logo are the two brand assets an email can rely on.
// Mail clients strip <style> blocks and external stylesheets unpredictably,
// so everything here is an inline attribute on a table — the one layout that
// still renders the same in Outlook as in Gmail.
const (
	brandPurple = "#460070"
	logoURL     = "https://flomation-dev-static.s3.eu-west-2.amazonaws.com/flomation_logo_purple_300px.png"
)

// inc exists only so the step list can show 1,2,3 — range gives a 0-based
// index and text/template has no arithmetic.
var shell = template.Must(template.New("onboarding").
	Funcs(template.FuncMap{"inc": func(i int) int { return i + 1 }}).
	Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>{{.Heading}}</title>
<style>
body { margin:0; padding:0; background-color:#f4f4f7; font-family:'Helvetica Neue',Helvetica,Arial,sans-serif; -webkit-font-smoothing:antialiased; }
table { border-spacing:0; }
td { padding:0; }
@media screen and (max-width:600px) {
  .content { width:100% !important; border-radius:0 !important; }
  .wrapper { padding:10px !important; }
  .gutter { padding-left:24px !important; padding-right:24px !important; }
}
</style>
</head>
<body>
<table width="100%" border="0" cellspacing="0" cellpadding="0" bgcolor="#f4f4f7" class="wrapper" style="padding:40px 0;">
<tr><td align="center">
<table width="600" border="0" cellspacing="0" cellpadding="0" class="content" style="background-color:#ffffff;border-radius:12px;overflow:hidden;box-shadow:0 4px 10px rgba(0,0,0,0.05);">

<tr><td class="gutter" style="padding:40px 40px 20px 40px;text-align:left;">
<a href="{{.AppURL}}"><img width="240" height="67" src="{{.LogoURL}}" alt="Flomation" title="Flomation" style="display:block;border:0;"/></a>
</td></tr>

<tr><td class="gutter" style="padding:8px 40px 0 40px;">
<h1 style="font-size:26px;color:#1a1a1a;margin:0 0 18px 0;line-height:1.25;">{{.Heading}}</h1>
<p style="font-size:16px;line-height:1.65;color:#4b5563;margin:0 0 24px 0;">{{if .Greeting}}Hi {{.Greeting}},<br><br>{{end}}{{.Intro}}</p>
</td></tr>

<tr><td class="gutter" style="padding:0 40px;">
<table width="100%" border="0" cellspacing="0" cellpadding="0">
{{range $i, $s := .Steps}}<tr>
  <td width="34" valign="top" style="padding:0 0 18px 0;">
    <div style="width:26px;height:26px;border-radius:13px;background-color:{{$.BrandPurple}};color:#ffffff;font-size:13px;font-weight:bold;line-height:26px;text-align:center;">{{inc $i}}</div>
  </td>
  <td valign="top" style="padding:0 0 18px 0;font-size:15px;line-height:1.6;color:#4b5563;">{{$s}}</td>
</tr>{{end}}
</table>
</td></tr>

<tr><td class="gutter" style="padding:14px 40px 0 40px;">
<table border="0" cellspacing="0" cellpadding="0">
<tr><td align="center" bgcolor="{{.BrandPurple}}" style="border-radius:8px;">
<a href="{{.ButtonURL}}" target="_blank" style="font-size:16px;font-weight:bold;color:#ffffff;text-decoration:none;padding:14px 30px;display:inline-block;">{{.ButtonText}}</a>
</td></tr>
</table>
</td></tr>

<tr><td class="gutter" style="padding:26px 40px 34px 40px;">
<p style="font-size:15px;line-height:1.65;color:#6b7280;margin:0;">{{.Closing}}</p>
</td></tr>

<tr><td style="padding:0 40px;"><hr style="border:0;border-top:1px solid #e5e7eb;margin:0;"></td></tr>

<tr><td class="gutter" style="padding:24px 40px 34px 40px;background-color:#fafafa;">
<p style="font-size:12px;line-height:1.6;color:#9ca3af;margin:0 0 12px 0;">
You are getting this because you created a Flomation account. It is one of three short emails about using the product — no newsletter, and nothing to buy.
<a href="{{.UnsubscribeURL}}" style="color:#6b7280;">Stop these emails</a>.
</p>
<p style="font-size:11px;line-height:1.6;color:#9ca3af;margin:0;">
<strong>Flomation Ltd</strong><br>
Ruscoe House, The Chequer, Whitchurch, Wrexham, Wales, SY13 2JJ
</p>
</td></tr>

</table>
</td></tr>
</table>
</body>
</html>
`))

// Render produces both halves of the message.
//
// Every field the copy carries is escaped by html/template. Nothing in this
// package interpolates a caller's string into markup, which matters because
// the greeting comes from a user-editable profile name.
func Render(e Email, l Links) Rendered {
	appURL := strings.TrimRight(l.AppURL, "/")

	var buf bytes.Buffer
	_ = shell.Execute(&buf, struct {
		Email
		AppURL         string
		ButtonURL      string
		LogoURL        string
		BrandPurple    string
		UnsubscribeURL string
		Greeting       string
	}{
		Email:          e,
		AppURL:         appURL,
		ButtonURL:      appURL + e.Path,
		LogoURL:        logoURL,
		BrandPurple:    brandPurple,
		UnsubscribeURL: l.UnsubscribeURL,
		Greeting:       l.Greeting,
	})

	return Rendered{
		Subject:        e.Subject,
		HTML:           buf.String(),
		Text:           renderText(e, appURL, l),
		UnsubscribeURL: l.UnsubscribeURL,
	}
}

// renderText is a real alternative part, not a fallback nobody reads. Spam
// filters score a multipart message with a missing or token text part worse
// than one with none at all, and some corporate clients still display it in
// preference to the HTML.
func renderText(e Email, appURL string, l Links) string {
	var b strings.Builder

	b.WriteString(e.Heading)
	b.WriteString("\n\n")
	if l.Greeting != "" {
		fmt.Fprintf(&b, "Hi %s,\n\n", l.Greeting)
	}
	b.WriteString(e.Intro)
	b.WriteString("\n\n")
	for i, s := range e.Steps {
		fmt.Fprintf(&b, "%d. %s\n\n", i+1, s)
	}
	fmt.Fprintf(&b, "%s: %s\n\n", e.ButtonText, appURL+e.Path)
	b.WriteString(e.Closing)
	b.WriteString("\n\n--\n")
	b.WriteString("You are getting this because you created a Flomation account.\n")
	b.WriteString("It is one of three short emails about using the product.\n")
	fmt.Fprintf(&b, "To stop them: %s\n\n", l.UnsubscribeURL)
	b.WriteString("Flomation Ltd, Ruscoe House, The Chequer, Whitchurch, Wrexham, Wales, SY13 2JJ\n")

	return b.String()
}
