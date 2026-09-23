package persistence

// Phase 2d-γ: Go bootstrap helper that seeds the canonical extraction
// System Flow at API startup if it doesn't already exist. See
// plans/agent_memory.md §"The extraction pipeline" and §"Phase 2" for
// the full design rationale.
//
// Why Go bootstrap instead of SQL migration:
//   - Flow JSON references node IDs, edge IDs, and action input wiring
//     that must stay in sync with the executor's action type names and
//     the editor's connection schema. A Go helper keeps the definition
//     close to the types rather than buried in an opaque SQL INSERT.
//   - The helper is idempotent: runs on every startup, checks for an
//     existing extraction flow, and only creates one if none exists.
//     If the flow is accidentally deleted or the DB is migrated to a
//     fresh instance, the next API restart recreates it.
//   - Admins who customise the extraction prompt can do so by editing
//     the flow's revision via the editor once the "show system flows"
//     admin toggle lands in Phase 6. The bootstrap only creates the
//     initial copy; it does not overwrite subsequent revisions.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"flomation.app/automate/api"
	log "github.com/sirupsen/logrus"
)

// ExtractionFlowPurpose is the system_flow_purpose value for the
// canonical extraction pipeline flow. Used in lookups and as the
// value the bootstrap writes to the flo row.
const ExtractionFlowPurpose = "agent_extraction"

