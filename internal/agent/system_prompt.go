// Package agent contains the agent orchestration logic that was migrated
// from the Launch service to the API. The API has direct DB access, so
// system prompt assembly (which previously required 4-5 HTTP round-trips
// from Launch to the API) now uses direct persistence calls.
package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/embedding"
	apipersistence "flomation.app/automate/api/internal/persistence"
	log "github.com/sirupsen/logrus"

	"github.com/pgvector/pgvector-go"
)

// layerZeroHonestyDirective is the Phase 3 version of the honesty rule.
const layerZeroHonestyDirective = "" +
	"You may make time-bounded commitments to the user. Examples: " +
	"'I'll get back to you in 30 minutes', 'remind me tomorrow at 9am', " +
	"'I'll check on that in an hour', 'I'll remind you in 1 minute'. " +
	"The platform will honour these automatically — you do not need to " +
	"remember them yourself. Any duration is valid, including 1 minute " +
	"or even 30 seconds. Never refuse or suggest a longer duration than " +
	"the user requested. Always confirm the exact timeframe the user asked for. " +
	"Do NOT make open-ended commitments without a specific time or condition " +
	"(e.g. avoid 'I'll look into it' with no timeframe).\n" +
	"You can also set up RECURRING SCHEDULED TASKS. When a user asks you to " +
	"do something regularly (e.g. 'check my tasks every morning at 8am', " +
	"'send me a weekly summary on Mondays at 9am'), agree naturally and " +
	"the platform will schedule it automatically. Schedules repeat until " +
	"cancelled. You can modify or cancel existing schedules when asked. " +
	"Schedules are different from one-off reminders — they persist and fire " +
	"repeatedly on the configured pattern.\n" +
	"Never repeat yourself within a conversation. If you have already answered " +
	"a question, fetched data, called a tool, sent a message, or completed an " +
	"action — do not do it again unless the user explicitly asks you to retry, " +
	"refresh, or update. This includes re-summarising previous answers, " +
	"re-running searches, re-sending notifications, AND repeating the same " +
	"phrasing or narrative structure. If the user says \"try again\", just do " +
	"the action — do not re-announce what you're about to do with the same " +
	"words you used last time. Be direct: skip the preamble, execute, report.\n" +
	"Identity resolution rule: When the user requests \"my\" profile, data, or " +
	"account information on any connected platform, ALWAYS resolve their identity " +
	"from linked account mappings first. Never infer identity from search results, " +
	"workspace member lists, or guesswork. If no linked account exists for the " +
	"relevant platform, ask the user to confirm their identity before presenting " +
	"any data. Never present another user's data as the requesting user's."

// Persistence defines the subset of the persistence layer the system
// prompt assembler needs. Keeps the package testable without importing
// the full persistence service.
type Persistence interface {
	GetAgentMemoriesForUser(agentUserID string, pinnedOnly bool, limit int) ([]*api.AgentMemory, error)
	GetOpenPendingActionsForUser(agentUserID string) ([]*api.AgentPendingAction, error)
	SearchMemoriesByEmbedding(agentID, agentUserID string, emb pgvector.Vector, topK int, excludePinned bool) ([]*api.AgentMemory, error)
	GetAgentSchedulesForUser(agentID, agentUserID string) ([]*api.AgentSchedule, error)
}

// ToolSummaryProvider retrieves the tool summary for an agent.
type ToolSummaryProvider interface {
	GetToolSummary(agentID string) ([]AssembledTool, error)
}

// AssembledTool is a tool available in the agent's orchestrator flow.
type AssembledTool struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// assembledMemory is the prompt-assembly view of a memory.
type assembledMemory struct {
	Title string
	Body  string
	Type  string
}

// assembledPendingAction is the prompt-assembly view of a pending action.
type assembledPendingAction struct {
	Type     string
	Evidence string
}

// assembledSchedule is the prompt-assembly view of an active schedule.
type assembledSchedule struct {
	Name        string
	Description string
	Summary     string // e.g. "Daily at 08:00 (Europe/London)"
}

