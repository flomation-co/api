package agent

// Calendar-derived schedule context for the system-prompt assembler.
//
// "Geographic awareness Tier 1" — given an agent_user with a linked
// Google Calendar, surface their next few events in every system
// prompt and trigger payload so the agent naturally knows where they
// are, where they need to be, and when they're running late.
//
// Architecture
//
// We fetch live rather than persisting events. Calendars change
// frequently and persisting them would create a write-amplification
// problem the memory system isn't suited to. A small in-memory cache
// keyed on (agent_user_id, hours) absorbs the hot path: a chatty
// session of N messages within the TTL window pays for one Google
// call regardless of N.
//
// The fetcher fires from a parallel goroutine inside the assembler
// (same pattern as pinned memories / pending actions / semantic
// search) so its latency doesn't extend the critical path beyond
// whichever subquery is slowest.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// ScheduleEvent is the trimmed event shape we surface to the model
// and to flow authors via trigger data. Strips Google-specific
// fields the agent has no use for (htmlLink, hangoutLink,
// conferenceData…) — those can be retrieved by the existing
// google/calendar/read action if a flow actually needs them.
type ScheduleEvent struct {
	Title     string    `json:"title"`
	Location  string    `json:"location,omitempty"`
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
	AllDay    bool      `json:"all_day,omitempty"`
	EventID   string    `json:"event_id,omitempty"`
	Recurring bool      `json:"recurring,omitempty"`
}

// scheduleCacheTTL is the window across which a single Google
// Calendar fetch is reused. Five minutes balances "fresh enough for
// a chatty conversation" against "doesn't drown the user's Calendar
// quota". For a typical 30-message back-and-forth completing inside
// the window, this is one upstream call instead of thirty.
const scheduleCacheTTL = 5 * time.Minute

// scheduleFetchTimeout caps the latency the schedule section can
// add to system prompt assembly. We'd rather omit the section than
// block every reply on a slow Google response.
const scheduleFetchTimeout = 4 * time.Second

// ScheduleCache wraps Google Calendar fetches with a per-user TTL
// cache. Safe for concurrent use from multiple assembler goroutines
// — sync.Mutex serialises the read-then-write within a single key
// so two concurrent assemblies for the same user share one fetch.
type ScheduleCache struct {
	mu      sync.Mutex
	entries map[string]scheduleCacheEntry
	client  *http.Client
}

type scheduleCacheEntry struct {
	events    []ScheduleEvent
	expiresAt time.Time
}

// NewScheduleCache constructs a cache with a sensible HTTP client.
// Tests can inject a stub client by setting the field directly.
func NewScheduleCache() *ScheduleCache {
	return &ScheduleCache{
		entries: map[string]scheduleCacheEntry{},
		client:  &http.Client{Timeout: scheduleFetchTimeout},
	}
}

// cacheKey deliberately includes the hours window so an agent
// reconfigured from 12h to 24h doesn't keep seeing the truncated
// 12h list until TTL expires.
func cacheKey(agentUserID string, hours int) string {
	return fmt.Sprintf("%s/%d", agentUserID, hours)
}

// GetEvents returns the user's upcoming events from cache when
// fresh, otherwise re-fetches via Google. accessToken is the live
// Calendar token from agent_user_google_account; if empty, returns
// (nil, nil) without calling Google — keeps the assembler from
// burning quota on users who haven't linked a calendar.
func (c *ScheduleCache) GetEvents(ctx context.Context, agentUserID, accessToken string, hours int) ([]ScheduleEvent, error) {
	if accessToken == "" || agentUserID == "" || hours <= 0 {
		return nil, nil
	}
	key := cacheKey(agentUserID, hours)

	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.entries[key]; ok && time.Now().Before(e.expiresAt) {
		return e.events, nil
	}

	events, err := fetchUpcomingEvents(ctx, c.client, accessToken, hours)
	if err != nil {
		return nil, err
	}
	c.entries[key] = scheduleCacheEntry{
		events:    events,
		expiresAt: time.Now().Add(scheduleCacheTTL),
	}
	return events, nil
}