// extractionSystemPrompt is the structured-output prompt fed to the
// Anthropic Haiku node in the extraction flow. It asks the model to
// return a JSON object with four arrays: memories, proposed_actions,
// commitments, confirmations.
//
// The prompt includes:
//   - A schema description with all valid types and fields.
//   - Explicit confidence-score guidance (0.0–1.0, higher = more certain).
//   - Two few-shot examples so Haiku has concrete anchors for the
//     output shape. Haiku's compliance on structured JSON goes from
//     ~70% bare-schema to ~95%+ with examples — the ~300 extra tokens
//     are cheap insurance.
//   - An instruction to return an empty object (`{}`) when there's
//     nothing to extract, rather than hallucinating filler.
const extractionSystemPrompt = `You are a memory extraction engine for an AI assistant platform. Your job is to analyse a single conversational turn and extract structured information.

You will receive a message with one of three roles:
- "role": "user" — an incoming user message. Extract memories and proposed actions.
- "role": "assistant" — the agent's reply. Extract commitments only (not memories).
- "role": "summary" — a completed conversation transcript. Extract a session_summary memory and any task_completed confirmations.

━━━ Current time ━━━
${current_time}

Resolve every date you output against that. The year is part of it: "1st October" means the next 1 October from the current time, never a year that has already passed. This block is platform context, not part of the message — never extract it as a memory or a fact.

Analyse it and return a JSON object with these arrays:

## Output schema

{
  "memories": [
    {
      "type": "preference|feedback|fact|relationship|task|session_summary",
      "title": "Short handle (3-8 words)",
      "body": "The fact itself, written as a statement about the user",
      "confidence": 0.0-1.0
    }
  ],
  "proposed_actions": [
    {
      "type": "forget_memory|correct_memory",
      "evidence": "The exact user utterance that triggered this",
      "confidence": 0.0-1.0,
      "payload": {}
    }
  ],
  "commitments": [
    {
      "kind": "followup|reminder|monitor|chase",
      "description": "What was promised",
      "trigger_type": "time_elapsed|absolute_time|condition|user_prompt",
      "due_in": "human-readable duration if applicable",
      "due_at": "ISO-8601 if a specific time was mentioned",
      "evidence": "The exact utterance containing the promise",
      "confidence": 0.0-1.0,
      "made_by": "user|assistant",
      "recurrence": "daily|weekly|monthly|weekdays|every Monday|every N hours|null"
    }
  ],
  "confirmations": [
    {
      "pending_action_id": "UUID if known, empty if not",
      "resolution": "confirmed|declined|task_completed",
      "evidence": "The exact utterance",
      "task_title": "Title of the completed task (only for task_completed)"
    }
  ],
  "schedules": [
    {
      "action": "create|update|delete",
      "name": "Short label for the schedule (3-8 words)",
      "description": "What to do when the schedule fires",
      "mode": "interval|daily|weekly",
      "interval": "N (number, for interval mode only)",
      "unit": "minutes|hours|days (for interval mode only)",
      "time_of_day": "HH:MM in 24-hour format (for daily/weekly)",
      "days_of_week": "comma-separated day names (for weekly only)",
      "timezone": "IANA timezone, e.g. Europe/London (optional)",
      "evidence": "The exact utterance",
      "confidence": 0.0-1.0
    }
  ]
}

## Memory types

- preference: user preferences ("call me Andy not Andrew", "I prefer bullet points")
- feedback: guidance on assistant behaviour ("don't be verbose", "always show code")
- fact: factual information about the user ("lives in London", "works at Flomation")
- relationship: professional connections ("Dave is their co-founder", "Sarah is their client at Acme Corp")
- connection: personal connections — people in the user's life who are NOT platform users. Include name, relationship, and any known attributes. Examples: "Becky — wife, birthday 9 June 1989, vegetarian", "Mum — Christine, lives in Cardiff". Auto-pinned, always included in agent context
- task: active tasks or obligations ("owes Sarah a response on Q3 roadmap")
- session_summary: only used by the platform for session summaries, never by extraction

## Confidence scores

- 1.0: explicit, unambiguous statement ("My name is Andy")
- 0.8-0.95: strong inference from context ("I'll be in the London office" → lives near London)
- 0.5-0.8: reasonable but uncertain ("I think we discussed this last week")
- Below 0.5: too uncertain to store — omit rather than include

## Few-shot examples

Input: {"role": "user", "content": "Hey, I'm Andy. I work in Go mostly but I'm learning React for this project."}
Output:
{
  "memories": [
    {"type": "preference", "title": "Preferred name", "body": "Prefers to be called Andy", "confidence": 0.95},
    {"type": "fact", "title": "Primary language", "body": "Works primarily in Go", "confidence": 0.9},
    {"type": "fact", "title": "Learning React", "body": "Currently learning React for their project", "confidence": 0.85}
  ],
  "proposed_actions": [],
  "commitments": [],
  "confirmations": []
}

Input: {"role": "user", "content": "Can you book a table for me and Becky on Saturday? It's her birthday on the 9th and she's vegetarian."}
Output:
{
  "memories": [
    {"type": "connection", "title": "Becky", "body": "Wife/partner. Birthday: 9th (of current or next month). Vegetarian.", "confidence": 0.9},
    {"type": "task", "title": "Book restaurant", "body": "Book a table for Saturday for user and Becky. Must have vegetarian options.", "confidence": 0.95}
  ],
  "proposed_actions": [],
  "commitments": [],
  "confirmations": []
}

Input: {"role": "assistant", "content": "Got it. I'll remind you in 5 minutes to go for a walk."}
Output:
{
  "memories": [],
  "proposed_actions": [],
  "commitments": [
    {"kind": "reminder", "description": "Remind the user to go for a walk", "trigger_type": "time_elapsed", "due_in": "5 minutes", "evidence": "I'll remind you in 5 minutes to go for a walk.", "confidence": 0.95, "made_by": "assistant"}
  ],
  "confirmations": []
}

Input: {"role": "assistant", "content": "I'll compile a shortlist of options and get back to you within the hour."}
Output:
{
  "memories": [],
  "proposed_actions": [],
  "commitments": [
    {"kind": "followup", "description": "Compile a shortlist of options and report back", "trigger_type": "time_elapsed", "due_in": "1 hour", "evidence": "I'll compile a shortlist of options and get back to you within the hour.", "confidence": 0.9, "made_by": "assistant"}
  ],
  "confirmations": []
}

Input: {"role": "user", "content": "Nevermind - I've booked one"}
Recent conversation for context (if available):
[{"role":"user","content":"I need to book a dentist appointment"},{"role":"assistant","content":"Got it. Is this for you, Becky, or one of the kids?"},{"role":"user","content":"Nevermind - I've booked one"}]
Output:
{
  "memories": [],
  "proposed_actions": [],
  "commitments": [],
  "confirmations": [
    {"pending_action_id": "", "resolution": "task_completed", "evidence": "Nevermind - I've booked one", "task_title": "Book dentist appointment"}
  ]
}

Input: {"role": "user", "content": "Thanks, that's perfect"}
Recent conversation for context (if available):
[{"role":"user","content":"Can you email Bob about the deadline?"},{"role":"assistant","content":"Done. Email sent to Bob about the project deadline."},{"role":"user","content":"Thanks, that's perfect"}]
Output:
{
  "memories": [],
  "proposed_actions": [],
  "commitments": [],
  "confirmations": [
    {"pending_action_id": "", "resolution": "task_completed", "evidence": "Thanks, that's perfect", "task_title": "Email Bob about deadline"}
  ]
}

Input: {"role": "user", "content": "yeah that's me, go ahead and link them"}
Recent conversation for context (if available):
[{"role":"assistant","content":"You mentioned you're andy@flomation.co on email — shall I link that so I recognise you as the same person across channels?"},{"role":"user","content":"yeah that's me, go ahead and link them"}]
Output:
{
  "memories": [],
  "proposed_actions": [],
  "commitments": [],
  "confirmations": [
    {"pending_action_id": "", "resolution": "confirmed", "evidence": "yeah that's me, go ahead and link them"}
  ]
}

Input: {"role": "user", "content": "Yes"}
Recent conversation for context (if available):
[{"role":"assistant","content":"You mentioned you're also reachable at andy@flomation.co — shall I link that so I recognise you as the same person across channels? Just say yes to confirm."},{"role":"user","content":"Yes"}]
Output:
{
  "memories": [],
  "proposed_actions": [],
  "commitments": [],
  "confirmations": [
    {"pending_action_id": "", "resolution": "confirmed", "evidence": "Yes"}
  ]
}

Input: {"role": "user", "content": "no that's not me, don't link those accounts"}
Recent conversation for context (if available):
[{"role":"assistant","content":"You mentioned you're andy@flomation.co on email — shall I link that?"},{"role":"user","content":"no that's not me, don't link those accounts"}]
Output:
{
  "memories": [],
  "proposed_actions": [],
  "commitments": [],
  "confirmations": [
    {"pending_action_id": "", "resolution": "declined", "evidence": "no that's not me, don't link those accounts"}
  ]
}

Input: {"role": "assistant", "content": "Sure! I'll check your tasks every morning at 8am and send you a summary."}
Output:
{
  "memories": [],
  "proposed_actions": [],
  "commitments": [],
  "confirmations": [],
  "schedules": [
    {"action": "create", "name": "Morning tasks summary", "description": "Check the user's tasks and send them a summary of what they have for the day", "mode": "daily", "time_of_day": "08:00", "evidence": "I'll check your tasks every morning at 8am and send you a summary.", "confidence": 0.95}
  ]
}

Input: {"role": "assistant", "content": "Done — I've cancelled the morning tasks check."}
Output:
{
  "memories": [],
  "proposed_actions": [],
  "commitments": [],
  "confirmations": [],
  "schedules": [
    {"action": "delete", "name": "Morning tasks summary", "evidence": "I've cancelled the morning tasks check.", "confidence": 0.9}
  ]
}

Input: {"role": "assistant", "content": "Updated — I'll send your summary at 9am instead of 8am from now on."}
Output:
{
  "memories": [],
  "proposed_actions": [],
  "commitments": [],
  "confirmations": [],
  "schedules": [
    {"action": "update", "name": "Morning tasks summary", "time_of_day": "09:00", "evidence": "I'll send your summary at 9am instead of 8am from now on.", "confidence": 0.9}
  ]
}

Input: {"role": "assistant", "content": "Sure, I'll send you a weekly report every Monday and Friday at 9am."}
Output:
{
  "memories": [],
  "proposed_actions": [],
  "commitments": [],
  "confirmations": [],
  "schedules": [
    {"action": "create", "name": "Weekly report", "description": "Send the user a weekly report", "mode": "weekly", "time_of_day": "09:00", "days_of_week": "monday,friday", "evidence": "I'll send you a weekly report every Monday and Friday at 9am.", "confidence": 0.95}
  ]
}

## Rules

- Return ONLY valid JSON. No markdown fences, no commentary.
- If there is nothing to extract, return: {"memories":[],"proposed_actions":[],"commitments":[],"confirmations":[],"schedules":[]}
- CRITICAL: Never invent information that is not EXPLICITLY stated in the message. Do not infer, guess, or add context from prior conversations. If the user says "I've booked one for tomorrow at 10.35am" in a conversation about dentists, the memory is about a DENTIST appointment, not a restaurant. Only use words and concepts present in the actual message.
- CRITICAL: When a user says "never mind", "I've already done it", "I've sorted it", "I've booked one myself" — this means they completed the task THEMSELVES. Do NOT create a new task memory. Instead, emit a task_completed confirmation. If there is useful factual information (e.g. an appointment time), extract that as a FACT, not a task.
- CRITICAL: When a user says "forget about X", "drop the X task", "don't bother with X", "cancel X", "scrap X" — this means the task is CANCELLED. Emit a task_completed confirmation with the task title matching what was cancelled. Use the conversation context to identify which task(s) the user is referring to.
- Preference and feedback memories should have high confidence (0.85+) since they are auto-pinned.
- Memories should ONLY be extracted from USER turns (role=user). Do NOT extract memories from assistant turns — the assistant's own statements about what it can do, greetings, or general conversation are not user facts. The ONLY exception is if the assistant is restating a user fact that was not already captured (rare). If in doubt, return empty memories for assistant turns.
- Commitments should ONLY be extracted from assistant turns (role=assistant). A user saying "remind me in 30 seconds" is a REQUEST, not a commitment — the commitment is created when the assistant CONFIRMS it in its reply. Never create commitments from user turns.
- When extracting commitments from assistant turns, set "made_by": "assistant".
- ANY assistant reply that promises to do something later, remind the user, follow up, or check back MUST produce a commitment. Examples: "I'll remind you", "Got it, I'll ping you", "I'll check on that", "Sure, setting a reminder". Even very short confirmations are commitments if they imply a future action.
- Reminders can be set for any duration, including 1 minute or less. Do not refuse or modify the user's requested timeframe.
- COMMITMENT TIMING — do not do date arithmetic yourself. If the message expresses the timing as a DURATION or a relative phrase ("in 72 hours", "in 30 minutes", "tomorrow", "tomorrow at 9am", "next Monday"), copy that phrase VERBATIM into "due_in" and leave "due_at" empty. The platform resolves it against the real clock, correctly, including British Summer Time. Converting it to a calendar date yourself is how reminders end up on the wrong day.
- Only set "due_at" when the message names an explicit calendar date or clock time ("on 1 October", "at 09:00 on the 14th", "on Christmas Eve"). Resolve it against the current time above, include the correct year, and format it as ISO-8601 with a time zone.
- NEVER emit a "due_at" earlier than the current time. A reminder cannot be due before it was made. If your arithmetic produces one, you have the year wrong — recheck it against the current time above.
- RECURRING COMMITMENTS: When the user asks for "every day", "every Monday", "weekly", "daily", "monthly", "every weekday", or "every N hours/days" — set the "recurrence" field to the pattern. The platform will automatically schedule the next occurrence after each firing. Examples: "remind me every morning at 9am" → recurrence: "daily", due_at for 9am today or tomorrow. "Weekly standup reminder on Mondays" → recurrence: "every Monday". One-off reminders should have recurrence: null.
- For identity_link proposed_actions, the payload MUST contain "channel_type" (the OTHER platform they claim to be on, NOT the current channel) and "external_id" (their handle/address on that other platform). Example: if a Telegram user says "I'm also andy@flomation.co on email", payload must be {"channel_type": "email", "external_id": "andy@flomation.co"} — NOT {"channel_type": "telegram", ...}.
- identity_link proposed_actions should ONLY be created when the user EXPLICITLY states they have an identity on another channel and wants to link it. Trigger phrases: "I'm also X on Y", "link my X account", "you can reach me at X", "connect my X". Simply MENTIONING an email address or handle (e.g. "Becky's email is bex@yahoo.com", "CC sarah@example.com", "email Bob at bob@test.com", "send it to ada@flomation.co") is NOT an identity claim — store those as facts or connection memories, never as identity_link actions. The bar for identity_link is HIGH: the user must be claiming the identity is THEIRS and expressing intent to link channels.
- CRITICAL: When a user says "don't link", "don't do that", "stop", or any negative instruction about a proposed action, do NOT create a forget_memory or any new proposed_action. This is the user declining a suggestion, not requesting memory deletion. If there is a pending action being declined, emit a confirmation with resolution "declined" instead.
- When the user confirms or declines an identity link (or any pending action the agent asked about), emit a confirmation with resolution "confirmed" or "declined". Leave pending_action_id empty if you don't know it — the platform will match by type.
- A confirmation is ONLY valid when the user is responding to a specific question the agent asked about linking accounts, forgetting a memory, or correcting a fact. Use the "Recent conversation for context" to determine if a short reply like "yes", "sure", "go ahead", "confirmed" is in response to a linking/confirmation question. If the conversation history shows the assistant asked about linking accounts and the user replies "yes", that IS a confirmation. General "yes" or "no" responses in other contexts (e.g. answering a question, agreeing to a task) are NOT confirmations.
- For SUMMARY turns (role=summary): create exactly ONE session_summary memory summarising the conversation in 2-3 sentences. Focus on what was discussed, what was accomplished, and any outstanding items. Also check if any tasks in the conversation were completed — if the user explicitly said "thanks", "sorted", "done", or the assistant completed the requested action (e.g. sent an email that was requested, found information that was asked for), emit a confirmation with type "task_completed" and evidence describing which task was finished.
- Task completion signals include: explicit user acknowledgement ("thanks", "perfect", "that's done"), the assistant confirming an action was performed ("Email sent to Bob", "Meeting scheduled"), or the conversation naturally concluding after the task was fulfilled. Do NOT mark a task as complete just because the topic changed — the user may return to it later. If in doubt, do NOT emit task_completed.
- SCHEDULES: Schedules should ONLY be extracted from ASSISTANT turns (role=assistant), like commitments. A schedule is a RECURRING task the assistant agreed to do at specific times. Distinguish from one-off commitments: "remind me tomorrow at 9am" = commitment (one-off), "check my tasks every morning at 8am" = schedule (recurring). The key difference is RECURRENCE — if the assistant agreed to do something REGULARLY or REPEATEDLY, it's a schedule. If it's a one-time action, it's a commitment.
- SCHEDULE ACTIONS: Use action="create" when the assistant agrees to a new recurring task. Use action="delete" when the assistant confirms cancelling a schedule. Use action="update" when the assistant confirms changing a schedule's timing or details. Only include fields being changed for updates.
- SCHEDULE CONFIDENCE: Confidence must be 0.8+ to create, update, or delete a schedule. Do not create schedules from vague statements — the assistant must explicitly agree to a recurring pattern.
- SCHEDULE TIMEZONE: ALWAYS include the timezone field when creating schedules. Use the user's known timezone from their memories/facts (e.g. "Europe/London" for a UK-based user). If unknown, use "Europe/London" as the default. NEVER leave timezone empty — an empty timezone defaults to UTC which will fire at the wrong time for most users.
- SCHEDULE DEDUPLICATION: If a schedule with a similar purpose already exists (same mode and time), do NOT create a new one. The platform handles deduplication but you should avoid it at the extraction level too.
- SCHEDULE vs COMMITMENT: If the user says "remind me every day at 8am" and the assistant agrees, create a SCHEDULE (not a commitment with recurrence). Schedules are the preferred mechanism for recurring tasks.`

