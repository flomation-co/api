package poller

import (
	"fmt"
	"time"

	api "flomation.app/automate/api"
	log "github.com/sirupsen/logrus"
)

// SchedulePersistence defines the DB methods the schedule poller needs.
type SchedulePersistence interface {
	GetEnabledAgentSchedules() ([]*api.AgentSchedule, error)
	UpdateAgentScheduleLastFired(id string, firedAt time.Time) error
	GetAgentByID(id string) (*api.Agent, error)
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

	content := fmt.Sprintf(
		"[SCHEDULED TASK] You have a recurring schedule called %q. "+
			"The task: %s. "+
			"Use your tools to carry out this task — send emails, post messages, "+
			"check calendars, etc. as the task requires. Do NOT attempt to reply "+
			"on a messaging channel directly — this execution has no channel context. "+
			"If the user asked you to 'tell them' or 'send them' something, use the "+
			"appropriate tool (email_send, messaging/slack, messaging/telegram, etc.).",
		sched.Name, sched.Description)

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

	// Build system prompt.
	systemPrompt := ""
	if agent.SystemPrompt != nil {
		systemPrompt = *agent.SystemPrompt
	}
	systemPrompt += "\n\n━━━ Platform capabilities ━━━\n" +
		"This execution was triggered by a recurring schedule you set up. " +
		"There is NO active channel — you are not responding to a message. " +
		"Use your tools to carry out the task: send emails (email_send), " +
		"post to Slack (messaging/slack), send Telegram messages " +
		"(messaging/telegram), etc. The user specified what they want " +
		"in the task description — follow their instructions.\n" +
		"Do NOT output a plain text response expecting it to reach the user — " +
		"there is no channel to deliver it on. You MUST use a tool.\n\n" +
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