// SystemPromptRequest contains the parameters for assembling a system prompt.
type SystemPromptRequest struct {
	AgentID     string
	Persona     string
	ChannelType string
	AgentUserID string
	Content     string // inbound message text for semantic search

	// PriorConversations is the trigger-time snapshot of past
	// conversation summaries surfaced to the agent. Each entry's
	// conversation_id is what the agent passes to the
	// agent/get_conversation tool when it wants the full history.
	// Caller (inbound dispatch) has already capped this at the
	// agent's PriorConversationCount.
	PriorConversations []apipersistence.PriorConversationSummary
}

// SystemPromptResult is the response from AssembleSystemPrompt.
type SystemPromptResult struct {
	Prompt            string `json:"system_prompt"`
	HasPendingActions bool   `json:"has_pending_actions"`
}

// SystemPromptAssembler builds system prompts using direct DB access.
type SystemPromptAssembler struct {
	persistence  Persistence
	toolProvider ToolSummaryProvider
	embedding    embedding.Provider
	topK         int
}

// NewSystemPromptAssembler creates an assembler with the given dependencies.
func NewSystemPromptAssembler(p Persistence, tp ToolSummaryProvider, emb embedding.Provider, topK int) *SystemPromptAssembler {
	if topK <= 0 {
		topK = 10
	}
	return &SystemPromptAssembler{
		persistence:  p,
		toolProvider: tp,
		embedding:    emb,
		topK:         topK,
	}
}

// AssembleSystemPrompt builds the full system prompt with direct DB access.
// No HTTP round-trips — all data is fetched from the persistence layer.
func (a *SystemPromptAssembler) AssembleSystemPrompt(req SystemPromptRequest) SystemPromptResult {
	// Always fetch tool summary (lightweight).
	var toolSummary []AssembledTool
	if a.toolProvider != nil {
		tools, err := a.toolProvider.GetToolSummary(req.AgentID)
		if err != nil {
			log.WithFields(log.Fields{
				"agent_id": req.AgentID,
				"error":    err,
			}).Warn("failed to fetch tool summary")
		} else {
			toolSummary = tools
		}
	}

	// Without an agent_user_id, degrade to persona + honesty + channel.
	if req.AgentUserID == "" {
		prompt := BuildSystemPrompt(req.Persona, nil, nil, nil, nil, toolSummary, req.ChannelType, nil)
		// Plan-task augmentation lands even on the degraded path —
		// the Plan Task Trigger may dispatch with no agent_user_id
		// and the AI still needs to know it's in autonomous mode.
		prompt = AppendPlanTaskInstructions(prompt, req.ChannelType)
		prompt = AppendPlanAuthoringInstructions(prompt, req.ChannelType)
		return SystemPromptResult{
			Prompt: prompt,
		}
	}

	// Parallel fetch: pinned memories, pending actions, schedules, semantic search.
	// All direct DB calls — no HTTP overhead.
	var wg sync.WaitGroup
	var pinnedMem []assembledMemory
	var pending []assembledPendingAction
	var relevantMem []assembledMemory
	var schedules []assembledSchedule

	wg.Add(3)
	go func() {
		defer wg.Done()
		pinnedMem = a.fetchPinnedMemories(req.AgentUserID)
	}()
	go func() {
		defer wg.Done()
		pending = a.fetchOpenPendingActions(req.AgentUserID)
	}()
	go func() {
		defer wg.Done()
		schedules = a.fetchActiveSchedules(req.AgentID, req.AgentUserID)
	}()

	if a.embedding != nil && req.Content != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			relevantMem = a.fetchRelevantMemories(req.AgentID, req.AgentUserID, req.Content)
		}()
	}

	wg.Wait()

	log.WithFields(log.Fields{
		"agent_id":          req.AgentID,
		"agent_user_id":     req.AgentUserID,
		"pinned_memories":   len(pinnedMem),
		"relevant_memories": len(relevantMem),
		"pending_actions":   len(pending),
		"schedules":         len(schedules),
		"tools":             len(toolSummary),
	}).Info("system prompt assembly complete (API-side)")

	prompt := BuildSystemPrompt(req.Persona, pinnedMem, relevantMem, pending, schedules, toolSummary, req.ChannelType, req.PriorConversations)

	// Plan-task augmentation (M1.5 commit 4) — invisible to flow
	// authors. Detection via the channel_type field the Plan Task
	// Trigger populates; no new SystemPromptRequest field needed.
	prompt = AppendPlanTaskInstructions(prompt, req.ChannelType)

	return SystemPromptResult{
		Prompt:            prompt,
		HasPendingActions: len(pending) > 0,
	}
}