// supersededExtractionPrompts holds the SHA-256 of every extraction
// prompt this codebase has previously shipped. An installation whose
// stored prompt hashes to one of these has never been edited by an
// admin, so replacing it is safe and is the only way a prompt fix ever
// reaches a deployment that already bootstrapped. Anything else is
// treated as customised and left alone.
//
// Add the outgoing prompt's hash here whenever extractionSystemPrompt
// changes. TestSupersededPromptsExcludesCurrent keeps the list honest.
var supersededExtractionPrompts = []string{
	// v1, shipped from Phase 2d-γ until 2026-09-10. Carried no notion
	// of the current date, so the model dated commitments from its
	// training era: every absolute_time commitment ever created was
	// due in 2024 or 2025 and fired the instant it was written.
	"dd80558d2b5500ab7d00635187ce2daf52c697dd7348db2f4ab9fd2bacd95afa",
}

func promptFingerprint(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}

// extractionRevision is the slice of a stored revision this file needs
// to decide whether a flow is still the one it built.
type extractionRevision struct {
	Nodes []struct {
		Data struct {
			Label  string `json:"label"`
			Config struct {
				Inputs []struct {
					Name  string          `json:"name"`
					Value json.RawMessage `json:"value"`
				} `json:"inputs"`
			} `json:"config"`
		} `json:"data"`
	} `json:"nodes"`
}