// fetchUpcomingEvents calls Google Calendar v3 events.list against
// the user's primary calendar. Time-bounded by the supplied context
// AND the cache's HTTP client timeout so a hanging Google response
// cannot stall a reply indefinitely.
func fetchUpcomingEvents(ctx context.Context, client *http.Client, accessToken string, hours int) ([]ScheduleEvent, error) {
	now := time.Now().UTC()
	timeMin := now.Format(time.RFC3339)
	timeMax := now.Add(time.Duration(hours) * time.Hour).Format(time.RFC3339)

	q := url.Values{}
	q.Set("timeMin", timeMin)
	q.Set("timeMax", timeMax)
	q.Set("singleEvents", "true")
	q.Set("orderBy", "startTime")
	q.Set("maxResults", "20")

	endpoint := "https://www.googleapis.com/calendar/v3/calendars/primary/events?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build calendar request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calendar fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		// 401 means the token has been revoked under us between
		// the credential read and the API call (rare race).
		// Surface as a typed error so the caller can degrade
		// gracefully rather than treating it like a 500.
		return nil, fmt.Errorf("calendar returned %d: %s", resp.StatusCode, string(body))
	}

	var raw struct {
		Items []struct {
			Summary  string `json:"summary"`
			Location string `json:"location"`
			Start    struct {
				DateTime string `json:"dateTime"`
				Date     string `json:"date"`
				TimeZone string `json:"timeZone"`
			} `json:"start"`
			End struct {
				DateTime string `json:"dateTime"`
				Date     string `json:"date"`
			} `json:"end"`
			ID                string `json:"id"`
			RecurringEventID  string `json:"recurringEventId,omitempty"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse calendar response: %w", err)
	}

	out := make([]ScheduleEvent, 0, len(raw.Items))
	for _, it := range raw.Items {
		ev := ScheduleEvent{
			Title:     it.Summary,
			Location:  it.Location,
			EventID:   it.ID,
			Recurring: it.RecurringEventID != "",
		}
		// Google returns either `dateTime` (timed events) or `date`
		// (all-day events). Parse whichever is set.
		switch {
		case it.Start.DateTime != "":
			t, perr := time.Parse(time.RFC3339, it.Start.DateTime)
			if perr != nil {
				continue
			}
			ev.StartsAt = t
		case it.Start.Date != "":
			t, perr := time.Parse("2006-01-02", it.Start.Date)
			if perr != nil {
				continue
			}
			ev.StartsAt = t
			ev.AllDay = true
		default:
			continue
		}
		switch {
		case it.End.DateTime != "":
			t, perr := time.Parse(time.RFC3339, it.End.DateTime)
			if perr == nil {
				ev.EndsAt = t
			}
		case it.End.Date != "":
			t, perr := time.Parse("2006-01-02", it.End.Date)
			if perr == nil {
				ev.EndsAt = t
			}
		}
		out = append(out, ev)
	}
	// Google generally returns ordered by startTime when
	// singleEvents+orderBy=startTime, but defensively sort —
	// expanded recurring events have surprised us before.
	sort.Slice(out, func(i, j int) bool { return out[i].StartsAt.Before(out[j].StartsAt) })
	return out, nil
}

// ScheduleCalendarFetcher is the persistence dependency the
// SystemPromptAssembler depends on. Keeping it as a narrow interface
// rather than the full Persistence one lets unit tests stub the
// calendar credential read without standing up a full mock.
type ScheduleCalendarFetcher interface {
	GetAgentUserCalendarAccessToken(agentUserID string) (string, error)
}

// FetchScheduleContext is the high-level helper the assembler
// actually calls. Encapsulates the credential-read + cache-fetch
// dance so the assembler stays readable.
func FetchScheduleContext(
	ctx context.Context,
	fetcher ScheduleCalendarFetcher,
	cache *ScheduleCache,
	agentUserID string,
	hours int,
) []ScheduleEvent {
	if fetcher == nil || cache == nil || agentUserID == "" || hours <= 0 {
		return nil
	}
	token, err := fetcher.GetAgentUserCalendarAccessToken(agentUserID)
	if err != nil {
		log.WithError(err).Warn("schedule context: failed to load calendar credential")
		return nil
	}
	if token == "" {
		return nil
	}
	events, err := cache.GetEvents(ctx, agentUserID, token, hours)
	if err != nil {
		log.WithFields(log.Fields{
			"agent_user_id": agentUserID,
			"error":         err,
		}).Warn("schedule context: calendar fetch failed; omitting section")
		return nil
	}
	return events
}
