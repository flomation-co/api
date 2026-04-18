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
	"(e.g. avoid 'I'll look into it' with no timeframe). " +
	"Never repeat work within a conversation. If you have already answered a " +
	"question, fetched data, called a tool, sent a message, or completed an " +
	"action — do not do it again unless the user explicitly asks you to retry, " +
	"refresh, or update. This includes re-summarising previous answers, " +
	"re-running searches, and re-sending notifications. Trust your prior " +
	"results and move on."

// Persistence defines the subset of the persistence layer the system
// prompt assembler needs. Keeps the package testable without importing
// the full persistence service.
type Persistence interface {
	GetAgentMemoriesForUser(agentUserID string, pinnedOnly bool, limit int) ([]*api.AgentMemory, error)
	GetOpenPendingActionsForUser(agentUserID string) ([]*api.AgentPendingAction, error)
	SearchMemoriesByEmbedding(agentID, agentUserID string, emb pgvector.Vector, topK int, excludePinned bool) ([]*api.AgentMemory, error)
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
			Prompt: BuildSystemPrompt(req.Persona, nil, nil, nil, toolSummary, req.ChannelType),
		}
	}

	// Parallel fetch: pinned memories, pending actions, semantic search.
	// All direct DB calls — no HTTP overhead.
	var wg sync.WaitGroup
	var pinnedMem []assembledMemory
	var pending []assembledPendingAction
	var relevantMem []assembledMemory

	wg.Add(2)
	go func() {
		defer wg.Done()
		pinnedMem = a.fetchPinnedMemories(req.AgentUserID)
	}()
	go func() {
		defer wg.Done()
		pending = a.fetchOpenPendingActions(req.AgentUserID)
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
		"tools":             len(toolSummary),
	}).Info("system prompt assembly complete (API-side)")

	prompt := BuildSystemPrompt(req.Persona, pinnedMem, relevantMem, pending, toolSummary, req.ChannelType)

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

// BuildSystemPrompt is the pure-function core of the assembler. Given
// already-fetched data it composes the final string deterministically.
// Exported so it can be unit-tested directly.
func BuildSystemPrompt(
	persona string,
	pinnedMemories []assembledMemory,
	relevantMemories []assembledMemory,
	pendingActions []assembledPendingAction,
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
		b.WriteString("\n\n")
	}

	if len(pendingActions) > 0 {
		b.WriteString("━━━ ACTION REQUIRED ━━━\n")
		b.WriteString("CRITICAL: You MUST address the items below in your VERY NEXT reply. Do NOT ignore them. Do NOT just answer the user's question without also addressing these items. Weave them naturally into your response.\n")
		b.WriteString("When the user responds affirmatively (e.g. \"yes\", \"link them\", \"go ahead\"), treat it as confirmation of these items — NOT as a request to use a tool.\n")
		for _, pa := range pendingActions {
			switch pa.Type {
			case "identity_link":
				fmt.Fprintf(&b, "• IDENTITY LINK PENDING: The user previously said: %q. You have not yet asked them to confirm this. You MUST ask: \"I noticed you mentioned [identity] — would you like me to link your accounts so I can recognise you as the same person across channels?\" Do this NOW, in this reply.\n",
					pa.Evidence)
			case "identity_link_verification":
				fmt.Fprintf(&b, "• IDENTITY VERIFICATION PENDING: Someone on another channel claims to also be this user: %q. You MUST ask them to confirm or deny: \"Someone on [channel] says they're also you — is that right?\" Do this NOW.\n",
					pa.Evidence)
			default:
				fmt.Fprintf(&b, "• %s was inferred from: %q. You MUST confirm this with the user in your reply.\n",
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
		return "Responding via Telegram voice message — the user sent a voice note which has been transcribed. Your text response will be converted to speech, so write naturally as if speaking. Avoid formatting, bullet points, URLs, and code blocks — they don't translate well to speech. Keep responses concise and conversational."
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