// fetchPinnedMemories loads pinned memories directly from the DB.
func (a *SystemPromptAssembler) fetchPinnedMemories(agentUserID string) []assembledMemory {
	mems, err := a.persistence.GetAgentMemoriesForUser(agentUserID, true, 50)
	if err != nil {
		log.WithFields(log.Fields{
			"agent_user_id": agentUserID,
			"error":         err,
		}).Warn("failed to fetch pinned memories")
		return nil
	}
	result := make([]assembledMemory, 0, len(mems))
	for _, m := range mems {
		result = append(result, assembledMemory{
			Title: m.Title,
			Body:  m.Body,
			Type:  m.MemoryType,
		})
	}
	return result
}

// fetchOpenPendingActions loads pending actions directly from the DB.
func (a *SystemPromptAssembler) fetchOpenPendingActions(agentUserID string) []assembledPendingAction {
	actions, err := a.persistence.GetOpenPendingActionsForUser(agentUserID)
	if err != nil {
		log.WithFields(log.Fields{
			"agent_user_id": agentUserID,
			"error":         err,
		}).Warn("failed to fetch pending actions")
		return nil
	}
	result := make([]assembledPendingAction, 0, len(actions))
	for _, pa := range actions {
		result = append(result, assembledPendingAction{
			Type:     pa.Type,
			Evidence: pa.Evidence,
		})
	}
	return result
}

// fetchRelevantMemories generates an embedding and performs semantic search.
func (a *SystemPromptAssembler) fetchRelevantMemories(agentID, agentUserID, content string) []assembledMemory {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	vec, err := a.embedding.Embed(ctx, content)
	if err != nil {
		log.WithFields(log.Fields{
			"agent_id": agentID,
			"error":    err,
		}).Warn("failed to generate embedding for semantic search")
		return nil
	}

	mems, err := a.persistence.SearchMemoriesByEmbedding(
		agentID, agentUserID, pgvector.NewVector(vec), a.topK, true,
	)
	if err != nil {
		log.WithFields(log.Fields{
			"agent_id": agentID,
			"error":    err,
		}).Warn("failed to search memories by embedding")
		return nil
	}

	result := make([]assembledMemory, 0, len(mems))
	for _, m := range mems {
		result = append(result, assembledMemory{
			Title: m.Title,
			Body:  m.Body,
			Type:  m.MemoryType,
		})
	}
	return result
}

// fetchActiveSchedules loads enabled schedules for a user.
func (a *SystemPromptAssembler) fetchActiveSchedules(agentID, agentUserID string) []assembledSchedule {
	scheds, err := a.persistence.GetAgentSchedulesForUser(agentID, agentUserID)
	if err != nil {
		log.WithFields(log.Fields{
			"agent_id":      agentID,
			"agent_user_id": agentUserID,
			"error":         err,
		}).Warn("failed to fetch active schedules")
		return nil
	}
	result := make([]assembledSchedule, 0, len(scheds))
	for _, s := range scheds {
		result = append(result, assembledSchedule{
			Name:        s.Name,
			Description: s.Description,
			Summary:     formatScheduleSummary(s),
		})
	}
	return result
}

// formatScheduleSummary produces a human-readable schedule description.
func formatScheduleSummary(s *api.AgentSchedule) string {
	tz := s.Timezone
	if tz == "" || tz == "UTC" {
		tz = ""
	} else {
		tz = " (" + tz + ")"
	}

	switch s.ScheduleMode {
	case "interval":
		val := ""
		unit := ""
		if s.IntervalVal != nil {
			val = *s.IntervalVal
		}
		if s.Unit != nil {
			unit = *s.Unit
		}
		return fmt.Sprintf("Every %s %s%s", val, unit, tz)
	case "daily":
		tod := "00:00"
		if s.TimeOfDay != nil {
			tod = *s.TimeOfDay
		}
		return fmt.Sprintf("Daily at %s%s", tod, tz)
	case "weekly":
		tod := "00:00"
		if s.TimeOfDay != nil {
			tod = *s.TimeOfDay
		}
		days := ""
		if s.DaysOfWeek != nil {
			days = *s.DaysOfWeek
		}
		return fmt.Sprintf("Weekly on %s at %s%s", days, tod, tz)
	default:
		return s.ScheduleMode
	}
}

