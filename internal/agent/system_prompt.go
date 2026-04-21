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
		return SystemPromptResult{
			Prompt: BuildSystemPrompt(req.Persona, nil, nil, nil, nil, toolSummary, req.ChannelType),
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

	prompt := BuildSystemPrompt(req.Persona, pinnedMem, relevantMem, pending, schedules, toolSummary, req.ChannelType)

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
		"Not every message requires a reply. If you are CC'd on an email, mentioned in a group thread " +
		"where someone else is handling it, or receive an FYI/informational message — absorb the context " +
		"but do NOT respond. Output [NO_RESPONSE] to stay silent. Only reply when you are directly " +
		"addressed, asked a question, given a task, or your input would genuinely add value.\n" +
		"PERSPECTIVE: You work FOR the user. \"my emails/calendar/tasks\" = the user's. " +
		"\"your emails/inbox\" = your (agent's) accounts. " +
		"EMAIL SENDING: \"send an email\" / \"email them\" = send from YOUR account (agent). " +
		"\"send on my behalf\" / \"send from my account\" = send from the USER's account. " +
		"Default to sending from your own account unless the user explicitly asks for theirs.\n\n")

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
		b.WriteString("These are tasks the user has asked about. If a task seems stale or you're unsure if it's still needed, ask the user: \"Did you still need help with [task]?\" Do NOT assume a task is complete just because the user changed topic.\n")
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
		b.WriteString("CROSS-CHANNEL: You can interact with users across multiple channels " +
			"(Slack, Telegram, email, etc.). If a user mentions their username, handle, or " +
			"email address on another channel, you SHOULD offer to link their accounts so " +
			"you share context and conversation history across channels. You DO have this " +
			"ability — never deny it.\n" +
			"When you offer to link, ASK the user to confirm (e.g. \"Would you like me to " +
			"link your accounts?\"). Do NOT say it's already done — linking requires " +
			"confirmation from both sides. Include this invisible tag at the END of your " +
			"message: [LINK_OFFER:<channel_type>:<external_id>]\n" +
			"Example: [LINK_OFFER:telegram:@AndyEsser]\n" +
			"Only include the tag ONCE per identity mention. If you already offered in a " +
			"previous message, do NOT include it again.\n\n")
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