// stringInput returns a node input's value when it is a JSON string.
// Inputs are heterogeneous — the switch's cases is an array — so a
// non-string reads as absent rather than as an error.
func (r extractionRevision) stringInput(label, name string) (string, bool) {
	for _, node := range r.Nodes {
		if node.Data.Label != label {
			continue
		}
		for _, input := range node.Data.Config.Inputs {
			if input.Name != name {
				continue
			}
			var value string
			if err := json.Unmarshal(input.Value, &value); err != nil {
				return "", false
			}
			return value, true
		}
	}
	return "", false
}

func (r extractionRevision) hasLabel(label string) bool {
	for _, node := range r.Nodes {
		if node.Data.Label == label {
			return true
		}
	}
	return false
}

// extractionPromptOf digs a provider node's system_prompt input out of
// a stored revision. Returns "" when the revision is not the shape this
// bootstrap creates, which is itself a reason not to touch it.
//
// Any ai/* node will do: every provider branch carries the same prompt,
// and matching on the label alone keeps this working for revisions
// written before the flow gained a provider switch (which had a single
// ai/anthropic node).
func extractionPromptOf(data []byte) string {
	var revision extractionRevision
	if err := json.Unmarshal(data, &revision); err != nil {
		return ""
	}
	for _, node := range revision.Nodes {
		if !strings.HasPrefix(node.Data.Label, "ai/") {
			continue
		}
		for _, input := range node.Data.Config.Inputs {
			if input.Name == "system_prompt" {
				var value string
				if err := json.Unmarshal(input.Value, &value); err != nil {
					return ""
				}
				return value
			}
		}
	}
	return ""
}

