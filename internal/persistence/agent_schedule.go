package persistence

import (
	"fmt"
	"time"

	api "flomation.app/automate/api"
)

// CreateAgentSchedule inserts a new agent schedule and returns its ID.
func (s *Service) CreateAgentSchedule(sched api.AgentSchedule) (*string, error) {
	tz := sched.Timezone
	if tz == "" {
		tz = "UTC"
	}
	var id string
	err := s.conn.QueryRow(
		`INSERT INTO agent_schedule
			(agent_id, agent_user_id, conversation_id, name, description,
			 schedule_mode, interval_val, unit, time_of_day, days_of_week,
			 timezone, enabled)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 RETURNING id`,
		sched.AgentID, sched.AgentUserID, sched.ConversationID,
		sched.Name, sched.Description,
		sched.ScheduleMode, sched.IntervalVal, sched.Unit,
		sched.TimeOfDay, sched.DaysOfWeek,
		tz, true,
	).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("insert agent_schedule: %w", err)
	}
	return &id, nil
}

// GetAgentSchedules returns all schedules for an agent.
func (s *Service) GetAgentSchedules(agentID string) ([]*api.AgentSchedule, error) {
	var schedules []*api.AgentSchedule
	err := s.conn.Select(&schedules,
		`SELECT * FROM agent_schedule WHERE agent_id = $1 ORDER BY created_at`, agentID)
	if err != nil {
		return nil, fmt.Errorf("select agent_schedules: %w", err)
	}
	return schedules, nil
}

// GetAgentSchedulesForUser returns enabled schedules for a specific user
// on an agent, used for system prompt injection.
func (s *Service) GetAgentSchedulesForUser(agentID, agentUserID string) ([]*api.AgentSchedule, error) {
	var schedules []*api.AgentSchedule
	err := s.conn.Select(&schedules,
		`SELECT * FROM agent_schedule
		 WHERE agent_id = $1 AND agent_user_id = $2 AND enabled = true
		 ORDER BY created_at`, agentID, agentUserID)
	if err != nil {
		return nil, fmt.Errorf("select agent_schedules for user: %w", err)
	}
	return schedules, nil
}

// GetAgentScheduleByID returns a single schedule by ID.
func (s *Service) GetAgentScheduleByID(id string) (*api.AgentSchedule, error) {
	var sched api.AgentSchedule
	err := s.conn.Get(&sched,
		`SELECT * FROM agent_schedule WHERE id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("get agent_schedule: %w", err)
	}
	return &sched, nil
}

// UpdateAgentSchedule updates the mutable fields of a schedule.
func (s *Service) UpdateAgentSchedule(sched api.AgentSchedule) error {
	_, err := s.conn.Exec(
		`UPDATE agent_schedule SET
			name = $2, description = $3, schedule_mode = $4,
			interval_val = $5, unit = $6, time_of_day = $7,
			days_of_week = $8, timezone = $9, enabled = $10,
			updated_at = NOW()
		 WHERE id = $1`,
		sched.ID, sched.Name, sched.Description, sched.ScheduleMode,
		sched.IntervalVal, sched.Unit, sched.TimeOfDay,
		sched.DaysOfWeek, sched.Timezone, sched.Enabled,
	)
	if err != nil {
		return fmt.Errorf("update agent_schedule: %w", err)
	}
	return nil
}

// DeleteAgentSchedule removes a schedule by ID.
func (s *Service) DeleteAgentSchedule(id string) error {
	_, err := s.conn.Exec(`DELETE FROM agent_schedule WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete agent_schedule: %w", err)
	}
	return nil
}

// DeleteAgentScheduleByName removes a schedule by agent ID and name
// (case-insensitive). Used by the extraction pipeline when the AI says
// "cancel the morning tasks check".
func (s *Service) DeleteAgentScheduleByName(agentID, name string) error {
	_, err := s.conn.Exec(
		`DELETE FROM agent_schedule WHERE agent_id = $1 AND LOWER(name) = LOWER($2)`,
		agentID, name)
	if err != nil {
		return fmt.Errorf("delete agent_schedule by name: %w", err)
	}
	return nil
}

// GetEnabledAgentSchedules returns all enabled schedules on running,
// non-archived agents. This is the schedule poller's hot-path query.
func (s *Service) GetEnabledAgentSchedules() ([]*api.AgentSchedule, error) {
	var schedules []*api.AgentSchedule
	err := s.conn.Select(&schedules,
		`SELECT s.*
		 FROM agent_schedule s
		 JOIN agent a ON a.id = s.agent_id
		 WHERE s.enabled = true
		   AND a.status = 'running'
		   AND a.archived_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("select enabled agent_schedules: %w", err)
	}
	return schedules, nil
}

// UpdateAgentScheduleLastFired stamps the last_fired_at time for a schedule.
func (s *Service) UpdateAgentScheduleLastFired(id string, firedAt time.Time) error {
	_, err := s.conn.Exec(
		`UPDATE agent_schedule SET last_fired_at = $2, updated_at = NOW() WHERE id = $1`,
		id, firedAt)
	if err != nil {
		return fmt.Errorf("update agent_schedule last_fired_at: %w", err)
	}
	return nil
}

// FindAgentScheduleByName finds a schedule by agent ID and name
// (case-insensitive). Used for deduplication and updates by name.
func (s *Service) FindAgentScheduleByName(agentID, name string) (*api.AgentSchedule, error) {
	var sched api.AgentSchedule
	err := s.conn.Get(&sched,
		`SELECT * FROM agent_schedule
		 WHERE agent_id = $1 AND LOWER(name) = LOWER($2)
		 LIMIT 1`, agentID, name)
	if err != nil {
		return nil, err
	}
	return &sched, nil
}
