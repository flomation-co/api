// Package onboardingemail holds the post-registration onboarding sequence:
// which emails exist, what they say, when each is due, and the conditions
// under which one should not be sent at all.
//
// These are instructional, not promotional. Each one teaches a single thing
// the product can do and links to the one screen where the reader can do it.
// There is no pricing, no upgrade path and no "limited time" anything — a
// person who has just signed up needs to know how the thing works, and an
// email that arrives selling instead of explaining is the reason onboarding
// sequences get filtered.
//
// The package is deliberately free of database and SMTP concerns so the copy
// and the schedule can be asserted on directly. Delivery lives in
// internal/poller, storage in internal/persistence.
package onboardingemail

import "time"

// Keys are stored in user_onboarding_email.email_key and therefore must never
// be renamed once shipped: a changed key reads as an email nobody has been
// sent, so everyone already enrolled would receive it again.
const (
	KeyFirstFlow  = "first_flow"
	KeyFirstAgent = "first_agent"
	KeyInviteTeam = "invite_team"
)

// Goal is the thing an email is trying to get across. The poller checks
// whether the reader has already done it and skips the email if so — nobody
// needs instructions for something they did last week.
type Goal int

const (
	// GoalCreateFlow is met once the reader owns any flow.
	GoalCreateFlow Goal = iota
	// GoalCreateAgent is met once the reader owns any agent.
	GoalCreateAgent
	// GoalInviteTeam is met once the reader has invited somebody, or is
	// already sharing an organisation with another person.
	GoalInviteTeam
)

// Email is one message in the sequence.
type Email struct {
	Key string
	// Delay is measured from enrolment, which happens the first time the
	// product sees the account.
	Delay time.Duration
	Goal  Goal

	Subject string
	// Heading is the <h1>. Kept distinct from Subject because a subject line
	// is read in a list of thirty others and a heading is read alone.
	Heading string
	// Intro is the opening paragraph. Plain text; the renderer escapes it.
	Intro string
	// Steps are the numbered instructions. Three at most — a longer list
	// stops being something anybody follows in a browser tab.
	Steps []string
	// Closing sits under the button.
	Closing string

	ButtonText string
	// Path is appended to the configured app URL, so the link follows the
	// environment rather than hard-coding the live host.
	//
	// It must be a real route in the editor's app/routes.ts. Nothing here can
	// check that — a wrong path renders, sends and looks correct, and only the
	// reader finds out. "/editor" shipped once and is not a route at all (the
	// flow canvas is "/flo", the list is "/flow"). TestPathsAreRealEditorRoutes
	// pins every value against the route table; re-read routes.ts before
	// changing one.
	Path string
}

// Sequence is the whole sequence, in the order it is sent.
//
// The spacing is the argument: an hour, so the first one lands while the
// reason for signing up is still fresh; then three days and a week, which is
// far enough apart that somebody who is busy does not feel chased.
var Sequence = []Email{
	{
		Key:     KeyFirstFlow,
		Delay:   time.Hour,
		Goal:    GoalCreateFlow,
		Subject: "Building your first flow",
		Heading: "Let's build your first flow",
		Intro: "A flow is a chain of steps that runs on its own. Every flow " +
			"starts with a trigger — something that happens — and then does " +
			"whatever you wire up after it. The quickest way to see how it " +
			"works is to build a small one end to end.",
		Steps: []string{
			"Open Flows and start a new one. Drop a Manual trigger onto the canvas — that gives you something you can run by hand while you are still experimenting.",
			"Add a step after it and drag a wire between the two handles. Try Send Message, or Format Date if you would rather see a value change.",
			"Press Run. Each node lights up as it goes, and clicking one shows exactly what went in and what came out.",
		},
		Closing: "Once a flow does something useful by hand, swap the Manual " +
			"trigger for a schedule, a webhook or an incoming email and it " +
			"will keep doing it without you.",
		ButtonText: "Open Flows",
		Path:       "/flow",
	},
	{
		Key:     KeyFirstAgent,
		Delay:   3 * 24 * time.Hour,
		Goal:    GoalCreateAgent,
		Subject: "Putting an agent on a channel",
		Heading: "Give someone an agent to talk to",
		Intro: "An agent is a flow with a conversation in front of it. It " +
			"listens on a channel you choose — Slack, Telegram, email — and " +
			"decides which of your flows to run based on what somebody asks " +
			"it. You write the instructions; it handles the wording.",
		Steps: []string{
			"Create an agent and let it start from a blank flow. That flow is its orchestrator: the place where you say what it is allowed to do.",
			"Write a system prompt. Be specific about the job and blunt about the limits — an agent follows a short, plain instruction far more reliably than a long, polite one.",
			"Add the tools it may use by wiring your existing flows in as nodes, then connect a channel and say hello to it.",
		},
		Closing: "Everything it does is recorded run by run, so when an answer " +
			"looks wrong you can open the conversation and see which step " +
			"produced it.",
		ButtonText: "Create an agent",
		Path:       "/agent",
	},
	{
		Key:     KeyInviteTeam,
		Delay:   7 * 24 * time.Hour,
		Goal:    GoalInviteTeam,
		Subject: "Working on flows with other people",
		Heading: "Bring the rest of your team in",
		Intro: "Flows that matter tend to stop being one person's. An " +
			"organisation is how work stops living in a single account: " +
			"flows, credentials and environments belong to the organisation, " +
			"so they outlast whoever happened to build them.",
		Steps: []string{
			"Create an organisation, or open the one you are already in.",
			"Invite people by email address. Each invite carries a role, so somebody who only needs to watch executions never gets the keys to your credentials.",
			"Move shared secrets into an environment rather than typing them into nodes. Everyone gets the same values and nobody has to be sent a password.",
		},
		Closing: "This is also what stops a flow breaking when one person is " +
			"on holiday.",
		ButtonText: "Invite your team",
		Path:       "/organisation",
	},
}

// Find returns the email with the given key.
func Find(key string) (Email, bool) {
	for _, e := range Sequence {
		if e.Key == key {
			return e, true
		}
	}
	return Email{}, false
}

// Schedule turns an enrolment time into the due time for every email. The
// poller never computes these itself — they are written down once, at
// enrolment, so changing the spacing later cannot retroactively make an old
// account's email overdue and fire the whole sequence at once.
func Schedule(enrolledAt time.Time) map[string]time.Time {
	due := make(map[string]time.Time, len(Sequence))
	for _, e := range Sequence {
		due[e.Key] = enrolledAt.Add(e.Delay)
	}
	return due
}