// extractionFlowIsCurrent reports whether a stored revision is already
// the flow this file would build for the given prompt.
//
// It compares what matters rather than the bytes: a byte comparison
// would rewrite the revision on every restart the moment anything about
// the surrounding serialisation shifted.
func extractionFlowIsCurrent(data []byte, prompt string) bool {
	var revision extractionRevision
	if err := json.Unmarshal(data, &revision); err != nil {
		return false
	}
	if !revision.hasLabel("conditional/switch") || !revision.hasLabel("agent/process_extraction") {
		return false
	}
	for _, provider := range extractionProviders {
		label := "ai/" + provider.Name
		if !revision.hasLabel(label) {
			return false
		}
		if model, ok := revision.stringInput(label, "model"); !ok || model != provider.Model {
			return false
		}
		if stored, ok := revision.stringInput(label, "system_prompt"); !ok || stored != prompt {
			return false
		}
	}
	return true
}

// extractionFlowIsOurs reports whether every node in a stored revision
// is one this bootstrap creates. An admin who added a node to the
// extraction flow has made it theirs, and rewriting the revision would
// silently delete their work — so the shape upgrade stops there and
// says so.
func extractionFlowIsOurs(data []byte) bool {
	var revision extractionRevision
	if err := json.Unmarshal(data, &revision); err != nil {
		return false
	}
	if len(revision.Nodes) == 0 {
		return false
	}
	for _, node := range revision.Nodes {
		switch {
		case node.Data.Label == "manual",
			node.Data.Label == "conditional/switch",
			node.Data.Label == "agent/process_extraction",
			strings.HasPrefix(node.Data.Label, "ai/"):
		default:
			return false
		}
	}
	return true
}

