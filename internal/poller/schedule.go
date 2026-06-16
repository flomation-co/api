package poller

import (
	"fmt"
	"time"

	api "flomation.app/automate/api"
	"flomation.app/automate/api/internal/persistence"
	log "github.com/sirupsen/logrus"
)

// SchedulePersistence defines the DB methods the schedule poller needs.
type SchedulePersistence interface {
	GetEnabledAgentSchedules() ([]*api.AgentSchedule, error)
	UpdateAgentScheduleLastFired(id string, firedAt time.Time) error
	GetAgentByID(id string) (*api.Agent, error)
	GetAgentConversationByID(id string) (*api.AgentConversation, error)
	GetAgentConversationMessages(conversationID string, limit int) ([]*api.AgentMessage, error)
	ResolveOrCreateAgentConversation(
		agentID string,
		agentUserID *string,
		channelType, channelID string,
		threadID *string,
		idleTimeout int,
	) (*persistence.ConversationResolution, error)
}

// SchedulePoller fires due agent schedules by dispatching orchestrator
// flows. Runs every 30 seconds.
type SchedulePoller struct {
	persistence SchedulePersistence
	dispatcher  FlowDispatcher
}

// StartSchedulePoller creates and starts the schedule poller goroutine.
func StartSchedulePoller(p SchedulePersistence, d FlowDispatcher) *SchedulePoller {
	sp := &SchedulePoller{persistence: p, dispatcher: d}
	go sp.watch()
	return sp
}

func (sp *SchedulePoller) watch() {
	time.Sleep(15 * time.Second)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	log.Info("schedule poller started (API-side, 30s interval)")

	for range ticker.C {
		sp.poll()
	}
}

func (sp *SchedulePoller) poll() {
	schedules, err := sp.persistence.GetEnabledAgentSchedules()
	if err != nil {
		log.WithError(err).Warn("schedule poller: failed to fetch enabled schedules")
		return
	}
	if len(schedules) == 0 {
		return
	}

	for _, sched := range schedules {
		sp.processSchedule(sched)
	}
}

