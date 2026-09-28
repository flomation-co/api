package onboardingemail

import (
	"html"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
)

// esc is what a string looks like once html/template has been through it.
// Copy containing an apostrophe — "Let's build your first flow" — arrives in
// the markup as "Let&#39;s", so asserting on the raw string would fail for
// the very reason the template is correct.
func esc(s string) string { return html.EscapeString(s) }

func testLinks() Links {
	return Links{
		AppURL:         "https://app.example.com",
		UnsubscribeURL: "https://api.example.com/api/v1/onboarding/email/unsubscribe/abc",
		Greeting:       "Grace",
	}
}

func TestRenderProducesBothParts(t *testing.T) {
	RegisterTestingT(t)

	e, _ := Find(KeyFirstFlow)
	r := Render(e, testLinks())

	Expect(r.Subject).To(Equal(e.Subject))
	Expect(r.HTML).To(ContainSubstring(esc(e.Heading)))
	Expect(r.Text).To(ContainSubstring(e.Heading))

	// The text part is an alternative, not a placeholder: every step has to
	// be in it, or a reader whose client prefers text gets an empty lesson.
	for _, s := range e.Steps {
		Expect(r.Text).To(ContainSubstring(s))
	}
}

func TestStepsAreNumberedFromOne(t *testing.T) {
	RegisterTestingT(t)

	e, _ := Find(KeyFirstFlow)
	r := Render(e, testLinks())

	// range gives a 0-based index; a template without arithmetic would happily
	// render a list starting at zero.
	Expect(r.HTML).To(ContainSubstring(">1</div>"))
	Expect(r.HTML).To(ContainSubstring(">3</div>"))
	Expect(r.HTML).ToNot(ContainSubstring(">0</div>"))
	Expect(r.Text).To(ContainSubstring("1. "))
}

func TestButtonFollowsTheConfiguredAppURL(t *testing.T) {
	RegisterTestingT(t)

	e, _ := Find(KeyInviteTeam)
	r := Render(e, testLinks())

	Expect(r.HTML).To(ContainSubstring(`href="https://app.example.com/organisation"`))
	Expect(r.Text).To(ContainSubstring("https://app.example.com/organisation"))

	// Nothing may point at production from a test environment.
	Expect(r.HTML).ToNot(ContainSubstring("www.flomation.app"))
}

func TestTrailingSlashOnAppURLDoesNotDoubleUp(t *testing.T) {
	RegisterTestingT(t)

	l := testLinks()
	l.AppURL = "https://app.example.com/"
	r := Render(mustFind(KeyFirstFlow), l)

	Expect(r.HTML).To(ContainSubstring(`href="https://app.example.com/editor"`))
	Expect(r.HTML).ToNot(ContainSubstring("example.com//editor"))
}

func TestUnsubscribeLinkIsAlwaysPresent(t *testing.T) {
	RegisterTestingT(t)

	for _, e := range Sequence {
		r := Render(e, testLinks())
		Expect(r.HTML).To(ContainSubstring(testLinks().UnsubscribeURL),
			"%s has no unsubscribe link in the HTML part", e.Key)
		Expect(r.Text).To(ContainSubstring(testLinks().UnsubscribeURL),
			"%s has no unsubscribe link in the text part", e.Key)
		Expect(r.UnsubscribeURL).To(Equal(testLinks().UnsubscribeURL))
	}
}

func TestGreetingIsOptional(t *testing.T) {
	RegisterTestingT(t)

	l := testLinks()
	l.Greeting = ""
	r := Render(mustFind(KeyFirstFlow), l)

	// No name is better than a wrong one — the email simply opens with its
	// first sentence.
	Expect(r.HTML).ToNot(ContainSubstring("Hi ,"))
	Expect(r.Text).ToNot(ContainSubstring("Hi ,"))
	Expect(r.HTML).To(ContainSubstring(esc(mustFind(KeyFirstFlow).Intro)))
}

// The greeting is a user-editable profile name, so it reaches the template
// from outside. Escaping is the template's job and this is the assertion that
// it is actually doing it.
func TestGreetingIsEscaped(t *testing.T) {
	RegisterTestingT(t)

	l := testLinks()
	l.Greeting = `<script>alert(1)</script>`
	r := Render(mustFind(KeyFirstFlow), l)

	Expect(r.HTML).ToNot(ContainSubstring("<script>"))
	Expect(r.HTML).To(ContainSubstring("&lt;script&gt;"))
}

func TestSubjectLinesAreShortEnoughToRead(t *testing.T) {
	RegisterTestingT(t)

	// Roughly where a phone stops showing the rest of it.
	for _, e := range Sequence {
		Expect(len(e.Subject)).To(BeNumerically("<=", 60),
			"%s subject is truncated on a phone: %q", e.Key, e.Subject)
	}
}

func TestTextPartHasNoMarkup(t *testing.T) {
	RegisterTestingT(t)

	for _, e := range Sequence {
		r := Render(e, testLinks())
		Expect(r.Text).ToNot(ContainSubstring("<"),
			"%s text part contains markup", e.Key)
		Expect(strings.TrimSpace(r.Text)).ToNot(BeEmpty())
	}
}

func mustFind(key string) Email {
	e, ok := Find(key)
	if !ok {
		panic("unknown key " + key)
	}
	return e
}