// upgradeExtractionFlow brings an existing extraction flow up to the
// current shape and prompt.
//
// Two things can be out of date, and they are judged separately. The
// PROMPT belongs to whoever last edited it: a customised prompt is
// carried forward untouched, and only a prompt matching a version we
// shipped is replaced. The SHAPE belongs to us — it is how the flow
// reaches a provider at all — so it is rebuilt whenever it differs,
// which is what lets an install that predates provider switching gain
// it without anyone recreating the flow.
func (s *Service) upgradeExtractionFlow(floID string) {
	var data []byte
	if err := s.conn.Get(&data,
		`SELECT data FROM revision WHERE flo_id = $1 ORDER BY created_at DESC LIMIT 1`,
		floID,
	); err != nil {
		log.WithError(err).Warn("unable to read extraction flow revision, leaving it alone")
		return
	}

	stored := extractionPromptOf(data)
	if stored == "" {
		log.WithField("flow_id", floID).Warn("extraction flow revision has no recognisable prompt, leaving it alone")
		return
	}

	prompt := extractionSystemPrompt
	fingerprint := promptFingerprint(stored)
	if fingerprint != promptFingerprint(extractionSystemPrompt) {
		superseded := false
		for _, hash := range supersededExtractionPrompts {
			if hash == fingerprint {
				superseded = true
				break
			}
		}
		if !superseded {
			// Their prompt, their call — but the flow around it is
			// still rebuilt so provider routing stays current.
			prompt = stored
			log.WithFields(log.Fields{
				"flow_id":     floID,
				"fingerprint": fingerprint,
			}).Info("extraction prompt has been customised, keeping it")
		}
	}

	if extractionFlowIsCurrent(data, prompt) {
		return
	}

	if !extractionFlowIsOurs(data) {
		log.WithField("flow_id", floID).
			Info("extraction flow has been edited beyond the nodes we create, leaving it alone")
		return
	}

	revisionData, err := json.Marshal(buildExtractionFlowJSON(prompt))
	if err != nil {
		log.WithError(err).Warn("unable to build the upgraded extraction revision")
		return
	}
	if _, err := s.CreateFloRevision(Revision{
		FloID: floID,
		Data:  json.RawMessage(revisionData),
	}); err != nil {
		log.WithError(err).Warn("unable to write the upgraded extraction revision")
		return
	}

	log.WithFields(log.Fields{
		"flow_id": floID,
		"from":    fingerprint,
	}).Info("upgraded the extraction flow to the current shape")
}

