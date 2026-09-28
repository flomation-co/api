package onboardingemail

import (
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"
)

func TestSequenceIsThreeEmailsInOrder(t *testing.T) {
	RegisterTestingT(t)

	// The brief was three at most. A fourth would need a deliberate decision,
	// not a quiet addition.
	Expect(Sequence).To(HaveLen(3))

	var last time.Duration
	for i, e := range Sequence {
		Expect(e.Delay).To(BeNumerically(">", last),
			"email %d must fall due after the one before it", i)
		last = e.Delay
	}
}

func TestKeysAreUniqueAndStable(t *testing.T) {
	RegisterTestingT(t)

	seen := map[string]bool{}
	for _, e := range Sequence {
		Expect(seen[e.Key]).To(BeFalse(), "duplicate key %q", e.Key)
		seen[e.Key] = true
	}

	// These strings are written into the database. Renaming one makes every
	// enrolled account eligible for an email it has already had, so the
	// literals are asserted rather than derived from the constants.
	Expect(seen).To(HaveKey("first_flow"))
	Expect(seen).To(HaveKey("first_agent"))
	Expect(seen).To(HaveKey("invite_team"))
}

func TestEveryEmailIsComplete(t *testing.T) {
	RegisterTestingT(t)

	for _, e := range Sequence {
		Expect(e.Subject).ToNot(BeEmpty(), "%s has no subject", e.Key)
		Expect(e.Heading).ToNot(BeEmpty(), "%s has no heading", e.Key)
		Expect(e.Intro).ToNot(BeEmpty(), "%s has no intro", e.Key)
		Expect(e.Closing).ToNot(BeEmpty(), "%s has no closing", e.Key)
		Expect(e.ButtonText).ToNot(BeEmpty(), "%s has no button", e.Key)
		Expect(e.Steps).ToNot(BeEmpty(), "%s has no steps", e.Key)
		Expect(len(e.Steps)).To(BeNumerically("<=", 3),
			"%s has more steps than anybody follows in one sitting", e.Key)
		Expect(e.Path).To(HavePrefix("/"), "%s must link somewhere in the app", e.Key)
	}
}

// These are onboarding emails, not marketing. The distinction is the entire
// brief, and it is the kind of thing that erodes one edit at a time.
func TestCopyDoesNotSell(t *testing.T) {
	RegisterTestingT(t)

	forbidden := []string{
		"upgrade", "free trial", "limited time", "don't miss",
		"act now", "discount", "% off", "pricing", "per month",
		"subscribe", "sale", "offer ends",
	}

	for _, e := range Sequence {
		body := strings.ToLower(strings.Join(append([]string{
			e.Subject, e.Heading, e.Intro, e.Closing, e.ButtonText,
		}, e.Steps...), " "))

		for _, phrase := range forbidden {
			Expect(body).ToNot(ContainSubstring(phrase),
				"%s reads as marketing: found %q", e.Key, phrase)
		}
	}
}

func TestScheduleIsRelativeToEnrolment(t *testing.T) {
	RegisterTestingT(t)

	enrolled := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	due := Schedule(enrolled)

	Expect(due).To(HaveLen(3))
	Expect(due[KeyFirstFlow]).To(Equal(enrolled.Add(time.Hour)))
	Expect(due[KeyFirstAgent]).To(Equal(enrolled.Add(72 * time.Hour)))
	Expect(due[KeyInviteTeam]).To(Equal(enrolled.Add(168 * time.Hour)))
}

func TestFind(t *testing.T) {
	RegisterTestingT(t)

	e, ok := Find(KeyFirstAgent)
	Expect(ok).To(BeTrue())
	Expect(e.Goal).To(Equal(GoalCreateAgent))

	_, ok = Find("no_such_email")
	Expect(ok).To(BeFalse())
}

func TestEachEmailHasItsOwnGoal(t *testing.T) {
	RegisterTestingT(t)

	// Two emails sharing a goal would mean one of them can never be skipped
	// for the right reason.
	goals := map[Goal]bool{}
	for _, e := range Sequence {
		Expect(goals[e.Goal]).To(BeFalse(), "%s shares a goal with an earlier email", e.Key)
		goals[e.Goal] = true
	}
}

// Every Path must be a real route in the editor.
//
// "/editor" shipped in the first version and is not a route at all — the flow
// canvas is "/flo" and the list is "/flow" — so the button in the first email
// went nowhere. Nothing could catch it: the email renders, sends and looks
// right, and only the reader finds out.
//
// This pins each path against the editor's route table rather than trusting
// that whoever edits the copy also checks. The list below is transcribed from
// automate/editor/app/routes.ts; if a path is added here it has to be read off
// that file first, which is the whole point.
func TestPathsAreRealEditorRoutes(t *testing.T) {
	RegisterTestingT(t)

	// Top-level routes and prefix() indexes from editor app/routes.ts,
	// checked 2026-09-28.
	editorRoutes := map[string]bool{
		"/":             true,
		"/profile":      true,
		"/billing":      true,
		"/flow":         true, // the flows list
		"/flo":          true, // the canvas: index of prefix("flo")
		"/trigger":      true,
		"/integration":  true,
		"/runner":       true,
		"/queue":        true,
		"/embed":        true,
		"/gateway":      true,
		"/organisation": true,
		"/team":         true,
		"/sso":          true,
		"/usage":        true,
		"/status":       true,
		"/environment":  true,
		"/board":        true,
		"/agent":        true,
		"/execution":    true,
	}

	for _, e := range Sequence {
		Expect(editorRoutes[e.Path]).To(BeTrue(),
			"%s links to %q, which is not a route in the editor — check "+
				"automate/editor/app/routes.ts", e.Key, e.Path)
	}
}

// The button text and the first step should agree with where the link lands.
// The first email said "Open the editor" while pointing at a path that did not
// exist; the copy and the destination drifted apart unnoticed.
func TestFirstEmailSendsPeopleToFlows(t *testing.T) {
	RegisterTestingT(t)

	e, ok := Find(KeyFirstFlow)
	Expect(ok).To(BeTrue())
	Expect(e.Path).To(Equal("/flow"))
	Expect(e.ButtonText).To(ContainSubstring("Flows"))
	Expect(e.Steps[0]).To(ContainSubstring("Flows"),
		"the first step must describe the page the button opens")
}