func (sp *SchedulePoller) processSchedule(sched *api.AgentSchedule) {
	l := log.WithFields(log.Fields{
		"schedule_id": sched.ID,
		"agent_id":    sched.AgentID,
		"name":        sched.Name,
		"mode":        sched.ScheduleMode,
	})

	// Parse timezone.
	loc := time.UTC
	if sched.Timezone != "" {
		parsed, err := time.LoadLocation(sched.Timezone)
		if err != nil {
			l.WithField("timezone", sched.Timezone).Warn("schedule poller: invalid timezone, using UTC")
		} else {
			loc = parsed
		}
	}

	now := time.Now().In(loc)

	// First-poll guard: if never fired, stamp now and skip.
	// Prevents immediate firing when a schedule is first created.
	if sched.LastFiredAt == nil {
		if err := sp.persistence.UpdateAgentScheduleLastFired(sched.ID, now); err != nil {
			l.WithError(err).Warn("schedule poller: failed to stamp initial last_fired_at")
		}
		return
	}

	// Build config and check if it should fire.
	cfg := ScheduleConfig{
		Mode:       sched.ScheduleMode,
		TimeOfDay:  derefStr(sched.TimeOfDay),
		DaysOfWeek: derefStr(sched.DaysOfWeek),
		Interval:   derefStr(sched.IntervalVal),
		Unit:       derefStr(sched.Unit),
	}

	lastFired := sched.LastFiredAt.In(loc)
	if !ShouldFire(cfg, lastFired, now) {
		return
	}

	l.Info("schedule firing")

	// Claim: stamp last_fired_at before dispatching to prevent
	// double-fires if the dispatch takes longer than the poll interval.
	if err := sp.persistence.UpdateAgentScheduleLastFired(sched.ID, now); err != nil {
		l.WithError(err).Warn("schedule poller: failed to stamp last_fired_at")
		return
	}

	// Look up agent for orchestrator flow ID.
	agent, err := sp.persistence.GetAgentByID(sched.AgentID)
	if err != nil || agent == nil {
		l.Warn("schedule poller: agent not found")
		return
	}
	if agent.OrchestratorFlowID == nil || *agent.OrchestratorFlowID == "" {
		l.Warn("schedule poller: agent has no orchestrator flow")
		return
	}

	// Determine delivery channel: use source_channel if set, otherwise
	// the task description may name a channel explicitly.
	deliveryChannel := ""
	if sched.SourceChannel != nil && *sched.SourceChannel != "" {
		deliveryChannel = *sched.SourceChannel
	}

	channelInstruction := ""
	if deliveryChannel != "" {
		channelInstruction = fmt.Sprintf(
			"Deliver the result on %s (the channel the user was on when they "+
				"created this schedule). ONLY use a different channel if the task "+
				"description explicitly names one (e.g. 'send me an email', "+
				"'post to Slack'). Do NOT broadcast to multiple channels.",
			deliveryChannel)
	} else {
		channelInstruction = "The task description should indicate where to deliver " +
			"the result. If unclear, pick the single most appropriate channel. " +
			"Do NOT broadcast to multiple channels."
	}

	content := fmt.Sprintf(
		"[SCHEDULED TASK] You have a recurring schedule called %q. "+
			"The task: %s. "+
			"Execute ONLY this specific task — nothing else. "+
			"Do NOT add extra content such as briefings, ticket summaries, "+
			"calendar checks, or email summaries unless the task description "+
			"explicitly asks for them. "+
			"Use your tools to carry out this task. %s "+
			"Do NOT attempt to reply on a messaging channel directly — "+
			"this execution has no channel context. You MUST use a tool.",
		sched.Name, sched.Description, channelInstruction)

	triggerData := map[string]interface{}{
		"agent_id":       sched.AgentID,
		"trigger_source": "schedule",
		"schedule_id":    sched.ID,
		"schedule_name":  sched.Name,
		"content":        content,
		"sender":         "system",
		"channel_type":   "schedule",
	}

	if sched.AgentUserID != nil {
		triggerData["agent_user_id"] = *sched.AgentUserID
	}

	// Anchor to the active conversation for this schedule. The pinned
	// conversation_id is used as a seed: we read its (channel_type,
	// channel_id, thread_id) tuple and ask ResolveOrCreateAgentConversation
	// to return the currently-active conversation — which may be the
	// pinned one, or a fresh one if the pin has idled out. Without this
	// resolution, schedules fire into closed conversations and the AI's
	// history-loading either gets stale context or none at all.
	if conv := sp.resolveActiveConversation(sched, agent); conv != nil {
		triggerData["conversation_id"] = conv.ID
		triggerData["channel_type"] = conv.ChannelType
		triggerData["channel_id"] = conv.ChannelID
		if conv.ThreadID != nil && *conv.ThreadID != "" {
			triggerData["thread_id"] = *conv.ThreadID
		}

		if msgs, err := sp.persistence.GetAgentConversationMessages(conv.ID, 5); err == nil {
			history := normaliseHistory(msgs)
			if len(history) > 0 {
				triggerData["conversation_history"] = history
			}
		}
	}

	// Build system prompt.
	systemPrompt := ""
	if agent.SystemPrompt != nil {
		systemPrompt = *agent.SystemPrompt
	}
	systemPrompt += "\n\n━━━ Schedule execution constraints ━━━\n" +
		"This execution was triggered by a recurring schedule, NOT a user message. " +
		"You are executing a single, specific task — the one described above. " +
		"Do NOT proactively check calendars, emails, Linear tickets, or any other " +
		"data source unless the task description explicitly requires it. " +
		"Do NOT produce a daily briefing or summary unless the task says to. " +
		"Deliver ONLY what the task asks for — nothing more.\n\n" +
		"There is NO active channel — you are not responding to a message. " +
		"You MUST use a tool to deliver the result (e.g. messaging/slack, " +
		"messaging/telegram, email_send). Do NOT output a plain text response " +
		"expecting it to reach the user — there is no channel to deliver it on.\n" +
		"Send to ONE channel only. " + channelInstruction + "\n\n" +
		"━━━ Current time ━━━\n" + time.Now().Format("Monday, 2 January 2006 15:04 MST")
	triggerData["system_prompt"] = systemPrompt

	// Dispatch.
	if err := sp.dispatcher.DispatchFlow(*agent.OrchestratorFlowID, nil, triggerData); err != nil {
		l.WithError(err).Warn("schedule poller: failed to dispatch flow")
		return
	}

	l.Info("scheduled flow dispatched")
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// resolveActiveConversation returns the conversation the schedule should
// fire into right now. It uses the schedule's pinned conversation_id as a
// seed to discover the (channel_type, channel_id, thread_id) tuple, then
// calls ResolveOrCreateAgentConversation — which returns the active
// conversation for that tuple, closing the pinned one and creating a
// fresh one if it has idled out.
//
// This prevents schedules from firing into stale, long-closed conversations
// after the originating conversation rotates due to idle timeout.
//
// Returns nil when:
//   - The schedule has no pinned conversation_id (legacy schedules
//     pre-dating the conversation_id capture in agent_create_schedule).
//   - The seed conversation row no longer exists.
//   - The resolver fails — but in that case we'd rather fire with no
//     context than fire into a definitely-stale conversation.
func (sp *SchedulePoller) resolveActiveConversation(
	sched *api.AgentSchedule,
	agent *api.Agent,
) *api.AgentConversation {
	if sched.ConversationID == nil || *sched.ConversationID == "" {
		return nil
	}

	seed, err := sp.persistence.GetAgentConversationByID(*sched.ConversationID)
	if err != nil || seed == nil {
		return nil
	}

	res, err := sp.persistence.ResolveOrCreateAgentConversation(
		agent.ID,
		sched.AgentUserID,
		seed.ChannelType,
		seed.ChannelID,
		seed.ThreadID,
		agent.IdleTimeoutSeconds,
	)
	if err != nil || res == nil || res.Conversation == nil {
		// Fall back to the seed — better to fire into a possibly-stale
		// conversation than to lose the routing entirely.
		return seed
	}
	return res.Conversation
}