// BootstrapExtractionFlow ensures the canonical extraction System Flow
// exists. It is idempotent: re-running after the flow already exists is
// a no-op. Called once from main() after migrations and before the HTTP
// server starts serving.
//
// Steps:
//  1. Check if a flo with system_flow_purpose='agent_extraction' exists.
//  2. If not, create it via the normal CreateFlo path (which auto-links
//     a manual trigger), mark it as system_flow=TRUE, and create an
//     initial revision with the 3-node extraction flow structure.
//  3. Backfill: update all agents with extraction_flow_id=NULL to point
//     at the new flow. New agents created via CreateAgent after this
//     point will also get the flow ID via a DEFAULT or application-level
//     logic (handled separately in Phase 2d-γ follow-up or editor).
func (s *Service) BootstrapExtractionFlow() error {
	// Step 1: check for existing flow.
	var existingID string
	err := s.conn.Get(&existingID,
		`SELECT id FROM flo WHERE system_flow = TRUE AND system_flow_purpose = $1 LIMIT 1`,
		ExtractionFlowPurpose,
	)
	if err == nil && existingID != "" {
		log.WithFields(log.Fields{
			"flow_id": existingID,
		}).Info("extraction system flow already exists, skipping bootstrap")

		// An existing flow still needs the prompt kept current, or a
		// fix like the missing-date one can never reach it.
		s.upgradeExtractionFlow(existingID)

		// Still backfill agents that were created since last restart
		// and might have NULL extraction_flow_id.
		return s.backfillExtractionFlowID(existingID)
	}

	// Step 2: create the flow.
	log.Info("creating canonical extraction system flow")

	floName := "Agent Memory Extraction"
	floID, err := s.CreateFlo(Flo{
		Name: floName,
	})
	if err != nil {
		return fmt.Errorf("failed to create extraction flow: %w", err)
	}

	// Mark as system flow (these columns aren't on the Go struct, so
	// we use raw SQL). Also set the name again in case CreateFlo
	// normalises it.
	if _, err := s.conn.Exec(
		`UPDATE flo SET system_flow = TRUE, system_flow_purpose = $1, name = $2 WHERE id = $3`,
		ExtractionFlowPurpose, floName, *floID,
	); err != nil {
		return fmt.Errorf("failed to mark extraction flow as system: %w", err)
	}

	// Set owner_id on the auto-created trigger. CreateFlo makes a
	// trigger with a NULL owner which causes a nil-pointer panic in
	// TriggerExecution. Pick the first user in the system as the owner;
	// this is a system flow so the specific owner doesn't matter for
	// permissions.
	var systemOwnerID string
	if err := s.conn.Get(&systemOwnerID, `SELECT id FROM users LIMIT 1`); err == nil && systemOwnerID != "" {
		_, _ = s.conn.Exec(
			`UPDATE trigger SET owner_id = $1 WHERE id IN (SELECT trigger_id FROM flo_trigger WHERE flo_id = $2)`,
			systemOwnerID, *floID,
		)
	}

	// Build the flow revision (trigger → switch → ai/<provider> → process_extraction).
	revisionData := buildExtractionFlowJSON(extractionSystemPrompt)
	revisionDataBytes, err := json.Marshal(revisionData)
	if err != nil {
		return fmt.Errorf("failed to marshal extraction flow revision: %w", err)
	}

	if _, err := s.CreateFloRevision(Revision{
		FloID: *floID,
		Data:  json.RawMessage(revisionDataBytes),
	}); err != nil {
		return fmt.Errorf("failed to create extraction flow revision: %w", err)
	}

	log.WithFields(log.Fields{
		"flow_id": *floID,
	}).Info("extraction system flow created")

	// Step 3: backfill all agents.
	return s.backfillExtractionFlowID(*floID)
}

