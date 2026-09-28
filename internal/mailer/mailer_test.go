package mailer

import (
	"net/mail"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
)

func testMailer() *Mailer {
	return New(Config{
		Host: "smtp.example.com", Port: 587,
		Username: "AKIAEXAMPLE", Password: "secret",
		From: "Flomation <hello@flomation.app>",
	})
}

func TestEnvelopeSenderIsTheAddressNotTheUsername(t *testing.T) {
	RegisterTestingT(t)

	// With SES the SMTP username is an IAM access key ID. Using it as MAIL
	// FROM gets the message rejected, which is why this is not simply
	// cfg.Username.
	Expect(testMailer().EnvelopeSender()).To(Equal("hello@flomation.app"))
}

func TestEnvelopeSenderHandlesABareAddress(t *testing.T) {
	RegisterTestingT(t)

	m := New(Config{Host: "h", From: "hello@flomation.app"})
	Expect(m.EnvelopeSender()).To(Equal("hello@flomation.app"))
}

func TestConfiguredNeedsAHostAndASender(t *testing.T) {
	RegisterTestingT(t)

	Expect(testMailer().Configured()).To(BeTrue())
	Expect(New(Config{From: "a@b.c"}).Configured()).To(BeFalse())
	Expect(New(Config{Host: "h"}).Configured()).To(BeFalse())

	// A nil mailer is what a caller holds when SMTP is unconfigured.
	var nilMailer *Mailer
	Expect(nilMailer.Configured()).To(BeFalse())
}

func TestSendRefusesWhenUnconfigured(t *testing.T) {
	RegisterTestingT(t)

	err := New(Config{}).Send(Message{To: "a@b.c", Text: "hi"})
	Expect(err).To(HaveOccurred())
	Expect(err.Error()).To(ContainSubstring("not configured"))
}

func TestMessageParsesAsEmail(t *testing.T) {
	RegisterTestingT(t)

	body, err := testMailer().build(Message{
		To: "grace@example.com", Subject: "Building your first flow",
		HTML: "<p>hello</p>", Text: "hello",
	})
	Expect(err).ToNot(HaveOccurred())

	msg, err := mail.ReadMessage(strings.NewReader(string(body)))
	Expect(err).ToNot(HaveOccurred(), "the message must be readable as RFC 5322")
	Expect(msg.Header.Get("To")).To(Equal("grace@example.com"))
	Expect(msg.Header.Get("Subject")).To(Equal("Building your first flow"))
	Expect(msg.Header.Get("Content-Type")).To(HavePrefix("multipart/alternative;"))
}

func TestTextPartComesBeforeHTML(t *testing.T) {
	RegisterTestingT(t)

	body, err := testMailer().build(Message{
		To: "a@b.c", Subject: "s", HTML: "<p>rich</p>", Text: "plain",
	})
	Expect(err).ToNot(HaveOccurred())

	s := string(body)
	// multipart/alternative is ordered least-preferred first: a client shows
	// the last part it understands. Reversed, every client would show plain
	// text and the HTML would never be seen.
	Expect(strings.Index(s, "plain")).To(BeNumerically("<", strings.Index(s, "<p>rich</p>")))
}

func TestUnsubscribeHeadersAreSetTogether(t *testing.T) {
	RegisterTestingT(t)

	body, _ := testMailer().build(Message{
		To: "a@b.c", Subject: "s", HTML: "<p>x</p>", Text: "x",
		UnsubscribeURL: "https://api.example.com/unsub/tok",
	})
	msg, err := mail.ReadMessage(strings.NewReader(string(body)))
	Expect(err).ToNot(HaveOccurred())

	Expect(msg.Header.Get("List-Unsubscribe")).To(Equal("<https://api.example.com/unsub/tok>"))
	// One-Click without List-Unsubscribe means nothing; the pair is what
	// Gmail and Outlook act on.
	Expect(msg.Header.Get("List-Unsubscribe-Post")).To(Equal("List-Unsubscribe=One-Click"))
}

func TestNoUnsubscribeHeadersWhenThereIsNoURL(t *testing.T) {
	RegisterTestingT(t)

	body, _ := testMailer().build(Message{To: "a@b.c", Subject: "s", Text: "x"})
	msg, _ := mail.ReadMessage(strings.NewReader(string(body)))

	Expect(msg.Header.Get("List-Unsubscribe")).To(BeEmpty())
	Expect(msg.Header.Get("List-Unsubscribe-Post")).To(BeEmpty())
}

// A newline in a header value would end the header and let the rest be read
// as more headers — a Bcc, a different From. Subjects here are built from
// stored data, so this is not hypothetical.
func TestHeaderInjectionIsStripped(t *testing.T) {
	RegisterTestingT(t)

	body, err := testMailer().build(Message{
		To:      "a@b.c",
		Subject: "Hello\r\nBcc: attacker@example.com",
		Text:    "x",
	})
	Expect(err).ToNot(HaveOccurred())

	msg, err := mail.ReadMessage(strings.NewReader(string(body)))
	Expect(err).ToNot(HaveOccurred())
	Expect(msg.Header.Get("Bcc")).To(BeEmpty())
	Expect(msg.Header.Get("Subject")).To(ContainSubstring("Bcc: attacker@example.com"),
		"the text survives, flattened into the subject")
}

func TestTextOnlyMessageIsNotMultipart(t *testing.T) {
	RegisterTestingT(t)

	body, _ := testMailer().build(Message{To: "a@b.c", Subject: "s", Text: "just words"})
	msg, err := mail.ReadMessage(strings.NewReader(string(body)))
	Expect(err).ToNot(HaveOccurred())
	Expect(msg.Header.Get("Content-Type")).To(HavePrefix("text/plain"))
}

func TestLineEndingsAreCRLF(t *testing.T) {
	RegisterTestingT(t)

	// Go source is LF; SMTP wants CRLF, and some MTAs treat a bare LF as the
	// end of the message — which truncates it silently.
	body, _ := testMailer().build(Message{
		To: "a@b.c", Subject: "s", HTML: "<p>one</p>\n<p>two</p>", Text: "one\ntwo",
	})
	s := string(body)
	Expect(strings.ReplaceAll(s, "\r\n", "")).ToNot(ContainSubstring("\n"),
		"every newline must be part of a CRLF pair")
}

func TestBoundariesAreUnique(t *testing.T) {
	RegisterTestingT(t)

	m := testMailer()
	msg := Message{To: "a@b.c", Subject: "s", HTML: "<p>x</p>", Text: "x"}
	a, _ := m.build(msg)
	b, _ := m.build(msg)
	Expect(string(a)).ToNot(Equal(string(b)))
}

func TestSendRequiresARecipient(t *testing.T) {
	RegisterTestingT(t)

	err := testMailer().Send(Message{Subject: "s", Text: "x"})
	Expect(err).To(HaveOccurred())
	Expect(err.Error()).To(ContainSubstring("no recipient"))
}
