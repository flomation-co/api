// Package mailer sends multipart email over SMTP.
//
// The API already had two places that built a message with fmt.Sprintf and
// called smtp.SendMail: the flow-share invite and the execution notification.
// Both send HTML only. This package exists because onboarding email needs
// three things those cannot express — a plain-text alternative part, RFC 8058
// unsubscribe headers, and a correct envelope sender — and because getting
// MIME boundaries right once is better than getting them right per caller.
//
// Existing senders are deliberately left alone; they can move over when
// somebody is changing them for another reason.
package mailer

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/smtp"
	"strings"
)

// Config is the SMTP configuration this package needs, named independently of
// any one service's config struct.
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	// From is the header value and may be a display name plus address, e.g.
	// `Flomation <hello@flomation.app>`.
	From string
}

// Message is one email to one recipient.
type Message struct {
	To      string
	Subject string
	HTML    string
	Text    string
	// UnsubscribeURL, when set, adds List-Unsubscribe and
	// List-Unsubscribe-Post. Mail clients that understand these show their own
	// unsubscribe control and POST to the URL, which is both a better
	// experience and a strong deliverability signal — Gmail and Outlook both
	// weight it.
	UnsubscribeURL string
}

// Mailer sends messages. Stateless; safe to share.
type Mailer struct {
	cfg Config
}

func New(cfg Config) *Mailer { return &Mailer{cfg: cfg} }

// Configured reports whether there is enough configuration to send anything.
// Callers use this to stay silent in environments without SMTP rather than
// logging a failure per message.
func (m *Mailer) Configured() bool {
	return m != nil && m.cfg.Host != "" && m.cfg.From != ""
}

// EnvelopeSender is the address used for MAIL FROM.
//
// It has to be the bare address, and it has to come from the From header
// rather than the username: with SES the SMTP username is an IAM access key
// ID, which is not a mailbox, and SES rejects it.
func (m *Mailer) EnvelopeSender() string {
	from := m.cfg.From
	if i := strings.Index(from, "<"); i >= 0 {
		from = strings.TrimSuffix(from[i+1:], ">")
	}
	return strings.TrimSpace(from)
}

// Send delivers one message.
func (m *Mailer) Send(msg Message) error {
	if !m.Configured() {
		return fmt.Errorf("mailer: SMTP is not configured")
	}
	if msg.To == "" {
		return fmt.Errorf("mailer: no recipient")
	}

	body, err := m.build(msg)
	if err != nil {
		return err
	}

	addr := fmt.Sprintf("%s:%d", m.cfg.Host, m.cfg.Port)

	var auth smtp.Auth
	if m.cfg.Username != "" {
		auth = smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
	}

	return smtp.SendMail(addr, auth, m.EnvelopeSender(), []string{msg.To}, body)
}

// build assembles the wire format. Exported behaviour is tested through this
// rather than through a live SMTP conversation.
func (m *Mailer) build(msg Message) ([]byte, error) {
	boundary, err := randomBoundary()
	if err != nil {
		return nil, err
	}

	var b strings.Builder

	writeHeader(&b, "From", m.cfg.From)
	writeHeader(&b, "To", msg.To)
	writeHeader(&b, "Subject", msg.Subject)
	writeHeader(&b, "MIME-Version", "1.0")

	if msg.UnsubscribeURL != "" {
		writeHeader(&b, "List-Unsubscribe", "<"+msg.UnsubscribeURL+">")
		// One-Click tells the client it may POST without asking the reader to
		// confirm on a web page. Only meaningful alongside List-Unsubscribe.
		writeHeader(&b, "List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
	}

	// A text-only message needs no multipart wrapper, and sending one makes
	// the source harder to read for no gain.
	if msg.HTML == "" {
		writeHeader(&b, "Content-Type", `text/plain; charset="UTF-8"`)
		b.WriteString("\r\n")
		b.WriteString(normaliseNewlines(msg.Text))
		return []byte(b.String()), nil
	}

	writeHeader(&b, "Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")

	// Least-preferred part first: multipart/alternative is ordered, and a
	// client picks the last part it understands.
	if msg.Text != "" {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n\r\n")
		b.WriteString(normaliseNewlines(msg.Text))
		b.WriteString("\r\n")
	}

	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n\r\n")
	b.WriteString(normaliseNewlines(msg.HTML))
	b.WriteString("\r\n")

	b.WriteString("--" + boundary + "--\r\n")

	return []byte(b.String()), nil
}

// writeHeader folds nothing and encodes nothing, but it does strip CR and LF.
// A header value carrying a newline would let a caller inject arbitrary
// headers — a Bcc, a different From — and subjects here are partly derived
// from stored data.
func writeHeader(b *strings.Builder, name, value string) {
	value = strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
	b.WriteString(name)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteString("\r\n")
}

// normaliseNewlines makes line endings CRLF. Go source uses LF, SMTP wants
// CRLF, and some MTAs treat a bare LF as the end of the message.
func normaliseNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

func randomBoundary() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "flomation-" + hex.EncodeToString(buf), nil
}