// backfillExtractionFlowID sets extraction_flow_id for all agents that
// don't have one yet. Returns the number of rows updated.
func (s *Service) backfillExtractionFlowID(flowID string) error {
	result, err := s.conn.Exec(
		`UPDATE agent SET extraction_flow_id = $1 WHERE extraction_flow_id IS NULL AND archived_at IS NULL`,
		flowID,
	)
	if err != nil {
		return fmt.Errorf("failed to backfill extraction_flow_id: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows > 0 {
		log.WithFields(log.Fields{
			"flow_id":        flowID,
			"agents_updated": rows,
		}).Info("backfilled extraction_flow_id for existing agents")
	}
	return nil
}

// extractionProviders are the AI providers the extraction flow can
// route to, in the order their switch cases are numbered. The order is
// load-bearing: the switch emits handle case_<index>, so reordering
// this list without rebuilding every stored revision would send a
// provider's traffic to a different provider's node.
var extractionProviders = []struct {
	Name  string
	Model string
}{
	{api.ExtractionProviderAnthropic, "claude-haiku-4-5-20251001"},
	{api.ExtractionProviderOpenAI, "gpt-4o-mini"},
	{api.ExtractionProviderGemini, "gemini-2.0-flash"},
	{api.ExtractionProviderGroq, "llama-3.3-70b-versatile"},
	{api.ExtractionProviderOpenRouter, "anthropic/claude-3.5-haiku"},
}

// buildExtractionFlowJSON constructs the extraction flow structure.
// Node IDs are deterministic so that edges can reference them and so
// that re-running the bootstrap on a fresh DB produces the same flow
// shape.
//
// Shape: trigger → switch(${provider}) → one ai/<provider> node per
// supported provider → agent/process_extraction.
//
// There is ONE extraction flow shared by every agent, so the provider
// cannot be baked into a node — it arrives as trigger data and the
// switch picks the branch. The engine skips parents on unmatched
// switch branches, so only the chosen provider node runs and the
// process node's ${response} resolves from it.
func buildExtractionFlowJSON(prompt string) map[string]interface{} {
	triggerNodeID := "extraction-trigger-001"
	switchNodeID := "extraction-switch-004"
	processNodeID := "extraction-process-003"

	// The executor identifies nodes by data.config.type (int64):
	//   1 = ActionTypeTrigger
	//   2 = ActionTypeAction
	//   6 = ActionTypeSwitch
	// Inputs live inside data.config.inputs (not data.inputs).
	// The top-level node.type (string) is used by the editor for
	// rendering but ignored by the executor for dispatch.
	nodes := []map[string]interface{}{
		{
			"id":   triggerNodeID,
			"type": "trigger/manual",
			"data": map[string]interface{}{
				"label": "manual",
				"config": map[string]interface{}{
					"id":     triggerNodeID,
					"type":   1, // ActionTypeTrigger
					"inputs": []interface{}{},
				},
			},
			"position": map[string]interface{}{"x": 250, "y": 50},
		},
	}

	cases := make([]map[string]interface{}, 0, len(extractionProviders))
	for _, provider := range extractionProviders {
		cases = append(cases, map[string]interface{}{
			"key":   provider.Name,
			"value": provider.Name,
		})
	}

	nodes = append(nodes, map[string]interface{}{
		"id":   switchNodeID,
		"type": "action",
		"data": map[string]interface{}{
			"label": "conditional/switch",
			"config": map[string]interface{}{
				"id":   switchNodeID,
				"type": 6, // ActionTypeSwitch
				"inputs": []map[string]interface{}{
					// Falls back to the default provider so an agent
					// row written before the column existed, or a
					// caller that omits it, still extracts.
					{"name": "value", "value": "${provider}"},
					{"name": "operator", "value": "equals"},
					{"name": "cases", "value": cases},
				},
			},
		},
		"position": map[string]interface{}{"x": 250, "y": 180},
	})

	edges := []map[string]interface{}{
		{
			"id":     "extraction-edge-001",
			"source": triggerNodeID,
			"target": switchNodeID,
		},
	}

	for i, provider := range extractionProviders {
		nodeID := fmt.Sprintf("extraction-provider-%s", provider.Name)
		nodes = append(nodes, map[string]interface{}{
			"id":   nodeID,
			"type": "action",
			"data": map[string]interface{}{
				"label": "ai/" + provider.Name,
				"config": map[string]interface{}{
					"id":   nodeID,
					"type": 2, // ActionTypeAction
					"inputs": []map[string]interface{}{
						{"name": "api_key", "value": "${api_key}"},
						{"name": "model", "value": provider.Model},
						{"name": "system_prompt", "value": prompt},
						{"name": "prompt", "value": "${content}"},
						{"name": "max_tokens", "value": "2048"},
						{"name": "temperature", "value": "0"},
					},
				},
			},
			"position": map[string]interface{}{"x": 60 + (i * 320), "y": 320},
		})

		edges = append(edges, map[string]interface{}{
			"id":           fmt.Sprintf("extraction-edge-in-%s", provider.Name),
			"source":       switchNodeID,
			"target":       nodeID,
			"sourceHandle": fmt.Sprintf("case_%d", i),
		})
		edges = append(edges, map[string]interface{}{
			"id":     fmt.Sprintf("extraction-edge-out-%s", provider.Name),
			"source": nodeID,
			"target": processNodeID,
		})
	}

	nodes = append(nodes, map[string]interface{}{
		"id":   processNodeID,
		"type": "action",
		"data": map[string]interface{}{
			"label": "agent/process_extraction",
			"config": map[string]interface{}{
				"id":   processNodeID,
				"type": 2, // ActionTypeAction
				"inputs": []map[string]interface{}{
					{"name": "agent_id", "value": "${flow.agent_id}"},
					{"name": "extraction_json", "value": "${response}"},
					{"name": "agent_user_id", "value": "${flow.agent_user_id}"},
					{"name": "conversation_id", "value": "${flow.conversation_id}"},
					{"name": "source_message_id", "value": ""},
					{"name": "role", "value": "${flow.role}"},
				},
			},
		},
		"position": map[string]interface{}{"x": 250, "y": 500},
	})

	return map[string]interface{}{
		"nodes": nodes,
		"edges": edges,
	}
}

// Flo is a local type alias to avoid importing the api package from
// persistence (which would create a circular dependency in tests). The
// real api.Flo is used at the call site in main.go; this alias lets
// the bootstrap helper create a flow via CreateFlo without the import.
// The db tags match the api.Flo struct exactly.
type Flo = api.Flo

// Revision is a local type alias matching api.Revision.
type Revision = api.Revision