// BuildSystemPrompt is the pure-function core of the assembler. Given
// already-fetched data it composes the final string deterministically.
// Exported so it can be unit-tested directly.
func BuildSystemPrompt(
	persona string,
	pinnedMemories []assembledMemory,
	relevantMemories []assembledMemory,
	pendingActions []assembledPendingAction,
	schedules []assembledSchedule,
	tools []AssembledTool,
	channelType string,
	priorConversations []apipersistence.PriorConversationSummary,
) string {
	var b strings.Builder

	if persona != "" {
		b.WriteString(persona)
		b.WriteString("\n\n")
	}

	b.WriteString("━━━ Current time ━━━\n")
	b.WriteString(time.Now().Format("Monday, 2 January 2006 15:04 MST"))
	b.WriteString("\n\n")

	b.WriteString("━━━ Layer 0 ━━━\n")
	b.WriteString(layerZeroHonestyDirective)
	b.WriteString("\n")
	b.WriteString("ABSOLUTE RULE — NEVER FABRICATE DATA: Every piece of factual information you " +
		"present to the user (names, numbers, dates, statuses, IDs, email addresses, titles, " +
		"descriptions, search results) MUST come directly from a tool call result in THIS " +
		"conversation. If you have not called a tool to retrieve the data, you do not have the " +
		"data. Never fill in gaps with plausible-sounding information. Never invent examples " +
		"that look like real data. Never present a guess as a fact.\n" +
		"If a tool returns partial or ambiguous results: report exactly what was returned — " +
		"nothing more, nothing less. If a tool returns no results: say so plainly. " +
		"If you are unsure about something: say \"I'm not sure\" or \"I don't have that " +
		"information\" — NEVER fabricate an answer to appear helpful. Being wrong is far worse " +
		"than admitting uncertainty.\n" +
		"When summarising tool results, do not add details that were not in the response. " +
		"Do not round numbers creatively, do not assume field values that were null or missing, " +
		"and do not merge data from different tool calls unless the relationship is explicit in " +
		"the results. If the user asks a follow-up about data you presented, re-check with a " +
		"tool call rather than relying on your summary of previous results.\n")
	b.WriteString("When the user says \"forget about X\", \"drop X\", \"cancel X\", or \"don't bother with X\" — " +
		"STOP working on that topic immediately. Do not continue researching, drafting, or acting on it. " +
		"Acknowledge briefly (\"Done, dropped.\") and move on. Even if your memory or active tasks mention " +
		"the topic, the user's instruction to forget overrides them.\n" +
		"DEFAULT TO SILENCE: Not every message requires a reply. You should ONLY respond when: " +
		"(1) you are directly addressed by name or @mention, " +
		"(2) someone asks a question you can specifically answer, " +
		"(3) you are given an explicit task or instruction, or " +
		"(4) your input would genuinely add value that no one else in the conversation is providing. " +
		"In ALL other cases — general chatter, conversations between other people, status updates, " +
		"announcements, FYI messages, emoji reactions, link shares, someone else already handling it, " +
		"or any message where you are clearly not the intended audience — absorb the context silently " +
		"and output [NO_RESPONSE]. When in doubt, do NOT respond. Silence is always safer than an " +
		"unwanted reply. Being helpful means knowing when NOT to speak.\n" +
		"PERSPECTIVE: You work FOR the user. \"my emails/calendar/tasks\" = the user's. " +
		"\"your emails/inbox\" = your (agent's) accounts. " +
		"EMAIL SENDING: \"send an email\" / \"email them\" = send from YOUR account (agent). " +
		"\"send on my behalf\" / \"send from my account\" = send from the USER's account. " +
		"Default to sending from your own account unless the user explicitly asks for theirs.\n" +
		"THIRD-PARTY REPLIES: When someone other than the user replies to a message " +
		"you sent (e.g. a reply to an email you sent on the user's behalf, or a response " +
		"from an external party in a thread the user started), do NOT reply to that " +
		"third party autonomously. Instead, relay the response back to the user — " +
		"summarise what was said and ask the user how they would like to proceed. " +
		"You are a messenger, not a decision-maker. Only reply directly to a third " +
		"party if the user has explicitly told you what to say, or has given you " +
		"standing instructions to handle that type of response autonomously. " +
		"When in doubt, always check with the user first.\n\n")

	if len(tools) > 0 {
		b.WriteString("━━━ Tools ━━━\n")
		b.WriteString("You have tools — use them. Never claim you did something without calling the tool. " +
			"Just do it when asked, don't seek confirmation. Infrastructure (tokens, IDs) is pre-configured.\n")
		b.WriteString("Available: ")
		for i, tool := range tools {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(tool.Name)
		}
		b.WriteString("\n")

		b.WriteString("\n")
	}

	if len(pinnedMemories) > 0 {
		b.WriteString("━━━ What you know about this user ━━━\n")
		for _, mem := range pinnedMemories {
			if mem.Title != "" && mem.Title != mem.Body {
				b.WriteString("• ")
				b.WriteString(mem.Title)
				b.WriteString(": ")
				b.WriteString(mem.Body)
			} else {
				b.WriteString("• ")
				b.WriteString(mem.Body)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(priorConversations) > 0 {
		b.WriteString("━━━ Recent conversations ━━━\n")
		b.WriteString("Past conversations with this user, in reverse chronological order. Each entry " +
			"is a SUMMARY plus the original conversation_id. If a summary looks relevant to the " +
			"current message, pass its conversation_id verbatim to the agent/get_conversation tool " +
			"to retrieve the full message history before answering. Do NOT invent details from a " +
			"summary alone — fetch the conversation first.\n")
		for _, pc := range priorConversations {
			ended := ""
			if pc.EndedAt != nil {
				ended = pc.EndedAt.Format("2 Jan 2006 15:04")
			}
			b.WriteString("• [")
			b.WriteString(pc.ConversationID)
			b.WriteString("] ")
			if pc.ChannelType != "" {
				b.WriteString("via ")
				b.WriteString(pc.ChannelType)
				b.WriteString(", ")
			}
			if ended != "" {
				b.WriteString("ended ")
				b.WriteString(ended)
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%d msgs", pc.MessageCount)
			b.WriteString("\n  ")
			// Trim summary to a single tight paragraph so a chatty
			// summariser doesn't blow the token budget. The full
			// text remains retrievable via get_conversation.
			summary := strings.TrimSpace(pc.Summary)
			if len(summary) > 600 {
				summary = summary[:600] + "…"
			}
			b.WriteString(summary)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(relevantMemories) > 0 {
		b.WriteString("━━━ Relevant context ━━━\n")
		for _, mem := range relevantMemories {
			if mem.Title != "" && mem.Title != mem.Body {
				b.WriteString("• ")
				b.WriteString(mem.Title)
				b.WriteString(": ")
				b.WriteString(mem.Body)
			} else {
				b.WriteString("• ")
				b.WriteString(mem.Body)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Active tasks section.
	var activeTasks []assembledMemory
	for _, mem := range pinnedMemories {
		if mem.Type == "task" {
			activeTasks = append(activeTasks, mem)
		}
	}
	for _, mem := range relevantMemories {
		if mem.Type == "task" {
			found := false
			for _, t := range activeTasks {
				if t.Title == mem.Title {
					found = true
					break
				}
			}
			if !found {
				activeTasks = append(activeTasks, mem)
			}
		}
	}
	if len(activeTasks) > 0 {
		b.WriteString("━━━ Active tasks ━━━\n")
		b.WriteString("These are tasks the user has previously asked about. " +
			"Do NOT volunteer or mention these unless the user's current message is directly relevant to one. " +
			"Never shoehorn task updates into unrelated replies. If a task seems stale, you may ask once: " +
			"\"Did you still need help with [task]?\" — but only when the conversation naturally relates to it.\n")
		for _, task := range activeTasks {
			b.WriteString("• ")
			if task.Title != "" {
				b.WriteString(task.Title)
				if task.Body != "" && task.Body != task.Title {
					b.WriteString(": ")
					b.WriteString(task.Body)
				}
			} else {
				b.WriteString(task.Body)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if directive := ChannelDirective(channelType); directive != "" {
		b.WriteString("━━━ Current channel ━━━\n")
		b.WriteString(directive)
		b.WriteString("\n")
		b.WriteString("REPLY SCOPE: Only reply in the channel where the message originated. " +
			"Do NOT broadcast, cross-post, or repeat your response to other channels unless " +
			"the user explicitly asks you to (e.g. \"post this in #general too\"). " +
			"If you use a messaging tool, the channel_id must match the inbound message's channel. " +
			"Sending the same information to multiple channels unprompted is never appropriate.\n")
		b.WriteString("CROSS-CHANNEL: You can interact with users across multiple channels " +
			"(Slack, Telegram, email, etc.). Users declare which of their channel handles " +
			"belong to them in their Flomation profile settings — you can see their " +
			"declared identities in the executing flow's context. If you do not recognise " +
			"the sender (no matching identity in their declared set), respond normally and " +
			"do NOT request identity verification or emit tags. If the user asks how to be " +
			"recognised across channels, point them at their Flomation profile's Identities " +
			"tab. Never claim accounts are linked when they are not.\n\n")
	}

	if len(schedules) > 0 {
		b.WriteString("━━━ Active schedules ━━━\n")
		b.WriteString("You have the following recurring tasks set up for this user. " +
			"You can modify or cancel these if the user asks.\n")
		for _, sched := range schedules {
			b.WriteString("• ")
			b.WriteString(sched.Name)
			b.WriteString(" — ")
			b.WriteString(sched.Summary)
			if sched.Description != "" {
				b.WriteString(": ")
				b.WriteString(sched.Description)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(pendingActions) > 0 {
		b.WriteString("━━━ PENDING ITEMS ━━━\n")
		b.WriteString("The following items are awaiting the user's response. Mention them ONCE if there is a natural opportunity, but do NOT force them into every reply. If the user is focused on something else, leave them for later. Never repeat an item the user has already addressed.\n")
		for _, pa := range pendingActions {
			switch pa.Type {
			case "identity_link":
				fmt.Fprintf(&b, "• Identity link: The user mentioned %q — you could ask if they'd like to link their accounts across channels.\n",
					pa.Evidence)
			case "identity_link_verification":
				fmt.Fprintf(&b, "• Identity verification: Someone on another channel claims to be this user: %q — ask them to confirm when appropriate.\n",
					pa.Evidence)
			default:
				fmt.Fprintf(&b, "• %s: %q — confirm with the user if relevant.\n",
					pa.Type, pa.Evidence)
			}
		}
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// ChannelDirective returns the formatting hint block for a given channel type.
func ChannelDirective(channelType string) string {
	switch channelType {
	case "slack":
		return "Responding via Slack — use mrkdwn formatting (*bold*, _italic_, `code`, triple-backtick code blocks). Do NOT use standard Markdown **bold** — it renders literally in Slack."
	case "telegram":
		return "Responding via Telegram — standard Markdown (**bold**, _italic_, `code`) is supported. Keep replies under 4096 characters."
	case "telegram_voice":
		return "The user sent a Telegram voice note (transcribed to text). " +
			"You can respond with EITHER voice or text — prefix your response with [VOICE] or [TEXT] to choose.\n" +
			"[VOICE] — default for conversational replies. Write naturally as spoken language. " +
			"No formatting, bullet points, URLs, code blocks, or markdown — these don't work in speech. Keep it concise.\n" +
			"[TEXT] — use when sending links, formatted information, code, lists, or anything visual. " +
			"Standard Telegram Markdown is supported.\n" +
			"Default to [VOICE] unless the content genuinely needs to be read rather than heard."
	case "email":
		return "Responding via email — use plain text. Keep formatting minimal and professional.\n" +
			"IMPORTANT: When using tools, do NOT emit any intermediate text alongside tool calls. " +
			"Email is not instant messaging — the user will receive a separate email for every text block you emit. " +
			"Wait until all tool calls are complete, then respond with a single consolidated reply. " +
			"Never include text blocks in a tool_use response on the email channel."
	case "webhook":
		return "Responding via webhook — the caller may be a machine. Respond concisely; use JSON only if the caller explicitly requests structured data."
	default:
		return ""
	}
}

// === Agent Planning M1.5: invisible plan-task augmentation ===
//
// When an inbound execution carries channel_type='plan_task' (set by
// the Plan Task Trigger node — see actions/trigger/plan_task in the
// executor), the agent is running autonomously to progress a plan
// task rather than responding to a user message. The system prompt
// gets an additional block telling the model:
//
//   1. Do NOT message any user-facing channel.
//   2. Terminate via set_output when done (downstream tasks may
//      depend on the outputs).
//   3. If genuinely stuck, call plan/block with a clear reason.
//
// Augmentation is invisible to flow authors — they don't need to
// edit the orchestrator's prompt template. Same posture as the
// existing blob-token instruction append on the AI side.

// ChannelTypePlanTask is the channel_type value the Plan Task
// Trigger populates. Pinned as a constant so the detection in
// AppendPlanTaskInstructions and the populator in the tick endpoint
// can't drift.
const ChannelTypePlanTask = "plan_task"

// PlanTaskInstructions is the focused instruction block appended to
// the system prompt when the active turn is a plan-task invocation.
// Order matters: this lands BEFORE the AI vendor actions' BlobToken
// instructions so the model reads "this is autonomous mode" before
// the mechanics of large-output handling.
const PlanTaskInstructions = `

PLAN TASK MODE — this invocation is the agent progressing a plan
task autonomously. Do NOT message any user-facing channel; the user
is not waiting on a reply.

Complete the work the task describes. Terminate the execution by
calling set_output with whatever downstream tasks need to consume
(an object whose keys are referenceable via ` + "`${this_task.X.output}`" + `).

If you genuinely cannot make progress (missing data, ambiguous
instruction, external dependency unreachable), call plan/block with
a clear reason. The user will see the blocked plan and can revise.
`

// AppendPlanTaskInstructions adds the PLAN TASK MODE block to the
// supplied system prompt when channel_type indicates a plan-task
// invocation. Idempotent — calling twice does not duplicate the
// block. Order-aware — callers should invoke this BEFORE any AI-
// vendor blob-token instruction append so the framing precedes the
// mechanics.
func AppendPlanTaskInstructions(systemPrompt, channelType string) string {
	if channelType != ChannelTypePlanTask {
		return systemPrompt
	}
	if strings.Contains(systemPrompt, "PLAN TASK MODE") {
		return systemPrompt
	}
	return systemPrompt + PlanTaskInstructions
}

// PlanAuthoringInstructions is the M4 draft-first guidance appended
// for USER-CHANNEL turns. Tells the AI that plan/create now produces
// drafts and that it must summarise + await approval before calling
// plan/start. Lands on every turn that isn't plan-task mode (we
// don't know the agent's tool list from inside the assembler; the
// guidance is harmless if the agent has no plan tools wired).
//
// The instruction order — "create → summarise → wait → start" — is
// load-bearing. Without it, an over-eager model will create + start
// in the same tool turn and we lose the human-in-the-loop checkpoint
// the whole milestone is about.
const PlanAuthoringInstructions = `

PLAN AUTHORING — plan/create produces a DRAFT plan, not an active
one. When the user asks you to plan something:

  1. Call plan/create to author the plan (it persists as a draft).
  2. Summarise the plan back to the user in their channel — the
     title, goal, and each task's name + a one-line description.
  3. Ask the user to confirm before starting.
  4. ONLY after explicit user approval ("go ahead", "start it",
     "proceed", "looks good", "yes"), call plan/start with the
     plan_id from step 1.

Do NOT call plan/start immediately after plan/create. The draft
phase is the user's checkpoint to revise scope, cancel, or approve.
`

// AppendPlanAuthoringInstructions adds the M4 draft-first guidance
// when the agent is NOT in plan-task mode. Idempotent.
func AppendPlanAuthoringInstructions(systemPrompt, channelType string) string {
	if channelType == ChannelTypePlanTask {
		// Plan-task turns already get PLAN TASK MODE which forbids
		// plan/create entirely. Adding draft-authoring guidance on
		// top would be confusing.
		return systemPrompt
	}
	if strings.Contains(systemPrompt, "PLAN AUTHORING") {
		return systemPrompt
	}
	return systemPrompt + PlanAuthoringInstructions
}
